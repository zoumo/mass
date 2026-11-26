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
	reply   func() (agent.Acceptance, error)
}

func (s *interactionAnswerer) Answer(_ context.Context, ref agent.InteractionRef, answer agent.Answer) (agent.InteractionReceipt, error) {
	s.answers = append(s.answers, answer)
	acceptance, err := s.reply()
	return agent.InteractionReceipt{Interaction: ref, Acceptance: acceptance}, err
}

func observeInteraction(t *testing.T, pending *Interactions, source events.Source, payload agent.Payload) {
	t.Helper()
	event, err := events.NewEvent(events.EventBase{
		EventID: uuid.NewString(), Time: time.Now().UTC(), SessionID: "native",
		Namespace: "one", SessionName: "mcp",
	}, payload, source)
	require.NoError(t, err)
	require.NoError(t, pending.observe(event))
}

func urlRequest() events.InteractionRequested {
	return events.InteractionRequested{
		Interaction: "mcp-url", Blocking: true, Request: agent.InteractionRequest{
			Kind: agent.InteractionURL, Message: "Authorize", URL: "https://example.test/authorize",
		},
	}
}

func TestURLAnswerAuthorityFollowsSDKClosure(t *testing.T) {
	pending := Interactions{}
	source := events.NewSource()
	observeInteraction(t, &pending, source, urlRequest())
	answerer := &interactionAnswerer{reply: func() (agent.Acceptance, error) {
		return agent.Acceptance{Sent: new(true)}, nil
	}}
	accept := map[string]any{"action": "accept"}
	_, err := pending.Answer(t.Context(), answerer, "mcp-url", accept, MapError)
	require.NoError(t, err)
	_, err = pending.Answer(t.Context(), answerer, "mcp-url", accept, MapError)
	require.Error(t, err)
	observeInteraction(t, &pending, source, events.InteractionClosed{
		Interaction: "mcp-url", Outcome: agent.InteractionCompleted,
	})
	_, err = pending.Answer(t.Context(), answerer, "mcp-url", accept, MapError)
	require.Error(t, err)
	require.Equal(t, []agent.Answer{agent.URLAnswer{Outcome: agent.URLAccept}}, answerer.answers)
	require.Empty(t, pending.pending)
}

func TestFailedAnswerRetriesOnlyWhileStillPending(t *testing.T) {
	for _, tc := range []struct {
		name               string
		code               agent.ErrorCode
		sent               *bool
		closedDuringAnswer bool
		wantRetry          bool
	}{
		{name: "unsent", code: agent.ErrorUnavailable, sent: new(false), wantRetry: true},
		{name: "closed before unsent reply", code: agent.ErrorUnavailable, sent: new(false), closedDuringAnswer: true},
		{name: "unknown dispatch outcome", code: agent.ErrorOutcomeUnknown},
		{name: "sent and rejected", code: agent.ErrorNativeRejected, sent: new(true)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var pending Interactions
			source := events.NewSource()
			request := urlRequest()
			observeInteraction(t, &pending, source, request)
			session := &interactionAnswerer{reply: func() (agent.Acceptance, error) {
				if tc.closedDuringAnswer {
					observeInteraction(t, &pending, source, events.InteractionClosed{
						Interaction: request.Interaction, Outcome: agent.InteractionCancelled,
					})
				}
				return agent.Acceptance{Sent: tc.sent}, &agent.Error{Code: tc.code}
			}}
			accept := map[string]any{"action": "accept"}
			_, err := pending.Answer(t.Context(), session, request.Interaction, accept, MapError)
			require.Error(t, err)
			session.reply = func() (agent.Acceptance, error) { return agent.Acceptance{Sent: new(true)}, nil }
			_, err = pending.Answer(t.Context(), session, request.Interaction, accept, MapError)
			if tc.wantRetry {
				require.NoError(t, err)
				require.Len(t, session.answers, 2)
			} else {
				require.ErrorIs(t, err, runtime.ErrFailedPrecondition)
				require.Len(t, session.answers, 1)
			}
		})
	}
}
