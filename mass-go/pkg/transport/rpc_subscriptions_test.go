package transport_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http/httptest"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/require"

	"github.com/zoumo/mass/mass-go/pkg/events"
	"github.com/zoumo/mass/mass-go/pkg/resource"
	"github.com/zoumo/mass/mass-go/pkg/runtime"
	"github.com/zoumo/mass/mass-go/pkg/service"
	"github.com/zoumo/mass/mass-go/pkg/transport"
)

type subscriptionRPCAPI struct {
	service.RuntimeService
	service.SessionService

	broadcast     *events.EventBroadcast
	broadcasts    map[string]*events.EventBroadcast
	subscriptions map[string]*events.Subscription

	mu          sync.Mutex
	prePublish  []events.SessionEvent
	entered     chan struct{}
	release     chan struct{}
	cancelCalls int
}

func (a *subscriptionRPCAPI) SubscribeEvents(ref resource.SessionRef) (*service.Subscription, error) {
	a.mu.Lock()
	entered, release := a.entered, a.release
	broadcast := a.broadcast
	if selected := a.broadcasts[ref.SessionID]; selected != nil {
		broadcast = selected
	}
	a.mu.Unlock()
	if entered != nil {
		select {
		case entered <- struct{}{}:
		default:
		}
	}
	if release != nil {
		<-release
	}
	subscription, err := broadcast.Subscribe()
	if err != nil {
		return nil, err
	}
	a.mu.Lock()
	if a.subscriptions != nil {
		a.subscriptions[ref.SessionID] = subscription
	}
	pending := append([]events.SessionEvent(nil), a.prePublish...)
	a.mu.Unlock()
	for _, event := range pending {
		if err := broadcast.Publish(event); err != nil {
			_ = subscription.Close()
			return nil, err
		}
	}
	return subscription, nil
}

func (a *subscriptionRPCAPI) GetSession(ref resource.SessionRef) (resource.Session, error) {
	return resource.Session{
		Metadata: resource.Metadata{Namespace: "demo", Name: "observed"},
		Status:   resource.SessionStatus{SessionID: "observed", State: events.Idle},
	}, nil
}

func (a *subscriptionRPCAPI) Cancel(context.Context, resource.SessionRef) (runtime.CancelReceipt, error) {
	a.mu.Lock()
	a.cancelCalls++
	a.mu.Unlock()
	return runtime.CancelReceipt{Result: "noActiveWork"}, nil
}

func (a *subscriptionRPCAPI) setPrePublish(subscriptions ...events.SessionEvent) {
	a.mu.Lock()
	a.prePublish = subscriptions
	a.mu.Unlock()
}

func (a *subscriptionRPCAPI) cancelCount() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.cancelCalls
}

func (a *subscriptionRPCAPI) subscription(sessionID string) *events.Subscription {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.subscriptions[sessionID]
}

type subscriptionRPCFixture struct {
	api        *subscriptionRPCAPI
	server     *httptest.Server
	broadcast  *events.EventBroadcast
	broadcasts map[string]*events.EventBroadcast
}

func newSubscriptionRPCFixture(t *testing.T, capacity int, opts transport.Options) *subscriptionRPCFixture {
	t.Helper()
	broadcast, err := events.NewBroadcast("demo", "observed", "observed", capacity)
	require.NoError(t, err)
	api := &subscriptionRPCAPI{broadcast: broadcast, subscriptions: map[string]*events.Subscription{}}
	if opts.Token == "" && !opts.LocalOnly {
		opts.Token = "test-token"
	}
	server, err := transport.New(api, opts)
	require.NoError(t, err)
	httpServer := httptest.NewServer(server)
	t.Cleanup(func() {
		_ = broadcast.Close()
		server.Shutdown()
		httpServer.Close()
	})
	return &subscriptionRPCFixture{api: api, server: httpServer, broadcast: broadcast}
}

