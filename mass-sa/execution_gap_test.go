package sa

import (
	"context"
	"encoding/json"
	"io"
	"testing"
)

type scriptedSessionEvents struct {
	events []SessionEvent
	index  int
}

func (s *scriptedSessionEvents) Next(context.Context) (SessionEvent, error) {
	if s.index >= len(s.events) {
		return SessionEvent{}, io.EOF
	}
	event := s.events[s.index]
	s.index++
	return event, nil
}

func (s *scriptedSessionEvents) Close() error { return nil }

func TestCollectSessionFinalContinuesAcrossRecoverableObservationGaps(t *testing.T) {
	for _, reason := range []string{
		"native_event_unsupported",
		"native_relationship_gap",
		"native_observation_missing",
	} {
		t.Run(reason, func(t *testing.T) {
			final := sessionEventForTest(
				"final",
				"agent.message",
				map[string]any{
					"messageId": "message-final",
					"content":   []any{map[string]any{"type": "text", "text": "complete"}},
				},
			)
			subscription := &scriptedSessionEvents{events: []SessionEvent{
				sessionEventForTest("gap", "session.error", map[string]any{
					"message": "native observation could not be represented",
					"reason":  reason,
				}),
				final,
				sessionEventForTest("idle", "session.state_update", map[string]any{"state": "idle"}),
			}}

			got, recovered, err := collectSessionFinal(
				context.Background(),
				nil,
				subscription,
				SessionRef{SessionID: "session"},
				"prompt",
				nil,
				"router",
			)
			if err != nil {
				t.Fatalf("collectSessionFinal returned %v", err)
			}
			if recovered {
				t.Fatal("collectSessionFinal unexpectedly used history recovery")
			}
			if got.EventID != "final" {
				t.Fatalf("final event = %q, want final", got.EventID)
			}
		})
	}
}

func TestCollectSessionFinalStopsOnTerminalSessionErrors(t *testing.T) {
	for _, reason := range []string{"native_error", "native_result_error", "assistant_aborted"} {
		t.Run(reason, func(t *testing.T) {
			subscription := &scriptedSessionEvents{events: []SessionEvent{
				sessionEventForTest("error", "session.error", map[string]any{
					"message": "native failure",
					"reason":  reason,
				}),
			}}

			_, _, err := collectSessionFinal(
				context.Background(),
				nil,
				subscription,
				SessionRef{SessionID: "session"},
				"prompt",
				nil,
				"router",
			)
			if err == nil {
				t.Fatal("collectSessionFinal accepted a terminal session error")
			}
			if got, ok := errorReason(err); !ok || got != reason {
				t.Fatalf("error reason = %q, want %q", got, reason)
			}
		})
	}
}

func sessionEventForTest(id, eventType string, payload any) SessionEvent {
	raw, err := json.Marshal(payload)
	if err != nil {
		panic(err)
	}
	return SessionEvent{
		EventID:   id,
		SessionID: "session",
		Type:      eventType,
		Payload:   raw,
	}
}
