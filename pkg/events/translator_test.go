package events

import (
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"

	acp "github.com/coder/acp-go-sdk"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func makeNotif(fn func(*acp.SessionUpdate)) acp.SessionNotification {
	var n acp.SessionNotification
	fn(&n.Update)
	return n
}

func drainShimEvent(t *testing.T, ch <-chan ShimEvent) ShimEvent {
	t.Helper()
	select {
	case ev := <-ch:
		return ev
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for ShimEvent")
		return ShimEvent{}
	}
}

// sessionContent extracts the typed Event content from a session category ShimEvent.
func sessionContent(t *testing.T, ev ShimEvent) Event {
	t.Helper()
	assert.Equal(t, CategorySession, ev.Category, "expected session category event")
	require.NotNil(t, ev.Content, "expected non-nil content")
	return ev.Content
}

func sendAndDrainShimEvent(t *testing.T, in chan<- acp.SessionNotification, ch <-chan ShimEvent, text string) ShimEvent {
	t.Helper()
	in <- makeNotif(func(u *acp.SessionUpdate) {
		u.AgentMessageChunk = &acp.SessionUpdateAgentMessageChunk{
			Content: acp.ContentBlock{Text: &acp.ContentBlockText{Text: text}},
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
			Content: acp.ContentBlock{Text: &acp.ContentBlockText{Text: "hello"}},
		}
	})

	ev := drainShimEvent(t, ch)
	assert.Equal(t, "run-1", ev.RunID)
	assert.Equal(t, 0, ev.Seq)
	assert.Equal(t, CategorySession, ev.Category)
	assert.Equal(t, "text", ev.Type)
	te, ok := ev.Content.(TextEvent)
	require.True(t, ok)
	assert.Equal(t, "hello", te.Text)
	require.NotNil(t, te.Content)
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
			Content: acp.ContentBlock{Text: &acp.ContentBlockText{Text: "thinking"}},
		}
	})

	ev := drainShimEvent(t, ch)
	te, ok := sessionContent(t, ev).(ThinkingEvent)
	require.True(t, ok)
	assert.Equal(t, "thinking", te.Text)
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
	tc, ok := sessionContent(t, ev).(ToolCallEvent)
	require.True(t, ok)
	assert.Equal(t, ToolCallEvent{ID: "tc-1", Kind: "shell", Title: "run ls"}, tc)
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
	tr_, ok := sessionContent(t, ev).(ToolResultEvent)
	require.True(t, ok)
	assert.Equal(t, ToolResultEvent{ID: "tc-1", Status: "completed"}, tr_)
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
	tr_, ok := sessionContent(t, ev).(ToolResultEvent)
	require.True(t, ok)
	assert.Equal(t, ToolResultEvent{ID: "tc-2", Status: "unknown"}, tr_)
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
	pe, ok := sessionContent(t, ev).(PlanEvent)
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
			Content: acp.ContentBlock{Text: &acp.ContentBlockText{Text: "hello from user"}},
		},
	}}

	ev := drainShimEvent(t, ch)
	ue, ok := sessionContent(t, ev).(UserMessageEvent)
	require.True(t, ok)
	assert.Equal(t, "hello from user", ue.Text)
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
			Content: acp.ContentBlock{Text: &acp.ContentBlockText{Text: "after"}},
		},
	}}

	ev1 := drainShimEvent(t, ch)
	ev2 := drainShimEvent(t, ch)
	ev3 := drainShimEvent(t, ch)
	assert.IsType(t, AvailableCommandsEvent{}, sessionContent(t, ev1))
	assert.IsType(t, CurrentModeEvent{}, sessionContent(t, ev2))
	te, ok := sessionContent(t, ev3).(TextEvent)
	require.True(t, ok)
	assert.Equal(t, "after", te.Text)
}

