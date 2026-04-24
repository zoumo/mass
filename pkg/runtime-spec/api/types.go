// Package api contains pure API type definitions shared across MASS packages.
// It has no external dependencies — only the Go standard library.
package api

// Status is the runtime status of an agent, mirroring OCI container status.
type Status string

const (
	// StatusCreating means MASS accepted create/restart and runtime bootstrap
	// is pending or in progress (fork/exec + protocol handshake).
	StatusCreating Status = "creating"

	// StatusIdle means the agent process is running and the ACP session is
	// established. The agent is idle, waiting for a prompt.
	StatusIdle Status = "idle"

	// StatusRunning means the agent is processing a session/prompt.
	StatusRunning Status = "running"

	// StatusRestarting means MASS accepted a restart request and is stopping
	// the existing agent-run before starting a new one.
	StatusRestarting Status = "restarting"

	// StatusStopped means the agent process has exited.
	StatusStopped Status = "stopped"

	// StatusError means the agent encountered an unrecoverable error.
	StatusError Status = "error"
)

// String implements fmt.Stringer.
func (s Status) String() string {
	return string(s)
}

// EnvVar is a name-value pair representing an environment variable.
type EnvVar struct {
	// Name is the environment variable name.
	Name string `json:"name"`

	// Value is the environment variable value.
	Value string `json:"value"`
}
