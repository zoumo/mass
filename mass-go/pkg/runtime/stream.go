package runtime

import (
	"context"
	"errors"
	"io"
	"sync"
	"sync/atomic"
)

// EventPipe is a bounded, single-consumer stream. End preserves queued facts.
type EventPipe struct {
	queue   chan SessionEvent
	done    chan struct{}
	mu      sync.Mutex
	err     error
	once    sync.Once
	reading atomic.Bool
}

func NewEventPipe(capacity int) *EventPipe {
	return &EventPipe{queue: make(chan SessionEvent, capacity), done: make(chan struct{})}
}
func (p *EventPipe) Send(ctx context.Context, e SessionEvent) error {
	copy, err := e.Clone()
	if err != nil {
		return err
	}
	select {
	case <-p.done:
		return errors.New("event stream ended")
	default:
	}
	select {
	case p.queue <- copy:
		return nil
	case <-p.done:
		return errors.New("event stream ended")
	case <-ctx.Done():
		return ctx.Err()
	}
}
func (p *EventPipe) End(err error) {
	p.once.Do(func() { p.mu.Lock(); p.err = err; p.mu.Unlock(); close(p.done) })
}
func (p *EventPipe) Next(ctx context.Context) (SessionEvent, error) {
	if !p.reading.CompareAndSwap(false, true) {
		return SessionEvent{}, errors.New("concurrent stream Next")
	}
	defer p.reading.Store(false)
	select {
	case e := <-p.queue:
		return e, nil
	default:
	}
	select {
	case e := <-p.queue:
		return e, nil
	case <-p.done:
		select {
		case e := <-p.queue:
			return e, nil
		default:
		}
		p.mu.Lock()
		err := p.err
		p.mu.Unlock()
		if err == nil {
			err = io.EOF
		}
		return SessionEvent{}, err
	case <-ctx.Done():
		return SessionEvent{}, ctx.Err()
	}
}
