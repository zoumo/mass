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

// ── AvailableCommandInput union marshal/unmarshal ───────────────────────────

func TestAvailableCommandInput_RoundTrip_Unstructured(t *testing.T) {
	input := api.AvailableCommandInput{
		Unstructured: &api.UnstructuredCommandInput{Hint: "run a command"},
	}
	data, err := json.Marshal(input)
	require.NoError(t, err)
	assert.Contains(t, string(data), `"hint"`)

	var decoded api.AvailableCommandInput
	require.NoError(t, json.Unmarshal(data, &decoded))
	require.NotNil(t, decoded.Unstructured)
	assert.Equal(t, "run a command", decoded.Unstructured.Hint)
}

func TestAvailableCommandInput_Marshal_Empty(t *testing.T) {
	input := api.AvailableCommandInput{}
	_, err := json.Marshal(input)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "empty AvailableCommandInput")
}

func TestAvailableCommandInput_Unmarshal_UnknownShape(t *testing.T) {
	var input api.AvailableCommandInput
	err := json.Unmarshal([]byte(`{"foo":"bar"}`), &input)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unknown AvailableCommandInput shape")
}

// ── ConfigOption union marshal/unmarshal ─────────────────────────────────────

func TestConfigOption_RoundTrip_Select(t *testing.T) {
	opt := api.ConfigOption{
		Select: &api.ConfigOptionSelect{
			ID:           "opt-1",
			Name:         "model",
			CurrentValue: "gpt-4",
			Options: api.ConfigSelectOptions{
				Ungrouped: []api.ConfigSelectOption{
					{Name: "GPT-4", Value: "gpt-4"},
					{Name: "Claude", Value: "claude"},
				},
			},
		},
	}
	data, err := json.Marshal(opt)
	require.NoError(t, err)
	assert.Contains(t, string(data), `"type":"select"`)

	var decoded api.ConfigOption
	require.NoError(t, json.Unmarshal(data, &decoded))
	require.NotNil(t, decoded.Select)
	assert.Equal(t, "opt-1", decoded.Select.ID)
	assert.Equal(t, "gpt-4", decoded.Select.CurrentValue)
}

func TestConfigOption_Marshal_Empty(t *testing.T) {
	opt := api.ConfigOption{}
	_, err := json.Marshal(opt)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "empty ConfigOption")
}

func TestConfigOption_Unmarshal_UnknownType(t *testing.T) {
	var opt api.ConfigOption
	err := json.Unmarshal([]byte(`{"type":"unknown"}`), &opt)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unknown ConfigOption type")
}

// ── ConfigSelectOptions union marshal/unmarshal ─────────────────────────────

func TestConfigSelectOptions_RoundTrip_Ungrouped(t *testing.T) {
	opts := api.ConfigSelectOptions{
		Ungrouped: []api.ConfigSelectOption{
			{Name: "A", Value: "a"},
			{Name: "B", Value: "b"},
		},
	}
	data, err := json.Marshal(opts)
	require.NoError(t, err)

	var decoded api.ConfigSelectOptions
	require.NoError(t, json.Unmarshal(data, &decoded))
	require.Len(t, decoded.Ungrouped, 2)
	assert.Equal(t, "a", decoded.Ungrouped[0].Value)
}

func TestConfigSelectOptions_RoundTrip_Grouped(t *testing.T) {
	opts := api.ConfigSelectOptions{
		Grouped: []api.ConfigSelectGroup{
			{
				Group: "models",
				Name:  "Models",
				Options: []api.ConfigSelectOption{
					{Name: "GPT-4", Value: "gpt-4"},
				},
			},
		},
	}
	data, err := json.Marshal(opts)
	require.NoError(t, err)

	var decoded api.ConfigSelectOptions
	require.NoError(t, json.Unmarshal(data, &decoded))
	require.Len(t, decoded.Grouped, 1)
	assert.Equal(t, "models", decoded.Grouped[0].Group)
}

func TestConfigSelectOptions_Marshal_Empty(t *testing.T) {
	opts := api.ConfigSelectOptions{}
	_, err := json.Marshal(opts)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "empty ConfigSelectOptions")
}

func TestConfigSelectOptions_Marshal_MultipleVariants(t *testing.T) {
	opts := api.ConfigSelectOptions{
		Ungrouped: []api.ConfigSelectOption{{Name: "A", Value: "a"}},
		Grouped:   []api.ConfigSelectGroup{{Group: "g", Name: "G"}},
	}
	_, err := json.Marshal(opts)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "multiple variants")
}

// ── EventTypeOf and eventType coverage ──────────────────────────────────────

func TestEventTypeOf(t *testing.T) {
	tests := []struct {
		name     string
		event    api.Event
		expected string
	}{
		{"ToolResult", api.ToolResultEvent{ID: "tr-1"}, api.EventTypeToolResult},
		{"Plan", api.PlanEvent{}, api.EventTypePlan},
		{"TurnStart", api.TurnStartEvent{}, api.EventTypeTurnStart},
		{"TurnEnd", api.TurnEndEvent{StopReason: "done"}, api.EventTypeTurnEnd},
		{"Error", api.ErrorEvent{Msg: "err"}, api.EventTypeError},
		{"RuntimeUpdate", api.RuntimeUpdateEvent{}, api.EventTypeRuntimeUpdate},
		{"ToolCall", api.ToolCallEvent{ID: "tc-1"}, api.EventTypeToolCall},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expected, api.EventTypeOf(tt.event))
		})
	}
}

// ── ToolResultEvent round-trip ──────────────────────────────────────────────

func TestAgentRunEvent_RoundTrip_ToolResult(t *testing.T) {
	now := time.Date(2026, 4, 17, 12, 0, 0, 0, time.UTC)
	ev := api.NewAgentRunEvent("run-1", "sess-1", 4, now,
		api.ToolResultEvent{
			ID:     "tr-1",
			Status: "success",
			Kind:   "bash",
			Title:  "Test result",
		},
		"turn-001",
	)

	data, err := json.Marshal(ev)
	require.NoError(t, err)

	var decoded api.AgentRunEvent
	require.NoError(t, json.Unmarshal(data, &decoded))

	assert.Equal(t, api.EventTypeToolResult, decoded.Type)
	tr, ok := decoded.Payload.(api.ToolResultEvent)
	require.True(t, ok)
	assert.Equal(t, "tr-1", tr.ID)
	assert.Equal(t, "success", tr.Status)
}

// ── PlanEvent round-trip ────────────────────────────────────────────────────

func TestAgentRunEvent_RoundTrip_Plan(t *testing.T) {
	now := time.Date(2026, 4, 17, 12, 0, 0, 0, time.UTC)
	ev := api.NewAgentRunEvent("run-1", "sess-1", 6, now,
		api.PlanEvent{},
		"turn-001",
	)

	data, err := json.Marshal(ev)
	require.NoError(t, err)

	var decoded api.AgentRunEvent
	require.NoError(t, json.Unmarshal(data, &decoded))
	assert.Equal(t, api.EventTypePlan, decoded.Type)
	_, ok := decoded.Payload.(api.PlanEvent)
	require.True(t, ok)
}
