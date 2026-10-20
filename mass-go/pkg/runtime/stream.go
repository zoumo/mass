package runtime

import (
	"context"
	"errors"
	"io"
	"sync"
	"sync/atomic"
)

type streamEntry struct {
	event   SessionEvent
	barrier chan struct{}
}

// EventPipe is a bounded, single-consumer stream. End preserves queued facts.
type EventPipe struct {
	queue   chan streamEntry
	done    chan struct{}
	mu      sync.Mutex
	err     error
	once    sync.Once
	reading atomic.Bool
}

func NewEventPipe(capacity int) *EventPipe {
	return &EventPipe{queue: make(chan streamEntry, capacity), done: make(chan struct{})}
}

func (p *EventPipe) Send(ctx context.Context, e SessionEvent) error {
	cloned, err := e.Clone()
	if err != nil {
		return err
	}
	select {
	case <-p.done:
		return errors.New("event stream ended")
	default:
	}
	select {
	case p.queue <- streamEntry{event: cloned}:
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
	return p.next(ctx)
}

func (p *EventPipe) next(ctx context.Context) (SessionEvent, error) {
	for {
		var entry streamEntry
		select {
		case entry = <-p.queue:
		default:
			select {
			case entry = <-p.queue:
			case <-p.done:
				select {
				case entry = <-p.queue:
				default:
					p.mu.Lock()
					err := p.err
					p.mu.Unlock()
					if err == nil {
						err = io.EOF
					}
					return SessionEvent{}, err
				}
			case <-ctx.Done():
				return SessionEvent{}, ctx.Err()
			}
		}
		if entry.barrier != nil {
			close(entry.barrier)
			continue
		}
		return entry.event, nil
	}
}

// Flush waits until the sole consumer has routed every preceding entry.
// Call after a Session producer has ended; it is not a business event.
func (p *EventPipe) Flush(ctx context.Context) error {
	b := make(chan struct{})
	select {
	case p.queue <- streamEntry{barrier: b}:
	case <-ctx.Done():
		return ctx.Err()
	case <-p.done:
		return errors.New("stream ended before drain")
	}
	select {
	case <-b:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
