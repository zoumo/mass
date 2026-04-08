package events

import (
	"encoding/json"
	"fmt"
	"path/filepath"
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

func drainEnvelope(t *testing.T, ch <-chan Envelope) Envelope {
	t.Helper()
	select {
	case env := <-ch:
		return env
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for envelope")
		return Envelope{}
	}
}

func sessionPayload(t *testing.T, env Envelope) Event {
	t.Helper()
	require.Equal(t, MethodSessionUpdate, env.Method)
	params, ok := env.Params.(SessionUpdateParams)
	require.True(t, ok)
	payload, ok := params.Event.Payload.(Event)
	require.True(t, ok, "expected concrete Event payload, got %T", params.Event.Payload)
	return payload
}

func TestTranslate_AgentMessageChunk(t *testing.T) {
	in := make(chan acp.SessionNotification, 1)
	tr := NewTranslator("session-1", in, nil)
	ch, _, _ := tr.Subscribe()
	tr.Start()
	defer tr.Stop()

	in <- makeNotif(func(u *acp.SessionUpdate) {
		u.AgentMessageChunk = &acp.SessionUpdateAgentMessageChunk{
			Content: acp.ContentBlock{Text: &acp.ContentBlockText{Text: "hello"}},
		}
	})

	env := drainEnvelope(t, ch)
	params := env.Params.(SessionUpdateParams)
	assert.Equal(t, "session-1", params.SessionID)
	assert.Equal(t, 0, params.Seq)
	assert.Equal(t, TextEvent{Text: "hello"}, sessionPayload(t, env))
}

func TestTranslate_AgentThoughtChunk(t *testing.T) {
	in := make(chan acp.SessionNotification, 1)
	tr := NewTranslator("session-1", in, nil)
	ch, _, _ := tr.Subscribe()
	tr.Start()
	defer tr.Stop()

	in <- makeNotif(func(u *acp.SessionUpdate) {
		u.AgentThoughtChunk = &acp.SessionUpdateAgentThoughtChunk{
			Content: acp.ContentBlock{Text: &acp.ContentBlockText{Text: "thinking"}},
		}
	})

	assert.Equal(t, ThinkingEvent{Text: "thinking"}, sessionPayload(t, drainEnvelope(t, ch)))
}

func TestTranslate_ToolCall(t *testing.T) {
	in := make(chan acp.SessionNotification, 1)
	tr := NewTranslator("session-1", in, nil)
	ch, _, _ := tr.Subscribe()
	tr.Start()
	defer tr.Stop()

	in <- makeNotif(func(u *acp.SessionUpdate) {
		u.ToolCall = &acp.SessionUpdateToolCall{ToolCallId: "tc-1", Kind: "shell", Title: "run ls"}
	})

	assert.Equal(t, ToolCallEvent{ID: "tc-1", Kind: "shell", Title: "run ls"}, sessionPayload(t, drainEnvelope(t, ch)))
}

func TestTranslate_ToolCallUpdate(t *testing.T) {
	in := make(chan acp.SessionNotification, 1)
	tr := NewTranslator("session-1", in, nil)
	ch, _, _ := tr.Subscribe()
	tr.Start()
	defer tr.Stop()

	status := acp.ToolCallStatus("completed")
	in <- makeNotif(func(u *acp.SessionUpdate) {
		u.ToolCallUpdate = &acp.SessionToolCallUpdate{ToolCallId: "tc-1", Status: &status}
	})

	assert.Equal(t, ToolResultEvent{ID: "tc-1", Status: "completed"}, sessionPayload(t, drainEnvelope(t, ch)))
}

func TestTranslate_ToolCallUpdate_NilStatus(t *testing.T) {
	in := make(chan acp.SessionNotification, 1)
	tr := NewTranslator("session-1", in, nil)
	ch, _, _ := tr.Subscribe()
	tr.Start()
	defer tr.Stop()

	in <- makeNotif(func(u *acp.SessionUpdate) {
		u.ToolCallUpdate = &acp.SessionToolCallUpdate{ToolCallId: "tc-2"}
	})

	assert.Equal(t, ToolResultEvent{ID: "tc-2", Status: "unknown"}, sessionPayload(t, drainEnvelope(t, ch)))
}

func TestTranslate_Plan(t *testing.T) {
	in := make(chan acp.SessionNotification, 1)
	tr := NewTranslator("session-1", in, nil)
	ch, _, _ := tr.Subscribe()
	tr.Start()
	defer tr.Stop()

	entries := []acp.PlanEntry{{Content: "step 1", Status: acp.PlanEntryStatusPending}, {Content: "step 2", Status: acp.PlanEntryStatusInProgress}}
	in <- makeNotif(func(u *acp.SessionUpdate) {
		u.Plan = &acp.SessionUpdatePlan{Entries: entries}
	})

	pe, ok := sessionPayload(t, drainEnvelope(t, ch)).(PlanEvent)
	require.True(t, ok)
	assert.Len(t, pe.Entries, 2)
	assert.Equal(t, "step 1", pe.Entries[0].Content)
}

func TestTranslate_UserMessageChunk(t *testing.T) {
	in := make(chan acp.SessionNotification, 1)
	tr := NewTranslator("session-1", in, nil)
	ch, _, _ := tr.Subscribe()
	tr.Start()
	defer tr.Stop()

	in <- acp.SessionNotification{Update: acp.SessionUpdate{
		UserMessageChunk: &acp.SessionUpdateUserMessageChunk{
			Content: acp.ContentBlock{Text: &acp.ContentBlockText{Text: "hello from user"}},
		},
	}}

	assert.Equal(t, UserMessageEvent{Text: "hello from user"}, sessionPayload(t, drainEnvelope(t, ch)))
}

func TestTranslate_IgnoredVariants(t *testing.T) {
	in := make(chan acp.SessionNotification, 3)
	tr := NewTranslator("session-1", in, nil)
	ch, _, _ := tr.Subscribe()
	tr.Start()
	defer tr.Stop()

	in <- acp.SessionNotification{Update: acp.SessionUpdate{AvailableCommandsUpdate: &acp.SessionAvailableCommandsUpdate{}}}
	in <- acp.SessionNotification{Update: acp.SessionUpdate{CurrentModeUpdate: &acp.SessionCurrentModeUpdate{}}}
	in <- acp.SessionNotification{Update: acp.SessionUpdate{
		AgentMessageChunk: &acp.SessionUpdateAgentMessageChunk{
			Content: acp.ContentBlock{Text: &acp.ContentBlockText{Text: "after ignored"}},
		},
	}}

	assert.Equal(t, TextEvent{Text: "after ignored"}, sessionPayload(t, drainEnvelope(t, ch)))
}

func TestTranslate_UnknownVariant(t *testing.T) {
	in := make(chan acp.SessionNotification, 1)
	tr := NewTranslator("session-1", in, nil)
	ch, _, _ := tr.Subscribe()
	tr.Start()
	defer tr.Stop()

	in <- acp.SessionNotification{}

	assert.Equal(t, ErrorEvent{Msg: "unknown session update variant"}, sessionPayload(t, drainEnvelope(t, ch)))
}

func TestFanOut_ThreeSubscribers(t *testing.T) {
	in := make(chan acp.SessionNotification, 1)
	tr := NewTranslator("session-1", in, nil)
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

	want := TextEvent{Text: "broadcast"}
	assert.Equal(t, want, sessionPayload(t, drainEnvelope(t, ch1)))
	assert.Equal(t, want, sessionPayload(t, drainEnvelope(t, ch2)))
	assert.Equal(t, want, sessionPayload(t, drainEnvelope(t, ch3)))
}

func TestNotifyTurnStartAndEnd(t *testing.T) {
	in := make(chan acp.SessionNotification)
	tr := NewTranslator("session-1", in, nil)
	ch, _, _ := tr.Subscribe()
	tr.Start()
	defer tr.Stop()

	tr.NotifyTurnStart()
	tr.NotifyTurnEnd(acp.StopReason("end_turn"))

	first := drainEnvelope(t, ch).Params.(SessionUpdateParams)
	second := drainEnvelope(t, ch).Params.(SessionUpdateParams)
	assert.Equal(t, 0, first.Seq)
	assert.Equal(t, 1, second.Seq)
	assert.Equal(t, TurnStartEvent{}, first.Event.Payload)
	assert.Equal(t, TurnEndEvent{StopReason: "end_turn"}, second.Event.Payload)

	// Turn field assertions.
	assert.NotEmpty(t, first.TurnId, "turn_start must carry a non-empty TurnId")
	assert.Equal(t, first.TurnId, second.TurnId, "turn_end must carry the same TurnId as turn_start")
	require.NotNil(t, first.StreamSeq, "turn_start must carry StreamSeq")
	assert.Equal(t, 0, *first.StreamSeq, "turn_start StreamSeq must be 0")
	require.NotNil(t, second.StreamSeq, "turn_end must carry StreamSeq")
	assert.Equal(t, 1, *second.StreamSeq, "turn_end StreamSeq must be 1")
}

func TestNotifyStateChange(t *testing.T) {
	in := make(chan acp.SessionNotification)
	tr := NewTranslator("session-1", in, nil)
	ch, _, nextSeq := tr.Subscribe()
	tr.Start()
	defer tr.Stop()

	assert.Equal(t, 0, nextSeq)
	tr.NotifyStateChange("created", "running", 1234, "prompt-started")

	env := drainEnvelope(t, ch)
	assert.Equal(t, MethodRuntimeStateChange, env.Method)
	params, ok := env.Params.(RuntimeStateChangeParams)
	require.True(t, ok)
	assert.Equal(t, "session-1", params.SessionID)
	assert.Equal(t, 0, params.Seq)
	assert.Equal(t, "created", params.PreviousStatus)
	assert.Equal(t, "running", params.Status)
	assert.Equal(t, 1234, params.PID)
	assert.Equal(t, "prompt-started", params.Reason)
}

func TestEnvelopeRoundTripPreservesTypedPayload(t *testing.T) {
	env := NewSessionUpdateEnvelope("session-1", 7, time.Now().UTC(), ToolCallEvent{ID: "1", Kind: "shell", Title: "ls"})
	data, err := env.MarshalJSON()
	require.NoError(t, err)

	var decoded Envelope
	require.NoError(t, decoded.UnmarshalJSON(data))
	params, ok := decoded.Params.(SessionUpdateParams)
	require.True(t, ok)
	assert.Equal(t, ToolCallEvent{ID: "1", Kind: "shell", Title: "ls"}, params.Event.Payload)
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
	}
	for _, tc := range cases {
		assert.Equal(t, tc.want, tc.ev.eventType(), "wrong eventType for %T", tc.ev)
	}
}

