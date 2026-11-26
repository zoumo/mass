package integration_test

import (
	"bytes"
	"encoding/json"
	"os"
	"slices"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/zoumo/mass/massd/pkg/events"
	"github.com/zoumo/mass/massd/pkg/resource"
	"github.com/zoumo/mass/massd/pkg/runtime"
	"github.com/zoumo/mass/massd/pkg/service"
)

func TestClaudeMCPFormURLAndPermissionSettlement(t *testing.T) {
	const sessionID = "claude-session"
	request := func(id string, fields map[string]any) map[string]any {
		return map[string]any{"type": "control_request", "request_id": id, "request": fields}
	}
	response := func(id string, result map[string]any) map[string]any {
		return map[string]any{"type": "control_response", "response": map[string]any{
			"subtype": "success", "request_id": id, "response": result,
		}}
	}
	completion := func(id string) map[string]any {
		return map[string]any{
			"type": "system", "subtype": "elicitation_complete", "session_id": sessionID,
			"mcp_server_name": "fixture", "elicitation_id": id,
		}
	}
	running := map[string]any{"type": "system", "subtype": "session_state_changed", "session_id": sessionID, "state": "running"}
	frames := capacityFrames(sessionID)
	frames = appendServerFrame(frames, request("form", map[string]any{
		"subtype": "elicitation", "mode": "form", "mcp_server_name": "fixture", "message": "Select a tenant",
		"requested_schema": map[string]any{"type": "object", "properties": map[string]any{"tenant": map[string]any{"type": "string"}}},
	}))
	formAnswer := map[string]any{"action": "accept", "content": map[string]any{"tenant": "demo"}}
	frames = append(frames, scenarioFrame(len(frames)+1, "client", response("form", formAnswer)))
	frames = appendServerFrame(frames, running)
	frames = appendServerFrame(frames, request("permission", map[string]any{
		"subtype": "can_use_tool", "tool_name": "Bash", "tool_use_id": "tool-1", "input": map[string]any{"command": "printf ignored"},
	}))
	frames = append(frames, scenarioFrame(len(frames)+1, "client", response("permission", map[string]any{
		"behavior": "deny", "message": "User canceled the request",
	})))
	frames = appendServerFrame(frames, running)
	frames = appendServerFrame(frames, request("url", map[string]any{
		"subtype": "elicitation", "mode": "url", "mcp_server_name": "fixture", "message": "Authorize",
		"elicitation_id": "actual-flow", "url": "https://example.test/authorize",
	}))
	urlAnswer := map[string]any{"action": "accept"}
	frames = append(frames, scenarioFrame(len(frames)+1, "client", response("url", urlAnswer)))
	frames = appendServerFrame(frames, running)
	frames = appendServerFrame(frames, completion("different-flow"))
	frames = appendServerFrame(frames, completion("actual-flow"))
	frames = appendServerFrame(frames, map[string]any{
		"type": "result", "subtype": "success", "session_id": sessionID, "is_error": false, "result": "complete",
	})
	frames = appendServerFrame(frames, map[string]any{"type": "system", "subtype": "session_state_changed", "session_id": sessionID, "state": "idle"})
	scenario := map[string]any{"version": 1, "provenance": "synthetic", "frames": frames}

	svc, ref, sub := openMCPScenario(t, "claude", claudeMock, scenario)
	expected := []struct {
		kind    events.InteractionKind
		answer  map[string]any
		outcome events.InteractionOutcome
	}{
		{events.InteractionForm, formAnswer, events.InteractionAnswered},
		{events.InteractionPermission, map[string]any{"outcome": "selected", "optionId": "cancel"}, events.InteractionCancelled},
		{events.InteractionURL, urlAnswer, events.InteractionCompleted},
	}
	pending := make(map[events.InteractionRef]events.InteractionOutcome)
	asked, settled := 0, 0
	for settled < len(expected) {
		event, err := nextSubscription(sub, ctx(t))
		require.NoError(t, err)
		switch payload := event.Payload().(type) {
		case events.InteractionRequested:
			require.Less(t, asked, len(expected))
			want := expected[asked]
			require.Equal(t, want.kind, payload.Request.Kind)
			if want.kind == events.InteractionPermission {
				require.Contains(t, payload.Request.Options, events.PermissionOption{OptionID: "cancel", Kind: events.PermissionCancel, Name: "Cancel"})
			}
			receipt, err := svc.AnswerInteraction(ctx(t), ref, string(payload.Interaction), want.answer)
			require.NoError(t, err)
			require.Equal(t, runtime.Yes(), receipt.Evidence.Sent)
			pending[payload.Interaction] = want.outcome
			asked++
		case events.InteractionClosed:
			require.Equal(t, pending[payload.Interaction], payload.Outcome)
			delete(pending, payload.Interaction)
			settled++
		}
	}
	_, err := svc.CloseSession(ctx(t), ref)
	require.NoError(t, err)
}

