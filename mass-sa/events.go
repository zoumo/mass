package sa

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	rpc2 "github.com/sourcegraph/jsonrpc2"
)

const massEventQueueCapacity = 256

// SessionEvent is the public MASS event envelope consumed by mass-sa.
type SessionEvent struct {
	Replay      *ReplaySource   `json:"replay,omitempty"`
	EventID     string          `json:"eventId"`
	Namespace   string          `json:"namespace"`
	SessionName string          `json:"sessionName"`
	SessionID   string          `json:"sessionId"`
	ItemID      string          `json:"itemId,omitempty"`
	Type        string          `json:"type"`
	Time        time.Time       `json:"time"`
	Meta        map[string]any  `json:"_meta,omitempty"`
	Payload     json.RawMessage `json:"payload"`
}

// ReplaySource identifies explicitly loaded native history.
type ReplaySource struct {
	LoadID         string `json:"loadId"`
	NativeRecordID string `json:"nativeRecordId,omitempty"`
	NativeParentID string `json:"nativeParentId,omitempty"`
}

// HistoryQuery selects committed MASS events.
type HistoryQuery struct {
	Types []string
	After string
	Limit int
}

// HistoryPage is one committed event page.
type HistoryPage struct {
	Events        []SessionEvent `json:"events"`
	Next          string         `json:"next,omitempty"`
	SyncedEventID *string        `json:"syncedEventId"`
}

type sessionEvent interface {
	Next(context.Context) (SessionEvent, error)
	Close() error
}

// History reads committed events through the public JSON-RPC method.
func (c *MASSClient) History(ctx context.Context, ref SessionRef, query HistoryQuery) (HistoryPage, error) {
	if err := validateSessionRef(ref); err != nil {
		return HistoryPage{}, err
	}
	if query.Limit < 0 {
		return HistoryPage{}, operationError("invalid_argument", "invalid_history_query", "history limit must not be negative")
	}
	params := map[string]any{"session": ref}
	if len(query.Types) != 0 || query.After != "" || query.Limit != 0 {
		rawQuery := map[string]any{}
		if len(query.Types) != 0 {
			rawQuery["types"] = query.Types
		}
		if query.After != "" {
			rawQuery["after"] = query.After
		}
		if query.Limit != 0 {
			rawQuery["limit"] = query.Limit
		}
		params["query"] = rawQuery
	}
	var page HistoryPage
	if err := c.call(ctx, "session.history", params, &page, false); err != nil {
		return HistoryPage{}, err
	}
	return page, nil
}

