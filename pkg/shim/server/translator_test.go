package server

import (
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"

	acp "github.com/coder/acp-go-sdk"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	apishim "github.com/zoumo/mass/pkg/shim/api"
)

func makeNotif(fn func(*acp.SessionUpdate)) acp.SessionNotification {
	var n acp.SessionNotification
	fn(&n.Update)
	return n
}

func drainShimEvent(t *testing.T, ch <-chan apishim.ShimEvent) apishim.ShimEvent {
	t.Helper()
	select {
	case ev := <-ch:
		return ev
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for ShimEvent")
		return apishim.ShimEvent{}
	}
}

// sessionPayload extracts the typed Event content from a session category ShimEvent.
func sessionPayload(t *testing.T, ev apishim.ShimEvent) apishim.Event {
	t.Helper()
	assert.Equal(t, apishim.CategorySession, ev.Category, "expected session category event")
	require.NotNil(t, ev.Payload, "expected non-nil payload")
	return ev.Payload
}

func sendAndDrainShimEvent(t *testing.T, in chan<- acp.SessionNotification, ch <-chan apishim.ShimEvent, text string) apishim.ShimEvent {
	t.Helper()
	in <- makeNotif(func(u *acp.SessionUpdate) {
		u.AgentMessageChunk = &acp.SessionUpdateAgentMessageChunk{
			Content: acp.TextBlock(text),
		}
	})
	return drainShimEvent(t, ch)
}

func TestTranslate_AgentMessageChunk(t *testing.T) {
	in := make(chan acp.SessionNotification, 1)
	tr := NewTranslator("run-1", in, nil)
	ch, _, _ := tr.Subscribe()
	tr.Start()
	defer tr.Stop()

	in <- makeNotif(func(u *acp.SessionUpdate) {
		u.AgentMessageChunk = &acp.SessionUpdateAgentMessageChunk{
			Content: acp.TextBlock("hello"),
		}
	})

	ev := drainShimEvent(t, ch)
	assert.Equal(t, "run-1", ev.RunID)
	assert.Equal(t, 0, ev.Seq)
	assert.Equal(t, apishim.CategorySession, ev.Category)
	assert.Equal(t, apishim.EventTypeAgentMessage, ev.Type)
	te, ok := ev.Payload.(apishim.ContentEvent)
	require.True(t, ok)
	require.NotNil(t, te.Content.Text)
	assert.Equal(t, "hello", te.Content.Text.Text)
}

func TestTranslate_AgentThoughtChunk(t *testing.T) {
	in := make(chan acp.SessionNotification, 1)
	tr := NewTranslator("run-1", in, nil)
	ch, _, _ := tr.Subscribe()
	tr.Start()
	defer tr.Stop()

	in <- makeNotif(func(u *acp.SessionUpdate) {
		u.AgentThoughtChunk = &acp.SessionUpdateAgentThoughtChunk{
			Content: acp.TextBlock("thinking"),
		}
	})

	ev := drainShimEvent(t, ch)
	te, ok := sessionPayload(t, ev).(apishim.ContentEvent)
	require.True(t, ok)
	require.NotNil(t, te.Content.Text)
	assert.Equal(t, "thinking", te.Content.Text.Text)
}

func TestTranslate_ToolCall(t *testing.T) {
	in := make(chan acp.SessionNotification, 1)
	tr := NewTranslator("run-1", in, nil)
	ch, _, _ := tr.Subscribe()
	tr.Start()
	defer tr.Stop()

	in <- makeNotif(func(u *acp.SessionUpdate) {
		u.ToolCall = &acp.SessionUpdateToolCall{ToolCallId: "tc-1", Kind: "shell", Title: "run ls"}
	})

	ev := drainShimEvent(t, ch)
	tc, ok := sessionPayload(t, ev).(apishim.ToolCallEvent)
	require.True(t, ok)
	assert.Equal(t, apishim.ToolCallEvent{ID: "tc-1", Kind: "shell", Title: "run ls"}, tc)
}

func TestTranslate_ToolCallUpdate(t *testing.T) {
	in := make(chan acp.SessionNotification, 1)
	tr := NewTranslator("run-1", in, nil)
	ch, _, _ := tr.Subscribe()
	tr.Start()
	defer tr.Stop()

	status := acp.ToolCallStatus("completed")
	in <- makeNotif(func(u *acp.SessionUpdate) {
		u.ToolCallUpdate = &acp.SessionToolCallUpdate{ToolCallId: "tc-1", Status: &status}
	})

	ev := drainShimEvent(t, ch)
	tr_, ok := sessionPayload(t, ev).(apishim.ToolResultEvent)
	require.True(t, ok)
	assert.Equal(t, apishim.ToolResultEvent{ID: "tc-1", Status: "completed"}, tr_)
}

