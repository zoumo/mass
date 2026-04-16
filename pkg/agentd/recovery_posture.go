// Package agentd implements the agent daemon that manages agent runtime sessions.
// This file defines recovery posture types and phase tracking for the daemon's
// fail-closed recovery behavior. During session recovery, operational actions
// (prompt, cancel) are blocked while read-only inspection methods continue to
// work.
package agentd

import "time"

// ────────────────────────────────────────────────────────────────────────────
// RecoveryPhase — daemon-level recovery lifecycle
// ────────────────────────────────────────────────────────────────────────────

// RecoveryPhase represents the daemon's current position in the recovery
// lifecycle. The phase is stored as an atomic int32 on ProcessManager so it
// can be read without acquiring the process map lock.
type RecoveryPhase int32

const (
	// RecoveryPhaseIdle means no recovery is in progress. This is the
	// default phase for a freshly-created ProcessManager and the steady
	// state after startup completes without needing recovery.
	RecoveryPhaseIdle RecoveryPhase = 0

	// RecoveryPhaseRecovering means the daemon is actively reconnecting to
	// surviving agent-run processes. Operational actions (prompt, cancel) MUST be
	// refused while this phase is active (fail-closed posture).
	RecoveryPhaseRecovering RecoveryPhase = 1

	// RecoveryPhaseComplete means the recovery pass finished. The daemon
	// transitions here after all candidate sessions have been processed
	// (whether they recovered successfully or were marked stopped).
	RecoveryPhaseComplete RecoveryPhase = 2
)

// String returns a human-readable label for the recovery phase.
func (p RecoveryPhase) String() string {
	switch p {
	case RecoveryPhaseIdle:
		return "idle"
	case RecoveryPhaseRecovering:
		return "recovering"
	case RecoveryPhaseComplete:
		return "complete"
	default:
		return "unknown"
	}
}

// ────────────────────────────────────────────────────────────────────────────
// RecoveryInfo — per-session recovery metadata
// ────────────────────────────────────────────────────────────────────────────

// RecoveryOutcome describes how a session's recovery attempt concluded.
type RecoveryOutcome string

const (
	// RecoveryOutcomePending means recovery has not yet been attempted or
	// is still in progress for this session.
	RecoveryOutcomePending RecoveryOutcome = "pending"

	// RecoveryOutcomeRecovered means the session was successfully
	// reconnected to its surviving agent-run process.
	RecoveryOutcomeRecovered RecoveryOutcome = "recovered"

	// RecoveryOutcomeFailed means the session could not be recovered and
	// was marked stopped (fail-closed).
	RecoveryOutcomeFailed RecoveryOutcome = "failed"
)

// RecoveryInfo captures the result of a recovery attempt for a single session.
// It is stored on RunProcess and surfaced through ARI session/status.
type RecoveryInfo struct {
	// Recovered indicates whether the session was successfully recovered
	// during the last daemon restart.
	Recovered bool `json:"recovered"`

	// RecoveredAt is the wall-clock time when recovery completed for this
	// session. Nil if recovery has not completed yet.
	RecoveredAt *time.Time `json:"recoveredAt,omitempty"`

	// Outcome is the recovery result: "recovered", "failed", or "pending".
	Outcome RecoveryOutcome `json:"outcome"`
}
