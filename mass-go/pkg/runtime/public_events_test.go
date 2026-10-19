package runtime_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	rt "github.com/zoumo/mass/mass-go/pkg/runtime"
)

func observation() rt.Event {
	return rt.Event{RuntimeName: "codex", SessionID: "native-session", ConnectionEpoch: "private-epoch", WorkRef: "private-turn", SourcePosition: 99, ObservedAt: time.Date(2026, 10, 19, 0, 0, 0, 0, time.UTC), Origin: rt.EventOrigin{Kind: "native", Attribution: "root", Completeness: "complete"}}
}

func TestPublicMessagePreviewAndComplete(t *testing.T) {
	p := rt.NewEventProjector("demo", "/work")
	e := observation()
	e.Message = &rt.MessagePayload{MessageRef: "native-message", Role: "assistant", Stage: "delta", Content: rt.TextContent("preview", "delta", "partial", nil)}
	preview, err := p.Project(e)
	require.NoError(t, err)
	require.Equal(t, "agent.message_chunk", preview.Type)
	require.Zero(t, preview.Seq)
	e.Message.Stage, e.Message.Content = "completed", rt.TextContent("authoritative", "completed", "complete", nil)
	e.Message.Phase = "final_answer"
	full, err := p.Project(e)
	require.NoError(t, err)
	require.Equal(t, "agent.message", full.Type)
	require.Equal(t, preview.ItemID, full.ItemID)
	require.NotEqual(t, "native-message", full.ItemID)
	require.Equal(t, uint64(1), full.Seq)
	data, err := json.Marshal(full)
	require.NoError(t, err)
	var wire map[string]any
	require.NoError(t, json.Unmarshal(data, &wire))
	require.Len(t, wire, 8)
	require.Equal(t, float64(2), wire["schemaVersion"])
	require.Equal(t, "demo", wire["sessionName"])
	require.Equal(t, "native-session", wire["sessionId"])
	require.Equal(t, map[string]any{"messageId": full.ItemID, "phase": "final_answer", "content": []any{map[string]any{"type": "text", "text": "authoritative"}}}, wire["payload"])
	for _, private := range []string{"native-message", "private-turn", "private-epoch", "sourcePosition", "origin", "observedAt", "preview"} {
		require.NotContains(t, string(data), private)
	}
	e.ConnectionEpoch = "next-attachment"
	resumed, err := rt.NewEventProjector("demo", "/work").Project(e)
	require.NoError(t, err)
	require.Equal(t, full.ItemID, resumed.ItemID)
	user := observation()
	user.Message = &rt.MessagePayload{MessageRef: "u", Role: "user", Stage: "completed", Content: rt.TextContent("", "completed", "complete", nil)}
	result, err := p.Project(user)
	require.NoError(t, err)
	require.Equal(t, "user.message", result.Type)
	require.Equal(t, uint64(2), result.Seq)
}

func TestPublicReasoningChannelsAndBoundaries(t *testing.T) {
	p := rt.NewEventProjector("demo", "/work")
	e := observation()
	index := 2
	e.Reasoning = &rt.ReasoningPayload{MessageRef: "r", Stage: "delta", Channel: "summary", Content: rt.TextContent("thought", "delta", "partial", &index)}
	e.Reasoning.Content.Blocks[0].Namespace = "summary"
	chunk, err := p.Project(e)
	require.NoError(t, err)
	require.Equal(t, "agent.thinking_chunk", chunk.Type)
	e.Reasoning.Stage = "completed"
	e.Reasoning.Content.Completeness = "complete"
	e.Reasoning.Content.Blocks[0].Completeness = "complete"
	full, err := p.Project(e)
	require.NoError(t, err)
	require.Equal(t, "agent.thinking", full.Type)
	require.Equal(t, chunk.ItemID, full.ItemID)
	message := full.Payload.(rt.PublicMessage)
	require.Equal(t, "summary", message.Content[0].Channel)
	require.Equal(t, 2, *message.Content[0].Index)
	e.Reasoning.Content = rt.ContentSet{Availability: "missing", Completeness: "unknown"}
	boundary, err := p.Project(e)
	require.NoError(t, err)
	require.Nil(t, boundary, "message-stop cannot manufacture a full snapshot")
	e.Reasoning.Content = rt.ContentSet{Availability: "redacted", Completeness: "partial"}
	redacted, err := p.Project(e)
	require.NoError(t, err)
	require.Nil(t, redacted)
}