func TestTranslate_ToolCallUpdate_NilStatus(t *testing.T) {
	in := make(chan acp.SessionNotification, 1)
	tr := NewTranslator("run-1", in, nil)
	ch, _, _ := tr.Subscribe()
	tr.Start()
	defer tr.Stop()

	in <- makeNotif(func(u *acp.SessionUpdate) {
		u.ToolCallUpdate = &acp.SessionToolCallUpdate{ToolCallId: "tc-2"}
	})

	ev := drainShimEvent(t, ch)
	tr_, ok := sessionPayload(t, ev).(apishim.ToolResultEvent)
	require.True(t, ok)
	assert.Equal(t, apishim.ToolResultEvent{ID: "tc-2", Status: "unknown"}, tr_)
}

func TestTranslate_Plan(t *testing.T) {
	in := make(chan acp.SessionNotification, 1)
	tr := NewTranslator("run-1", in, nil)
	ch, _, _ := tr.Subscribe()
	tr.Start()
	defer tr.Stop()

	entries := []acp.PlanEntry{{Content: "step 1", Status: acp.PlanEntryStatusPending}, {Content: "step 2", Status: acp.PlanEntryStatusInProgress}}
	in <- makeNotif(func(u *acp.SessionUpdate) {
		u.Plan = &acp.SessionUpdatePlan{Entries: entries}
	})

	ev := drainShimEvent(t, ch)
	pe, ok := sessionPayload(t, ev).(apishim.PlanEvent)
	require.True(t, ok)
	assert.Len(t, pe.Entries, 2)
	assert.Equal(t, "step 1", pe.Entries[0].Content)
}

func TestTranslate_UserMessageChunk(t *testing.T) {
	in := make(chan acp.SessionNotification, 1)
	tr := NewTranslator("run-1", in, nil)
	ch, _, _ := tr.Subscribe()
	tr.Start()
	defer tr.Stop()

	in <- acp.SessionNotification{Update: acp.SessionUpdate{
		UserMessageChunk: &acp.SessionUpdateUserMessageChunk{
			Content: acp.TextBlock("hello from user"),
		},
	}}

	ev := drainShimEvent(t, ch)
	ue, ok := sessionPayload(t, ev).(apishim.ContentEvent)
	require.True(t, ok)
	require.NotNil(t, ue.Content.Text)
	assert.Equal(t, "hello from user", ue.Content.Text.Text)
}

func TestTranslate_PreviouslyIgnoredVariants(t *testing.T) {
	in := make(chan acp.SessionNotification, 3)
	tr := NewTranslator("run-1", in, nil)
	ch, _, _ := tr.Subscribe()
	tr.Start()
	defer tr.Stop()

	in <- acp.SessionNotification{Update: acp.SessionUpdate{AvailableCommandsUpdate: &acp.SessionAvailableCommandsUpdate{}}}
	in <- acp.SessionNotification{Update: acp.SessionUpdate{CurrentModeUpdate: &acp.SessionCurrentModeUpdate{}}}
	in <- acp.SessionNotification{Update: acp.SessionUpdate{
		AgentMessageChunk: &acp.SessionUpdateAgentMessageChunk{
			Content: acp.TextBlock("after"),
		},
	}}

	ev1 := drainShimEvent(t, ch)
	ev2 := drainShimEvent(t, ch)
	ev3 := drainShimEvent(t, ch)
	assert.IsType(t, apishim.AvailableCommandsEvent{}, sessionPayload(t, ev1))
	assert.IsType(t, apishim.CurrentModeEvent{}, sessionPayload(t, ev2))
	te, ok := sessionPayload(t, ev3).(apishim.ContentEvent)
	require.True(t, ok)
	require.NotNil(t, te.Content.Text)
	assert.Equal(t, "after", te.Content.Text.Text)
}

func TestTranslate_UnknownVariant(t *testing.T) {
	in := make(chan acp.SessionNotification, 1)
	tr := NewTranslator("run-1", in, nil)
	ch, _, _ := tr.Subscribe()
	tr.Start()
	defer tr.Stop()

	in <- acp.SessionNotification{}

	ev := drainShimEvent(t, ch)
	ee, ok := sessionPayload(t, ev).(apishim.ErrorEvent)
	require.True(t, ok)
	assert.Equal(t, apishim.ErrorEvent{Msg: "unknown session update variant"}, ee)
}

func TestFanOut_ThreeSubscribers(t *testing.T) {
	in := make(chan acp.SessionNotification, 1)
	tr := NewTranslator("run-1", in, nil)
	ch1, _, _ := tr.Subscribe()
	ch2, _, _ := tr.Subscribe()
	ch3, _, _ := tr.Subscribe()
	tr.Start()
	defer tr.Stop()

	in <- makeNotif(func(u *acp.SessionUpdate) {
		u.AgentMessageChunk = &acp.SessionUpdateAgentMessageChunk{
			Content: acp.TextBlock("broadcast"),
		}
	})

	for _, ch := range []<-chan apishim.ShimEvent{ch1, ch2, ch3} {
		ev := drainShimEvent(t, ch)
		te, ok := ev.Payload.(apishim.ContentEvent)
		require.True(t, ok)
		require.NotNil(t, te.Content.Text)
		assert.Equal(t, "broadcast", te.Content.Text.Text)
	}
}