func TestCodexMCPURLClosesWhenNativeRequestResolves(t *testing.T) {
	var scenario map[string]any
	raw, err := os.ReadFile("testdata/codex-question.json")
	require.NoError(t, err)
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	require.NoError(t, decoder.Decode(&scenario))
	frames := scenario["frames"].([]any)
	frames[8].(map[string]any)["wire"] = map[string]any{
		"id": "question-1", "method": "mcpServer/elicitation/request", "params": map[string]any{
			"threadId": "codex-session", "turnId": "turn-1", "serverName": "fixture", "message": "Authorize",
			"mode": "url", "elicitationId": "flow", "url": "https://example.test/authorize",
		},
	}
	frames[9].(map[string]any)["wire"] = map[string]any{
		"id": "question-1", "result": map[string]any{"action": "accept", "content": nil, "_meta": nil},
	}
	frames = slices.Insert(frames, 10, any(scenarioFrame(11, "server", map[string]any{
		"method": "serverRequest/resolved", "params": map[string]any{"threadId": "codex-session", "requestId": "question-1"},
	})))
	for i, frame := range frames {
		frame.(map[string]any)["ordinal"] = i + 1
	}
	scenario["frames"] = frames
	svc, ref, sub := openMCPScenario(t, "codex", mock, scenario)
	var urlRef events.InteractionRef
	for {
		event, err := nextSubscription(sub, ctx(t))
		require.NoError(t, err)
		switch payload := event.Payload().(type) {
		case events.InteractionRequested:
			require.Equal(t, events.InteractionURL, payload.Request.Kind)
			receipt, err := svc.AnswerInteraction(ctx(t), ref, string(payload.Interaction), map[string]any{"action": "accept"})
			require.NoError(t, err)
			require.Equal(t, runtime.Yes(), receipt.Evidence.Sent)
			urlRef = payload.Interaction
		case events.InteractionClosed:
			require.Equal(t, urlRef, payload.Interaction)
			require.Equal(t, events.InteractionAnswered, payload.Outcome)
			require.Equal(t, "native_resolved", payload.Reason)
			_, err := svc.CloseSession(ctx(t), ref)
			require.NoError(t, err)
			return
		}
	}
}

func openMCPScenario(t *testing.T, kind, executable string, scenario map[string]any) (*service.Service, resource.SessionRef, *service.Subscription) {
	t.Helper()
	svc, _ := setup(t, nil)
	ensureNativeReadyObservation(scenario, kind)
	path, work := writeNativeScenario(t, scenario)
	const name = "mcp"
	_, err := svc.CreateRuntime(resource.Runtime{Metadata: resource.Metadata{Name: name}, Spec: resource.RuntimeSpec{
		Type: kind, Config: runtime.Config{"executable": executable, "scenario": path},
	}})
	require.NoError(t, err)
	_, err = svc.StartRuntime(ctx(t), name)
	require.NoError(t, err)
	opened, err := svc.CreateSession(ctx(t), resource.Session{
		Metadata: resource.Metadata{Namespace: "default", Name: name},
		Spec:     resource.SessionSpec{RuntimeRef: name, SessionSpec: runtime.SessionSpec{Cwd: work}},
	})
	require.NoError(t, err)
	ref := resource.SessionRef{SessionID: opened.Status.SessionID}
	sub, err := svc.SubscribeEvents(ref)
	require.NoError(t, err)
	t.Cleanup(func() { _ = sub.Close() })
	_, err = svc.SendPrompt(ctx(t), ref, runtime.Input{Content: []events.ContentBlock{events.Text("hello")}})
	require.NoError(t, err)
	return svc, ref, sub
}
