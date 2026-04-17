package api_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	api "github.com/zoumo/mass/pkg/agentrun/api"
)

// ── AgentRunEvent round-trip tests ───────────────────────────────────────────

func TestAgentRunEvent_RoundTrip_ContentEvent(t *testing.T) {
	now := time.Date(2026, 4, 17, 12, 0, 0, 0, time.UTC)
	ev := api.NewAgentRunEvent("run-1", "sess-1", 1, now,
		api.NewContentEvent(api.EventTypeAgentMessage, api.BlockStatusStart, api.TextBlock("hello")),
		"turn-001",
	)

	data, err := json.Marshal(ev)
	require.NoError(t, err)

	var decoded api.AgentRunEvent
	require.NoError(t, json.Unmarshal(data, &decoded))

	assert.Equal(t, "run-1", decoded.RunID)
	assert.Equal(t, "sess-1", decoded.SessionID)
	assert.Equal(t, 1, decoded.Seq)
	assert.Equal(t, api.EventTypeAgentMessage, decoded.Type)
	assert.Equal(t, "turn-001", decoded.TurnID)

	content, ok := decoded.Payload.(api.ContentEvent)
	require.True(t, ok, "payload should be ContentEvent")
	assert.Equal(t, api.BlockStatusStart, content.Status)
}

func TestAgentRunEvent_RoundTrip_TurnEnd(t *testing.T) {
	now := time.Date(2026, 4, 17, 12, 0, 0, 0, time.UTC)
	ev := api.NewAgentRunEvent("run-1", "sess-1", 5, now,
		api.TurnEndEvent{StopReason: "end_turn"},
		"turn-001",
	)

	data, err := json.Marshal(ev)
	require.NoError(t, err)

	var decoded api.AgentRunEvent
	require.NoError(t, json.Unmarshal(data, &decoded))

	assert.Equal(t, api.EventTypeTurnEnd, decoded.Type)
	assert.Equal(t, "turn-001", decoded.TurnID)

	te, ok := decoded.Payload.(api.TurnEndEvent)
	require.True(t, ok, "payload should be TurnEndEvent")
	assert.Equal(t, "end_turn", te.StopReason)
}

func TestAgentRunEvent_RoundTrip_RuntimeUpdate(t *testing.T) {
	now := time.Date(2026, 4, 17, 12, 0, 0, 0, time.UTC)
	ev := api.NewAgentRunEvent("run-1", "", 10, now,
		api.RuntimeUpdateEvent{
			Status: &api.RuntimeStatus{
				PreviousStatus: "creating",
				Status:         "idle",
				PID:            12345,
			},
		},
		"turn-001", // should be dropped for runtime_update
	)

	data, err := json.Marshal(ev)
	require.NoError(t, err)

	var decoded api.AgentRunEvent
	require.NoError(t, json.Unmarshal(data, &decoded))

	assert.Equal(t, api.EventTypeRuntimeUpdate, decoded.Type)
	assert.Empty(t, decoded.TurnID, "runtime_update should not carry turnId")

	ru, ok := decoded.Payload.(api.RuntimeUpdateEvent)
	require.True(t, ok, "payload should be RuntimeUpdateEvent")
	require.NotNil(t, ru.Status)
	assert.Equal(t, "idle", ru.Status.Status)
	assert.Equal(t, 12345, ru.Status.PID)
}

func TestAgentRunEvent_RoundTrip_ToolCall(t *testing.T) {
	now := time.Date(2026, 4, 17, 12, 0, 0, 0, time.UTC)
	ev := api.NewAgentRunEvent("run-1", "sess-1", 3, now,
		api.ToolCallEvent{
			ID:    "tc-1",
			Kind:  "bash",
			Title: "Run tests",
			Content: []api.ToolCallContent{
				{Content: &api.ToolCallContentContent{Content: api.TextBlock("go test ./...")}},
			},
		},
		"turn-001",
	)

	data, err := json.Marshal(ev)
	require.NoError(t, err)

	var decoded api.AgentRunEvent
	require.NoError(t, json.Unmarshal(data, &decoded))

	assert.Equal(t, api.EventTypeToolCall, decoded.Type)
	tc, ok := decoded.Payload.(api.ToolCallEvent)
	require.True(t, ok)
	assert.Equal(t, "tc-1", tc.ID)
	assert.Equal(t, "bash", tc.Kind)
	require.Len(t, tc.Content, 1)
	require.NotNil(t, tc.Content[0].Content)
}