func TestSubscribeFromSeq_BackfillAndLive(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "events.jsonl")

	// Write 5 entries (seq 0-4) to the log file.
	log1, err := OpenEventLog(logPath)
	require.NoError(t, err)
	for i := 0; i < 5; i++ {
		require.NoError(t, log1.Append(NewSessionUpdateEnvelope("s1", i, time.Now().UTC(), TextEvent{Text: fmt.Sprintf("msg-%d", i)})))
	}
	require.NoError(t, log1.Close())

	// Create a fresh Translator with an EventLog opened on the same path.
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
	for i, env := range entries {
		seq, serr := env.Seq()
		require.NoError(t, serr)
		assert.Equal(t, i+2, seq, "backfill entry %d has wrong seq", i)
	}

	// nextSeq should be 5 (next after the 5 already written).
	assert.Equal(t, 5, nextSeq)

	// Broadcast a new event via the Translator — it will get seq 5.
	in <- makeNotif(func(u *acp.SessionUpdate) {
		u.AgentMessageChunk = &acp.SessionUpdateAgentMessageChunk{
			Content: acp.ContentBlock{Text: &acp.ContentBlockText{Text: "live"}},
		}
	})

	// The subscription channel should receive the new event (seq 5).
	liveEnv := drainEnvelope(t, ch)
	liveSeq, err := liveEnv.Seq()
	require.NoError(t, err)
	assert.Equal(t, 5, liveSeq, "live event should have seq 5, no gap after backfill end at seq 4")
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
	assert.Empty(t, entries, "backfill should be empty for non-existent log")
	assert.Equal(t, 0, nextSeq)

	// Verify subscription works — broadcast an event.
	tr.NotifyTurnStart()
	liveEnv := drainEnvelope(t, ch)
	liveSeq, err := liveEnv.Seq()
	require.NoError(t, err)
	assert.Equal(t, 0, liveSeq)
}

