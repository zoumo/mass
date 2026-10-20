package service

import (
	"context"
	"io"
	"sync"

	"github.com/zoumo/mass/mass-go/pkg/events"
	"github.com/zoumo/mass/mass-go/pkg/runtime"
)

// Subscription observes future events only. Closing it has no Agent side effect.
type Subscription struct {
	mu     sync.Mutex
	queue  chan events.SessionEvent
	done   chan struct{}
	err    error
	closed bool
	remove func(*Subscription)
}

func newSubscription(remove func(*Subscription)) *Subscription {
	return &Subscription{queue: make(chan events.SessionEvent, queueCapacity), done: make(chan struct{}), remove: remove}
}

func (s *Subscription) finish(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}
	s.closed = true
	s.err = err
	close(s.done)
}

func (s *Subscription) publish(e events.SessionEvent) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}
	select {
	case s.queue <- e:
	default:
		s.err = runtime.Exhausted("slow_subscriber")
		s.closed = true
		close(s.done)
	}
}

func (s *Subscription) Next(ctx context.Context) (events.SessionEvent, error) {
	select {
	case <-s.done:
		s.mu.Lock()
		err := s.err
		s.mu.Unlock()
		if err == nil {
			err = io.EOF
		}
		return events.SessionEvent{}, err
	default:
	}
	select {
	case e := <-s.queue:
		return e, nil
	case <-s.done:
		s.mu.Lock()
		err := s.err
		s.mu.Unlock()
		if err == nil {
			err = io.EOF
		}
		return events.SessionEvent{}, err
	case <-ctx.Done():
		return events.SessionEvent{}, ctx.Err()
	}
}
func (s *Subscription) Close() error { s.finish(nil); s.remove(s); return nil }

func (s *Subscription) ended() bool { s.mu.Lock(); defer s.mu.Unlock(); return s.closed }