func TestPublicToolCallResultAndOutputPresence(t *testing.T) {
	for _, tc := range []struct {
		name     string
		output   rt.ContentSet
		provided bool
	}{
		{"missing", rt.ContentSet{Availability: "missing", Completeness: "unknown"}, false},
		{"null", rt.ContentSet{Availability: "null", Completeness: "unknown"}, false},
		{"empty", rt.TextContent("", "completed", "complete", nil), true},
		{"text", rt.TextContent("result", "completed", "complete", nil), true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := rt.NewEventProjector("demo", "/work")
			e := observation()
			e.Kind = rt.ToolAnnounced
			e.Tool = &rt.ToolPayload{CallRef: "native-call", Name: "commandExecution", Category: "execute", State: "announced", Input: rt.JSONValueContent(json.RawMessage(`{"command":"pwd","cwd":"/work","actions":[{"native":"private"}]}`), "completed", "complete")}
			call, err := p.Project(e)
			require.NoError(t, err)
			require.Equal(t, "agent.tool_call", call.Type)
			require.Equal(t, uint64(1), call.Seq)
			e.Kind = rt.ToolUpdated
			e.Tool.Input = rt.ContentSet{Availability: "missing", Completeness: "unknown"}
			e.Tool.Output = rt.TextContent("preview", "delta", "partial", nil)
			chunk, err := p.Project(e)
			require.NoError(t, err)
			require.Zero(t, chunk.Seq)
			require.Equal(t, "agent.tool_call_chunk", chunk.Type)
			e.Kind = rt.ToolCompleted
			e.Tool.State = "completed"
			e.Tool.Output = tc.output
			result, err := p.Project(e)
			require.NoError(t, err)
			require.Equal(t, "agent.tool_result", result.Type)
			require.Equal(t, call.ItemID, result.ItemID)
			require.Equal(t, uint64(2), result.Seq)
			tool := result.Payload.(rt.PublicTool)
			require.Equal(t, call.Payload, tool.CallContext)
			require.Equal(t, "shell", tool.Name)
			require.Equal(t, "execution", tool.InputRepresentation)
			require.Equal(t, &rt.ExecutionInput{Command: "pwd", Cwd: "/work"}, tool.RawInput)
			if tc.provided {
				require.Equal(t, "provided", tool.Output.Availability)
				require.Equal(t, "retained", tool.Output.Completeness)
				require.NotNil(t, tool.RawOutput)
			} else {
				require.Equal(t, &rt.PublicOutput{Availability: "unavailable", Completeness: "unknown"}, tool.Output)
				require.Nil(t, tool.RawOutput)
				require.Empty(t, tool.Content)
			}
			data, err := json.Marshal(result)
			require.NoError(t, err)
			for _, private := range []string{"native-call", "private", "actions", "native_status", "preview"} {
				require.NotContains(t, string(data), private)
			}
		})
	}
}

func TestPublicProjectionRejectsUnsupportedFactsAndDoesNotInventCompletion(t *testing.T) {
	p := rt.NewEventProjector("demo", "/work")
	e := observation()
	e.Kind = rt.ToolCompleted
	e.Tool = &rt.ToolPayload{CallRef: "call", Name: "Read"}
	_, err := p.Project(e)
	require.Error(t, err)
	e.Tool = nil
	e.Kind = rt.TurnCompleted
	e.Work = &rt.WorkPayload{Status: "unknown"}
	state, err := p.Project(e)
	require.NoError(t, err)
	require.Nil(t, state.Payload.(map[string]any)["state"])
	require.Zero(t, state.Seq)
	e.Origin.Attribution = "child"
	state, err = p.Project(e)
	require.NoError(t, err)
	require.Nil(t, state)
}