func TestSafeBlockText_NilText(t *testing.T) {
	assert.Equal(t, "", safeBlockText(acp.ContentBlock{}))
}

// sendTextNotif sends a single text ACP notification into the in channel and
// returns the envelope drained from ch.
func sendAndDrain(t *testing.T, in chan<- acp.SessionNotification, ch <-chan Envelope, text string) Envelope {
	t.Helper()
	in <- makeNotif(func(u *acp.SessionUpdate) {
		u.AgentMessageChunk = &acp.SessionUpdateAgentMessageChunk{
			Content: acp.ContentBlock{Text: &acp.ContentBlockText{Text: text}},
		}
	})
	return drainEnvelope(t, ch)
}

// ptrInt returns a pointer to an int literal — useful in table-driven assertions.
func ptrInt(v int) *int { return &v }

// TestTurnAwareEnvelope_TurnIdAssigned verifies that all session/update events
// emitted between NotifyTurnStart and NotifyTurnEnd carry the same non-empty
// TurnId, and that an event emitted after NotifyTurnEnd carries no TurnId.
func TestTurnAwareEnvelope_TurnIdAssigned(t *testing.T) {
	in := make(chan acp.SessionNotification, 4)
	tr := NewTranslator("session-1", in, nil)
	ch, _, _ := tr.Subscribe()
	tr.Start()
	defer tr.Stop()

	tr.NotifyTurnStart()
	tsEnv := drainEnvelope(t, ch)

	txt1Env := sendAndDrain(t, in, ch, "hello")
	txt2Env := sendAndDrain(t, in, ch, "world")

	tr.NotifyTurnEnd(acp.StopReason("end_turn"))
	teEnv := drainEnvelope(t, ch)

	tsParams := tsEnv.Params.(SessionUpdateParams)
	txt1Params := txt1Env.Params.(SessionUpdateParams)
	txt2Params := txt2Env.Params.(SessionUpdateParams)
	teParams := teEnv.Params.(SessionUpdateParams)

	require.NotEmpty(t, tsParams.TurnId)
	assert.Equal(t, tsParams.TurnId, txt1Params.TurnId, "text event 1 must carry TurnId")
	assert.Equal(t, tsParams.TurnId, txt2Params.TurnId, "text event 2 must carry TurnId")
	assert.Equal(t, tsParams.TurnId, teParams.TurnId, "turn_end must carry same TurnId")

	// Event after turn_end should have no TurnId.
	tr.NotifyStateChange("running", "created", 0, "done")
	scEnv := drainEnvelope(t, ch)
	assert.Equal(t, MethodRuntimeStateChange, scEnv.Method)
	// RuntimeStateChangeParams has no TurnId — correct type is sufficient proof.
	_, ok := scEnv.Params.(RuntimeStateChangeParams)
	assert.True(t, ok, "post-turn stateChange should be RuntimeStateChangeParams, not SessionUpdateParams")
}

