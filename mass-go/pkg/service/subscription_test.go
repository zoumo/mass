package service_test

import (
	"context"
	"errors"
	"io"
	"time"

	"github.com/zoumo/mass/mass-go/pkg/events"
	"github.com/zoumo/mass/mass-go/pkg/resource"
	"github.com/zoumo/mass/mass-go/pkg/store"
)

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
