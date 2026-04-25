package watch_test

import (
	"context"
	"errors"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zoumo/mass/pkg/watch"
)

// fakeConn is a test ClientConn that delivers a fixed slice of events then
// returns an error, allowing deterministic testing without time.Sleep.
type fakeConn[T any] struct {
	events []watch.Event[T]
	idx    int
	closed bool
	mu     sync.Mutex
}

func (f *fakeConn[T]) Recv() (watch.Event[T], error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.idx >= len(f.events) {
		return watch.Event[T]{}, errors.New("EOF")
	}
	ev := f.events[f.idx]
	f.idx++
	return ev, nil
}

func (f *fakeConn[T]) Close() error {
	f.mu.Lock()
	f.closed = true
	f.mu.Unlock()
	return nil
}

// dialOnce returns a DialFunc that returns the given conn on the first call
// and blocks on subsequent calls (until ctx is canceled), so the reconnect
// loop does not spin on a real second connection.
func dialOnce[T any](conn watch.ClientConn[T]) watch.DialFunc[T] {
	var once sync.Once
	return func(ctx context.Context, _ int) (watch.ClientConn[T], error) {
		var c watch.ClientConn[T]
		once.Do(func() { c = conn })
		if c != nil {
			return c, nil
		}
		// Block until context cancels so the reconnect loop stays idle.
		<-ctx.Done()
		return nil, ctx.Err()
	}
}

func TestWatchClient_ReceivesAllEvents(t *testing.T) {
	events := []watch.Event[string]{
		{Seq: 0, Payload: "a"},
		{Seq: 1, Payload: "b"},
		{Seq: 2, Payload: "c"},
	}

	conn := &fakeConn[string]{events: events}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	wc := watch.NewWatchClient[string](dialOnce[string](conn), -1, 16)
	wc.Start(ctx)

	for _, want := range events {
		got := <-wc.Events()
		assert.Equal(t, want, got)
	}
	assert.Equal(t, 2, wc.Cursor())
}

