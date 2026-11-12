package client

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"

	rpc2 "github.com/sourcegraph/jsonrpc2"

	"github.com/zoumo/mass/mass-go/pkg/events"
	rpcwire "github.com/zoumo/mass/mass-go/pkg/jsonrpc"
	"github.com/zoumo/mass/mass-go/pkg/resource"
	"github.com/zoumo/mass/mass-go/pkg/runtime"
)

type subscribeResult struct {
	SubscriptionID string `json:"subscriptionId"`
}

type sessionEventNotification struct {
	SubscriptionID string              `json:"subscriptionId"`
	Event          events.SessionEvent `json:"event"`
}

type subscriptionClosedNotification struct {
	SubscriptionID string                  `json:"subscriptionId"`
	Error          *runtime.OperationError `json:"error,omitempty"`
}

type unsubscribeParams struct {
	SubscriptionID string `json:"subscriptionId"`
}

// Subscription is one dedicated WebSocket observation with a bounded local
// queue. Next is the only consumer of that queue.
type Subscription struct {
	client *Client
	conn   *rpc2.Conn
	stream *rpcwire.Stream
	cancel context.CancelFunc

	idReady  chan struct{}
	ready    sync.Once
	id       string
	setupErr error

	mu                 sync.Mutex
	queue              chan events.SessionEvent
	done               chan struct{}
	terminal           error
	finished           bool
	pendingUnsubscribe bool

	closeOnce sync.Once
	closeErr  error
}

// Subscribe opens a new authenticated, initialized WebSocket and waits for the
// subscribe response before returning the observation.
func (c *Client) Subscribe(ctx context.Context, ref resource.SessionRef) (_ *Subscription, resultErr error) {
	if err := validateSessionRef(ref); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	subscription := &Subscription{
		client:  c,
		idReady: make(chan struct{}),
		queue:   make(chan events.SessionEvent, c.subscriptionQueueCapacity),
		done:    make(chan struct{}),
	}
	conn, stream, cancel, err := c.dial(ctx, clientHandler{subscription: subscription})
	if err != nil {
		return nil, err
	}
	subscription.conn = conn
	subscription.stream = stream
	subscription.cancel = cancel

	if !c.registerSubscription(subscription) {
		subscription.failSetup(errors.New("WebSocket client is closed"))
		_ = subscription.closeTransport()
		return nil, errors.New("WebSocket client is closed")
	}
	defer func() {
		if resultErr != nil {
			subscription.finish(resultErr)
			_ = subscription.closeTransport()
		}
	}()

	go func() {
		<-conn.DisconnectNotify()
		subscription.connectionLost()
	}()

	if err := c.initializeConnection(ctx, conn, stream, nil, nil, false); err != nil {
		subscription.failSetup(err)
		return nil, err
	}

	var raw json.RawMessage
	if err := conn.Call(ctx, "session.subscribe", sessionRefParams{Session: ref}, &raw, rpc2.StringID()); err != nil {
		err = c.callError(ctx, stream, err)
		subscription.failSetup(err)
		return nil, err
	}
	var subscribed subscribeResult
	if err := rpcwire.DecodeStrict(raw, &subscribed); err != nil || subscribed.SubscriptionID == "" {
		subscription.failSetup(errors.New("invalid session.subscribe result"))
		return nil, errors.New("invalid session.subscribe result")
	}
	subscription.id = subscribed.SubscriptionID
	subscription.ready.Do(func() { close(subscription.idReady) })
	return subscription, nil
}

func (c *Client) registerSubscription(s *Subscription) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return false
	}
	c.observations[s] = struct{}{}
	return true
}

func (c *Client) unregisterSubscription(s *Subscription) {
	c.mu.Lock()
	delete(c.observations, s)
	c.mu.Unlock()
}

func (s *Subscription) failSetup(err error) {
	s.setupErr = err
	s.ready.Do(func() { close(s.idReady) })
}

// Next returns the next ordered event. A clean subscription.closed is io.EOF.
// EOF without that notification is reported as an abnormal termination.
func (s *Subscription) Next(ctx context.Context) (events.SessionEvent, error) {
	for {
		select {
		case event := <-s.queue:
			return event, nil
		default:
		}
		select {
		case event := <-s.queue:
			return event, nil
		case <-ctx.Done():
			return events.SessionEvent{}, ctx.Err()
		case <-s.done:
			select {
			case event := <-s.queue:
				return event, nil
			default:
				return events.SessionEvent{}, s.Err()
			}
		}
	}
}

