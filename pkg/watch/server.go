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
	mu       sync.Mutex
	watchers map[uint64]*watcher[T]
	nextID   uint64
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
// Each watcher's delivery runs in its own goroutine so that a slow consumer
// does not block publication to others. Within a single watcher the sends are
// ordered because the goroutine drains the mailbox sequentially.
func (s *WatchServer[T]) Publish(ev Event[T]) {
	s.mu.Lock()
	// Snapshot the current watcher set so we release the lock before blocking.
	ws := make([]*watcher[T], 0, len(s.watchers))
	for _, w := range s.watchers {
		ws = append(ws, w)
	}
	s.mu.Unlock()

	var wg sync.WaitGroup
	for _, w := range ws {
		wg.Add(1)
		go func(w *watcher[T]) {
			defer wg.Done()
			select {
			case w.mailbox <- ev:
			case <-w.done:
			}
		}(w)
	}
	wg.Wait()
}
