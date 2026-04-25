package watch

import (
	"context"
	"sync"
	"sync/atomic"
	"time"
)

const (
	backoffInitial = 500 * time.Millisecond
	backoffMax     = 10 * time.Second
)

// WatchClient connects to a watch server via DialFunc, delivers events
// through a buffered channel, and reconnects automatically on error.
type WatchClient[T any] struct {
	dial   DialFunc[T]
	queue  chan Event[T]
	cursor atomic.Int64 // stores last successfully enqueued Event.Seq; -1 = none yet

	once sync.Once
}

// NewWatchClient creates a WatchClient.
// initialCursor=-1 means start from seq=0 (full replay); other values resume
// from cursor+1.
// queueSize is the capacity of the internal event channel.
func NewWatchClient[T any](dial DialFunc[T], initialCursor, queueSize int) *WatchClient[T] {
	w := &WatchClient[T]{
		dial:  dial,
		queue: make(chan Event[T], queueSize),
	}
	w.cursor.Store(int64(initialCursor))
	return w
}

// Start launches the background reconnect loop. It must be called exactly once.
// The loop runs until ctx is canceled.
func (w *WatchClient[T]) Start(ctx context.Context) {
	w.once.Do(func() {
		go func() {
			defer close(w.queue)
			w.reconnectLoop(ctx)
		}()
	})
}

// Events returns the channel from which callers consume watch events.
func (w *WatchClient[T]) Events() <-chan Event[T] {
	return w.queue
}

// Cursor returns the Seq of the last successfully enqueued event.
// Returns -1 if no event has been enqueued yet.
func (w *WatchClient[T]) Cursor() int {
	return int(w.cursor.Load())
}

// reconnectLoop drives runOnce with exponential backoff until ctx is canceled.
func (w *WatchClient[T]) reconnectLoop(ctx context.Context) {
	backoff := backoffInitial
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}
		w.runOnce(ctx) //nolint:errcheck

		timer := time.NewTimer(backoff)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}

		// Exponential backoff: double each time, capped at backoffMax.
		backoff *= 2
		if backoff > backoffMax {
			backoff = backoffMax
		}
	}
}

// runOnce dials and drains events until the connection fails or ctx is
// canceled. Returns when the connection is no longer usable.
func (w *WatchClient[T]) runOnce(ctx context.Context) error {
	conn, err := w.dial(ctx, int(w.cursor.Load())+1)
	if err != nil {
		return err
	}
	defer conn.Close()

	// Ensure conn is closed when ctx is canceled, to unblock Recv().
	stop := make(chan struct{})
	defer close(stop)
	go func() {
		select {
		case <-ctx.Done():
			conn.Close()
		case <-stop:
			// runOnce exited normally
		}
	}()

	for {
		ev, err := conn.Recv()
		if err != nil {
			return err
		}

		// Enqueue with backpressure: block until consumer reads or ctx done.
		select {
		case w.queue <- ev:
			w.cursor.Store(int64(ev.Seq))
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}
