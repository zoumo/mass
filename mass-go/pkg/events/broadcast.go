package events

import (
	"errors"
	"fmt"
	"log/slog"
	"reflect"
	"sync"
)

// EventHandler owns its resources after registration. Required is optional and
// defaults to false; if implemented, Required() bool is read once on admission.
type EventHandler interface {
	Handle(SessionEvent) error
	Close() error
}

// Subscription is a cancellable, single-consumer realtime stream.
type Subscription struct {
	mu     sync.Mutex
	queue  chan SessionEvent
	err    error
	closed bool
	remove func()
}

func (s *Subscription) Events() <-chan SessionEvent { return s.queue }
func (s *Subscription) Err() error                  { s.mu.Lock(); defer s.mu.Unlock(); return s.err }
func (s *Subscription) Close() error                { s.end(nil); return nil }
func (s *Subscription) end(err error) {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.finishLocked(err)
	s.mu.Unlock()
	s.remove()
}

// Caller holds mu: fix the cause before closure becomes observable.
func (s *Subscription) finishLocked(err error) {
	s.closed = true
	s.err = err
	close(s.queue)
}

// ErrSlowSubscriber indicates that a subscription exceeded its bounded queue.
var ErrSlowSubscriber = errors.New("slow_subscriber")

type queueHandler struct{ sub *Subscription }

func (h *queueHandler) Handle(e SessionEvent) error {
	s := h.sub
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	select {
	case s.queue <- e:
		s.mu.Unlock()
		return nil
	default:
		s.finishLocked(ErrSlowSubscriber)
		s.mu.Unlock()
		// Unregister outside the subscription lock; never wait on this Handle.
		s.remove()
		return ErrSlowSubscriber
	}
}
func (h *queueHandler) Close() error { return h.sub.Close() }

type handlerEntry struct {
	handler  EventHandler
	required bool
}

var handlerOwners = struct {
	sync.Mutex
	owners map[EventHandler]*EventBroadcast
}{owners: make(map[EventHandler]*EventBroadcast)}

// EventBroadcast serializes realtime delivery for one persistent Session.
// It has no history, Runtime or retry responsibilities.
type EventBroadcast struct {
	publish             sync.Mutex
	mu                  sync.Mutex
	namespace, name, id string
	capacity            int
	handlers            []*handlerEntry
	closed              bool
}

func NewBroadcast(namespace, name, id string, capacity int) (*EventBroadcast, error) {
	if namespace == "" || name == "" || id == "" || capacity < 1 {
		return nil, errors.New("invalid broadcast configuration")
	}
	return &EventBroadcast{namespace: namespace, name: name, id: id, capacity: capacity}, nil
}

func (b *EventBroadcast) AddHandler(h EventHandler) error {
	// Pointer identity prevents ambiguous ownership of copied value handlers.
	if h == nil || reflect.ValueOf(h).Kind() != reflect.Pointer || reflect.ValueOf(h).IsNil() {
		return errors.New("handler must be a non-nil pointer")
	}
	required := false
	if r, ok := h.(interface{ Required() bool }); ok {
		required = r.Required()
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.add(h, required)
}

func (b *EventBroadcast) add(h EventHandler, required bool) error {
	if b.closed {
		return errors.New("broadcast closed")
	}
	handlerOwners.Lock()
	defer handlerOwners.Unlock()
	if handlerOwners.owners[h] != nil {
		return errors.New("handler already registered")
	}
	handlerOwners.owners[h] = b
	b.handlers = append(b.handlers, &handlerEntry{handler: h, required: required})
	return nil
}

func (b *EventBroadcast) Subscribe() (*Subscription, error) {
	s := &Subscription{queue: make(chan SessionEvent, b.capacity)}
	h := &queueHandler{sub: s}
	s.remove = func() {
		b.mu.Lock()
		defer b.mu.Unlock()
		for i, entry := range b.handlers {
			if entry.handler == h {
				b.handlers = append(b.handlers[:i], b.handlers[i+1:]...)
				break
			}
		}
		handlerOwners.Lock()
		delete(handlerOwners.owners, h)
		handlerOwners.Unlock()
	}
	b.mu.Lock()
	err := b.add(h, false)
	b.mu.Unlock()
	if err != nil {
		s.end(nil)
		return nil, err
	}
	return s, nil
}

func (b *EventBroadcast) Publish(e SessionEvent) error {
	b.publish.Lock()
	defer b.publish.Unlock()
	if e.Namespace != b.namespace || e.SessionName != b.name || e.SessionID != b.id {
		return errors.New("broadcast session mismatch")
	}
	original, err := e.Clone()
	if err != nil {
		return err
	}
	original.source = Source{}
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return errors.New("broadcast closed")
	}
	handlers := append([]*handlerEntry(nil), b.handlers...)
	b.mu.Unlock()
	for _, entry := range handlers {
		detached, err := original.Clone()
		if err == nil {
			err = handleEvent(entry.handler, detached)
		}
		if err != nil {
			if entry.required {
				return err
			}
			slog.Warn("event handler failed", "error", err, "session_id", b.id)
		}
	}
	return nil
}

func handleEvent(h EventHandler, e SessionEvent) (err error) {
	defer func() {
		if p := recover(); p != nil {
			err = fmt.Errorf("event handler panic: %v", p)
		}
	}()
	return h.Handle(e)
}

func closeHandler(h EventHandler) (err error) {
	defer func() {
		if p := recover(); p != nil {
			err = fmt.Errorf("event handler close panic: %v", p)
		}
	}()
	return h.Close()
}

func (b *EventBroadcast) Close() error {
	b.publish.Lock()
	defer b.publish.Unlock()
	b.mu.Lock()
	b.closed = true
	handlers := append([]*handlerEntry(nil), b.handlers...)
	b.mu.Unlock()
	var result error
	for _, entry := range handlers {
		if err := closeHandler(entry.handler); err != nil {
			result = errors.Join(result, err)
			continue
		}
		b.mu.Lock()
		for i, current := range b.handlers {
			if current == entry {
				b.handlers = append(b.handlers[:i], b.handlers[i+1:]...)
				break
			}
		}
		b.mu.Unlock()
		handlerOwners.Lock()
		delete(handlerOwners.owners, entry.handler)
		handlerOwners.Unlock()
	}
	return result
}
