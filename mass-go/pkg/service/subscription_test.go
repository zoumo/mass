package service_test

import (
	"context"
	"errors"
	"io"
	"time"

	"github.com/google/uuid"

	"github.com/zoumo/mass/mass-go/pkg/events"
	"github.com/zoumo/mass/mass-go/pkg/resource"
	"github.com/zoumo/mass/mass-go/pkg/store"
)

func (s *SessionWorkSuite) fact(id string, p events.Payload) events.SessionEvent {
	event, err := events.NewEvent(events.EventBase{EventID: uuid.NewString(), Namespace: "one", SessionName: "demo", SessionID: "original", ItemID: id, Time: time.Now().UTC()}, p, s.driver.source)
	s.Require().NoError(err)
	return event
}

func nextSubscription(sub *events.Subscription, ctx context.Context) (events.SessionEvent, error) {
	select {
	case e, ok := <-sub.Events():
		if ok {
			return e, nil
		}
		if err := sub.Err(); err != nil {
			return events.SessionEvent{}, err
		}
		return events.SessionEvent{}, io.EOF
	case <-ctx.Done():
		return events.SessionEvent{}, ctx.Err()
	}
}

type observingHandler struct {
	required bool
	handle   func(events.SessionEvent) error
}

func (h *observingHandler) Required() bool                     { return h.required }
func (h *observingHandler) Handle(e events.SessionEvent) error { return h.handle(e) }
func (h *observingHandler) Close() error                       { return nil }

func (s *SessionWorkSuite) TestCommittedHistorySurvivesPartialPublicationWithoutRetry() {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	sub, err := s.svc.SubscribeEvents(s.ref)
	s.Require().NoError(err)
	defer sub.Close()
	b, err := s.svc.EventBroadcast(s.ref)
	s.Require().NoError(err)
	processed := make(chan string, 2)
	s.Require().NoError(b.AddHandler(&observingHandler{required: true, handle: func(e events.SessionEvent) error {
		page, err := s.svc.ListHistoryEvents(s.ref, store.HistoryQuery{})
		if err != nil {
			return err
		}
		found := false
		for _, fact := range page.Events {
			if fact.EventID == e.EventID {
				found = true
			}
		}
		if !found {
			return errors.New("published before history commit")
		}
		processed <- e.EventID
		return errors.New("required business failure")
	}}))
	for _, id := range []string{"first", "second"} {
		e := s.fact(id, events.AgentMessage{MessageID: id, Content: []events.ContentBlock{events.Text(id)}})
		s.Require().NoError(s.driver.pipe.Send(ctx, e))
		got, err := nextSubscription(sub, ctx)
		s.Require().NoError(err)
		s.Equal(e.EventID, got.EventID)
		select {
		case id := <-processed:
			s.Equal(e.EventID, id)
		case <-ctx.Done():
			s.FailNow("handler did not see committed event")
		}
	}
	page, err := s.svc.ListHistoryEvents(s.ref, store.HistoryQuery{})
	s.Require().NoError(err)
	s.Len(page.Events, 2)
	s.Empty(sub.Events(), "no automatic retry or historical query publication")
}

func (s *SessionWorkSuite) TestRuntimeStopKeepsBroadcastAndSubscriptions() {
	b, err := s.svc.EventBroadcast(s.ref)
	s.Require().NoError(err)
	sub, err := s.svc.SubscribeEvents(s.ref)
	s.Require().NoError(err)
	defer sub.Close()
	_, err = s.svc.StopRuntime(context.Background(), "agent")
	s.Require().NoError(err)
	again, err := s.svc.EventBroadcast(resource.SessionRef{Namespace: "one", Name: "demo"})
	s.Require().NoError(err)
	s.Same(b, again)
	e := s.fact("offline", events.SessionError{Message: "realtime only"})
	s.Require().NoError(b.Publish(e))
	s.Equal(e.EventID, (<-sub.Events()).EventID)
	newer, err := s.svc.SubscribeEvents(s.ref)
	s.Require().NoError(err)
	s.Require().NoError(newer.Close())
}

func (s *SessionWorkSuite) TestNoSubscribersStillPersistHistory() {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	e := s.fact("unobserved", events.AgentMessage{MessageID: "unobserved", Content: []events.ContentBlock{events.Text("fact")}})
	s.Require().NoError(s.driver.pipe.Send(ctx, e))
	s.Require().Eventually(func() bool {
		page, err := s.svc.ListHistoryEvents(s.ref, store.HistoryQuery{})
		return err == nil && len(page.Events) == 1 && page.Events[0].EventID == e.EventID
	}, time.Second, time.Millisecond)
	sub, err := s.svc.SubscribeEvents(s.ref)
	s.Require().NoError(err)
	s.Empty(sub.Events())
	s.Require().NoError(sub.Close())
}