func TestNotifyTurnStartAndEnd(t *testing.T) {
	in := make(chan acp.SessionNotification)
	tr := NewTranslator("run-1", in, nil)
	ch, _, _ := tr.Subscribe()
	tr.Start()
	defer tr.Stop()

	tr.NotifyTurnStart()
	tr.NotifyTurnEnd(acp.StopReason("end_turn"))

	first := drainShimEvent(t, ch)
	second := drainShimEvent(t, ch)
	assert.Equal(t, 0, first.Seq)
	assert.Equal(t, 1, second.Seq)
	assert.Equal(t, apishim.CategorySession, first.Category)
	assert.Equal(t, apishim.CategorySession, second.Category)
	assert.Equal(t, "turn_start", first.Type)
	assert.Equal(t, "turn_end", second.Type)
	assert.NotEmpty(t, first.TurnID, "turn_start must carry a non-empty TurnID")
	assert.Equal(t, first.TurnID, second.TurnID, "turn_end must carry the same TurnID as turn_start")
	// After turn_end, TurnID is cleared — subsequent events won't have it.
}

func TestNotifyStateChange(t *testing.T) {
	in := make(chan acp.SessionNotification)
	tr := NewTranslator("run-1", in, nil)
	ch, _, nextSeq := tr.Subscribe()
	tr.Start()
	defer tr.Stop()

	assert.Equal(t, 0, nextSeq)
	tr.NotifyStateChange("created", "running", 1234, "prompt-started", nil)

	ev := drainShimEvent(t, ch)
	assert.Equal(t, apishim.CategoryRuntime, ev.Category)
	assert.Equal(t, "state_change", ev.Type)
	assert.Equal(t, "run-1", ev.RunID)
	assert.Equal(t, 0, ev.Seq)
	assert.Empty(t, ev.TurnID, "state_change must not carry TurnID")
	sc, ok := ev.Payload.(apishim.StateChangeEvent)
	require.True(t, ok)
	assert.Equal(t, "created", sc.PreviousStatus)
	assert.Equal(t, "running", sc.Status)
	assert.Equal(t, 1234, sc.PID)
	assert.Equal(t, "prompt-started", sc.Reason)
}

func TestNotifyStateChange_WithSessionChanged(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "events.jsonl")

	evLog, err := OpenEventLog(logPath)
	require.NoError(t, err)
	defer evLog.Close()

	in := make(chan acp.SessionNotification)
	tr := NewTranslator("run-1", in, evLog)
	tr.Start()

	tr.NotifyStateChange("idle", "idle", 42, "bootstrap-metadata", []string{"agentInfo", "capabilities"})

	// Read the event log to verify the persisted event.
	entries, readErr := ReadEventLog(logPath, 0)
	require.NoError(t, readErr)
	require.Len(t, entries, 1)

	entry := entries[0]
	assert.Equal(t, "state_change", entry.Type)
	assert.Equal(t, apishim.CategoryRuntime, entry.Category)

	sc, ok := entry.Payload.(apishim.StateChangeEvent)
	require.True(t, ok)
	assert.Equal(t, "bootstrap-metadata", sc.Reason)
	assert.Equal(t, []string{"agentInfo", "capabilities"}, sc.SessionChanged)
	assert.Equal(t, "idle", sc.PreviousStatus)
	assert.Equal(t, "idle", sc.Status)
	assert.Equal(t, 42, sc.PID)

	tr.Stop()
}

func TestShimEventRoundTrip(t *testing.T) {
	ev := apishim.ShimEvent{
		RunID:     "run-1",
		SessionID: "acp-xxx",
		Seq:       7,
		Time:      time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		Category:  apishim.CategorySession,
		Type:      "tool_call",
		TurnID:    "turn-001",
		
		Payload:   apishim.ToolCallEvent{ID: "1", Kind: "shell", Title: "ls"},
	}
	data, err := ev.MarshalJSON()
	require.NoError(t, err)

	var decoded apishim.ShimEvent
	require.NoError(t, decoded.UnmarshalJSON(data))
	assert.Equal(t, ev.RunID, decoded.RunID)
	assert.Equal(t, ev.SessionID, decoded.SessionID)
	assert.Equal(t, ev.Seq, decoded.Seq)
	assert.Equal(t, ev.Category, decoded.Category)
	assert.Equal(t, ev.Type, decoded.Type)
	assert.Equal(t, ev.TurnID, decoded.TurnID)
	tc, ok := decoded.Payload.(apishim.ToolCallEvent)
	require.True(t, ok)
	assert.Equal(t, apishim.ToolCallEvent{ID: "1", Kind: "shell", Title: "ls"}, tc)
}

func TestShimEventRoundTrip_NoTurnFields(t *testing.T) {
	// omitempty should suppress empty turn fields.
	ev := apishim.ShimEvent{
		RunID:    "run-1",
		Seq:      0,
		Time:     time.Now(),
		Category: apishim.CategorySession,
		Type:     apishim.EventTypeAgentMessage,
		Payload:  apishim.NewContentEvent(apishim.EventTypeAgentMessage, "", apishim.TextBlock("no turn")),
	}
	data, err := ev.MarshalJSON()
	require.NoError(t, err)
	assert.NotContains(t, string(data), "turnId", "omitempty should suppress empty turnId")
	assert.NotContains(t, string(data), "phase", "omitempty should suppress empty phase")
}