func TestTranslate_UnknownVariant(t *testing.T) {
	in := make(chan acp.SessionNotification, 1)
	tr := NewTranslator("run-1", in, nil)
	ch, _, _ := tr.Subscribe()
	tr.Start()
	defer tr.Stop()

	in <- acp.SessionNotification{}

	ev := drainShimEvent(t, ch)
	ee, ok := sessionContent(t, ev).(ErrorEvent)
	require.True(t, ok)
	assert.Equal(t, ErrorEvent{Msg: "unknown session update variant"}, ee)
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
			Content: acp.ContentBlock{Text: &acp.ContentBlockText{Text: "broadcast"}},
		}
	})

	for _, ch := range []<-chan ShimEvent{ch1, ch2, ch3} {
		ev := drainShimEvent(t, ch)
		te, ok := ev.Content.(TextEvent)
		require.True(t, ok)
		assert.Equal(t, "broadcast", te.Text)
		require.NotNil(t, te.Content)
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
	assert.Equal(t, CategorySession, first.Category)
	assert.Equal(t, CategorySession, second.Category)
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
	tr.NotifyStateChange("created", "running", 1234, "prompt-started")

	ev := drainShimEvent(t, ch)
	assert.Equal(t, CategoryRuntime, ev.Category)
	assert.Equal(t, "state_change", ev.Type)
	assert.Equal(t, "run-1", ev.RunID)
	assert.Equal(t, 0, ev.Seq)
	assert.Empty(t, ev.TurnID, "state_change must not carry TurnID")
	sc, ok := ev.Content.(StateChangeEvent)
	require.True(t, ok)
	assert.Equal(t, "created", sc.PreviousStatus)
	assert.Equal(t, "running", sc.Status)
	assert.Equal(t, 1234, sc.PID)
	assert.Equal(t, "prompt-started", sc.Reason)
}

func TestShimEventRoundTrip(t *testing.T) {
	ev := ShimEvent{
		RunID:     "run-1",
		SessionID: "acp-xxx",
		Seq:       7,
		Time:      time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		Category:  CategorySession,
		Type:      "tool_call",
		TurnID:    "turn-001",
		StreamSeq: 3,
		Phase:     "tool_call",
		Content:   ToolCallEvent{ID: "1", Kind: "shell", Title: "ls"},
	}
	data, err := ev.MarshalJSON()
	require.NoError(t, err)

	var decoded ShimEvent
	require.NoError(t, decoded.UnmarshalJSON(data))
	assert.Equal(t, ev.RunID, decoded.RunID)
	assert.Equal(t, ev.SessionID, decoded.SessionID)
	assert.Equal(t, ev.Seq, decoded.Seq)
	assert.Equal(t, ev.Category, decoded.Category)
	assert.Equal(t, ev.Type, decoded.Type)
	assert.Equal(t, ev.TurnID, decoded.TurnID)
	assert.Equal(t, ev.StreamSeq, decoded.StreamSeq)
	assert.Equal(t, ev.Phase, decoded.Phase)
	tc, ok := decoded.Content.(ToolCallEvent)
	require.True(t, ok)
	assert.Equal(t, ToolCallEvent{ID: "1", Kind: "shell", Title: "ls"}, tc)
}

func TestShimEventRoundTrip_NoTurnFields(t *testing.T) {
	// omitempty should suppress empty turn fields.
	ev := ShimEvent{
		RunID:    "run-1",
		Seq:      0,
		Time:     time.Now(),
		Category: CategorySession,
		Type:     "text",
		Content:  TextEvent{Text: "no turn"},
	}
	data, err := ev.MarshalJSON()
	require.NoError(t, err)
	assert.NotContains(t, string(data), "turnId", "omitempty should suppress empty turnId")
	assert.NotContains(t, string(data), "phase", "omitempty should suppress empty phase")
}

func TestEventTypes(t *testing.T) {
	cases := []struct {
		ev   Event
		want string
	}{
		{TextEvent{Text: "hi"}, "text"},
		{ThinkingEvent{Text: "hmm"}, "thinking"},
		{UserMessageEvent{Text: "yo"}, "user_message"},
		{ToolCallEvent{ID: "1", Kind: "shell", Title: "ls"}, "tool_call"},
		{ToolResultEvent{ID: "1", Status: "ok"}, "tool_result"},
		{FileWriteEvent{Path: "/a", Allowed: true}, "file_write"},
		{FileReadEvent{Path: "/b", Allowed: false}, "file_read"},
		{CommandEvent{Command: "ls", Allowed: true}, "command"},
		{PlanEvent{Entries: nil}, "plan"},
		{TurnStartEvent{}, "turn_start"},
		{TurnEndEvent{StopReason: "end_turn"}, "turn_end"},
		{ErrorEvent{Msg: "oops"}, "error"},
		{StateChangeEvent{PreviousStatus: "idle", Status: "running"}, "state_change"},
	}
	for _, tc := range cases {
		assert.Equal(t, tc.want, tc.ev.eventType(), "wrong eventType for %T", tc.ev)
	}
}

