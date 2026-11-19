package agentrelay

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"github.com/zoumo/agent-sdk-go/agent"

	"github.com/zoumo/mass/mass-go/pkg/events"
	"github.com/zoumo/mass/mass-go/pkg/runtime"
)

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
		pending, source := Interactions{}, events.NewSource()
		event := func(payload any, meta map[string]any) events.SessionEvent {
			e, err := events.NewEvent(events.EventBase{
				EventID: uuid.NewString(), Time: time.Now().UTC(), SessionID: "native", Namespace: "one", SessionName: "mcp", Meta: meta,
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
		_, err = pending.Answer(t.Context(), answerer, "mcp-url", map[string]any{"action": "accept"})
		require.NoError(t, err)
		_, err = pending.Answer(t.Context(), answerer, "mcp-url", map[string]any{"action": "cancel"})
		require.Error(t, err)
		require.Equal(t, []agent.Answer{agent.URLAnswer{Outcome: agent.URLAccept}}, answerer.answers)

		ack := event(events.InteractionClosed{Interaction: "mcp-url", Outcome: agent.InteractionAnswered}, nil)
		completion := func(key string) events.SessionEvent {
			return event(events.SessionError{Reason: "native_event_unsupported", Message: "SDK retained raw system observation"}, map[string]any{
				"claude": map[string]any{"type": "system", "subtype": "elicitation_complete", "raw": map[string]any{
					"session_id": "native", "mcp_server_name": "fixture", "elicitation_id": key,
				}},
			})
		}
		got, publish, err := pending.observe(completion("other-flow"))
		require.NoError(t, err)
		require.True(t, publish)
		require.IsType(t, events.SessionError{}, got.Payload())

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
			_, publish, err = pending.observe(ack)
			require.NoError(t, err)
			require.False(t, publish)
		}

		require.NoError(t, pending.Expire(t.Context(), "native", runtime.Address{Namespace: "one", SessionName: "mcp"}, source,
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