func TestEventTypes(t *testing.T) {
	cases := []struct {
		ev   apishim.Event
		want string
	}{
		{apishim.NewContentEvent(apishim.EventTypeAgentMessage, "", apishim.TextBlock("hi")), "agent_message"},
		{apishim.NewContentEvent(apishim.EventTypeAgentThinking, "", apishim.TextBlock("hmm")), "agent_thinking"},
		{apishim.NewContentEvent(apishim.EventTypeUserMessage, "", apishim.TextBlock("yo")), "user_message"},
		{apishim.ToolCallEvent{ID: "1", Kind: "shell", Title: "ls"}, "tool_call"},
		{apishim.ToolResultEvent{ID: "1", Status: "ok"}, "tool_result"},
		{apishim.PlanEvent{Entries: nil}, "plan"},
		{apishim.TurnStartEvent{}, "turn_start"},
		{apishim.TurnEndEvent{StopReason: "end_turn"}, "turn_end"},
		{apishim.ErrorEvent{Msg: "oops"}, "error"},
		{apishim.StateChangeEvent{PreviousStatus: "idle", Status: "running"}, "state_change"},
	}
	for _, tc := range cases {
		assert.Equal(t, tc.want, apishim.EventTypeOf(tc.ev), "wrong eventType for %T", tc.ev)
	}
}

func TestSubscribeFromSeq_BackfillAndLive(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "events.jsonl")

	at := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	log1, err := OpenEventLog(logPath)
	require.NoError(t, err)
	for i := 0; i < 5; i++ {
		ev := apishim.ShimEvent{RunID: "s1", Seq: i, Time: at, Category: apishim.CategorySession, Type: apishim.EventTypeAgentMessage, Payload: apishim.NewContentEvent(apishim.EventTypeAgentMessage, "", apishim.TextBlock(fmt.Sprintf("msg-%d", i)))}
		require.NoError(t, log1.Append(ev))
	}
	require.NoError(t, log1.Close())

	log2, err := OpenEventLog(logPath)
	require.NoError(t, err)
	defer log2.Close()

	in := make(chan acp.SessionNotification, 1)
	tr := NewTranslator("s1", in, log2)
	tr.Start()
	defer tr.Stop()

	// SubscribeFromSeq(logPath, 2) should return 3 backfill entries (seq 2,3,4).
	entries, ch, _, nextSeq, err := tr.SubscribeFromSeq(logPath, 2)
	require.NoError(t, err)
	require.Len(t, entries, 3)
	for i, e := range entries {
		assert.Equal(t, i+2, e.Seq, "backfill entry %d has wrong seq", i)
	}
	assert.Equal(t, 5, nextSeq)

	// Broadcast a new event — gets seq 5.
	in <- makeNotif(func(u *acp.SessionUpdate) {
		u.AgentMessageChunk = &acp.SessionUpdateAgentMessageChunk{
			Content: acp.TextBlock("live"),
		}
	})
	liveEv := drainShimEvent(t, ch)
	assert.Equal(t, 5, liveEv.Seq, "live event should have seq 5, no gap after backfill end at seq 4")
}

func TestSubscribeFromSeq_EmptyLog(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "nonexistent.jsonl")

	in := make(chan acp.SessionNotification, 1)
	tr := NewTranslator("s1", in, nil)
	tr.Start()
	defer tr.Stop()

	entries, ch, _, nextSeq, err := tr.SubscribeFromSeq(logPath, 0)
	require.NoError(t, err)
	assert.Empty(t, entries)
	assert.Equal(t, 0, nextSeq)

	tr.NotifyTurnStart()
	liveEv := drainShimEvent(t, ch)
	assert.Equal(t, 0, liveEv.Seq)
}

// TestTurnAwareShimEvent_TurnIdAssigned verifies that all session events
// emitted between NotifyTurnStart and NotifyTurnEnd carry the same non-empty
// TurnID, and that a runtime event emitted after NotifyTurnEnd carries no TurnID.
func TestTurnAwareShimEvent_TurnIdAssigned(t *testing.T) {
	in := make(chan acp.SessionNotification, 4)
	tr := NewTranslator("run-1", in, nil)
	ch, _, _ := tr.Subscribe()
	tr.Start()
	defer tr.Stop()

	tr.NotifyTurnStart()
	tsEv := drainShimEvent(t, ch)

	txt1Ev := sendAndDrainShimEvent(t, in, ch, "hello")
	txt2Ev := sendAndDrainShimEvent(t, in, ch, "world")

	tr.NotifyTurnEnd(acp.StopReason("end_turn"))
	blockEndEv := drainShimEvent(t, ch) // synthetic agent_message{end}
	teEv := drainShimEvent(t, ch)       // turn_end

	require.NotEmpty(t, tsEv.TurnID)
	assert.Equal(t, tsEv.TurnID, txt1Ev.TurnID, "text event 1 must carry TurnID")
	assert.Equal(t, tsEv.TurnID, txt2Ev.TurnID, "text event 2 must carry TurnID")
	assert.Equal(t, apishim.EventTypeAgentMessage, blockEndEv.Type, "synthetic end must be agent_message")
	assert.Equal(t, tsEv.TurnID, blockEndEv.TurnID, "synthetic end must carry TurnID")
	assert.Equal(t, tsEv.TurnID, teEv.TurnID, "turn_end must carry same TurnID")

	// State change after turn_end should not have TurnID.
	tr.NotifyStateChange("running", "created", 0, "done", nil)
	scEv := drainShimEvent(t, ch)
	assert.Equal(t, apishim.CategoryRuntime, scEv.Category)
	assert.Empty(t, scEv.TurnID, "runtime state_change must not carry TurnID")
}

