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
		Payload: agent.UnsupportedNativeEventPayload{AgentType: "codex", Raw: raw},
	}
	mapped, err := MapEvent(runtime.Address{Namespace: "one", SessionName: "chat"}, source, native)
	require.NoError(t, err)
	require.False(t, mapped.Persistent())
	require.Empty(t, mapped.Meta)
	wire, err := json.Marshal(mapped)
	require.NoError(t, err)
	require.Contains(t, string(wire), `"payload":{"agentType":"codex","raw":`+string(raw)+`}`)
	cloned, err := mapped.Clone()
	require.NoError(t, err)
	require.Equal(t, native.Payload, cloned.Payload())
	require.Equal(t, native.EventID, cloned.EventID)
	require.Equal(t, source, cloned.Source())
	cloned.Replay = &events.ReplaySource{LoadID: "replay"}
	require.Error(t, cloned.Validate())
}

type interactionAnswerer struct {
	agent.Session
	answers []agent.Answer
}

func (s *interactionAnswerer) Answer(_ context.Context, ref agent.InteractionRef, answer agent.Answer) (agent.InteractionReceipt, error) {
	s.answers = append(s.answers, answer)
	return agent.InteractionReceipt{Interaction: ref, Acceptance: agent.Acceptance{Sent: new(true)}}, nil
}

func TestURLAnswerAuthorityFollowsSDKClosure(t *testing.T) {
	pending := Interactions{}
	source := events.NewSource()
	makeEvent := func(payload agent.Payload) events.SessionEvent {
		event, err := events.NewEvent(events.EventBase{
			EventID: uuid.NewString(), Time: time.Now().UTC(), SessionID: "native",
			Namespace: "one", SessionName: "mcp",
		}, payload, source)
		require.NoError(t, err)
		return event
	}
	require.NoError(t, pending.observe(makeEvent(events.InteractionRequested{
		Interaction: "mcp-url", Blocking: true, Request: agent.InteractionRequest{
			Kind: agent.InteractionURL, Message: "Authorize", URL: "https://example.test/authorize",
		},
	})))
	answerer := &interactionAnswerer{}
	accept := map[string]any{"action": "accept"}
	_, err := pending.Answer(t.Context(), answerer, "mcp-url", accept, MapError)
	require.NoError(t, err)
	_, err = pending.Answer(t.Context(), answerer, "mcp-url", accept, MapError)
	require.Error(t, err)
	require.NoError(t, pending.observe(makeEvent(events.InteractionClosed{
		Interaction: "mcp-url", Outcome: agent.InteractionCompleted,
	})))
	_, err = pending.Answer(t.Context(), answerer, "mcp-url", accept, MapError)
	require.Error(t, err)
	require.Equal(t, []agent.Answer{agent.URLAnswer{Outcome: agent.URLAccept}}, answerer.answers)
	require.Empty(t, pending.pending)
}
