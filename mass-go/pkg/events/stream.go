package events

import (
	"context"
	"errors"
	"io"
	"sync"
	"sync/atomic"
)

// Stream delivers ordered events to a single consumer.
type Stream interface {
	Next(context.Context) (SessionEvent, error)
}

type streamEntry struct {
	event    SessionEvent
	sequence uint64
}

// EventPipe shares a fixed total budget fairly: each Session may occupy at most
// half (rounded up) of it. Next serves nonempty Sessions round-robin. Producers
// wait with their caller context, never in another forwarding queue.
type EventPipe struct {
	mu                          sync.Mutex
	queues                      map[string][]streamEntry
	ready                       []string
	count, capacity, perSession int
	sequence                    uint64
	inFlight                    uint64
	changed                     chan struct{}
	ended                       bool
	err                         error
	reading                     atomic.Bool
}

func NewEventPipe(capacity int) *EventPipe {
	if capacity < 1 {
		panic("event pipe capacity must be positive")
	}
	return &EventPipe{capacity: capacity, perSession: (capacity + 1) / 2, queues: map[string][]streamEntry{}, changed: make(chan struct{})}
}

func (p *EventPipe) signal() {
	close(p.changed)
	p.changed = make(chan struct{})
}

func (p *EventPipe) Send(ctx context.Context, e SessionEvent) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	cloned, err := e.Clone()
	if err != nil {
		return err
	}
	for {
		p.mu.Lock()
		if err := ctx.Err(); err != nil {
			p.mu.Unlock()
			return err
		}
		if p.ended {
			p.mu.Unlock()
			return errors.New("event stream ended")
		}
		queue := p.queues[e.SessionID]
		if p.count < p.capacity && len(queue) < p.perSession {
			p.sequence++
			if len(queue) == 0 {
				p.ready = append(p.ready, e.SessionID)
			}
			p.queues[e.SessionID] = append(queue, streamEntry{event: cloned, sequence: p.sequence})
			p.count++
			p.signal()
			p.mu.Unlock()
			return nil
		}
		changed := p.changed
		p.mu.Unlock()
		select {
		case <-changed:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

func (p *EventPipe) End(err error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.ended {
		p.ended = true
		p.err = err
		p.signal()
	}
}

func (p *EventPipe) Next(ctx context.Context) (SessionEvent, error) {
	if !p.reading.CompareAndSwap(false, true) {
		return SessionEvent{}, errors.New("concurrent stream Next")
	}
	defer p.reading.Store(false)
	// A new Next is the sole consumer's acknowledgement that the last returned
	// event was routed. Merely removing it from this queue is not that proof.
	p.mu.Lock()
	p.inFlight = 0
	p.signal()
	p.mu.Unlock()
	for {
		p.mu.Lock()
		if len(p.ready) > 0 {
			id := p.ready[0]
			p.ready = p.ready[1:]
			queue := p.queues[id]
			entry := queue[0]
			queue[0] = streamEntry{}
			queue = queue[1:]
			if len(queue) > 0 {
				p.ready = append(p.ready, id)
				p.queues[id] = queue
			} else {
				delete(p.queues, id)
			}
			p.count--
			p.inFlight = entry.sequence
			p.signal()
			p.mu.Unlock()
			return entry.event, nil
		}
		if p.ended {
			err := p.err
			if err == nil {
				err = io.EOF
			}
			p.mu.Unlock()
			return SessionEvent{}, err
		}
		changed := p.changed
		p.mu.Unlock()
		select {
		case <-changed:
		case <-ctx.Done():
			return SessionEvent{}, ctx.Err()
		}
	}
}

// Flush is a routing watermark, not persistence or native-release evidence. It
// waits for all entries admitted before this call, across round-robin ordering.
// Later producers cannot postpone this watermark. No barrier queue is added.
func (p *EventPipe) Flush(ctx context.Context) error {
	p.mu.Lock()
	target := p.sequence
	for {
		pending := p.inFlight != 0 && p.inFlight <= target
		for _, queue := range p.queues {
			if len(queue) > 0 && queue[0].sequence <= target {
				pending = true
				break
			}
		}
		if !pending {
			p.mu.Unlock()
			return nil
		}
		changed := p.changed
		p.mu.Unlock()
		select {
		case <-changed:
		case <-ctx.Done():
			return ctx.Err()
		}
		p.mu.Lock()
	}
}