// TestTurnAwareShimEvent_TurnIDChangesPerTurn verifies that TurnID changes
// between consecutive turns.
func TestTurnAwareShimEvent_TurnIDChangesPerTurn(t *testing.T) {
	in := make(chan acp.SessionNotification, 8)
	tr := NewTranslator("run-1", in, nil)
	ch, _, _ := tr.Subscribe()
	tr.Start()
	defer tr.Stop()

	// Turn 1.
	tr.NotifyTurnStart()
	ts1 := drainShimEvent(t, ch)
	sendAndDrainShimEvent(t, in, ch, "turn1")
	tr.NotifyTurnEnd(acp.StopReason("end_turn"))
	drainShimEvent(t, ch) // turn_end
	drainShimEvent(t, ch) // synthetic content end

	// Turn 2 — TurnID must differ.
	tr.NotifyTurnStart()
	ts2 := drainShimEvent(t, ch)
	assert.NotEqual(t, ts1.TurnID, ts2.TurnID, "turn 2 must have a different TurnID")
}

// TestTurnAwareShimEvent_StateChangeExcludesTurnFields verifies that state_change
// events emitted during an active turn do not carry turn fields.
func TestTurnAwareShimEvent_StateChangeExcludesTurnFields(t *testing.T) {
	in := make(chan acp.SessionNotification)
	tr := NewTranslator("run-1", in, nil)
	ch, _, _ := tr.Subscribe()
	tr.Start()
	defer tr.Stop()

	tr.NotifyTurnStart()
	tsEv := drainShimEvent(t, ch)
	require.NotEmpty(t, tsEv.TurnID)

	tr.NotifyStateChange("created", "running", 0, "", nil)
	scEv := drainShimEvent(t, ch)

	assert.Equal(t, apishim.CategoryRuntime, scEv.Category)
	assert.Empty(t, scEv.TurnID, "state_change must not carry TurnID even during active turn")

	// Seq must increment correctly.
	assert.Equal(t, tsEv.Seq+1, scEv.Seq, "state_change seq must follow turn_start seq")
}

// TestTurnAwareShimEvent_MetadataEventInTurn verifies that session metadata events
// (session_info, usage, etc.) carry TurnID when inside an active turn.
func TestTurnAwareShimEvent_MetadataEventInTurn(t *testing.T) {
	in := make(chan acp.SessionNotification, 2)
	tr := NewTranslator("run-1", in, nil)
	ch, _, _ := tr.Subscribe()
	tr.Start()
	defer tr.Stop()

	tr.NotifyTurnStart()
	tsEv := drainShimEvent(t, ch)
	require.NotEmpty(t, tsEv.TurnID)

	// Send a session_info update (metadata event) while inside an active turn.
	title := "My Session"
	in <- acp.SessionNotification{Update: acp.SessionUpdate{
		SessionInfoUpdate: &acp.SessionSessionInfoUpdate{Title: &title},
	}}
	siEv := drainShimEvent(t, ch)

	assert.Equal(t, apishim.CategorySession, siEv.Category)
	assert.Equal(t, "session_info", siEv.Type)
	assert.Equal(t, tsEv.TurnID, siEv.TurnID, "session_info in active turn must carry TurnID")
}

// TestTurnAwareShimEvent_MetadataEventOutsideTurn verifies that session metadata events
// do NOT carry TurnID when outside an active turn.
func TestTurnAwareShimEvent_MetadataEventOutsideTurn(t *testing.T) {
	in := make(chan acp.SessionNotification, 2)
	tr := NewTranslator("run-1", in, nil)
	ch, _, _ := tr.Subscribe()
	tr.Start()
	defer tr.Stop()

	// Send session_info BEFORE any turn starts.
	title := "My Session"
	in <- acp.SessionNotification{Update: acp.SessionUpdate{
		SessionInfoUpdate: &acp.SessionSessionInfoUpdate{Title: &title},
	}}
	siEv := drainShimEvent(t, ch)

	assert.Equal(t, apishim.CategorySession, siEv.Category)
	assert.Empty(t, siEv.TurnID, "session_info outside turn must NOT carry TurnID")
}

