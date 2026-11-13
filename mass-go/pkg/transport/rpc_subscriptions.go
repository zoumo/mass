package transport

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"sync"

	rpc2 "github.com/sourcegraph/jsonrpc2"

	"github.com/zoumo/mass/mass-go/pkg/events"
	rpcwire "github.com/zoumo/mass/mass-go/pkg/jsonrpc"
	"github.com/zoumo/mass/mass-go/pkg/resource"
	"github.com/zoumo/mass/mass-go/pkg/runtime"
	"github.com/zoumo/mass/mass-go/pkg/service"
)

type subscribedRPC struct {
	SubscriptionID string `json:"subscriptionId"`
}

type sessionEventRPC struct {
	SubscriptionID string              `json:"subscriptionId"`
	Event          events.SessionEvent `json:"event"`
}

type subscriptionClosedRPC struct {
	SubscriptionID string                  `json:"subscriptionId"`
	Error          *runtime.OperationError `json:"error,omitempty"`
}

type unsubscribeRPCParams struct {
	SubscriptionID *string `json:"subscriptionId"`
}

type rpcSubscription struct {
	id    string
	owner *rpcConnection
	conn  *rpc2.Conn

	ready     chan struct{}
	readyOnce sync.Once
	startErr  error

	mu       sync.Mutex
	sub      *service.Subscription
	aborted  bool
	shutdown error

	stop chan rpcSubscriptionStop
	done chan struct{}
}

type rpcSubscriptionStop struct {
	ack   chan struct{}
	cause error
}

func subscriptionAlreadyActive() *Error {
	return &Error{
		Code:    "conflict",
		Reason:  "subscription_already_active",
		Message: "Connection already has an active subscription",
	}
}

func decodeSubscriptionID(raw *json.RawMessage) (string, error) {
	var params unsubscribeRPCParams
	if decodeRPCParams(raw, &params) != nil || params.SubscriptionID == nil {
		return "", errInvalidRPCParams
	}
	return *params.SubscriptionID, nil
}

func (c *rpcConnection) reserveSubscription(conn *rpc2.Conn) (*rpcSubscription, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.disconnected || c.subscription != nil {
		return nil, false
	}
	c.nextSubscription++
	s := &rpcSubscription{
		id:    strconv.FormatUint(c.nextSubscription, 10),
		owner: c,
		conn:  conn,
		ready: make(chan struct{}),
		stop:  make(chan rpcSubscriptionStop),
		done:  make(chan struct{}),
	}
	c.subscription = s
	return s, true
}

func (c *rpcConnection) releaseSubscription(s *rpcSubscription) {
	c.mu.Lock()
	if c.subscription == s {
		c.subscription = nil
	}
	c.mu.Unlock()
	c.maybeDrained()
}

func (c *rpcConnection) disconnect() {
	c.mu.Lock()
	c.disconnected = true
	s := c.subscription
	c.mu.Unlock()
	c.maybeDrained()
	if s != nil {
		s.abort()
	}
}

func (c *rpcConnection) subscriptionByID(id string) *rpcSubscription {
	c.mu.Lock()
	s := c.subscription
	c.mu.Unlock()
	if s == nil || s.id != id {
		return nil
	}
	<-s.ready
	if s.startErr != nil || s.subscription() == nil {
		return nil
	}
	return s
}

func (c *rpcConnection) dispatchSubscribe(
	ctx context.Context,
	conn *rpc2.Conn,
	req *rpc2.Request,
	key string,
	ref resource.SessionRef,
	s *rpcSubscription,
) {
	defer c.end(key)
	if c.api == nil {
		c.failSubscription(s)
		replyInternalError(ctx, conn, req.ID)
		return
	}
	sub, err := c.api.SubscribeEvents(ref)
	if err != nil {
		c.failSubscription(s)
		replyServiceError(ctx, conn, req.ID, err)
		return
	}
	if !s.setSubscription(sub) {
		c.releaseSubscription(s)
		s.finish()
		return
	}

	response := &rpc2.Response{ID: req.ID}
	if err := response.SetResult(subscribedRPC{SubscriptionID: s.id}); err != nil {
		c.failSubscription(s)
		replyInternalError(ctx, conn, req.ID)
		return
	}
	// This write completes before the forwarder is started, so no event for
	// this subscription can overtake its successful response.
	if err := conn.SendResponse(ctx, response); err != nil {
		c.failSubscription(s)
		return
	}
	s.readyOnce.Do(func() { close(s.ready) })
	go s.forward()
}