func TestAgentRunEvent_RoundTrip_Error(t *testing.T) {
	now := time.Date(2026, 4, 17, 12, 0, 0, 0, time.UTC)
	ev := api.NewAgentRunEvent("run-1", "sess-1", 7, now,
		api.ErrorEvent{Msg: "something went wrong"},
		"turn-001",
	)

	data, err := json.Marshal(ev)
	require.NoError(t, err)

	var decoded api.AgentRunEvent
	require.NoError(t, json.Unmarshal(data, &decoded))

	assert.Equal(t, api.EventTypeError, decoded.Type)
	ee, ok := decoded.Payload.(api.ErrorEvent)
	require.True(t, ok)
	assert.Equal(t, "something went wrong", ee.Msg)
}

// ── ToolCallContent discriminated union tests ────────────────────────────────

func TestToolCallContent_RoundTrip_ContentVariant(t *testing.T) {
	tc := api.ToolCallContent{
		Content: &api.ToolCallContentContent{Content: api.TextBlock("file data")},
	}
	data, err := json.Marshal(tc)
	require.NoError(t, err)

	var decoded api.ToolCallContent
	require.NoError(t, json.Unmarshal(data, &decoded))
	require.NotNil(t, decoded.Content)
	assert.Nil(t, decoded.Diff)
	assert.Nil(t, decoded.Terminal)
}

func TestToolCallContent_RoundTrip_DiffVariant(t *testing.T) {
	old := "old text"
	tc := api.ToolCallContent{
		Diff: &api.ToolCallContentDiff{Path: "main.go", OldText: &old, NewText: "new text"},
	}
	data, err := json.Marshal(tc)
	require.NoError(t, err)

	var decoded api.ToolCallContent
	require.NoError(t, json.Unmarshal(data, &decoded))
	require.NotNil(t, decoded.Diff)
	assert.Equal(t, "main.go", decoded.Diff.Path)
	assert.Equal(t, "new text", decoded.Diff.NewText)
	require.NotNil(t, decoded.Diff.OldText)
	assert.Equal(t, "old text", *decoded.Diff.OldText)
}

func TestToolCallContent_RoundTrip_TerminalVariant(t *testing.T) {
	tc := api.ToolCallContent{
		Terminal: &api.ToolCallContentTerminal{TerminalID: "t-99"},
	}
	data, err := json.Marshal(tc)
	require.NoError(t, err)

	var decoded api.ToolCallContent
	require.NoError(t, json.Unmarshal(data, &decoded))
	require.NotNil(t, decoded.Terminal)
	assert.Equal(t, "t-99", decoded.Terminal.TerminalID)
}

func TestToolCallContent_Empty_ReturnsError(t *testing.T) {
	tc := api.ToolCallContent{}
	_, err := json.Marshal(tc)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "empty ToolCallContent")
}

// ── WatchID transport-only field test ─────────────────────────────────────────

func TestAgentRunEvent_WatchID_IncludedInJSON(t *testing.T) {
	now := time.Date(2026, 4, 17, 12, 0, 0, 0, time.UTC)
	ev := api.AgentRunEvent{
		WatchID: "watch-42",
		RunID:   "run-1",
		Seq:     1,
		Time:    now,
		Type:    api.EventTypeTurnStart,
		Payload: api.TurnStartEvent{},
	}

	data, err := json.Marshal(ev)
	require.NoError(t, err)
	assert.Contains(t, string(data), `"watchId":"watch-42"`)

	var decoded api.AgentRunEvent
	require.NoError(t, json.Unmarshal(data, &decoded))
	assert.Equal(t, "watch-42", decoded.WatchID)
}

func TestAgentRunEvent_WatchID_OmittedWhenEmpty(t *testing.T) {
	now := time.Date(2026, 4, 17, 12, 0, 0, 0, time.UTC)
	ev := api.AgentRunEvent{
		RunID:   "run-1",
		Seq:     1,
		Time:    now,
		Type:    api.EventTypeTurnStart,
		Payload: api.TurnStartEvent{},
	}

	data, err := json.Marshal(ev)
	require.NoError(t, err)
	assert.NotContains(t, string(data), `"watchId"`)
}
