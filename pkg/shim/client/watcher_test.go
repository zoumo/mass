package client

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	apishim "github.com/zoumo/mass/pkg/shim/api"
	"github.com/zoumo/mass/pkg/jsonrpc"
)

// makeNotifMsg builds a jsonrpc.NotificationMsg from an AgentRunEvent.
func makeNotifMsg(t *testing.T, ev apishim.AgentRunEvent) jsonrpc.NotificationMsg {
	t.Helper()
	raw, err := json.Marshal(ev)
	require.NoError(t, err)
	return jsonrpc.NotificationMsg{
		Method: apishim.MethodRuntimeEventUpdate,
		Params: raw,
	}
}

// TestWatcher_DeliverMatchingEvents verifies that events whose watchID matches
// the watcher's ID are delivered through ResultChan in order.
func TestWatcher_DeliverMatchingEvents(t *testing.T) {
	notifCh := make(chan jsonrpc.NotificationMsg, 8)
	unsub := func() { close(notifCh) }
	w := newWatcher("w-1", 0, notifCh, unsub)
	defer w.Stop()

	// Send 3 events with matching watchID.
	for i := 0; i < 3; i++ {
		notifCh <- makeNotifMsg(t, apishim.AgentRunEvent{
			WatchID: "w-1",
			RunID:   "run-1",
			Seq:     i,
			Type:    apishim.EventTypeAgentMessage,
			Payload: apishim.NewContentEvent(apishim.EventTypeAgentMessage, "", apishim.TextBlock("hello")),
		})
	}

	for i := 0; i < 3; i++ {
		select {
		case ev := <-w.ResultChan():
			assert.Equal(t, i, ev.Seq, "event %d seq mismatch", i)
			assert.Equal(t, "run-1", ev.RunID)
			assert.Equal(t, apishim.EventTypeAgentMessage, ev.Type)
		case <-time.After(2 * time.Second):
			t.Fatalf("timeout waiting for event %d", i)
		}
	}
}

// TestWatcher_FilterByWatchID verifies that events with a different watchID
// are silently filtered out and do not appear on ResultChan.
func TestWatcher_FilterByWatchID(t *testing.T) {
	notifCh := make(chan jsonrpc.NotificationMsg, 8)
	unsub := func() { close(notifCh) }
	w := newWatcher("w-1", 0, notifCh, unsub)
	defer w.Stop()

	// Send event with wrong watchID, then one with correct watchID.
	notifCh <- makeNotifMsg(t, apishim.AgentRunEvent{
		WatchID: "w-other",
		RunID:   "run-1",
		Seq:     0,
		Type:    apishim.EventTypeAgentMessage,
		Payload: apishim.NewContentEvent(apishim.EventTypeAgentMessage, "", apishim.TextBlock("wrong")),
	})
	notifCh <- makeNotifMsg(t, apishim.AgentRunEvent{
		WatchID: "w-1",
		RunID:   "run-1",
		Seq:     1,
		Type:    apishim.EventTypeAgentMessage,
		Payload: apishim.NewContentEvent(apishim.EventTypeAgentMessage, "", apishim.TextBlock("right")),
	})

	// Only the matching event should arrive.
	select {
	case ev := <-w.ResultChan():
		assert.Equal(t, 1, ev.Seq, "should receive only the matching watchID event")
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for matching event")
	}

	// No more events should be pending.
	select {
	case ev := <-w.ResultChan():
		t.Fatalf("unexpected event received: seq=%d", ev.Seq)
	case <-time.After(100 * time.Millisecond):
		// expected: filtered event was dropped
	}
}