// TestTurnAwareEnvelope_StreamSeqMonotonic verifies that streamSeq increments
// 0→1→2→3 across turn_start, text, text, turn_end within a single turn.
func TestTurnAwareEnvelope_StreamSeqMonotonic(t *testing.T) {
	in := make(chan acp.SessionNotification, 4)
	tr := NewTranslator("session-1", in, nil)
	ch, _, _ := tr.Subscribe()
	tr.Start()
	defer tr.Stop()

	tr.NotifyTurnStart()
	tsParams := drainEnvelope(t, ch).Params.(SessionUpdateParams)

	txt1Params := sendAndDrain(t, in, ch, "a").Params.(SessionUpdateParams)
	txt2Params := sendAndDrain(t, in, ch, "b").Params.(SessionUpdateParams)

	tr.NotifyTurnEnd(acp.StopReason("end_turn"))
	teParams := drainEnvelope(t, ch).Params.(SessionUpdateParams)

	require.NotNil(t, tsParams.StreamSeq)
	require.NotNil(t, txt1Params.StreamSeq)
	require.NotNil(t, txt2Params.StreamSeq)
	require.NotNil(t, teParams.StreamSeq)

	assert.Equal(t, 0, *tsParams.StreamSeq, "turn_start streamSeq must be 0")
	assert.Equal(t, 1, *txt1Params.StreamSeq, "first text streamSeq must be 1")
	assert.Equal(t, 2, *txt2Params.StreamSeq, "second text streamSeq must be 2")
	assert.Equal(t, 3, *teParams.StreamSeq, "turn_end streamSeq must be 3")
}

