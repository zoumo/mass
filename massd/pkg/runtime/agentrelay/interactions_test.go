package agentrelay

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"github.com/zoumo/agent-sdk-go/agent"

	"github.com/zoumo/mass/massd/pkg/events"
	"github.com/zoumo/mass/massd/pkg/runtime"
)

func TestUnsupportedNativeEventPreservesJSONOnTheWire(t *testing.T) {
	raw := json.RawMessage(`{"method":"hook/completed","params":{"future":[null,9007199254740993]}}`)
	source := events.NewSource()
	native := agent.Event{
		EventID: uuid.NewString(), Time: time.Now().UTC(), SessionID: "native",
		Payload: agent.UnsupportedNativeEventPayload{Provider: "codex", NativeMethod: "hook/completed"},
		Meta:    map[string]any{"codex": map[string]any{"raw": raw}},
	}
	mapped, err := MapEvent(runtime.Address{Namespace: "one", SessionName: "chat"}, source, native)
	require.NoError(t, err)
	require.False(t, mapped.Persistent())
	require.Empty(t, mapped.Meta)
	require.Equal(t, raw, native.Meta["codex"].(map[string]any)["raw"])
	wire, err := json.Marshal(mapped)
	require.NoError(t, err)
	require.Contains(t, string(wire), `"payload":{"agentType":"codex","event":`+string(raw)+`}`)
	cloned, err := mapped.Clone()
	require.NoError(t, err)
	require.Equal(t, events.UnsupportedNativeEvent{AgentType: "codex", Event: raw}, cloned.Payload())
	require.Equal(t, native.EventID, cloned.EventID)
	require.Equal(t, source, cloned.Source())
}

type interactionAnswerer struct {
	agent.Session
	answers []agent.Answer
}

func (s *interactionAnswerer) Answer(_ context.Context, ref agent.InteractionRef, answer agent.Answer) (agent.InteractionReceipt, error) {
	s.answers = append(s.answers, answer)
	return agent.InteractionReceipt{Interaction: ref, Acceptance: agent.Acceptance{Sent: new(true)}}, nil
}

func TestURLClosesOnCorrelatedCompletionInEitherOrder(t *testing.T) {
	for _, early := range []bool{false, true} {
		address := runtime.Address{Namespace: "one", SessionName: "mcp"}
		pending, source := Interactions{}, events.NewSource()
		event := func(payload any, meta map[string]any) events.SessionEvent {
			e, err := events.NewEvent(events.EventBase{
				EventID: uuid.NewString(), Time: time.Now().UTC(), SessionID: "native", Namespace: address.Namespace, SessionName: address.SessionName, Meta: meta,
			}, payload, source)
			require.NoError(t, err)
			return e
		}
		request := event(events.InteractionRequested{Interaction: "mcp-url", Blocking: true, Request: agent.InteractionRequest{
			Kind: agent.InteractionURL, Message: "Authorize", URL: "https://example.test/authorize",
		}}, map[string]any{"claude": map[string]any{
			"type": "control_request", "subtype": "elicitation", "raw": map[string]any{"request": map[string]any{
				"mcp_server_name": "fixture", "elicitation_id": "flow-id",
			}},
		}})
		_, publish, err := pending.observe(request)
		require.NoError(t, err)
		require.True(t, publish)

		answerer := &interactionAnswerer{}
		_, err = pending.Answer(t.Context(), answerer, "mcp-url", map[string]any{"action": "accept"}, MapError)
		require.NoError(t, err)
		_, err = pending.Answer(t.Context(), answerer, "mcp-url", map[string]any{"action": "cancel"}, MapError)
		require.Error(t, err)
		require.Equal(t, []agent.Answer{agent.URLAnswer{Outcome: agent.URLAccept}}, answerer.answers)

		ack := event(events.InteractionClosed{Interaction: "mcp-url", Outcome: agent.InteractionAnswered}, nil)
		completion := func(key string) events.SessionEvent {
			native := agent.Event{
				EventID: uuid.NewString(), Time: time.Now().UTC(), SessionID: "native",
				Payload: agent.UnsupportedNativeEventPayload{
					Provider: "claude", NativeType: "system", NativeSubtype: "elicitation_complete",
				},
				Meta: map[string]any{
					"claude": map[string]any{"type": "system", "subtype": "elicitation_complete", "raw": map[string]any{
						"session_id": "native", "mcp_server_name": "fixture", "elicitation_id": key,
					}},
				},
			}
			mapped, err := MapEvent(address, source, native)
			require.NoError(t, err)
			require.Equal(t, native.EventID, mapped.EventID)
			require.Equal(t, native.Time, mapped.Time)
			require.Equal(t, native.SessionID, mapped.SessionID)
			require.Equal(t, map[string]any{"claude": map[string]any{"type": "system", "subtype": "elicitation_complete"}}, mapped.Meta)
			require.Equal(t, "claude", mapped.Payload().(events.UnsupportedNativeEvent).AgentType)
			return mapped
		}
		got, publish, err := pending.observe(completion("other-flow"))
		require.NoError(t, err)
		require.True(t, publish)
		require.Equal(t, string(agent.EventSessionUnsupportedNativeEvent), got.Type())

		if !early {
			_, publish, err = pending.observe(ack)
			require.NoError(t, err)
			require.False(t, publish)
		}
		nativeCompletion := completion("flow-id")
		got, publish, err = pending.observe(nativeCompletion)
		require.NoError(t, err)
		require.True(t, publish)
		require.Equal(t, events.InteractionClosed{Interaction: "mcp-url", Outcome: agent.InteractionCompleted}, got.Payload())
		require.NotEqual(t, nativeCompletion.EventID, got.EventID)
		require.Equal(t, nativeCompletion.Time, got.Time)
		require.Nil(t, got.Meta)
		if early {
			_, publish, err = pending.observe(completion("flow-id"))
			require.NoError(t, err)
			require.False(t, publish, "a repeated completion must not settle a second time before acknowledgment")
			_, publish, err = pending.observe(ack)
			require.NoError(t, err)
			require.False(t, publish)
		}
		got, publish, err = pending.observe(completion("flow-id"))
		require.NoError(t, err)
		require.True(t, publish)
		require.Equal(t, string(agent.EventSessionUnsupportedNativeEvent), got.Type())

		require.NoError(t, pending.Expire(t.Context(), "native", address, source,
			func(context.Context, events.SessionEvent) error {
				t.Error("completed flow must not expire")
				return nil
			}))
	}
}

func TestUnconfirmedURLExpiresOnlyOnRelease(t *testing.T) {
	pending := Interactions{pending: map[agent.InteractionRef]*interaction{
		"pending-url": {kind: agent.InteractionURL, acceptURL: true, answering: true, acknowledged: true},
	}}
	var expired []events.SessionEvent
	err := pending.Expire(t.Context(), "native", runtime.Address{Namespace: "one", SessionName: "mcp"}, events.NewSource(),
		func(_ context.Context, event events.SessionEvent) error {
			expired = append(expired, event)
			return nil
		})
	require.NoError(t, err)
	require.Len(t, expired, 1)
	require.Equal(t, events.InteractionClosed{Interaction: "pending-url", Outcome: agent.InteractionExpired, Reason: "session_closed"}, expired[0].Payload())
}