// TestWatcher_NotifChClosed_ClosesResultChan verifies that when the notification
// channel closes (connection lost, server eviction, etc.), ResultChan is closed
// too — allowing consumers to detect disconnect via range loop exit.
func TestWatcher_NotifChClosed_ClosesResultChan(t *testing.T) {
	notifCh := make(chan jsonrpc.NotificationMsg, 4)
	unsubCalled := false
	unsub := func() { unsubCalled = true }
	w := newWatcher("w-1", 0, notifCh, unsub)

	// Send one event, then close the notification channel.
	notifCh <- makeNotifMsg(t, apishim.AgentRunEvent{
		WatchID: "w-1",
		RunID:   "run-1",
		Seq:     0,
		Type:    apishim.EventTypeTurnStart,
		Payload: apishim.TurnStartEvent{},
	})
	close(notifCh)

	// Drain the delivered event.
	select {
	case <-w.ResultChan():
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for event before close")
	}

	// ResultChan should be closed after notifCh closes.
	select {
	case _, ok := <-w.ResultChan():
		assert.False(t, ok, "ResultChan must be closed after notifCh closes")
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for ResultChan close")
	}

	// Stop should still be safe after channel close (idempotent).
	assert.NotPanics(t, func() { w.Stop() })
	assert.True(t, unsubCalled)
}

// TestWatcher_UnmarshalFailure_Skipped verifies that a notification with
// malformed JSON params is silently skipped and does not block or crash the
// watcher. Subsequent valid events are still delivered.
func TestWatcher_UnmarshalFailure_Skipped(t *testing.T) {
	notifCh := make(chan jsonrpc.NotificationMsg, 8)
	unsub := func() { close(notifCh) }
	w := newWatcher("w-1", 0, notifCh, unsub)
	defer w.Stop()

	// Send malformed notification.
	notifCh <- jsonrpc.NotificationMsg{
		Method: apishim.MethodRuntimeEventUpdate,
		Params: json.RawMessage(`{not valid json`),
	}

	// Send valid event after the malformed one.
	notifCh <- makeNotifMsg(t, apishim.AgentRunEvent{
		WatchID: "w-1",
		RunID:   "run-1",
		Seq:     1,
		Type:    apishim.EventTypeTurnEnd,
		Payload: apishim.TurnEndEvent{StopReason: "end_turn"},
	})

	// The valid event should be delivered; the malformed one silently dropped.
	select {
	case ev := <-w.ResultChan():
		assert.Equal(t, 1, ev.Seq)
		assert.Equal(t, apishim.EventTypeTurnEnd, ev.Type)
	case <-time.After(2 * time.Second):
		t.Fatal("timeout: valid event after malformed notification should still be delivered")
	}
}

// TestWatcher_SlowConsumer_DropsEvent verifies that when ResultChan is full
// (consumer not draining), additional events are dropped without blocking the
// filter goroutine. The watcher must not deadlock.
func TestWatcher_SlowConsumer_DropsEvent(t *testing.T) {
	notifCh := make(chan jsonrpc.NotificationMsg, 512)
	unsub := func() { close(notifCh) }
	w := newWatcher("w-1", 0, notifCh, unsub)
	defer w.Stop()

	// Flood the watcher with more events than the result channel buffer (256).
	const total = 300
	for i := 0; i < total; i++ {
		notifCh <- makeNotifMsg(t, apishim.AgentRunEvent{
			WatchID: "w-1",
			RunID:   "run-1",
			Seq:     i,
			Type:    apishim.EventTypeAgentMessage,
			Payload: apishim.NewContentEvent(apishim.EventTypeAgentMessage, "", apishim.TextBlock("flood")),
		})
	}

	// Let the filter goroutine process all notifications.
	time.Sleep(200 * time.Millisecond)

	// Drain whatever arrived — should be <= 256 (buffer size).
	var received int
	for {
		select {
		case _, ok := <-w.ResultChan():
			if !ok {
				t.Fatal("ResultChan should not be closed")
			}
			received++
		default:
			goto done
		}
	}
done:
	assert.LessOrEqual(t, received, 256,
		"received events must not exceed result channel buffer size")
	assert.Greater(t, received, 0,
		"at least some events should be delivered")
	t.Logf("delivered %d/%d events (dropped %d)", received, total, total-received)
}

// TestWatcher_Accessors verifies that WatchID() and NextSeq() return the values
// provided at construction time.
func TestWatcher_Accessors(t *testing.T) {
	notifCh := make(chan jsonrpc.NotificationMsg)
	unsub := func() { close(notifCh) }
	w := newWatcher("w-42", 17, notifCh, unsub)
	defer w.Stop()

	assert.Equal(t, "w-42", w.WatchID())
	assert.Equal(t, 17, w.NextSeq())
}