func TestSubscribeFromSeq_BackfillAndLive(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "events.jsonl")

	at := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	log1, err := OpenEventLog(logPath)
	require.NoError(t, err)
	for i := 0; i < 5; i++ {
		ev := ShimEvent{RunID: "s1", Seq: i, Time: at, Category: CategorySession, Type: "text", Content: TextEvent{Text: fmt.Sprintf("msg-%d", i)}}
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
			Content: acp.ContentBlock{Text: &acp.ContentBlockText{Text: "live"}},
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

func TestSafeBlockText_NilText(t *testing.T) {
	assert.Empty(t, safeBlockText(acp.ContentBlock{}))
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
	teEv := drainShimEvent(t, ch)

	require.NotEmpty(t, tsEv.TurnID)
	assert.Equal(t, tsEv.TurnID, txt1Ev.TurnID, "text event 1 must carry TurnID")
	assert.Equal(t, tsEv.TurnID, txt2Ev.TurnID, "text event 2 must carry TurnID")
	assert.Equal(t, tsEv.TurnID, teEv.TurnID, "turn_end must carry same TurnID")

	// State change after turn_end should not have TurnID.
	tr.NotifyStateChange("running", "created", 0, "done")
	scEv := drainShimEvent(t, ch)
	assert.Equal(t, CategoryRuntime, scEv.Category)
	assert.Empty(t, scEv.TurnID, "runtime state_change must not carry TurnID")
}

// TestTurnAwareShimEvent_StreamSeq verifies StreamSeq increments within a turn.
func TestTurnAwareShimEvent_StreamSeq(t *testing.T) {
	in := make(chan acp.SessionNotification, 4)
	tr := NewTranslator("run-1", in, nil)
	ch, _, _ := tr.Subscribe()
	tr.Start()
	defer tr.Stop()

	tr.NotifyTurnStart()
	tsEv := drainShimEvent(t, ch)
	assert.Equal(t, 0, tsEv.StreamSeq, "turn_start must have StreamSeq=0")

	txt1Ev := sendAndDrainShimEvent(t, in, ch, "a")
	txt2Ev := sendAndDrainShimEvent(t, in, ch, "b")

	assert.Equal(t, 1, txt1Ev.StreamSeq, "first text event StreamSeq")
	assert.Equal(t, 2, txt2Ev.StreamSeq, "second text event StreamSeq")

	tr.NotifyTurnEnd(acp.StopReason("end_turn"))
	teEv := drainShimEvent(t, ch)
	assert.Equal(t, 3, teEv.StreamSeq, "turn_end StreamSeq")
	// turn_end itself DOES carry the TurnID (cleared after the event is built).
	assert.NotEmpty(t, teEv.TurnID, "turn_end event itself must carry TurnID")
}

// TestTurnAwareShimEvent_StreamSeqResetsPerTurn verifies that StreamSeq resets to 0
// at the start of a new turn.
func TestTurnAwareShimEvent_StreamSeqResetsPerTurn(t *testing.T) {
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
	drainShimEvent(t, ch)

	// Turn 2 — StreamSeq must reset to 0.
	tr.NotifyTurnStart()
	ts2 := drainShimEvent(t, ch)
	assert.Equal(t, 0, ts2.StreamSeq, "turn 2 must reset StreamSeq to 0")
	assert.NotEqual(t, ts1.TurnID, ts2.TurnID, "turn 2 must have a different TurnID")
}

// TestTurnAwareShimEvent_PhaseMapping verifies the Phase field is set correctly.
func TestTurnAwareShimEvent_PhaseMapping(t *testing.T) {
	in := make(chan acp.SessionNotification, 4)
	tr := NewTranslator("run-1", in, nil)
	ch, _, _ := tr.Subscribe()
	tr.Start()
	defer tr.Stop()

	tr.NotifyTurnStart()
	tsEv := drainShimEvent(t, ch)
	assert.Equal(t, "acting", tsEv.Phase)

	// Send a thinking event.
	in <- makeNotif(func(u *acp.SessionUpdate) {
		u.AgentThoughtChunk = &acp.SessionUpdateAgentThoughtChunk{
			Content: acp.ContentBlock{Text: &acp.ContentBlockText{Text: "hmm"}},
		}
	})
	thinkEv := drainShimEvent(t, ch)
	assert.Equal(t, "thinking", thinkEv.Phase)

	// Send a tool_call.
	in <- makeNotif(func(u *acp.SessionUpdate) {
		u.ToolCall = &acp.SessionUpdateToolCall{ToolCallId: "tc-1", Kind: "shell", Title: "ls"}
	})
	toolEv := drainShimEvent(t, ch)
	assert.Equal(t, "tool_call", toolEv.Phase)

	// Send a text event.
	in <- makeNotif(func(u *acp.SessionUpdate) {
		u.AgentMessageChunk = &acp.SessionUpdateAgentMessageChunk{
			Content: acp.ContentBlock{Text: &acp.ContentBlockText{Text: "done"}},
		}
	})
	textEv := drainShimEvent(t, ch)
	assert.Equal(t, "acting", textEv.Phase)
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

	tr.NotifyStateChange("created", "running", 0, "")
	scEv := drainShimEvent(t, ch)

	assert.Equal(t, CategoryRuntime, scEv.Category)
	assert.Empty(t, scEv.TurnID, "state_change must not carry TurnID even during active turn")
	assert.Equal(t, 0, scEv.StreamSeq, "state_change must not carry StreamSeq")
	assert.Empty(t, scEv.Phase, "state_change must not carry Phase")

	// Seq must increment correctly.
	assert.Equal(t, tsEv.Seq+1, scEv.Seq, "state_change seq must follow turn_start seq")
}

// TestTurnAwareShimEvent_MetadataEventInTurn verifies that session metadata events
// (session_info, usage, etc.) carry TurnID/StreamSeq/Phase when inside an active turn.
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

	assert.Equal(t, CategorySession, siEv.Category)
	assert.Equal(t, "session_info", siEv.Type)
	assert.Equal(t, tsEv.TurnID, siEv.TurnID, "session_info in active turn must carry TurnID")
	assert.Greater(t, siEv.StreamSeq, 0, "session_info in active turn must have StreamSeq > 0")
	assert.Equal(t, "acting", siEv.Phase)
}

