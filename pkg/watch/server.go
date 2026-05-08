package watch

import (
	"sync"
	"time"
)

const defaultSendTimeout = 5 * time.Second

// ServerOption customizes a WatchServer.
type ServerOption[T any] func(*WatchServer[T])

// WithSendTimeout configures how long Publish waits for a watcher to accept an
// event before treating it as a slow consumer and closing it.
func WithSendTimeout[T any](timeout time.Duration) ServerOption[T] {
	return func(s *WatchServer[T]) {
		s.sendTimeout = timeout
	}
}

// ServerConn represents the server side of a single watcher connection.
type ServerConn[T any] interface {
	Send(ev Event[T]) error
	Close() error
}

// watcher holds the per-connection state on the server side.
type watcher[T any] struct {
	id      uint64
	conn    ServerConn[T]
	mailbox chan Event[T]
	closed  chan struct{}
	done    chan struct{} // closed when watcher goroutine exits
	once    sync.Once
}

// WatchServer fans out published events to all registered watchers.
// Each watcher gets its own goroutine, so one slow or broken connection
// does not block the others.
type WatchServer[T any] struct {
	mu          sync.Mutex
	watchers    map[uint64]*watcher[T]
	nextID      uint64
	publishMu   sync.Mutex // serializes Publish calls to guarantee per-watcher event order
	sendTimeout time.Duration
}

// NewWatchServer creates an empty WatchServer.
func NewWatchServer[T any](opts ...ServerOption[T]) *WatchServer[T] {
	s := &WatchServer[T]{
		watchers:    make(map[uint64]*watcher[T]),
		sendTimeout: defaultSendTimeout,
	}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

// Accept registers a new watcher connection and starts its send goroutine.
func (s *WatchServer[T]) Accept(conn ServerConn[T]) {
	s.mu.Lock()
	id := s.nextID
	s.nextID++
	w := &watcher[T]{
		id:      id,
		conn:    conn,
		mailbox: make(chan Event[T]), // unbuffered: Publish blocks per watcher
		closed:  make(chan struct{}),
		done:    make(chan struct{}),
	}
	s.watchers[id] = w
	s.mu.Unlock()

	go func() {
		defer func() {
			s.removeWatcher(w)
			close(w.done)
		}()

		for {
			select {
			case ev := <-w.mailbox:
				if err := conn.Send(ev); err != nil {
					s.closeWatcher(w)
					return
				}
			case <-w.closed:
				return
			}
		}
	}()
}

func (s *WatchServer[T]) removeWatcher(w *watcher[T]) {
	s.mu.Lock()
	if s.watchers[w.id] == w {
		delete(s.watchers, w.id)
	}
	s.mu.Unlock()
}

func (s *WatchServer[T]) closeWatcher(w *watcher[T]) {
	w.once.Do(func() {
		s.removeWatcher(w)
		close(w.closed)
		_ = w.conn.Close()
	})
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
		case <-w.closed:
		case <-w.done:
		case <-time.After(s.sendTimeout):
			s.closeWatcher(w)
		}
	}
}
