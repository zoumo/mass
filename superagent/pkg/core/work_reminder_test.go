package core

import (
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/zoumo/mass/mass-go/pkg/client"
	"github.com/zoumo/mass/mass-go/pkg/runtime"
)

func TestWorkerSubmissionReceipt(t *testing.T) {
	for _, tc := range []struct {
		name       string
		result     string
		acceptance runtime.Acceptance
		err        error
		attempt    string
		state      string
	}{
		{"written", "applied_best_effort", runtime.Acceptance{Sent: runtime.Yes()}, nil, "delivered", "delivered"},
		{"not applied", "not_applied", runtime.Acceptance{Sent: runtime.Yes()}, nil, "blocked", "unknown"},
		{"native rejection", "applied_best_effort", runtime.Acceptance{Sent: runtime.Yes(), NativeAccepted: runtime.Bool(false)}, nil, "blocked", "unknown"},
		{"not sent", "applied_best_effort", runtime.Acceptance{Sent: runtime.Bool(false)}, nil, "blocked", "unknown"},
		{"no write evidence", "applied_best_effort", runtime.Acceptance{}, nil, "blocked", "unknown"},
		{"lost response", "", runtime.Acceptance{}, client.ErrWorkResponseUnconfirmed, "transport_unknown", "unknown"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			attempt := classifyAttempt(Attempt{}, tc.acceptance, tc.result, true, tc.err)
			require.Equal(t, tc.attempt, attempt.Status)
			watch := workerWatch{evidence: WorkObservation{Provider: "claude", State: "awaiting_receipt"}}
			watch.recordReceipt(attempt, "")
			require.Equal(t, tc.state, watch.evidence.State)
			require.Nil(t, watch.evidence.End)
		})
	}
}

func TestCodexDeliveredWithoutLiveObservation(t *testing.T) {
	for _, tc := range []struct {
		submission string
		gap        string
	}{
		{"", "native_work_identity_unavailable"},
		{"turn-1", "live_subscription_unavailable"},
	} {
		t.Run(tc.gap, func(t *testing.T) {
			watch := workerWatch{evidence: WorkObservation{Provider: "codex", State: "awaiting_receipt"}}
			watch.recordReceipt(Attempt{Status: "delivered"}, tc.submission)
			require.Equal(t, "delivered", watch.evidence.State)
			require.Equal(t, tc.gap, watch.evidence.ObservationGap)
			require.False(t, watch.evidence.Continuous)
			require.Nil(t, watch.evidence.End)
		})
	}
}