// TestFailClosed_AppendFailureDropsEvent verifies that if EventLog.Append fails,
// the event is not fanned out and nextSeq is not incremented.
func TestFailClosed_AppendFailureDropsEvent(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "events.jsonl")

	evLog, err := OpenEventLog(logPath)
	require.NoError(t, err)
	defer evLog.Close()

	in := make(chan acp.SessionNotification, 4)
	tr := NewTranslator("run-1", in, evLog)
	ch, _, _ := tr.Subscribe()
	tr.Start()
	defer tr.Stop()

	// Send one successful event (seq 0).
	in <- makeNotif(func(u *acp.SessionUpdate) {
		u.AgentMessageChunk = &acp.SessionUpdateAgentMessageChunk{
			Content: acp.TextBlock("first"),
		}
	})
	ev0 := drainShimEvent(t, ch)
	assert.Equal(t, 0, ev0.Seq)

	// Now close the event log to force Append to fail.
	require.NoError(t, evLog.Close())

	// Send a second event — Append will fail (file closed), so it should be dropped.
	in <- makeNotif(func(u *acp.SessionUpdate) {
		u.AgentMessageChunk = &acp.SessionUpdateAgentMessageChunk{
			Content: acp.TextBlock("dropped"),
		}
	})

	// Wait a bit to allow the translator goroutine to process.
	select {
	case unexpectedEv := <-ch:
		t.Fatalf("event should have been dropped, but got seq=%d type=%s", unexpectedEv.Seq, unexpectedEv.Type)
	case <-time.After(200 * time.Millisecond):
		// Expected: the dropped event did not reach the subscriber.
	}
}

// TestConcurrentBroadcast_SeqContinuous verifies that concurrent broadcasts
// from NotifyStateChange and ACP events produce a JSONL log with no seq gaps.
func TestConcurrentBroadcast_SeqContinuous(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "events.jsonl")

	evLog, err := OpenEventLog(logPath)
	require.NoError(t, err)

	in := make(chan acp.SessionNotification, 100)
	tr := NewTranslator("run-1", in, evLog)
	ch, subID, _ := tr.Subscribe()
	tr.Start()

	const numEvents = 20
	var wg sync.WaitGroup

	// Producer 1: ACP text notifications.
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < numEvents/2; i++ {
			in <- makeNotif(func(u *acp.SessionUpdate) {
				u.AgentMessageChunk = &acp.SessionUpdateAgentMessageChunk{
					Content: acp.TextBlock("txt"),
				}
			})
		}
	}()

	// Producer 2: state change notifications.
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < numEvents/2; i++ {
			tr.NotifyStateChange("idle", "running", i, "test", nil)
		}
	}()

	// Drain all events.
	received := make([]apishim.ShimEvent, 0, numEvents)
	done := make(chan struct{})
	go func() {
		defer close(done)
		for ev := range ch {
			received = append(received, ev)
			if len(received) == numEvents {
				return
			}
		}
	}()

	wg.Wait()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("timeout waiting for all events")
	}

	tr.Unsubscribe(subID)
	tr.Stop()
	require.NoError(t, evLog.Close())

	// Verify JSONL file has consecutive seq numbers.
	entries, err := ReadEventLog(logPath, 0)
	require.NoError(t, err)
	require.Len(t, entries, numEvents, "JSONL must have exactly numEvents entries")
	for i, e := range entries {
		assert.Equal(t, i, e.Seq, "JSONL seq at index %d must equal %d", i, i)
	}
}

// TestTurnAwareShimEvent_ReplayOrdering verifies replay ordering invariants.
func TestTurnAwareShimEvent_ReplayOrdering(t *testing.T) {
	in := make(chan acp.SessionNotification, 8)
	tr := NewTranslator("run-1", in, nil)
	ch, _, _ := tr.Subscribe()
	tr.Start()
	defer tr.Stop()

	// Turn 1: turn_start + 2 text events + synthetic block end + turn_end.
	tr.NotifyTurnStart()
	ts1Ev := drainShimEvent(t, ch)
	t1aEv := sendAndDrainShimEvent(t, in, ch, "t1-a")
	t1bEv := sendAndDrainShimEvent(t, in, ch, "t1-b")
	tr.NotifyTurnEnd(acp.StopReason("end_turn"))
	t1EndEv := drainShimEvent(t, ch) // synthetic agent_message{end}
	te1Ev := drainShimEvent(t, ch)   // turn_end
	turn1 := []apishim.ShimEvent{ts1Ev, t1aEv, t1bEv, t1EndEv, te1Ev}

	// Turn 2: turn_start + 1 text event + synthetic block end + turn_end.
	tr.NotifyTurnStart()
	ts2Ev := drainShimEvent(t, ch)
	t2aEv := sendAndDrainShimEvent(t, in, ch, "t2-a")
	tr.NotifyTurnEnd(acp.StopReason("end_turn"))
	t2EndEv := drainShimEvent(t, ch) // synthetic agent_message{end}
	te2Ev := drainShimEvent(t, ch)   // turn_end
	turn2 := []apishim.ShimEvent{ts2Ev, t2aEv, t2EndEv, te2Ev}

	// (1) All turn 1 events share a common TurnID.
	tid1 := turn1[0].TurnID
	require.NotEmpty(t, tid1)
	for i, ev := range turn1 {
		assert.Equal(t, tid1, ev.TurnID, "turn1[%d] TurnID mismatch", i)
	}

	// (2) All turn 2 events share a different TurnID.
	tid2 := turn2[0].TurnID
	require.NotEmpty(t, tid2)
	assert.NotEqual(t, tid1, tid2, "turn 2 must have a different TurnID than turn 1")
	for i, ev := range turn2 {
		assert.Equal(t, tid2, ev.TurnID, "turn2[%d] TurnID mismatch", i)
	}

	// (3) Global seq is strictly monotonic across both turns.
	all := append(turn1, turn2...)
	for i := 1; i < len(all); i++ {
		assert.Equal(t, all[i-1].Seq+1, all[i].Seq, "global seq must be monotonic at position %d", i)
	}

	// (4) TurnID differs between turns.
	assert.NotEqual(t, ts1Ev.TurnID, ts2Ev.TurnID, "turn 1 and turn 2 must have different TurnIDs")
}