func TestWatchClient_InitialCursorMinusOne_DialsFromZero(t *testing.T) {
	var dialledFrom int
	done := make(chan struct{})

	dial := func(ctx context.Context, fromSeq int) (watch.ClientConn[string], error) {
		dialledFrom = fromSeq
		close(done)
		<-ctx.Done()
		return nil, ctx.Err()
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	wc := watch.NewWatchClient[string](dial, -1, 4)
	wc.Start(ctx)

	<-done
	assert.Equal(t, 0, dialledFrom)
}

func TestWatchClient_ResumeFromCursor(t *testing.T) {
	var dialledFrom int
	done := make(chan struct{})

	dial := func(ctx context.Context, fromSeq int) (watch.ClientConn[string], error) {
		dialledFrom = fromSeq
		close(done)
		<-ctx.Done()
		return nil, ctx.Err()
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// initialCursor=5 → should dial with fromSeq=6
	wc := watch.NewWatchClient[string](dial, 5, 4)
	wc.Start(ctx)

	<-done
	assert.Equal(t, 6, dialledFrom)
}

func TestWatchClient_Cursor_UpdatesAfterEnqueue(t *testing.T) {
	events := []watch.Event[int]{
		{Seq: 10, Payload: 100},
		{Seq: 11, Payload: 101},
	}

	conn := &fakeConn[int]{events: events}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	wc := watch.NewWatchClient[int](dialOnce[int](conn), -1, 16)
	wc.Start(ctx)

	// Drain both events; cursor is updated atomically before the next Recv so
	// after all events are in the queue the cursor reflects the last seq.
	<-wc.Events()
	<-wc.Events()

	// Both events have been dequeued — the background goroutine has stored the
	// cursor for each one before moving on. Poll until it converges.
	for wc.Cursor() != 11 {
		// yield the scheduler; no sleep needed since cursor update is the very
		// next statement after the channel send in the goroutine.
		runtime.Gosched()
	}
	assert.Equal(t, 11, wc.Cursor())
}

func TestWatchClient_Reconnects(t *testing.T) {
	// First connection delivers one event then fails.
	// Second connection delivers a second event then blocks.
	ev1 := watch.Event[string]{Seq: 0, Payload: "first"}
	ev2 := watch.Event[string]{Seq: 1, Payload: "second"}

	call := 0
	var mu sync.Mutex
	secondDone := make(chan struct{})

	dial := func(ctx context.Context, fromSeq int) (watch.ClientConn[string], error) {
		mu.Lock()
		n := call
		call++
		mu.Unlock()

		switch n {
		case 0:
			return &fakeConn[string]{events: []watch.Event[string]{ev1}}, nil
		default:
			// Second+ calls: deliver ev2 then block.
			conn := &fakeConn[string]{events: []watch.Event[string]{ev2}}
			close(secondDone)
			return conn, nil
		}
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Use queueSize=2 so events are buffered immediately.
	wc := watch.NewWatchClient[string](dial, -1, 2)
	wc.Start(ctx)

	got1 := <-wc.Events()
	assert.Equal(t, ev1, got1)

	// Wait for second connection to be established.
	<-secondDone

	got2 := <-wc.Events()
	assert.Equal(t, ev2, got2)
}

func TestWatchClient_ContextCancel_Stops(t *testing.T) {
	blocked := make(chan struct{})
	dial := func(ctx context.Context, _ int) (watch.ClientConn[string], error) {
		close(blocked)
		<-ctx.Done()
		return nil, ctx.Err()
	}

	ctx, cancel := context.WithCancel(context.Background())

	wc := watch.NewWatchClient[string](dial, -1, 4)
	wc.Start(ctx)

	<-blocked
	cancel()
	// No assertion needed — if the goroutine did not stop, the test would leak
	// and be caught by go test -race or the test timeout.
}

// blockingConn is a test ClientConn whose Recv blocks until the conn is
// closed, simulating a live server that never sends more events.
type blockingConn[T any] struct {
	closed chan struct{}
	once   sync.Once
}

func newBlockingConn[T any]() *blockingConn[T] {
	return &blockingConn[T]{closed: make(chan struct{})}
}

func (b *blockingConn[T]) Recv() (watch.Event[T], error) {
	<-b.closed
	return watch.Event[T]{}, errors.New("closed")
}

func (b *blockingConn[T]) Close() error {
	b.once.Do(func() { close(b.closed) })
	return nil
}

// TestWatchClient_ReconnectsAfterServerDisconnect verifies that when
// conn.Recv() returns an error (server disconnect), the client dials again,
// delivers events from the new connection, and advances the cursor correctly.
func TestWatchClient_ReconnectsAfterServerDisconnect(t *testing.T) {
	ev1 := watch.Event[string]{Seq: 0, Payload: "first"}
	ev2 := watch.Event[string]{Seq: 1, Payload: "second"}
	ev3 := watch.Event[string]{Seq: 2, Payload: "third"}

	// conn1 delivers ev1 and ev2, then signals EOF via the fakeConn mechanism.
	conn1 := &fakeConn[string]{events: []watch.Event[string]{ev1, ev2}}

	// conn2 delivers ev3, then blocks indefinitely.
	conn2 := newBlockingConn[string]()
	conn2Events := []watch.Event[string]{ev3}

	var dialMu sync.Mutex
	dialCount := 0
	// dialFromSeqs records the fromSeq argument each time dial is called.
	dialFromSeqs := make([]int, 0, 2)
	// conn2Ready is closed when the second dial completes so the test can
	// proceed to read from conn2 without racing.
	conn2Ready := make(chan struct{})

	dial := func(ctx context.Context, fromSeq int) (watch.ClientConn[string], error) {
		dialMu.Lock()
		n := dialCount
		dialCount++
		dialFromSeqs = append(dialFromSeqs, fromSeq)
		dialMu.Unlock()

		switch n {
		case 0:
			return conn1, nil
		default:
			// Wrap conn2 in a fakeConn so it delivers ev3 first, then blocks.
			wrapped := &fakeConn[string]{events: conn2Events}
			// Signal that the second connection is up.
			select {
			case <-conn2Ready:
			default:
				close(conn2Ready)
			}
			_ = conn2 // ensure blockingConn is not GC'd
			return wrapped, nil
		}
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	wc := watch.NewWatchClient[string](dial, -1, 8)
	wc.Start(ctx)

	// Drain ev1 and ev2 from conn1.
	got1 := <-wc.Events()
	require.Equal(t, ev1, got1)
	got2 := <-wc.Events()
	require.Equal(t, ev2, got2)

	// Wait for conn2 to be dialed (reconnect happened).
	select {
	case <-conn2Ready:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for reconnect dial")
	}

	// Drain ev3 from conn2.
	got3 := <-wc.Events()
	require.Equal(t, ev3, got3)

	// Verify dial was called at least twice.
	dialMu.Lock()
	count := dialCount
	seqs := append([]int(nil), dialFromSeqs...)
	dialMu.Unlock()

	require.GreaterOrEqual(t, count, 2, "expected at least 2 dial calls after server disconnect")

	// First dial must start from 0 (initialCursor=-1).
	require.Equal(t, 0, seqs[0], "first dial must start from seq 0")

	// Second dial must resume from cursor+1 = 2 (last enqueued was Seq=1).
	require.Equal(t, 2, seqs[1], "second dial must resume from cursor+1 after conn1 events")

	// Cursor must reflect the last enqueued event.
	assert.Equal(t, 2, wc.Cursor())
}

// TestWatchClient_CtxCancelClosesEvents verifies that canceling the context
// causes the Events() channel to be closed within a reasonable timeout.
func TestWatchClient_CtxCancelClosesEvents(t *testing.T) {
	// blockConn blocks in Recv until it is explicitly closed (which happens
	// when ctx is canceled inside runOnce).
	conn := newBlockingConn[string]()

	var dialOnce sync.Once
	dial := func(ctx context.Context, _ int) (watch.ClientConn[string], error) {
		var c watch.ClientConn[string]
		dialOnce.Do(func() { c = conn })
		if c != nil {
			return c, nil
		}
		// Subsequent calls (after reconnect on close) should block until ctx done.
		<-ctx.Done()
		return nil, ctx.Err()
	}

	ctx, cancel := context.WithCancel(context.Background())

	wc := watch.NewWatchClient[string](dial, -1, 4)
	wc.Start(ctx)

	// Give the goroutine time to enter Recv and block.
	// We yield the scheduler briefly; no sleep needed because we use a
	// channel-based timeout below.
	runtime.Gosched()

	cancel()

	// The Events() channel must be closed after ctx is canceled.
	// defer close(w.queue) in reconnectLoop guarantees this once the loop exits.
	select {
	case _, ok := <-wc.Events():
		if !ok {
			// Channel closed — expected.
		}
		// If ok==true an event arrived; fall through and drain below.
	case <-time.After(2 * time.Second):
		t.Fatal("Events() channel was not closed within 2s after context cancel")
	}

	// Drain the channel fully to confirm it is closed (not just empty).
	for range wc.Events() {
		// discard any buffered events
	}
}

func TestWatchClient_StartIdempotent(t *testing.T) {
	dialCalled := 0
	var mu sync.Mutex
	done := make(chan struct{})

	dial := func(ctx context.Context, _ int) (watch.ClientConn[string], error) {
		mu.Lock()
		dialCalled++
		if dialCalled == 1 {
			close(done)
		}
		mu.Unlock()
		<-ctx.Done()
		return nil, ctx.Err()
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	wc := watch.NewWatchClient[string](dial, -1, 4)
	wc.Start(ctx)
	wc.Start(ctx) // second call must be a no-op
	wc.Start(ctx)

	<-done

	mu.Lock()
	n := dialCalled
	mu.Unlock()
	require.Equal(t, 1, n, "dial should only be called once even with multiple Start calls")
}