// Subscribe opens an independent MASS connection and returns only after the
// session.subscribe response is ready. Events are queued before that readiness
// response without being dropped.
func (c *MASSClient) Subscribe(ctx context.Context, ref SessionRef) (sessionEvent, error) {
	if err := validateSessionRef(ref); err != nil {
		return nil, err
	}
	endpoint, err := normalizeMASSEndpoint(c.config.Endpoint)
	if err != nil {
		return nil, err
	}
	token, err := resolveMASSToken(c.config)
	if err != nil {
		return nil, err
	}
	headers := http.Header{}
	if token != "" {
		headers.Set("Authorization", "Bearer "+token)
	}
	dialer := websocket.Dialer{
		Proxy:            http.ProxyFromEnvironment,
		HandshakeTimeout: rpcWriteTimeout,
	}
	socket, response, err := dialer.DialContext(ctx, endpoint, headers)
	if response != nil {
		_ = response.Body.Close()
	}
	if err != nil {
		if response != nil {
			return nil, operationError("unavailable", "mass_unreachable", "MASS subscription handshake failed: "+response.Status)
		}
		return nil, operationError("unavailable", "mass_unreachable", "MASS subscription connection failed")
	}
	socket.SetReadLimit(4 << 20)

	life, cancel := context.WithCancel(context.Background())
	stream := &websocketObjectStream{conn: socket, writeTimeout: rpcWriteTimeout}
	subscription := &MASSEventSubscription{
		queue: make(chan SessionEvent, massEventQueueCapacity),
		done:  make(chan struct{}),
	}
	conn := rpc2.NewConn(life, stream, subscriptionHandler{subscription: subscription}, rpc2.SetLogger(rpcLogger{}))
	subscription.conn = conn
	subscription.stream = stream
	subscription.cancel = cancel
	go func() {
		<-conn.DisconnectNotify()
		subscription.mu.Lock()
		subscription.setFinishedLocked(operationError("unavailable", "mass_observation_disconnected", "MASS observation disconnected; preview may be missing"))
		subscription.mu.Unlock()
	}()
	client := &MASSClient{config: c.config, conn: conn, stream: stream, cancel: cancel}

	var initialized initializeResult
	if err := client.call(ctx, "initialize", map[string]any{
		"protocolVersion": massProtocolVersion,
		"clientInfo": map[string]string{
			"name":    "mass-sa",
			"version": "0.1.0-demo",
		},
	}, &initialized, false); err != nil {
		_ = subscription.closeTransport()
		return nil, err
	}
	if initialized.ProtocolVersion != massProtocolVersion || initialized.ServerInfo.Name == "" || initialized.ServerInfo.Version == "" {
		_ = subscription.closeTransport()
		return nil, operationError("unavailable", "mass_protocol_mismatch", "MASS initialize result is incompatible")
	}

	var result struct {
		SubscriptionID string `json:"subscriptionId"`
	}
	if err := client.call(ctx, "session.subscribe", map[string]any{"session": ref}, &result, false); err != nil {
		terminal := subscription.Err()
		_ = subscription.closeTransport()
		if terminal != nil {
			return nil, terminal
		}
		return nil, err
	}
	if result.SubscriptionID == "" {
		_ = subscription.closeTransport()
		return nil, operationError("unavailable", "mass_subscription_invalid", "MASS subscription result has no subscription ID")
	}
	if err := subscription.activate(result.SubscriptionID); err != nil {
		_ = subscription.closeTransport()
		return nil, err
	}
	return subscription, nil
}

type notificationEnvelope struct {
	SubscriptionID string          `json:"subscriptionId"`
	Event          SessionEvent    `json:"event"`
	Error          *OperationError `json:"error,omitempty"`
}

type subscriptionHandler struct {
	subscription *MASSEventSubscription
}

func (h subscriptionHandler) Handle(ctx context.Context, conn *rpc2.Conn, request *rpc2.Request) {
	if !request.Notif {
		_ = conn.ReplyWithError(ctx, request.ID, &rpc2.Error{Code: rpc2.CodeMethodNotFound, Message: "Method not found"})
		return
	}
	if request.Params == nil {
		h.subscription.fail(operationError("unavailable", "mass_subscription_invalid", "MASS subscription notification has no params"))
		return
	}
	var notification notificationEnvelope
	if err := json.Unmarshal(*request.Params, &notification); err != nil || notification.SubscriptionID == "" {
		h.subscription.fail(operationError("unavailable", "mass_subscription_invalid", "MASS subscription notification is invalid"))
		return
	}
	switch request.Method {
	case "session.event":
		if notification.Event.EventID == "" || notification.Event.SessionID == "" ||
			notification.Event.Namespace == "" || notification.Event.SessionName == "" ||
			notification.Event.Type == "" {
			h.subscription.fail(operationError("unavailable", "mass_subscription_invalid", "MASS session event is incomplete"))
			return
		}
		h.subscription.deliver(notification.SubscriptionID, notification.Event)
	case "subscription.closed":
		if notification.Error != nil {
			h.subscription.finish(notification.SubscriptionID, notification.Error)
		} else {
			h.subscription.finish(notification.SubscriptionID, io.EOF)
		}
	default:
		h.subscription.fail(operationError("unavailable", "mass_subscription_invalid", "MASS subscription sent an unexpected notification"))
	}
}

// MASSEventSubscription is one bounded ordered observation.
type MASSEventSubscription struct {
	conn   *rpc2.Conn
	stream *websocketObjectStream
	cancel context.CancelFunc

	mu              sync.Mutex
	id              string
	pendingEvents   []SessionEvent
	pendingTerminal error
	queue           chan SessionEvent
	done            chan struct{}
	terminal        error
	finished        bool
	closeOnce       sync.Once
	closeErr        error
}

