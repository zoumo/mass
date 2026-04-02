package spec

// Status is the runtime status of an agent, mirroring OCI container status.
type Status string

const (
	// StatusCreating means the agent is being created (ACP handshake not yet complete).
	StatusCreating Status = "creating"

	// StatusCreated means the agent process is running and the ACP session is
	// established. The agent is idle, waiting for a prompt.
	StatusCreated Status = "created"

	// StatusRunning means the agent is processing a session/prompt.
	StatusRunning Status = "running"

	// StatusStopped means the agent process has exited.
	StatusStopped Status = "stopped"
)

// String implements fmt.Stringer.
func (s Status) String() string {
	return string(s)
}

// LastTurn records the outcome of the most recent agent turn.
// Written when a turn ends (success or error) so callers can check
// the result without needing a live event stream subscription.
type LastTurn struct {
	// StopReason is the ACP stop reason (e.g. "end_turn", "cancelled").
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
	Status Status `json:"status"`

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
}
