package events

import (
	"testing"
	"time"

	acp "github.com/coder/acp-go-sdk"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// makeNotif constructs a SessionNotification with the update configured by fn.
func makeNotif(fn func(*acp.SessionUpdate)) acp.SessionNotification {
	var n acp.SessionNotification
	fn(&n.Update)
	return n
}

// drain waits for one Event on ch with a 1-second timeout.
func drain(t *testing.T, ch <-chan Event) Event {
	t.Helper()
	select {
	case ev := <-ch:
		return ev
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for event")
		return nil
	}
}

func TestTranslate_AgentMessageChunk(t *testing.T) {
	in := make(chan acp.SessionNotification, 1)
	tr := NewTranslator(in, nil)
	ch, _ := tr.Subscribe()
	tr.Start()
	defer tr.Stop()

	in <- makeNotif(func(u *acp.SessionUpdate) {
		u.AgentMessageChunk = &acp.SessionUpdateAgentMessageChunk{
			Content: acp.ContentBlock{Text: &acp.ContentBlockText{Text: "hello"}},
		}
	})

	ev := drain(t, ch)
	require.IsType(t, TextEvent{}, ev)
	assert.Equal(t, TextEvent{Text: "hello"}, ev)
}

func TestTranslate_AgentThoughtChunk(t *testing.T) {
	in := make(chan acp.SessionNotification, 1)
	tr := NewTranslator(in, nil)
	ch, _ := tr.Subscribe()
	tr.Start()
	defer tr.Stop()

	in <- makeNotif(func(u *acp.SessionUpdate) {
		u.AgentThoughtChunk = &acp.SessionUpdateAgentThoughtChunk{
			Content: acp.ContentBlock{Text: &acp.ContentBlockText{Text: "thinking"}},
		}
	})

	ev := drain(t, ch)
	require.IsType(t, ThinkingEvent{}, ev)
	assert.Equal(t, ThinkingEvent{Text: "thinking"}, ev)
}

func TestTranslate_ToolCall(t *testing.T) {
	in := make(chan acp.SessionNotification, 1)
	tr := NewTranslator(in, nil)
	ch, _ := tr.Subscribe()
	tr.Start()
	defer tr.Stop()

	in <- makeNotif(func(u *acp.SessionUpdate) {
		u.ToolCall = &acp.SessionUpdateToolCall{
			ToolCallId: "tc-1",
			Kind:       "shell",
			Title:      "run ls",
		}
	})

	ev := drain(t, ch)
	require.IsType(t, ToolCallEvent{}, ev)
	assert.Equal(t, ToolCallEvent{ID: "tc-1", Kind: "shell", Title: "run ls"}, ev)
}

func TestTranslate_ToolCallUpdate(t *testing.T) {
	in := make(chan acp.SessionNotification, 1)
	tr := NewTranslator(in, nil)
	ch, _ := tr.Subscribe()
	tr.Start()
	defer tr.Stop()

	status := acp.ToolCallStatus("completed")
	in <- makeNotif(func(u *acp.SessionUpdate) {
		u.ToolCallUpdate = &acp.SessionToolCallUpdate{
			ToolCallId: "tc-1",
			Status:     &status,
		}
	})

	ev := drain(t, ch)
	require.IsType(t, ToolResultEvent{}, ev)
	assert.Equal(t, ToolResultEvent{ID: "tc-1", Status: "completed"}, ev)
}

func TestTranslate_ToolCallUpdate_NilStatus(t *testing.T) {
	in := make(chan acp.SessionNotification, 1)
	tr := NewTranslator(in, nil)
	ch, _ := tr.Subscribe()
	tr.Start()
	defer tr.Stop()

	in <- makeNotif(func(u *acp.SessionUpdate) {
		u.ToolCallUpdate = &acp.SessionToolCallUpdate{
			ToolCallId: "tc-2",
			Status:     nil,
		}
	})

	ev := drain(t, ch)
	require.IsType(t, ToolResultEvent{}, ev)
	assert.Equal(t, ToolResultEvent{ID: "tc-2", Status: "unknown"}, ev)
}

func TestTranslate_Plan(t *testing.T) {
	in := make(chan acp.SessionNotification, 1)
	tr := NewTranslator(in, nil)
	ch, _ := tr.Subscribe()
	tr.Start()
	defer tr.Stop()

	entries := []acp.PlanEntry{
		{Content: "step 1", Status: acp.PlanEntryStatusPending},
		{Content: "step 2", Status: acp.PlanEntryStatusInProgress},
	}
	in <- makeNotif(func(u *acp.SessionUpdate) {
		u.Plan = &acp.SessionUpdatePlan{Entries: entries}
	})

	ev := drain(t, ch)
	require.IsType(t, PlanEvent{}, ev)
	pe := ev.(PlanEvent)
	assert.Len(t, pe.Entries, 2)
	assert.Equal(t, "step 1", pe.Entries[0].Content)
}

func TestTranslate_UserMessageChunk(t *testing.T) {
	in := make(chan acp.SessionNotification, 1)
	tr := NewTranslator(in, nil)
	ch, _ := tr.Subscribe()
	tr.Start()
	defer tr.Stop()

	in <- acp.SessionNotification{Update: acp.SessionUpdate{
		UserMessageChunk: &acp.SessionUpdateUserMessageChunk{
			Content: acp.ContentBlock{Text: &acp.ContentBlockText{Text: "hello from user"}},
		},
	}}

	ev := drain(t, ch)
	require.IsType(t, UserMessageEvent{}, ev)
	assert.Equal(t, "hello from user", ev.(UserMessageEvent).Text)
}

func TestTranslate_AvailableCommandsUpdate_Ignored(t *testing.T) {
	in := make(chan acp.SessionNotification, 2)
	tr := NewTranslator(in, nil)
	ch, _ := tr.Subscribe()
	tr.Start()
	defer tr.Stop()

	// Send an ignored variant followed by a real event so we can confirm
	// the ignored one was dropped and didn't block the stream.
	in <- acp.SessionNotification{Update: acp.SessionUpdate{
		AvailableCommandsUpdate: &acp.SessionAvailableCommandsUpdate{},
	}}
	in <- acp.SessionNotification{Update: acp.SessionUpdate{
		AgentMessageChunk: &acp.SessionUpdateAgentMessageChunk{
			Content: acp.ContentBlock{Text: &acp.ContentBlockText{Text: "after ignored"}},
		},
	}}

	ev := drain(t, ch)
	require.IsType(t, TextEvent{}, ev, "AvailableCommandsUpdate should be silently dropped")
	assert.Equal(t, "after ignored", ev.(TextEvent).Text)
}

func TestTranslate_CurrentModeUpdate_Ignored(t *testing.T) {
	in := make(chan acp.SessionNotification, 2)
	tr := NewTranslator(in, nil)
	ch, _ := tr.Subscribe()
	tr.Start()
	defer tr.Stop()

	in <- acp.SessionNotification{Update: acp.SessionUpdate{
		CurrentModeUpdate: &acp.SessionCurrentModeUpdate{},
	}}
	in <- acp.SessionNotification{Update: acp.SessionUpdate{
		AgentMessageChunk: &acp.SessionUpdateAgentMessageChunk{
			Content: acp.ContentBlock{Text: &acp.ContentBlockText{Text: "after ignored"}},
		},
	}}

	ev := drain(t, ch)
	require.IsType(t, TextEvent{}, ev, "CurrentModeUpdate should be silently dropped")
	assert.Equal(t, "after ignored", ev.(TextEvent).Text)
}

func TestTranslate_UnknownVariant(t *testing.T) {
	in := make(chan acp.SessionNotification, 1)
	tr := NewTranslator(in, nil)
	ch, _ := tr.Subscribe()
	tr.Start()
	defer tr.Stop()

	in <- acp.SessionNotification{} // empty update — no variant set

	ev := drain(t, ch)
	require.IsType(t, ErrorEvent{}, ev)
	assert.Equal(t, ErrorEvent{Msg: "unknown session update variant"}, ev)
}

func TestFanOut_ThreeSubscribers(t *testing.T) {
	in := make(chan acp.SessionNotification, 1)
	tr := NewTranslator(in, nil)
	ch1, _ := tr.Subscribe()
	ch2, _ := tr.Subscribe()
	ch3, _ := tr.Subscribe()
	tr.Start()
	defer tr.Stop()

	in <- makeNotif(func(u *acp.SessionUpdate) {
		u.AgentMessageChunk = &acp.SessionUpdateAgentMessageChunk{
			Content: acp.ContentBlock{Text: &acp.ContentBlockText{Text: "broadcast"}},
		}
	})

	want := TextEvent{Text: "broadcast"}
	assert.Equal(t, want, drain(t, ch1))
	assert.Equal(t, want, drain(t, ch2))
	assert.Equal(t, want, drain(t, ch3))
}

func TestNotifyTurnStart(t *testing.T) {
	in := make(chan acp.SessionNotification)
	tr := NewTranslator(in, nil)
	ch, _ := tr.Subscribe()
	tr.Start()
	defer tr.Stop()

	tr.NotifyTurnStart()

	ev := drain(t, ch)
	assert.Equal(t, TurnStartEvent{}, ev)
}

func TestNotifyTurnEnd(t *testing.T) {
	in := make(chan acp.SessionNotification)
	tr := NewTranslator(in, nil)
	ch, _ := tr.Subscribe()
	tr.Start()
	defer tr.Stop()

	tr.NotifyTurnEnd(acp.StopReason("end_turn"))

	ev := drain(t, ch)
	assert.Equal(t, TurnEndEvent{StopReason: "end_turn"}, ev)
}

// TestEventTypes exercises the unexported eventType() discriminator on every
// concrete Event implementation so the coverage tool counts them as covered.
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

// TestSafeBlockText_NilText covers the branch where cb.Text == nil.
func TestSafeBlockText_NilText(t *testing.T) {
	result := safeBlockText(acp.ContentBlock{}) // Text field is nil
	assert.Equal(t, "", result)
}
