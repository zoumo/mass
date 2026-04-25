package watch

import (
	"context"
	"sync"
	"sync/atomic"
	"time"
)

const (
	retryBackoffInitial = 500 * time.Millisecond
	retryBackoffMax     = 10 * time.Second
)

// WatchFunc establishes a new watch stream starting at fromSeq.
// fromSeq is cursor+1 of the last successfully enqueued event, or 0 for full
// replay from the beginning.
type WatchFunc[T any] func(ctx context.Context, fromSeq int) (Interface[T], error)

// RetryWatcher wraps a WatchFunc and reconnects automatically on stream end.
// Implements Interface[T]. Modeled after k8s client-go Reflector.
type RetryWatcher[T any] struct {
	wf     WatchFunc[T]
	getSeq func(T) int
	cursor atomic.Int64

	result chan T

	activeMu sync.Mutex
	active   Interface[T]

	cancel context.CancelFunc
	once   sync.Once
}

// NewRetryWatcher creates a RetryWatcher that immediately starts reconnecting.
//
// initCursor: -1 = start from seq=0 (full replay); N = resume from N+1.
// getSeq extracts the sequence number from an event for cursor tracking.
// queueSize is the capacity of the result channel.
func NewRetryWatcher[T any](
	ctx context.Context,
	wf WatchFunc[T],
	initCursor int,
	getSeq func(T) int,
	queueSize int,
) *RetryWatcher[T] {
	ctx, cancel := context.WithCancel(ctx)
	rw := &RetryWatcher[T]{
		wf:     wf,
		getSeq: getSeq,
		result: make(chan T, queueSize),
		cancel: cancel,
	}
	rw.cursor.Store(int64(initCursor))
	go func() {
		defer close(rw.result)
		rw.reconnectLoop(ctx)
	}()
	return rw
}

func (rw *RetryWatcher[T]) ResultChan() <-chan T { return rw.result }

// Cursor returns -1 if no events have been delivered yet.
func (rw *RetryWatcher[T]) Cursor() int { return int(rw.cursor.Load()) }

func (rw *RetryWatcher[T]) Stop() {
	rw.once.Do(func() {
		rw.cancel()
		rw.activeMu.Lock()
		active := rw.active
		rw.activeMu.Unlock()
		if active != nil {
			active.Stop()
		}
	})
}

func (rw *RetryWatcher[T]) reconnectLoop(ctx context.Context) {
	backoff := retryBackoffInitial
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}
		delivered := rw.runOnce(ctx)
		if delivered {
			backoff = retryBackoffInitial
		}

		timer := time.NewTimer(backoff)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
		backoff *= 2
		if backoff > retryBackoffMax {
			backoff = retryBackoffMax
		}
	}
}

func (rw *RetryWatcher[T]) runOnce(ctx context.Context) bool {
	cursor := int(rw.cursor.Load())
	fromSeq := cursor + 1
	if cursor < 0 {
		fromSeq = 0
	}

	active, err := rw.wf(ctx, fromSeq)
	if err != nil {
		return false
	}

	rw.activeMu.Lock()
	rw.active = active
	rw.activeMu.Unlock()

	delivered := false
	defer func() {
		active.Stop()
		rw.activeMu.Lock()
		if rw.active == active {
			rw.active = nil
		}
		rw.activeMu.Unlock()
	}()

	for ev := range active.ResultChan() {
		select {
		case rw.result <- ev:
			rw.cursor.Store(int64(rw.getSeq(ev)))
			delivered = true
		case <-ctx.Done():
			return delivered
		}
	}
	return delivered
}