func (c *rpcConnection) failSubscription(s *rpcSubscription) {
	s.fail(nil)
	c.releaseSubscription(s)
	s.finish()
}

func (c *rpcConnection) dispatchUnsubscribe(
	ctx context.Context,
	conn *rpc2.Conn,
	req *rpc2.Request,
	key, id string,
) {
	defer c.end(key)
	s := c.subscriptionByID(id)
	if s == nil {
		c.replyNull(ctx, conn, req.ID)
		return
	}

	ack := make(chan struct{})
	select {
	case s.stop <- rpcSubscriptionStop{ack: ack}:
		<-ack
		<-s.done
	case <-s.done:
	}
	c.replyNull(ctx, conn, req.ID)
}

func (c *rpcConnection) replyNull(ctx context.Context, conn *rpc2.Conn, id rpc2.ID) {
	response := &rpc2.Response{ID: id}
	if err := response.SetResult(nil); err != nil {
		return
	}
	_ = conn.SendResponse(ctx, response)
}

func (s *rpcSubscription) forward() {
	sub := s.subscription()
	if sub == nil {
		s.owner.releaseSubscription(s)
		s.finish()
		return
	}
	defer func() {
		s.owner.releaseSubscription(s)
		s.finish()
	}()
	defer s.abort()

	for {
		select {
		case request := <-s.stop:
			s.abort()
			for event := range sub.Events() {
				if err := s.notifyEvent(event); err != nil {
					if errors.Is(err, rpcwire.ErrLossyJSON) {
						s.sendClosed(err)
					} else {
						_ = s.conn.Close()
					}
					close(request.ack)
					return
				}
			}
			cause := request.cause
			if cause == nil {
				cause = sub.Err()
			}
			s.sendClosed(cause)
			close(request.ack)
			return
		case event, ok := <-sub.Events():
			if !ok {
				cause := sub.Err()
				if shutdown := s.shutdownCause(); shutdown != nil {
					cause = shutdown
				}
				s.sendClosed(cause)
				return
			}
			if err := s.notifyEvent(event); err != nil {
				if errors.Is(err, rpcwire.ErrLossyJSON) {
					s.sendClosed(err)
					return
				}
				_ = s.conn.Close()
				return
			}
		}
	}
}

func (s *rpcSubscription) markShutdown(cause error) {
	s.mu.Lock()
	s.shutdown = cause
	s.mu.Unlock()
}

func (s *rpcSubscription) shutdownCause() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.shutdown
}

func (s *rpcSubscription) notifyEvent(event events.SessionEvent) error {
	params := sessionEventRPC{
		SubscriptionID: s.id,
		Event:          event,
	}
	if err := rpcwire.ValidateLossless(params); err != nil {
		return err
	}
	if err := rpcwire.ValidateLossless(event.Payload()); err != nil {
		return err
	}
	return s.conn.Notify(context.Background(), "session.event", params)
}

func (s *rpcSubscription) abort() {
	s.mu.Lock()
	s.aborted = true
	sub := s.sub
	s.mu.Unlock()
	if sub != nil {
		_ = sub.Close()
	}
	s.readyOnce.Do(func() { close(s.ready) })
}

func (s *rpcSubscription) fail(err error) {
	s.mu.Lock()
	s.aborted = true
	sub := s.sub
	s.mu.Unlock()
	if sub != nil {
		_ = sub.Close()
	}
	s.startErr = err
	s.readyOnce.Do(func() { close(s.ready) })
}

func (s *rpcSubscription) setSubscription(sub *service.Subscription) bool {
	s.mu.Lock()
	if s.aborted {
		s.mu.Unlock()
		_ = sub.Close()
		return false
	}
	s.sub = sub
	s.mu.Unlock()
	return true
}

func (s *rpcSubscription) subscription() *service.Subscription {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.sub
}

func (s *rpcSubscription) finish() {
	close(s.done)
	s.owner.maybeDrained()
}

func (s *rpcSubscription) sendClosed(cause error) {
	notification := subscriptionClosedRPC{SubscriptionID: s.id}
	if cause != nil {
		public, ok := publicError(cause)
		if !ok {
			public = &Error{
				Code:    "internal",
				Reason:  "event_delivery_failed",
				Message: "Event delivery failed",
			}
		}
		notification.Error = public
	}
	if rpcwire.ValidateLossless(notification) != nil {
		notification.Error = &Error{
			Code:    "internal",
			Reason:  "event_delivery_failed",
			Message: "Event delivery failed",
		}
	}
	if err := s.conn.Notify(context.Background(), "subscription.closed", notification); err != nil {
		_ = s.conn.Close()
	}
}