func newNamedSubscriptionRPCFixture(
	t *testing.T,
	sessionIDs []string,
	capacity int,
	opts transport.Options,
) *subscriptionRPCFixture {
	t.Helper()
	broadcasts := make(map[string]*events.EventBroadcast, len(sessionIDs))
	for _, id := range sessionIDs {
		broadcast, err := events.NewBroadcast("demo", id, id, capacity)
		require.NoError(t, err)
		broadcasts[id] = broadcast
	}
	api := &subscriptionRPCAPI{
		broadcast:     broadcasts[sessionIDs[0]],
		broadcasts:    broadcasts,
		subscriptions: map[string]*events.Subscription{},
	}
	if opts.Token == "" && !opts.LocalOnly {
		opts.Token = "test-token"
	}
	server, err := transport.New(api, opts)
	require.NoError(t, err)
	httpServer := httptest.NewServer(server)
	t.Cleanup(func() {
		for _, broadcast := range broadcasts {
			_ = broadcast.Close()
		}
		server.Shutdown()
		httpServer.Close()
	})
	return &subscriptionRPCFixture{
		api:        api,
		server:     httpServer,
		broadcast:  broadcasts[sessionIDs[0]],
		broadcasts: broadcasts,
	}
}

type rpcSubscriptionFrame struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
	Result  json.RawMessage `json:"result"`
	Error   *rpcError       `json:"error"`
}

func readSubscriptionFrame(t *testing.T, conn *websocket.Conn) rpcSubscriptionFrame {
	t.Helper()
	frame, err := readSubscriptionFrameError(conn, 5*time.Second)
	require.NoError(t, err)
	return frame
}

func readSubscriptionFrameError(conn *websocket.Conn, timeout time.Duration) (rpcSubscriptionFrame, error) {
	_ = conn.SetReadDeadline(time.Now().Add(timeout))
	messageType, data, err := conn.ReadMessage()
	if err != nil {
		return rpcSubscriptionFrame{}, err
	}
	if messageType != websocket.TextMessage {
		return rpcSubscriptionFrame{}, fmt.Errorf("unexpected message type %d", messageType)
	}
	var frame rpcSubscriptionFrame
	if err := json.Unmarshal(data, &frame); err != nil {
		return rpcSubscriptionFrame{}, err
	}
	return frame, nil
}

func subscriptionRPCConn(t *testing.T, fixture *subscriptionRPCFixture) *websocket.Conn {
	t.Helper()
	conn := dialRPC(t, fixture.server, rpcHeaders("test-token"))
	sendText(t, conn, initializeMessage(`"init"`, "1"))
	require.Nil(t, readRPC(t, conn).Error)
	return conn
}

func blockedRPCConn(t *testing.T, fixture *subscriptionRPCFixture) *websocket.Conn {
	t.Helper()
	dialer := *websocket.DefaultDialer
	dialer.NetDialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		netDialer := net.Dialer{Control: func(_, _ string, raw syscall.RawConn) error {
			var optionErr error
			if err := raw.Control(func(fd uintptr) {
				optionErr = syscall.SetsockoptInt(int(fd), syscall.SOL_SOCKET, syscall.SO_RCVBUF, 1024)
			}); err != nil {
				return err
			}
			return optionErr
		}}
		return netDialer.DialContext(ctx, network, address)
	}
	conn, response, err := dialer.Dial(rpcURL(fixture.server), rpcHeaders("test-token"))
	if response != nil {
		_ = response.Body.Close()
	}
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })
	sendText(t, conn, initializeMessage(`"init"`, "1"))
	require.Nil(t, readRPC(t, conn).Error)
	return conn
}

func subscribeRPC(t *testing.T, conn *websocket.Conn, id, sessionID string) string {
	t.Helper()
	sendCall(t, conn, id, "session.subscribe", fmt.Sprintf(`{"session":{"sessionId":%q}}`, sessionID))
	frame := readSubscriptionFrame(t, conn)
	require.Nil(t, frame.Error)
	require.Equal(t, `"`+id+`"`, string(frame.ID))
	var result struct {
		SubscriptionID string `json:"subscriptionId"`
	}
	require.NoError(t, json.Unmarshal(frame.Result, &result))
	require.NotEmpty(t, result.SubscriptionID)
	return result.SubscriptionID
}

