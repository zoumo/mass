package client

import (
	"encoding/json"
	"io"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	runapi "github.com/zoumo/mass/pkg/agentrun/api"
	"github.com/zoumo/mass/pkg/jsonrpc"
	"github.com/zoumo/mass/pkg/watch"
)

// makeSeqNotifMsg builds a NotificationMsg whose Params is the JSON-encoded seq integer.
func makeSeqNotifMsg(seq int) jsonrpc.NotificationMsg {
	raw, _ := json.Marshal(seq)
	return jsonrpc.NotificationMsg{Method: "test", Params: raw}
}

// TestRelayNotifications_NoDrop verifies that relayNotifications never drops
// messages even when dst would fill up — excess messages are buffered internally.
func TestRelayNotifications_NoDrop(t *testing.T) {
	const count = 2048
	src := make(chan jsonrpc.NotificationMsg, 64)
	dst := make(chan jsonrpc.NotificationMsg, 64)

	go relayNotifications(src, dst)

	// Produce count messages in a separate goroutine to avoid blocking.
	go func() {
		for i := 0; i < count; i++ {
			src <- makeSeqNotifMsg(i)
		}
		close(src)
	}()

	// Drain dst; relayNotifications must close dst after src is exhausted.
	received := 0
	for range dst {
		received++
	}
	assert.Equal(t, count, received, "relayNotifications must not drop any message")
}

// TestRelayNotifications_ClosePropagates verifies that closing src causes dst
// to be closed after all buffered messages are flushed to dst.
func TestRelayNotifications_ClosePropagates(t *testing.T) {
	src := make(chan jsonrpc.NotificationMsg, 4)
	dst := make(chan jsonrpc.NotificationMsg, 4)

	go relayNotifications(src, dst)

	src <- jsonrpc.NotificationMsg{Method: "m1"}
	src <- jsonrpc.NotificationMsg{Method: "m2"}
	close(src)

	var got []string
	for msg := range dst {
		got = append(got, msg.Method)
	}
	require.Equal(t, []string{"m1", "m2"}, got)
}

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