// TestEventCounts_PromptTurn verifies that EventCounts() returns correct
// per-type counts after a full prompt turn cycle.
func TestEventCounts_PromptTurn(t *testing.T) {
	in := make(chan acp.SessionNotification, 8)
	tr := NewTranslator("run-1", in, nil)
	ch, _, _ := tr.Subscribe()
	tr.Start()
	defer tr.Stop()

	// turn_start
	tr.NotifyTurnStart()
	drainShimEvent(t, ch)

	// user_message
	tr.NotifyUserPrompt("hello")
	drainShimEvent(t, ch)

	// 2 text events (AgentMessageChunk)
	sendAndDrainShimEvent(t, in, ch, "chunk-1")
	sendAndDrainShimEvent(t, in, ch, "chunk-2")

	// 1 tool_call (triggers synthetic agent_message{end} before tool_call)
	in <- makeNotif(func(u *acp.SessionUpdate) {
		u.ToolCall = &acp.SessionUpdateToolCall{ToolCallId: "tc-1", Kind: "shell", Title: "ls"}
	})
	drainShimEvent(t, ch) // synthetic agent_message{end}
	drainShimEvent(t, ch) // tool_call

	// turn_end
	tr.NotifyTurnEnd(acp.StopReason("end_turn"))
	drainShimEvent(t, ch)

	// state_change
	tr.NotifyStateChange("running", "idle", 0, "done", nil)
	drainShimEvent(t, ch)

	counts := tr.EventCounts()
	assert.Equal(t, 1, counts["turn_start"], "turn_start count")
	assert.Equal(t, 1, counts["user_message"], "user_message count")
	assert.Equal(t, 3, counts["agent_message"], "agent_message count (start+streaming+synthetic end)")
	assert.Equal(t, 1, counts["tool_call"], "tool_call count")
	assert.Equal(t, 1, counts["turn_end"], "turn_end count")
	assert.Equal(t, 1, counts["state_change"], "state_change count")
}

// TestEventCounts_FailClosedOnAppendFailure verifies that eventCounts are NOT
// incremented when EventLog.Append fails (fail-closed semantics).
func TestEventCounts_FailClosedOnAppendFailure(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "events.jsonl")

	evLog, err := OpenEventLog(logPath)
	require.NoError(t, err)
	defer evLog.Close()

	in := make(chan acp.SessionNotification, 4)
	tr := NewTranslator("run-1", in, evLog)
	ch, _, _ := tr.Subscribe()
	tr.Start()
	defer tr.Stop()

	// Send one successful AgentMessageChunk → count should be 1.
	sendAndDrainShimEvent(t, in, ch, "ok")
	assert.Equal(t, 1, tr.EventCounts()["agent_message"], "agent_message count after successful event")

	// Close the event log file to force Append failures.
	require.NoError(t, evLog.Close())

	// Send another AgentMessageChunk — should be dropped (fail-closed).
	in <- makeNotif(func(u *acp.SessionUpdate) {
		u.AgentMessageChunk = &acp.SessionUpdateAgentMessageChunk{
			Content: acp.TextBlock("dropped"),
		}
	})

	// Wait for the translator goroutine to process the failed event.
	select {
	case unexpectedEv := <-ch:
		t.Fatalf("event should have been dropped, but got seq=%d type=%s", unexpectedEv.Seq, unexpectedEv.Type)
	case <-time.After(200 * time.Millisecond):
		// Expected: dropped.
	}

	// Count must still be 1 — the failed append must NOT increment.
	assert.Equal(t, 1, tr.EventCounts()["agent_message"], "agent_message count must not increment on failed append")
}

