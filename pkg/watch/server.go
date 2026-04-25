package watch

import (
	"sync"
)

// ServerConn represents the server side of a single watcher connection.
type ServerConn[T any] interface {
	Send(ev Event[T]) error
	Close() error
}

// watcher holds the per-connection state on the server side.
type watcher[T any] struct {
	conn    ServerConn[T]
	mailbox chan Event[T]
	done    chan struct{} // closed when watcher goroutine exits
}

// WatchServer fans out published events to all registered watchers.
// Each watcher gets its own goroutine, so one slow or broken connection
// does not block the others.
type WatchServer[T any] struct {
	mu        sync.Mutex
	watchers  map[uint64]*watcher[T]
	nextID    uint64
	publishMu sync.Mutex // serializes Publish calls to guarantee per-watcher event order
}

// NewWatchServer creates an empty WatchServer.
func NewWatchServer[T any]() *WatchServer[T] {
	return &WatchServer[T]{
		watchers: make(map[uint64]*watcher[T]),
	}
}

// Accept registers a new watcher connection and starts its send goroutine.
func (s *WatchServer[T]) Accept(conn ServerConn[T]) {
	s.mu.Lock()
	id := s.nextID
	s.nextID++
	w := &watcher[T]{
		conn:    conn,
		mailbox: make(chan Event[T]), // unbuffered: Publish blocks per watcher
		done:    make(chan struct{}),
	}
	s.watchers[id] = w
	s.mu.Unlock()

	go func() {
		defer func() {
			s.mu.Lock()
			delete(s.watchers, id)
			s.mu.Unlock()
		}()

		for ev := range w.mailbox {
			if err := conn.Send(ev); err != nil {
				conn.Close()
				close(w.done)
				return
			}
		}
	}()
}

// Publish sends ev to all registered watchers.
// publishMu serializes concurrent Publish calls so that events are delivered
// to every watcher in the same global order, eliminating the race where two
// concurrent callers could interleave their sends to the same unbuffered mailbox.
func (s *WatchServer[T]) Publish(ev Event[T]) {
	s.publishMu.Lock()
	defer s.publishMu.Unlock()

	s.mu.Lock()
	// Snapshot the current watcher set so we release mu before blocking on sends.
	ws := make([]*watcher[T], 0, len(s.watchers))
	for _, w := range s.watchers {
		ws = append(ws, w)
	}
	s.mu.Unlock()

	// Send to each watcher sequentially. Because publishMu is held for the
	// entire loop, ev1 always enters every mailbox before ev2 can begin,
	// preserving ordered fan-out without spawning per-watcher goroutines.
	for _, w := range ws {
		select {
		case w.mailbox <- ev:
		case <-w.done:
		}
	}
}