// TestTurnAwareEnvelope_MultipleTurns verifies that a second turn gets a fresh
// TurnId and that streamSeq resets to 0 at the second turn_start.
func TestTurnAwareEnvelope_MultipleTurns(t *testing.T) {
	in := make(chan acp.SessionNotification, 4)
	tr := NewTranslator("session-1", in, nil)
	ch, _, _ := tr.Subscribe()
	tr.Start()
	defer tr.Stop()

	// Turn 1.
	tr.NotifyTurnStart()
	ts1 := drainEnvelope(t, ch).Params.(SessionUpdateParams)
	sendAndDrain(t, in, ch, "turn1-event") // consume
	tr.NotifyTurnEnd(acp.StopReason("end_turn"))
	drainEnvelope(t, ch) // consume turn_end

	// Turn 2.
	tr.NotifyTurnStart()
	ts2 := drainEnvelope(t, ch).Params.(SessionUpdateParams)
	sendAndDrain(t, in, ch, "turn2-event")
	tr.NotifyTurnEnd(acp.StopReason("end_turn"))
	te2 := drainEnvelope(t, ch).Params.(SessionUpdateParams)

	require.NotEmpty(t, ts1.TurnId)
	require.NotEmpty(t, ts2.TurnId)
	assert.NotEqual(t, ts1.TurnId, ts2.TurnId, "second turn must have a different TurnId")

	require.NotNil(t, ts2.StreamSeq)
	assert.Equal(t, 0, *ts2.StreamSeq, "second turn_start streamSeq must reset to 0")

	require.NotNil(t, te2.StreamSeq)
	assert.Equal(t, 2, *te2.StreamSeq, "second turn_end streamSeq must be 2 (start=0, text=1, end=2)")
}

// TestTurnAwareEnvelope_StateChangeExcludesTurnFields verifies that a
// runtime/stateChange envelope emitted during a turn is not a SessionUpdateParams
// (and therefore has no TurnId/StreamSeq), while its global seq still increments.
func TestTurnAwareEnvelope_StateChangeExcludesTurnFields(t *testing.T) {
	in := make(chan acp.SessionNotification)
	tr := NewTranslator("session-1", in, nil)
	ch, _, _ := tr.Subscribe()
	tr.Start()
	defer tr.Stop()

	tr.NotifyTurnStart()
	tsEnv := drainEnvelope(t, ch)

	tr.NotifyStateChange("created", "running", 0, "")
	scEnv := drainEnvelope(t, ch)

	assert.Equal(t, MethodRuntimeStateChange, scEnv.Method)
	_, ok := scEnv.Params.(RuntimeStateChangeParams)
	assert.True(t, ok, "stateChange params must be RuntimeStateChangeParams")

	// Seq must have incremented correctly.
	tsSeq, _ := tsEnv.Seq()
	scSeq, _ := scEnv.Seq()
	assert.Equal(t, tsSeq+1, scSeq, "stateChange seq must follow turn_start seq")
}

// TestTurnAwareEnvelope_RoundTrip verifies that TurnId, StreamSeq, and Phase
// survive a JSON marshal/unmarshal round-trip on SessionUpdateParams.
func TestTurnAwareEnvelope_RoundTrip(t *testing.T) {
	ss := 2
	original := SessionUpdateParams{
		SequenceMeta: SequenceMeta{SessionID: "s1", Seq: 5, Timestamp: "2024-01-01T00:00:00Z"},
		TurnId:       "test-turn",
		StreamSeq:    &ss,
		Phase:        "thinking",
		Event:        newTypedEvent(TextEvent{Text: "hello"}),
	}
	env := Envelope{Method: MethodSessionUpdate, Params: original}

	data, err := env.MarshalJSON()
	require.NoError(t, err)

	var decoded Envelope
	require.NoError(t, decoded.UnmarshalJSON(data))

	params, ok := decoded.Params.(SessionUpdateParams)
	require.True(t, ok)

	assert.Equal(t, "test-turn", params.TurnId)
	require.NotNil(t, params.StreamSeq)
	assert.Equal(t, 2, *params.StreamSeq)
	assert.Equal(t, "thinking", params.Phase)
	assert.Equal(t, TextEvent{Text: "hello"}, params.Event.Payload)

	// Also verify omitempty works: a params with no turn fields must not have those keys.
	bare := SessionUpdateParams{
		SequenceMeta: SequenceMeta{SessionID: "s2", Seq: 0, Timestamp: "2024-01-01T00:00:00Z"},
		Event:        newTypedEvent(TextEvent{Text: "no turn"}),
	}
	bareData, err := json.Marshal(bare)
	require.NoError(t, err)
	assert.NotContains(t, string(bareData), "turnId", "omitempty should suppress empty turnId")
	assert.NotContains(t, string(bareData), "streamSeq", "omitempty should suppress nil streamSeq")
	assert.NotContains(t, string(bareData), "phase", "omitempty should suppress empty phase")
}