// Err returns the fixed terminal cause, or nil while the observation is active.
func (s *Subscription) Err() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.terminal
}

// Unsubscribe explicitly ends this connection's subscription. The successful
// response is observed only after the server has sent the terminal notification.
func (s *Subscription) Unsubscribe(ctx context.Context) error {
	s.mu.Lock()
	if s.finished {
		err := s.terminal
		s.mu.Unlock()
		return err
	}
	s.pendingUnsubscribe = true
	s.mu.Unlock()

	<-s.idReady
	if s.setupErr != nil {
		return s.setupErr
	}
	var raw json.RawMessage
	err := s.conn.Call(ctx, "session.unsubscribe", unsubscribeParams{SubscriptionID: s.id}, &raw, rpc2.StringID())
	if err != nil {
		err = s.client.callError(ctx, s.stream, err)
		s.finish(err)
		_ = s.closeTransport()
		return err
	}
	if err := s.Err(); err != nil && !errors.Is(err, io.EOF) {
		_ = s.closeTransport()
		return err
	}
	if err := s.Err(); err == nil {
		err = errors.New("unsubscribe response arrived without subscription.closed")
		s.finish(err)
	}
	closeErr := s.closeTransport()
	if err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	return closeErr
}

// Close releases only this observation's connection. It does not send any
// Session close or cancel command.
func (s *Subscription) Close() error {
	s.finish(io.EOF)
	return s.closeTransport()
}

func (s *Subscription) closeTransport() error {
	s.closeOnce.Do(func() {
		if s.cancel != nil {
			s.cancel()
		}
		if s.conn != nil {
			if err := s.conn.Close(); !errors.Is(err, rpc2.ErrClosed) {
				s.closeErr = err
			}
		}
		if s.client != nil {
			s.client.unregisterSubscription(s)
		}
	})
	return s.closeErr
}

func (s *Subscription) handle(req *rpc2.Request) {
	if !req.Notif {
		return
	}
	<-s.idReady
	if s.setupErr != nil {
		return
	}
	switch req.Method {
	case "session.event":
		var notification sessionEventNotification
		if req.Params == nil || rpcwire.DecodeStrict(*req.Params, &notification) != nil ||
			notification.SubscriptionID != s.id {
			s.protocolFailure("invalid session.event notification")
			return
		}
		s.enqueue(notification.Event)
	case "subscription.closed":
		var notification subscriptionClosedNotification
		if req.Params == nil || rpcwire.DecodeStrict(*req.Params, &notification) != nil ||
			notification.SubscriptionID != s.id {
			s.protocolFailure("invalid subscription.closed notification")
			return
		}
		if notification.Error != nil {
			s.finishFromServer(notification.Error)
		} else {
			s.finishFromServer(io.EOF)
		}
	default:
		s.protocolFailure("unexpected server notification")
	}
}

func (s *Subscription) enqueue(event events.SessionEvent) {
	s.mu.Lock()
	if s.finished {
		s.mu.Unlock()
		s.protocolFailure("event received after subscription.closed")
		return
	}
	select {
	case s.queue <- event:
		s.mu.Unlock()
	default:
		s.setFinishedLocked(&runtime.OperationError{
			Code:    "resource_exhausted",
			Reason:  "slow_subscriber",
			Message: "Client subscription queue capacity exceeded",
		})
		s.mu.Unlock()
		go func() { _ = s.closeTransport() }()
	}
}

func (s *Subscription) finish(err error) {
	s.mu.Lock()
	s.setFinishedLocked(err)
	s.mu.Unlock()
}

func (s *Subscription) finishFromServer(err error) {
	closeNow := false
	s.mu.Lock()
	if !s.finished {
		s.setFinishedLocked(err)
		closeNow = !s.pendingUnsubscribe
	}
	s.mu.Unlock()
	if closeNow {
		go func() { _ = s.closeTransport() }()
	}
}

func (s *Subscription) setFinishedLocked(err error) {
	if s.finished {
		return
	}
	s.finished = true
	s.terminal = err
	close(s.done)
}

func (s *Subscription) connectionLost() {
	if s.Err() != nil {
		return
	}
	if profileErr := s.stream.ProtocolError(); profileErr != nil {
		s.finish(profileErr)
		return
	}
	s.finish(fmt.Errorf("WebSocket observation ended without subscription.closed: %w", io.ErrUnexpectedEOF))
}

func (s *Subscription) protocolFailure(message string) {
	s.finish(errors.New(message))
	go func() { _ = s.closeTransport() }()
}
