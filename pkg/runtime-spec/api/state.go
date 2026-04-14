// Package api defines the OAR Runtime Specification types.
// This file contains the State types written to state.json by agent-shim.
package api

// State is the runtime state of an agent instance, written to state.json.
// Mirrors the OCI runtime state structure.
type State struct {
	// OarVersion is the OAR Runtime Spec version this state complies with.
	OarVersion string `json:"oarVersion"`

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
}