func (s *RuntimeStopSuite) TestShutdownCleansSettledBroadcastWhenAnotherRuntimeFails() {
	original, err := s.svc.SubscribeEvents(s.ref)
	s.Require().NoError(err)
	defer original.Close()
	offline, err := s.meta.CreateSession(resource.Session{Metadata: resource.Metadata{Namespace: "two", Name: "offline"}, Spec: resource.SessionSpec{RuntimeRef: "unstarted"}, Status: resource.SessionStatus{SessionID: "offline", State: "offline"}})
	s.Require().NoError(err)
	other, err := s.svc.SubscribeEvents(resource.SessionRef{SessionID: offline.Status.SessionID})
	s.Require().NoError(err)
	defer other.Close()
	s.d.failStop.Store(true)
	s.Require().Error(s.svc.Shutdown(context.Background()))
	select {
	case _, ok := <-other.Events():
		s.False(ok, "settled Broadcast should close")
	default:
		s.Fail("failed Runtime prevented unrelated cleanup")
	}
	select {
	case <-original.Events():
		s.Fail("unsettled upstream Broadcast was closed")
	default:
	}
	s.d.failStop.Store(false)
	s.Require().NoError(s.svc.Shutdown(context.Background()))
	_, ok := <-original.Events()
	s.False(ok)
}

func (s *RuntimeStopSuite) TestCloseSettlesFinalDeliveryFenceAndKeepsRealtimeObjects() {
	b, err := s.svc.EventBroadcast(s.ref)
	s.Require().NoError(err)
	sub, err := s.svc.SubscribeEvents(s.ref)
	s.Require().NoError(err)
	defer sub.Close()
	final, err := events.NewEvent(events.EventBase{EventID: "01234567-89ab-4cde-8fab-0123456789ab", Namespace: "one", SessionName: "demo", SessionID: "original", ItemID: "final", Time: time.Now()}, events.AgentMessage{MessageID: "final", Content: []events.ContentBlock{events.Text("final before release")}}, s.d.source)
	s.Require().NoError(err)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	publishEntered := make(chan struct{})
	publishRelease := make(chan struct{})
	release := func(ch chan struct{}) {
		select {
		case <-ch:
		default:
			close(ch)
		}
	}
	defer release(s.history.release)
	defer release(publishRelease)
	s.Require().NoError(b.AddHandler(&observingHandler{handle: func(events.SessionEvent) error {
		close(publishEntered)
		select {
		case <-publishRelease:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}}))
	s.history.blocked.Store(true)
	// Match the existing Codex/Claude CloseSession delivery fence. This peer and
	// all facts are synthetic; the history append still uses a real temporary file.
	s.d.onClose = func(ctx context.Context) error {
		if err := s.d.pipe.Send(ctx, final); err != nil {
			return err
		}
		return s.d.pipe.Flush(ctx)
	}
	done := make(chan error, 1)
	go func() { _, err := s.svc.CloseSession(context.Background(), s.ref); done <- err }()
	select {
	case <-s.history.entered:
	case <-time.After(time.Second):
		s.FailNow("final history did not enter")
	}
	select {
	case err := <-done:
		s.Fail("close completed before history/publish settled", "%v", err)
	default:
	}
	release(s.history.release)
	select {
	case <-publishEntered:
	case <-ctx.Done():
		s.FailNow("final publish did not enter")
	}
	select {
	case err := <-done:
		s.Fail("close completed before publish settled", "%v", err)
	default:
	}
	release(publishRelease)
	s.Require().NoError(<-done)
	got, err := nextSubscription(sub, ctx)
	s.Require().NoError(err)
	s.Equal(final.EventID, got.EventID)
	page, err := s.svc.ListHistoryEvents(s.ref, store.HistoryQuery{})
	s.Require().NoError(err)
	s.Require().Len(page.Events, 1)
	again, err := s.svc.EventBroadcast(s.ref)
	s.Require().NoError(err)
	s.Same(b, again)
	s.d.onClose = nil
	_, err = s.svc.ResumeSession(context.Background(), s.ref)
	s.Require().NoError(err)
	again, err = s.svc.EventBroadcast(s.ref)
	s.Require().NoError(err)
	s.Same(b, again)
	// Restoring keeps the same Subscription alive; cancellation is its own
	// terminal action and does not close the restored Session.
	s.Require().NoError(sub.Close())
}
