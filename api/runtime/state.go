// Package runtime defines the OAR Runtime Specification types.
// This file contains the State types written to state.json by agent-shim.
package runtime

import "github.com/zoumo/oar/api"

// LastTurn records the outcome of the most recent agent turn.
// Written when a turn ends (success or error) so callers can check
// the result without needing a live event stream subscription.
type LastTurn struct {
	// StopReason is the ACP stop reason (e.g. "end_turn", "canceled").
	// Empty if the turn ended with an error.
	StopReason string `json:"stopReason,omitempty"`

	// Error is a non-empty error message if the turn failed.
	Error string `json:"error,omitempty"`

	// CompletedAt is the RFC 3339 timestamp when the turn ended.
	CompletedAt string `json:"completedAt,omitempty"`
}

// State is the runtime state of an agent instance, written to state.json.
// Mirrors the OCI runtime state structure.
type State struct {
	// OarVersion is the OAR Runtime Spec version this state complies with.
	OarVersion string `json:"oarVersion"`

	// ID is the agent's unique session ID.
	ID string `json:"id"`

	// Status is the current lifecycle status.
	Status api.Status `json:"status"`

	// PID is the OS process ID of the agent process.
	// Required when Status is created or running.
	PID int `json:"pid,omitempty"`

	// Bundle is the absolute path to the agent's bundle directory.
	Bundle string `json:"bundle"`

	// Annotations contains arbitrary metadata from the bundle config.
	Annotations map[string]string `json:"annotations,omitempty"`

	// LastTurn records the outcome of the most recent agent turn.
	// Nil before the first turn completes.
	LastTurn *LastTurn `json:"lastTurn,omitempty"`

	// ExitCode is the OS exit code of the agent process.
	// Nil while the process is running; populated after exit.
	ExitCode *int `json:"exitCode,omitempty"`
}
