package events

import (
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

func TestSafeBlockText_NilText(t *testing.T) {
	assert.Equal(t, "", safeBlockText(acp.ContentBlock{}))
}
