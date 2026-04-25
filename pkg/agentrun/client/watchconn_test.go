package client

import (
	"io"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	runapi "github.com/zoumo/mass/pkg/agentrun/api"
	"github.com/zoumo/mass/pkg/jsonrpc"
	"github.com/zoumo/mass/pkg/watch"
)

// newTestConn creates an agentRunConn backed by a Watcher driven by notifCh.
// The returned unsub function closes notifCh (simulating connection loss).
func newTestConn(t *testing.T) (*agentRunConn, chan jsonrpc.NotificationMsg, func()) {
	t.Helper()
	notifCh := make(chan jsonrpc.NotificationMsg, 16)
	closeOnce := make(chan struct{})
	unsub := func() {
		select {
		case <-closeOnce:
		default:
			close(closeOnce)
			close(notifCh)
		}
	}
	w := newWatcher("w-conn-1", 0, notifCh, unsub)
	return &agentRunConn{w: w}, notifCh, unsub
}

// TestAgentRunConn_RecvWrapsSeq verifies that Recv wraps AgentRunEvent into
// watch.Event with the correct Seq and Payload.
func TestAgentRunConn_RecvWrapsSeq(t *testing.T) {
	conn, notifCh, unsub := newTestConn(t)
	defer unsub()

	ev := runapi.AgentRunEvent{
		WatchID: "w-conn-1",
		RunID:   "run-1",
		Seq:     42,
		Type:    runapi.EventTypeTurnStart,
		Payload: runapi.TurnStartEvent{},
	}
	notifCh <- makeNotifMsg(t, ev)

	var got watch.Event[runapi.AgentRunEvent]
	done := make(chan error, 1)
	go func() {
		var err error
		got, err = conn.Recv()
		done <- err
	}()

	select {
	case err := <-done:
		require.NoError(t, err)
		assert.Equal(t, 42, got.Seq)
		assert.Equal(t, "run-1", got.Payload.RunID)
		assert.Equal(t, runapi.EventTypeTurnStart, got.Payload.Type)
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for Recv")
	}
}

// TestAgentRunConn_RecvEOFOnClose verifies that Recv returns io.EOF when the
// underlying channel is closed (connection lost or Stop called).
func TestAgentRunConn_RecvEOFOnClose(t *testing.T) {
	conn, _, unsub := newTestConn(t)
	unsub() // simulates connection loss

	done := make(chan error, 1)
	go func() { _, err := conn.Recv(); done <- err }()
	select {
	case err := <-done:
		assert.Equal(t, io.EOF, err)
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for EOF")
	}
}

// TestAgentRunConn_CloseStopsWatcher verifies that Close() calls Watcher.Stop(),
// which closes ResultChan and causes subsequent Recv to return io.EOF.
func TestAgentRunConn_CloseStopsWatcher(t *testing.T) {
	conn, _, _ := newTestConn(t)

	err := conn.Close()
	require.NoError(t, err)

	// After Close, the result channel should be closed → Recv must return io.EOF.
	done := make(chan error, 1)
	go func() { _, err := conn.Recv(); done <- err }()
	select {
	case err := <-done:
		assert.Equal(t, io.EOF, err)
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for EOF after Close")
	}
}

// TestAgentRunConn_MultipleEvents verifies that multiple events are delivered
// in order with correct Seq values.
func TestAgentRunConn_MultipleEvents(t *testing.T) {
	conn, notifCh, unsub := newTestConn(t)
	defer unsub()

	const count = 5
	for i := 0; i < count; i++ {
		notifCh <- makeNotifMsg(t, runapi.AgentRunEvent{
			WatchID: "w-conn-1",
			RunID:   "run-1",
			Seq:     i,
			Type:    runapi.EventTypeTurnStart,
			Payload: runapi.TurnStartEvent{},
		})
	}

	for i := 0; i < count; i++ {
		done := make(chan watch.Event[runapi.AgentRunEvent], 1)
		errCh := make(chan error, 1)
		go func() {
			ev, err := conn.Recv()
			if err != nil {
				errCh <- err
				return
			}
			done <- ev
		}()

		select {
		case ev := <-done:
			assert.Equal(t, i, ev.Seq, "event %d seq mismatch", i)
			assert.Equal(t, ev.Seq, ev.Payload.Seq, "Payload.Seq must match envelope Seq")
		case err := <-errCh:
			t.Fatalf("Recv error on event %d: %v", i, err)
		case <-time.After(2 * time.Second):
			t.Fatalf("timeout waiting for event %d", i)
		}
	}
}
