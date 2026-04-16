// Package api defines the MASS Runtime Specification types.
// This file contains the State types written to state.json by agent-run.
package api

// State is the runtime state of an agent instance, written to state.json.
// Mirrors the OCI runtime state structure.
type State struct {
	// MassVersion is the MASS Runtime Spec version this state complies with.
	MassVersion string `json:"massVersion"`

	// ID is the agent's unique session ID.
	ID string `json:"id"`

	// Status is the current lifecycle status.
	Status Status `json:"status"`

	// PID is the OS process ID of the agent process.
	// Required when Status is created or running.
	PID int `json:"pid,omitempty"`

	// Bundle is the absolute path to the agent's bundle directory.
	Bundle string `json:"bundle"`

	// Annotations contains arbitrary metadata from the bundle config.
	Annotations map[string]string `json:"annotations,omitempty"`

	// ExitCode is the OS exit code of the agent process.
	// Nil while the process is running; populated after exit.
	ExitCode *int `json:"exitCode,omitempty"`

	// UpdatedAt is the RFC3339Nano timestamp of the last state write.
	UpdatedAt string `json:"updatedAt,omitempty"`

	// Session contains ACP session metadata populated progressively
	// as the agent reports notifications.
	Session *SessionState `json:"session,omitempty"`

	// EventCounts maps event type strings to their cumulative counts.
	// Derived field — set on every state write, not independently.
	EventCounts map[string]int `json:"eventCounts,omitempty"`
}