func subscriptionTestEvent(t *testing.T, text string) events.SessionEvent {
	return subscriptionTestEventFor(t, "observed", text)
}

func subscriptionTestEventFor(t *testing.T, sessionID, text string) events.SessionEvent {
	t.Helper()
	event, err := events.NewEvent(events.EventBase{
		EventID:     uuid.NewString(),
		Namespace:   "demo",
		SessionName: sessionID,
		SessionID:   sessionID,
		ItemID:      uuid.NewString(),
		Time:        time.Now().UTC(),
	}, events.AgentMessage{MessageID: uuid.NewString(), Content: []events.ContentBlock{events.Text(text)}}, events.Source{})
	require.NoError(t, err)
	return event
}

func readSessionEventFrame(t *testing.T, frame rpcSubscriptionFrame) (string, events.SessionEvent) {
	t.Helper()
	require.Equal(t, "session.event", frame.Method)
	var params struct {
		SubscriptionID string              `json:"subscriptionId"`
		Event          events.SessionEvent `json:"event"`
	}
	require.NoError(t, json.Unmarshal(frame.Params, &params))
	return params.SubscriptionID, params.Event
}

func readClosedFrame(t *testing.T, frame rpcSubscriptionFrame) (string, *runtime.OperationError) {
	t.Helper()
	require.Equal(t, "subscription.closed", frame.Method)
	var params struct {
		SubscriptionID string                  `json:"subscriptionId"`
		Error          *runtime.OperationError `json:"error"`
	}
	require.NoError(t, json.Unmarshal(frame.Params, &params))
	return params.SubscriptionID, params.Error
}

func TestRPCSubscribeReadyBeforeEventsAndUnsubscribeOrdering(t *testing.T) {
	fixture := newSubscriptionRPCFixture(t, 8, transport.Options{Token: "test-token"})
	first := subscriptionTestEvent(t, "first")
	second := subscriptionTestEvent(t, "second")
	third := subscriptionTestEvent(t, "third")
	fixture.api.setPrePublish(first, second, third)
	conn := subscriptionRPCConn(t, fixture)

	subscriptionID := subscribeRPC(t, conn, "subscribe", "observed")
	for _, want := range []events.SessionEvent{first, second, third} {
		gotID, event := readSessionEventFrame(t, readSubscriptionFrame(t, conn))
		require.Equal(t, subscriptionID, gotID)
		require.Equal(t, want.EventID, event.EventID)
	}

	sendCall(t, conn, "unsubscribe", "session.unsubscribe", fmt.Sprintf(`{"subscriptionId":%q}`, subscriptionID))
	closedID, closedErr := readClosedFrame(t, readSubscriptionFrame(t, conn))
	require.Equal(t, subscriptionID, closedID)
	require.Nil(t, closedErr)
	response := readSubscriptionFrame(t, conn)
	require.Equal(t, `"unsubscribe"`, string(response.ID))
	require.JSONEq(t, "null", string(response.Result))
	require.Nil(t, response.Error)

	fixture.api.setPrePublish()
	replacementID := subscribeRPC(t, conn, "resubscribe", "observed")
	require.NotEqual(t, subscriptionID, replacementID)

	sendCall(t, conn, "again", "session.unsubscribe", fmt.Sprintf(`{"subscriptionId":%q}`, subscriptionID))
	again := readSubscriptionFrame(t, conn)
	require.Equal(t, `"again"`, string(again.ID))
	require.JSONEq(t, "null", string(again.Result))

	sendCall(t, conn, "stop-replacement", "session.unsubscribe", fmt.Sprintf(`{"subscriptionId":%q}`, replacementID))
	_, replacementErr := readClosedFrame(t, readSubscriptionFrame(t, conn))
	require.Nil(t, replacementErr)
	stopped := readSubscriptionFrame(t, conn)
	require.Equal(t, `"stop-replacement"`, string(stopped.ID))
	require.JSONEq(t, "null", string(stopped.Result))
	require.NoError(t, fixture.broadcast.Publish(subscriptionTestEvent(t, "after response")))
	require.NoError(t, conn.SetReadDeadline(time.Now().Add(150*time.Millisecond)))
	_, _, err := conn.ReadMessage()
	require.Error(t, err)
	require.Zero(t, fixture.api.cancelCount())
}

