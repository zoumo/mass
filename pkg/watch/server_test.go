package watch_test

import (
	"errors"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/zoumo/mass/pkg/watch"
)

// recordConn records every sent event and can be configured to fail after N sends.
// sendCh is closed (or receives) after each successful Send, allowing tests to
// synchronize without time.Sleep.
type recordConn[T any] struct {
	mu       sync.Mutex
	sent     []watch.Event[T]
	failAt   int // fail when len(sent) == failAt; -1 = never
	closed   bool
	closedCh chan struct{}
	sendCh   chan struct{} // receives a token after every successful Send
}

func newRecordConn[T any](failAt int) *recordConn[T] {
	return &recordConn[T]{
		failAt:   failAt,
		closedCh: make(chan struct{}),
		sendCh:   make(chan struct{}, 64), // buffered so Send never blocks on this
	}
}

func (r *recordConn[T]) Send(ev watch.Event[T]) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.failAt >= 0 && len(r.sent) >= r.failAt {
		return errors.New("send failed")
	}
	r.sent = append(r.sent, ev)
	r.sendCh <- struct{}{}
	return nil
}

func (r *recordConn[T]) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.closed {
		r.closed = true
		close(r.closedCh)
	}
	return nil
}

func (r *recordConn[T]) Sent() []watch.Event[T] {
	r.mu.Lock()
	defer r.mu.Unlock()
	cp := make([]watch.Event[T], len(r.sent))
	copy(cp, r.sent)
	return cp
}

// waitSends blocks until n successful Send calls have been made.
func (r *recordConn[T]) waitSends(t *testing.T, n int) {
	t.Helper()
	for i := 0; i < n; i++ {
		<-r.sendCh
	}
}

func TestWatchServer_PublishToSingleWatcher(t *testing.T) {
	srv := watch.NewWatchServer[string]()
	conn := newRecordConn[string](-1)

	srv.Accept(conn)

	ev := watch.Event[string]{Seq: 0, Payload: "hello"}
	srv.Publish(ev)

	// Wait for the send goroutine to actually call conn.Send before asserting.
	conn.waitSends(t, 1)

	assert.Equal(t, []watch.Event[string]{ev}, conn.Sent())
}

func TestWatchServer_PublishToMultipleWatchers(t *testing.T) {
	srv := watch.NewWatchServer[int]()

	const n = 5
	conns := make([]*recordConn[int], n)
	for i := range conns {
		conns[i] = newRecordConn[int](-1)
		srv.Accept(conns[i])
	}

	ev := watch.Event[int]{Seq: 1, Payload: 42}
	srv.Publish(ev)

	// Wait for all watchers to deliver.
	for _, c := range conns {
		c.waitSends(t, 1)
	}

	for i, c := range conns {
		got := c.Sent()
		assert.Len(t, got, 1, "conn %d should have received 1 event", i)
		assert.Equal(t, ev, got[0])
	}
}

func TestWatchServer_PublishOrdering(t *testing.T) {
	srv := watch.NewWatchServer[int]()
	conn := newRecordConn[int](-1)
	srv.Accept(conn)

	events := []watch.Event[int]{
		{Seq: 0, Payload: 0},
		{Seq: 1, Payload: 1},
		{Seq: 2, Payload: 2},
	}
	for _, ev := range events {
		srv.Publish(ev)
	}
	conn.waitSends(t, len(events))

	assert.Equal(t, events, conn.Sent())
}

func TestWatchServer_BrokenWatcherIsRemoved(t *testing.T) {
	srv := watch.NewWatchServer[string]()

	// failConn fails on its first send.
	failConn := newRecordConn[string](0)
	goodConn := newRecordConn[string](-1)

	srv.Accept(failConn)
	srv.Accept(goodConn)

	ev1 := watch.Event[string]{Seq: 0, Payload: "first"}
	srv.Publish(ev1)

	// Wait for failConn to be closed (its watcher goroutine detected the send error
	// and called conn.Close(); done channel is also closed at this point, so any
	// in-flight Publish goroutine targeting failConn will unblock immediately).
	<-failConn.closedCh

	// Also wait for goodConn to have delivered ev1 so its watcher goroutine
	// is back waiting on the mailbox before the second publish.
	goodConn.waitSends(t, 1)

	ev2 := watch.Event[string]{Seq: 1, Payload: "second"}
	// Use a separate goroutine so the test doesn't deadlock if failConn is
	// still in the map (its mailbox has no reader after the goroutine exited).
	publishDone := make(chan struct{})
	go func() {
		srv.Publish(ev2)
		close(publishDone)
	}()

	// goodConn must receive ev2.
	goodConn.waitSends(t, 1) // waits for the second send

	sent := goodConn.Sent()
	assert.Contains(t, sent, ev2)

	<-publishDone
}

func TestWatchServer_NoWatchers_PublishIsNoop(t *testing.T) {
	srv := watch.NewWatchServer[string]()
	// Should not panic or block.
	srv.Publish(watch.Event[string]{Seq: 0, Payload: "x"})
}

func TestWatchServer_Accept_ConcurrentPublish(t *testing.T) {
	srv := watch.NewWatchServer[int]()

	const numConns = 10
	const numEvents = 20

	conns := make([]*recordConn[int], numConns)
	for i := range conns {
		conns[i] = newRecordConn[int](-1)
		srv.Accept(conns[i])
	}

	for i := 0; i < numEvents; i++ {
		srv.Publish(watch.Event[int]{Seq: i, Payload: i})
	}

	for i, c := range conns {
		c.waitSends(t, numEvents)
		got := c.Sent()
		assert.Len(t, got, numEvents, "conn %d should have received all events", i)
		for j, ev := range got {
			assert.Equal(t, j, ev.Seq, "conn %d event %d wrong seq", i, j)
		}
	}
}
