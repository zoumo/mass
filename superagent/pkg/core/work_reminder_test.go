package core

import (
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/zoumo/agent-sdk-go/agent"

	"github.com/zoumo/mass/massd/pkg/runtime"
)

func TestIssueSubmissionReceipt(t *testing.T) {
	for _, tc := range []struct {
		name       string
		result     string
		acceptance runtime.Acceptance
		err        error
		attempt    string
		state      string
	}{
		{"written", "applied_best_effort", runtime.Acceptance{Sent: runtime.Bool(true)}, nil, "delivered", "delivered"},
		{"not applied", "not_applied", runtime.Acceptance{Sent: runtime.Bool(true)}, nil, "blocked", "unknown"},
		{"native rejection", "applied_best_effort", runtime.Acceptance{Sent: runtime.Bool(true), NativeAccepted: runtime.Bool(false)}, nil, "blocked", "unknown"},
		{"not sent", "applied_best_effort", runtime.Acceptance{Sent: runtime.Bool(false)}, nil, "blocked", "unknown"},
		{"no write evidence", "applied_best_effort", runtime.Acceptance{}, nil, "blocked", "unknown"},
		{"lost response", "", runtime.Acceptance{}, &runtime.OperationError{Code: "outcome_unknown", Reason: "operation_unconfirmed"}, "transport_unknown", "unknown"},
		{"native outcome unknown", "", runtime.Acceptance{}, &runtime.OperationError{Code: "outcome_unknown", Reason: "input_outcome_unknown"}, "blocked_unknown", "unknown"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			attempt := classifyAttempt(Attempt{}, tc.acceptance, tc.result, true, tc.err)
			require.Equal(t, tc.attempt, attempt.Status)
			watch := issueWatch{evidence: WorkObservation{Provider: "claude", State: "awaiting_receipt"}}
			watch.recordReceipt(attempt, agent.InputResult(tc.result), "")
			require.Equal(t, tc.state, watch.evidence.State)
			require.Nil(t, watch.evidence.End)
		})
	}
}

func TestIssueDeliveredWithoutLiveObservation(t *testing.T) {
	for _, provider := range []string{"codex", "claude"} {
		t.Run(provider, func(t *testing.T) {
			watch := issueWatch{evidence: WorkObservation{Provider: provider, State: "awaiting_receipt"}}
			watch.recordReceipt(Attempt{Status: "delivered"}, agent.InputAppliedNewWork, "")
			require.Equal(t, "delivered", watch.evidence.State)
			require.Equal(t, "live_subscription_unavailable", watch.evidence.ObservationGap)
			require.False(t, watch.evidence.Continuous)
			require.Nil(t, watch.evidence.End)
		})
	}
}