// TestSessionMetadataHook_ConfigOption verifies that sessionMetadataHook is
// called with a ConfigOptionEvent when a ConfigOptionUpdate notification arrives.
func TestSessionMetadataHook_ConfigOption(t *testing.T) {
	in := make(chan acp.SessionNotification, 1)
	tr := NewTranslator("run-1", in, nil)
	ch, _, _ := tr.Subscribe()

	var captured apishim.Event
	var hookMu sync.Mutex
	tr.SetSessionMetadataHook(func(ev apishim.Event) {
		hookMu.Lock()
		defer hookMu.Unlock()
		captured = ev
	})
	tr.Start()
	defer tr.Stop()

	// Inject a ConfigOptionUpdate notification.
	optValue := acp.SessionConfigValueId("dark")
	ungrouped := acp.SessionConfigSelectOptionsUngrouped{
		{Name: "Dark", Value: optValue},
	}
	in <- acp.SessionNotification{Update: acp.SessionUpdate{
		ConfigOptionUpdate: &acp.SessionConfigOptionUpdate{
			ConfigOptions: []acp.SessionConfigOption{
				{Select: &acp.SessionConfigOptionSelect{
					Id:           "theme",
					Name:         "Theme",
					CurrentValue: optValue,
					Options: acp.SessionConfigSelectOptions{
						Ungrouped: &ungrouped,
					},
				}},
			},
		},
	}}

	// Drain the broadcast event to ensure run() processed the notification.
	drainShimEvent(t, ch)

	hookMu.Lock()
	defer hookMu.Unlock()
	require.NotNil(t, captured, "sessionMetadataHook must be called for config_option")
	co, ok := captured.(apishim.ConfigOptionEvent)
	require.True(t, ok, "captured event must be ConfigOptionEvent")
	require.Len(t, co.ConfigOptions, 1)
	require.NotNil(t, co.ConfigOptions[0].Select)
	assert.Equal(t, "theme", co.ConfigOptions[0].Select.ID)
}

// TestSessionMetadataHook_IgnoresNonMetadata verifies that sessionMetadataHook
// is NOT called for non-metadata event types like text.
func TestSessionMetadataHook_IgnoresNonMetadata(t *testing.T) {
	in := make(chan acp.SessionNotification, 1)
	tr := NewTranslator("run-1", in, nil)
	ch, _, _ := tr.Subscribe()

	hookCalled := false
	tr.SetSessionMetadataHook(func(ev apishim.Event) {
		hookCalled = true
	})
	tr.Start()
	defer tr.Stop()

	// Send a text event (non-metadata).
	in <- makeNotif(func(u *acp.SessionUpdate) {
		u.AgentMessageChunk = &acp.SessionUpdateAgentMessageChunk{
			Content: acp.TextBlock("hello"),
		}
	})
	drainShimEvent(t, ch)

	// Give a small window for the hook to fire (it shouldn't).
	time.Sleep(50 * time.Millisecond)
	assert.False(t, hookCalled, "sessionMetadataHook must NOT be called for text events")
}

// TestSessionMetadataHook_AllFourTypes verifies the hook fires for all 4 metadata types.
func TestSessionMetadataHook_AllFourTypes(t *testing.T) {
	in := make(chan acp.SessionNotification, 4)
	tr := NewTranslator("run-1", in, nil)
	ch, _, _ := tr.Subscribe()

	var mu sync.Mutex
	var types []string
	tr.SetSessionMetadataHook(func(ev apishim.Event) {
		mu.Lock()
		defer mu.Unlock()
		types = append(types, apishim.EventTypeOf(ev))
	})
	tr.Start()
	defer tr.Stop()

	// available_commands
	in <- acp.SessionNotification{Update: acp.SessionUpdate{
		AvailableCommandsUpdate: &acp.SessionAvailableCommandsUpdate{
			AvailableCommands: []acp.AvailableCommand{{Name: "test", Description: "test cmd"}},
		},
	}}
	drainShimEvent(t, ch)

	// config_option
	optValue2 := acp.SessionConfigValueId("v1")
	ungrouped2 := acp.SessionConfigSelectOptionsUngrouped{{Name: "V1", Value: optValue2}}
	in <- acp.SessionNotification{Update: acp.SessionUpdate{
		ConfigOptionUpdate: &acp.SessionConfigOptionUpdate{
			ConfigOptions: []acp.SessionConfigOption{
				{Select: &acp.SessionConfigOptionSelect{Id: "x", Name: "X", CurrentValue: optValue2,
					Options: acp.SessionConfigSelectOptions{Ungrouped: &ungrouped2}}},
			},
		},
	}}
	drainShimEvent(t, ch)

	// session_info
	title := "My Session"
	in <- acp.SessionNotification{Update: acp.SessionUpdate{
		SessionInfoUpdate: &acp.SessionSessionInfoUpdate{Title: &title},
	}}
	drainShimEvent(t, ch)

	// current_mode
	modeID := acp.SessionModeId("code")
	in <- acp.SessionNotification{Update: acp.SessionUpdate{
		CurrentModeUpdate: &acp.SessionCurrentModeUpdate{CurrentModeId: modeID},
	}}
	drainShimEvent(t, ch)

	mu.Lock()
	defer mu.Unlock()
	assert.Equal(t, []string{"available_commands", "config_option", "session_info", "current_mode"}, types)
}