func TestRPCSubscriptionNaturalTermination(t *testing.T) {
	fixture := newSubscriptionRPCFixture(t, 4, transport.Options{Token: "test-token"})
	conn := subscriptionRPCConn(t, fixture)
	subscriptionID := subscribeRPC(t, conn, "subscribe", "observed")

	subscription := fixture.api.subscription("observed")
	require.NotNil(t, subscription)
	require.NoError(t, subscription.Close())
	closedID, closedErr := readClosedFrame(t, readSubscriptionFrame(t, conn))
	require.Equal(t, subscriptionID, closedID)
	require.Nil(t, closedErr)

	replacementID := subscribeRPC(t, conn, "replacement", "observed")
	require.NotEqual(t, subscriptionID, replacementID)
	require.Zero(t, fixture.api.cancelCount())
}

func TestRPCSubscribeReservesOneSlotAtomically(t *testing.T) {
	fixture := newSubscriptionRPCFixture(t, 4, transport.Options{Token: "test-token"})
	entered := make(chan struct{}, 1)
	release := make(chan struct{})
	fixture.api.mu.Lock()
	fixture.api.entered = entered
	fixture.api.release = release
	fixture.api.mu.Unlock()
	conn := subscriptionRPCConn(t, fixture)

	sendCall(t, conn, "first", "session.subscribe", `{"session":{"sessionId":"observed"}}`)
	<-entered
	sendCall(t, conn, "second", "session.subscribe", `{"session":{"sessionId":"observed"}}`)
	conflict := readSubscriptionFrame(t, conn)
	require.NotNil(t, conflict.Error)
	require.Equal(t, `"second"`, string(conflict.ID))
	var operation runtime.OperationError
	require.NoError(t, json.Unmarshal(conflict.Error.Data, &operation))
	require.Equal(t, "conflict", operation.Code)
	require.Equal(t, "subscription_already_active", operation.Reason)

	close(release)
	first := readSubscriptionFrame(t, conn)
	require.Equal(t, `"first"`, string(first.ID))
	require.Nil(t, first.Error)
}

func TestRPCSubscriptionQueueOverflowClosesOnlyThatObservation(t *testing.T) {
	fixture := newSubscriptionRPCFixture(t, 1, transport.Options{Token: "test-token"})
	first := subscriptionTestEvent(t, "buffered")
	overflow := subscriptionTestEvent(t, "overflow")
	fixture.api.setPrePublish(first, overflow)
	conn := subscriptionRPCConn(t, fixture)

	subscriptionID := subscribeRPC(t, conn, "subscribe", "observed")
	gotID, event := readSessionEventFrame(t, readSubscriptionFrame(t, conn))
	require.Equal(t, subscriptionID, gotID)
	require.Equal(t, first.EventID, event.EventID)
	closedID, closedErr := readClosedFrame(t, readSubscriptionFrame(t, conn))
	require.Equal(t, subscriptionID, closedID)
	require.NotNil(t, closedErr)
	require.Equal(t, "resource_exhausted", closedErr.Code)
	require.Equal(t, "slow_subscriber", closedErr.Reason)

	fixture.api.setPrePublish()
	replacementID := subscribeRPC(t, conn, "replacement", "observed")
	require.NotEqual(t, subscriptionID, replacementID)
}

