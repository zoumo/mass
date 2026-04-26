// Package api contains pure API type definitions shared across MASS packages.
// It has no external dependencies — only the Go standard library.
package api

// Phase is the lifecycle phase of an agent, mirroring OCI container status.
type Phase string

const (
	// PhaseCreating means MASS accepted create/restart and runtime bootstrap
	// is pending or in progress (fork/exec + protocol handshake).
	PhaseCreating Phase = "creating"

	// PhaseIdle means the agent process is running and the ACP session is
	// established. The agent is idle, waiting for a prompt.
	PhaseIdle Phase = "idle"

	// PhaseRunning means the agent is processing a session/prompt.
	PhaseRunning Phase = "running"

	// PhaseRestarting means MASS accepted a restart request and is stopping
	// the existing agent-run before starting a new one.
	PhaseRestarting Phase = "restarting"

	// PhaseStopped means the agent process has exited.
	PhaseStopped Phase = "stopped"

	// PhaseError means the agent encountered an unrecoverable error.
	PhaseError Phase = "error"
)

// String implements fmt.Stringer.
func (p Phase) String() string {
	return string(p)
}

// EnvVar is a name-value pair representing an environment variable.
type EnvVar struct {
	// Name is the environment variable name.
	Name string `json:"name"`

	// Value is the environment variable value.
	Value string `json:"value"`
}