// TestTurnAwareEnvelope_ReplayOrdering verifies replay ordering invariants:
//  1. All events in turn 1 share a common turnId.
//  2. All events in turn 2 share a different common turnId.
//  3. streamSeq is 0,1,2,... within each turn.
//  4. Global seq is strictly monotonic across both turns.
func TestTurnAwareEnvelope_ReplayOrdering(t *testing.T) {
	in := make(chan acp.SessionNotification, 8)
	tr := NewTranslator("session-1", in, nil)
	ch, _, _ := tr.Subscribe()
	tr.Start()
	defer tr.Stop()

	// Turn 1: turn_start + 2 text events + turn_end = 4 events.
	// Each event is drained immediately after being sent so the translator
	// goroutine processes it while the turn is still active (before NotifyTurnEnd
	// clears currentTurnId).
	tr.NotifyTurnStart()
	ts1Env := drainEnvelope(t, ch)
	in <- makeNotif(func(u *acp.SessionUpdate) {
		u.AgentMessageChunk = &acp.SessionUpdateAgentMessageChunk{
			Content: acp.ContentBlock{Text: &acp.ContentBlockText{Text: "t1-a"}},
		}
	})
	t1aEnv := drainEnvelope(t, ch)
	in <- makeNotif(func(u *acp.SessionUpdate) {
		u.AgentMessageChunk = &acp.SessionUpdateAgentMessageChunk{
			Content: acp.ContentBlock{Text: &acp.ContentBlockText{Text: "t1-b"}},
		}
	})
	t1bEnv := drainEnvelope(t, ch)
	tr.NotifyTurnEnd(acp.StopReason("end_turn"))
	te1Env := drainEnvelope(t, ch)
	turn1 := []Envelope{ts1Env, t1aEnv, t1bEnv, te1Env}

	// Turn 2: turn_start + 1 text event + turn_end = 3 events.
	tr.NotifyTurnStart()
	ts2Env := drainEnvelope(t, ch)
	in <- makeNotif(func(u *acp.SessionUpdate) {
		u.AgentMessageChunk = &acp.SessionUpdateAgentMessageChunk{
			Content: acp.ContentBlock{Text: &acp.ContentBlockText{Text: "t2-a"}},
		}
	})
	t2aEnv := drainEnvelope(t, ch)
	tr.NotifyTurnEnd(acp.StopReason("end_turn"))
	te2Env := drainEnvelope(t, ch)
	turn2 := []Envelope{ts2Env, t2aEnv, te2Env}

	// (1) All turn 1 events share a common TurnId.
	tid1 := turn1[0].Params.(SessionUpdateParams).TurnId
	require.NotEmpty(t, tid1)
	for i, env := range turn1 {
		p := env.Params.(SessionUpdateParams)
		assert.Equal(t, tid1, p.TurnId, "turn1[%d] TurnId mismatch", i)
	}

	// (2) All turn 2 events share a different common TurnId.
	tid2 := turn2[0].Params.(SessionUpdateParams).TurnId
	require.NotEmpty(t, tid2)
	assert.NotEqual(t, tid1, tid2, "turn 2 must have a different TurnId than turn 1")
	for i, env := range turn2 {
		p := env.Params.(SessionUpdateParams)
		assert.Equal(t, tid2, p.TurnId, "turn2[%d] TurnId mismatch", i)
	}

	// (3) streamSeq is 0,1,2,3 in turn 1 and 0,1,2 in turn 2.
	for i, env := range turn1 {
		p := env.Params.(SessionUpdateParams)
		require.NotNil(t, p.StreamSeq, "turn1[%d] StreamSeq must not be nil", i)
		assert.Equal(t, i, *p.StreamSeq, "turn1[%d] streamSeq mismatch", i)
	}
	for i, env := range turn2 {
		p := env.Params.(SessionUpdateParams)
		require.NotNil(t, p.StreamSeq, "turn2[%d] StreamSeq must not be nil", i)
		assert.Equal(t, i, *p.StreamSeq, "turn2[%d] streamSeq mismatch", i)
	}

	// (4) Global seq is strictly monotonic across both turns.
	all := append(turn1, turn2...)
	for i := 1; i < len(all); i++ {
		prevSeq, _ := all[i-1].Seq()
		curSeq, _ := all[i].Seq()
		assert.Equal(t, prevSeq+1, curSeq, "global seq must be monotonic at position %d", i)
	}
}