func (s *MASSEventSubscription) activate(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.finished {
		return s.terminal
	}
	s.id = id
	if s.pendingTerminal != nil {
		s.setFinishedLocked(s.pendingTerminal)
		s.pendingEvents = nil
		return s.terminal
	}
	for _, event := range s.pendingEvents {
		select {
		case s.queue <- event:
		default:
			s.setFinishedLocked(operationError("resource_exhausted", "slow_subscriber", "MASS subscription setup queue capacity exceeded"))
			return s.terminal
		}
	}
	s.pendingEvents = nil
	return nil
}

func (s *MASSEventSubscription) deliver(subscriptionID string, event SessionEvent) {
	s.mu.Lock()
	if s.finished {
		s.mu.Unlock()
		return
	}
	if s.id == "" {
		if len(s.pendingEvents) >= massEventQueueCapacity {
			s.setFinishedLocked(operationError("resource_exhausted", "slow_subscriber", "MASS subscription setup queue capacity exceeded"))
			s.mu.Unlock()
			go func() { _ = s.closeTransport() }()
			return
		}
		s.pendingEvents = append(s.pendingEvents, event)
		s.mu.Unlock()
		return
	}
	if subscriptionID != s.id {
		s.setFinishedLocked(operationError("unavailable", "mass_subscription_invalid", "MASS subscription identity changed"))
		s.mu.Unlock()
		go func() { _ = s.closeTransport() }()
		return
	}
	if s.finished {
		s.mu.Unlock()
		return
	}
	select {
	case s.queue <- event:
		s.mu.Unlock()
	default:
		s.setFinishedLocked(operationError("resource_exhausted", "slow_subscriber", "MASS subscription queue capacity exceeded"))
		s.mu.Unlock()
		go func() { _ = s.closeTransport() }()
	}
}

func (s *MASSEventSubscription) finish(subscriptionID string, err error) {
	s.mu.Lock()
	if s.id == "" {
		s.pendingTerminal = err
		s.mu.Unlock()
		return
	}
	if subscriptionID != s.id {
		s.setFinishedLocked(operationError("unavailable", "mass_subscription_invalid", "MASS subscription identity changed"))
	} else {
		s.setFinishedLocked(err)
	}
	s.mu.Unlock()
}

func (s *MASSEventSubscription) fail(err error) {
	s.finish("", err)
	go func() { _ = s.closeTransport() }()
}

func (s *MASSEventSubscription) Next(ctx context.Context) (SessionEvent, error) {
	select {
	case event := <-s.queue:
		return event, nil
	default:
	}
	select {
	case event := <-s.queue:
		return event, nil
	case <-ctx.Done():
		return SessionEvent{}, ctx.Err()
	case <-s.done:
		select {
		case event := <-s.queue:
			return event, nil
		default:
			return SessionEvent{}, s.Err()
		}
	}
}

func (s *MASSEventSubscription) Err() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.terminal
}

func (s *MASSEventSubscription) Close() error {
	s.mu.Lock()
	s.setFinishedLocked(io.EOF)
	s.mu.Unlock()
	return s.closeTransport()
}

func (s *MASSEventSubscription) setFinishedLocked(err error) {
	if s.finished {
		return
	}
	s.finished = true
	s.terminal = err
	close(s.done)
}

func (s *MASSEventSubscription) closeTransport() error {
	s.closeOnce.Do(func() {
		if s.cancel != nil {
			s.cancel()
		}
		if s.conn != nil {
			if err := s.conn.Close(); err != nil && !errors.Is(err, rpc2.ErrClosed) {
				s.closeErr = err
			}
		}
		if s.stream != nil {
			s.closeErr = errors.Join(s.closeErr, s.stream.Close())
		}
	})
	return s.closeErr
}

func (e SessionEvent) textContent() (string, bool) {
	var payload struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	}
	if err := json.Unmarshal(e.Payload, &payload); err != nil {
		return "", false
	}
	var parts []string
	for _, block := range payload.Content {
		if block.Type == "text" && strings.TrimSpace(block.Text) != "" {
			parts = append(parts, block.Text)
		}
	}
	if len(parts) == 0 {
		return "", false
	}
	return strings.Join(parts, "\n"), true
}