func TestRPCSubscriptionIDsAreConnectionLocal(t *testing.T) {
	fixture := newSubscriptionRPCFixture(t, 4, transport.Options{Token: "test-token"})
	owner := subscriptionRPCConn(t, fixture)
	other := subscriptionRPCConn(t, fixture)
	subscriptionID := subscribeRPC(t, owner, "subscribe", "observed")

	sendCall(t, other, "foreign", "session.unsubscribe", fmt.Sprintf(`{"subscriptionId":%q}`, subscriptionID))
	foreign := readSubscriptionFrame(t, other)
	require.Equal(t, `"foreign"`, string(foreign.ID))
	require.JSONEq(t, "null", string(foreign.Result))

	event := subscriptionTestEvent(t, "still observed")
	require.NoError(t, fixture.broadcast.Publish(event))
	gotID, got := readSessionEventFrame(t, readSubscriptionFrame(t, owner))
	require.Equal(t, subscriptionID, gotID)
	require.Equal(t, event.EventID, got.EventID)

	require.NoError(t, other.Close())
	later := subscriptionTestEvent(t, "after ordinary disconnect")
	require.NoError(t, fixture.broadcast.Publish(later))
	_, got = readSessionEventFrame(t, readSubscriptionFrame(t, owner))
	require.Equal(t, later.EventID, got.EventID)
	require.Zero(t, fixture.api.cancelCount())
}

func TestRPCSubscriptionWriteBlockIsolatesObserversAndOrdinaryRPC(t *testing.T) {
	fixture := newNamedSubscriptionRPCFixture(t, []string{"blocked", "observed"}, 64, transport.Options{
		Token:        "test-token",
		WriteTimeout: time.Second,
	})
	blocked := blockedRPCConn(t, fixture)
	healthy := subscriptionRPCConn(t, fixture)
	ordinary := subscriptionRPCConn(t, fixture)
	blockedID := subscribeRPC(t, blocked, "blocked-subscribe", "blocked")
	healthyID := subscribeRPC(t, healthy, "healthy-subscribe", "observed")

	large := subscriptionTestEventFor(t, "blocked", strings.Repeat("x", 12<<20))
	require.NoError(t, fixture.broadcasts["blocked"].Publish(large))

	received := make(chan string, 64)
	readerErrors := make(chan error, 1)
	go func() {
		for {
			frame, err := readSubscriptionFrameError(healthy, 10*time.Second)
			if err != nil {
				readerErrors <- err
				return
			}
			if frame.Method == "session.event" {
				var params struct {
					SubscriptionID string              `json:"subscriptionId"`
					Event          events.SessionEvent `json:"event"`
				}
				if json.Unmarshal(frame.Params, &params) == nil {
					received <- params.SubscriptionID + ":" + params.Event.EventID
				}
			}
		}
	}()

	marker := subscriptionTestEvent(t, "healthy marker")
	require.NoError(t, fixture.broadcasts["observed"].Publish(marker))

	healthyMarkerSeen := false
	require.Eventually(t, func() bool {
		for {
			select {
			case got := <-received:
				if got == healthyID+":"+marker.EventID {
					healthyMarkerSeen = true
				}
			case err := <-readerErrors:
				t.Logf("healthy reader failed: %v", err)
				return false
			default:
				return healthyMarkerSeen
			}
		}
	}, 8*time.Second, 10*time.Millisecond, "healthy observer did not receive its event while another write was blocked")

	sendCall(t, ordinary, "query", "session.get", `{"session":{"sessionId":"observed"}}`)
	query := readSubscriptionFrame(t, ordinary)
	require.Equal(t, `"query"`, string(query.ID))
	require.Nil(t, query.Error)

	require.NoError(t, blocked.Close())
	blockedSubscription := fixture.api.subscription("blocked")
	require.NotNil(t, blockedSubscription)
	require.Eventually(t, func() bool {
		select {
		case _, ok := <-blockedSubscription.Events():
			return !ok
		default:
			return false
		}
	}, 5*time.Second, 10*time.Millisecond, "connection failure did not release the blocked observation")

	later := subscriptionTestEvent(t, "after blocked eviction")
	require.NoError(t, fixture.broadcasts["observed"].Publish(later))
	gotLater := false
	require.Eventually(t, func() bool {
		for {
			select {
			case got := <-received:
				if got == healthyID+":"+later.EventID {
					gotLater = true
				}
			default:
				return gotLater
			}
		}
	}, 8*time.Second, 10*time.Millisecond, "healthy observation did not survive another observer's write timeout")
	require.NotEmpty(t, blockedID)
	require.Zero(t, fixture.api.cancelCount())
}