// TestTurnAwareShimEvent_MetadataEventOutsideTurn verifies that session metadata events
// do NOT carry TurnID/StreamSeq/Phase when outside an active turn.
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

	assert.Equal(t, CategorySession, siEv.Category)
	assert.Empty(t, siEv.TurnID, "session_info outside turn must NOT carry TurnID")
	assert.Equal(t, 0, siEv.StreamSeq, "session_info outside turn must have StreamSeq=0")
	assert.Empty(t, siEv.Phase, "session_info outside turn must NOT carry Phase")
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
			Content: acp.ContentBlock{Text: &acp.ContentBlockText{Text: "first"}},
		}
	})
	ev0 := drainShimEvent(t, ch)
	assert.Equal(t, 0, ev0.Seq)

	// Now close the event log to force Append to fail.
	require.NoError(t, evLog.Close())

	// Send a second event — Append will fail (file closed), so it should be dropped.
	in <- makeNotif(func(u *acp.SessionUpdate) {
		u.AgentMessageChunk = &acp.SessionUpdateAgentMessageChunk{
			Content: acp.ContentBlock{Text: &acp.ContentBlockText{Text: "dropped"}},
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
					Content: acp.ContentBlock{Text: &acp.ContentBlockText{Text: "txt"}},
				}
			})
		}
	}()

	// Producer 2: state change notifications.
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < numEvents/2; i++ {
			tr.NotifyStateChange("idle", "running", i, "test")
		}
	}()

	// Drain all events.
	received := make([]ShimEvent, 0, numEvents)
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

	// Turn 1: turn_start + 2 text events + turn_end.
	tr.NotifyTurnStart()
	ts1Ev := drainShimEvent(t, ch)
	t1aEv := sendAndDrainShimEvent(t, in, ch, "t1-a")
	t1bEv := sendAndDrainShimEvent(t, in, ch, "t1-b")
	tr.NotifyTurnEnd(acp.StopReason("end_turn"))
	te1Ev := drainShimEvent(t, ch)
	turn1 := []ShimEvent{ts1Ev, t1aEv, t1bEv, te1Ev}

	// Turn 2: turn_start + 1 text event + turn_end.
	tr.NotifyTurnStart()
	ts2Ev := drainShimEvent(t, ch)
	t2aEv := sendAndDrainShimEvent(t, in, ch, "t2-a")
	tr.NotifyTurnEnd(acp.StopReason("end_turn"))
	te2Ev := drainShimEvent(t, ch)
	turn2 := []ShimEvent{ts2Ev, t2aEv, te2Ev}

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

	// (4) StreamSeq resets in turn 2.
	assert.Equal(t, 0, ts1Ev.StreamSeq, "turn 1 start StreamSeq")
	assert.Equal(t, 0, ts2Ev.StreamSeq, "turn 2 start StreamSeq must reset to 0")
}
