// Package meta provides metadata storage for OAR agent and workspace records.
// It uses bbolt (pure Go embedded key-value store) for persistence.
package meta

import (
	"encoding/json"
	"time"

	"github.com/open-agent-d/open-agent-d/pkg/spec"
)

// ObjectMeta holds identity and lifecycle fields common to all stored objects.
type ObjectMeta struct {
	// Name is the unique name within the parent scope.
	// For Workspace: unique globally. For AgentRun: unique within the Workspace.
	Name string `json:"name"`

	// Workspace is the parent workspace name. Used only on AgentRun records.
	// Empty for Workspace records.
	Workspace string `json:"workspace,omitempty"`

	// Labels are arbitrary key-value metadata.
	Labels map[string]string `json:"labels,omitempty"`

	// CreatedAt is the timestamp when the object was created.
	CreatedAt time.Time `json:"createdAt"`

	// UpdatedAt is the timestamp when the object was last updated.
	UpdatedAt time.Time `json:"updatedAt"`
}

// ────────────────────────────────────────────────────────────────────────────
// AgentRun
// ────────────────────────────────────────────────────────────────────────────

const (
	// RestartPolicyTryReload attempts ACP session/load to restore conversation
	// history from the prior session when the agent is recovered after a
	// daemon restart.
	RestartPolicyTryReload = "tryReload"

	// RestartPolicyAlwaysNew (default) always starts a fresh ACP session on
	// recovery, discarding prior conversation history.
	RestartPolicyAlwaysNew = "alwaysNew"
)

// AgentRunSpec describes the desired configuration of an agent run.
type AgentRunSpec struct {
	// Agent is the agent definition name that this run is based on.
	// It references an Agent record by name (analogous to Kubernetes runtimeClassName).
	Agent string `json:"agent"`

	// RestartPolicy controls session continuation on agent restart.
	// Values: "tryReload" — attempt ACP session/load to restore conversation history;
	//         "alwaysNew" (default) — always start a fresh ACP session.
	RestartPolicy string `json:"restartPolicy,omitempty"`

	// Description is a human-readable description of the agent.
	Description string `json:"description,omitempty"`

	// SystemPrompt is the agent's system prompt.
	SystemPrompt string `json:"systemPrompt,omitempty"`
}

// AgentRunStatus holds the observed runtime state of an agent run.
// These fields are written by the daemon as the agent transitions through its lifecycle.
type AgentRunStatus struct {
	// State is the current lifecycle status of the agent.
	State spec.Status `json:"state"`

	// ErrorMessage is a non-empty error description when State is spec.StatusError.
	ErrorMessage string `json:"errorMessage,omitempty"`

	// ShimSocketPath is the Unix socket path for the shim's RPC endpoint.
	// Used during recovery to reconnect to a still-alive shim.
	ShimSocketPath string `json:"shimSocketPath,omitempty"`

	// ShimStateDir is the absolute path to the shim's state directory.
	// Contains the event log and other shim-local state.
	ShimStateDir string `json:"shimStateDir,omitempty"`

	// ShimPID is the OS process ID of the shim process.
	// Used during recovery to check if the shim is still alive.
	ShimPID int `json:"shimPid,omitempty"`

	// BootstrapConfig is the JSON-serialized config used to start this agent's shim.
	// Stored as a JSON blob so the schema stays stable as config fields evolve.
	BootstrapConfig json.RawMessage `json:"bootstrapConfig,omitempty"`
}

// AgentRun represents an agent run record.
// Identity is (Metadata.Workspace, Metadata.Name) — no UUID.
type AgentRun struct {
	// Metadata holds identity and lifecycle fields.
	Metadata ObjectMeta `json:"metadata"`

	// Spec describes the desired configuration.
	Spec AgentRunSpec `json:"spec"`

	// Status holds the observed runtime state.
	Status AgentRunStatus `json:"status"`
}

// AgentRunFilter defines filter criteria for listing agent runs.
type AgentRunFilter struct {
	// Workspace filters by workspace name. Empty means all workspaces.
	Workspace string

	// State filters by agent status. Empty/zero means all states.
	State spec.Status
}

// ────────────────────────────────────────────────────────────────────────────
// Workspace
// ────────────────────────────────────────────────────────────────────────────

// WorkspaceSpec describes the desired configuration of a workspace.
type WorkspaceSpec struct {
	// Source is the source specification (git/emptyDir/local), stored as raw JSON.
	Source json.RawMessage `json:"source,omitempty"`

	// Hooks is the lifecycle hooks specification, stored as raw JSON.
	Hooks json.RawMessage `json:"hooks,omitempty"`
}

// WorkspacePhase is the lifecycle phase of a workspace.
type WorkspacePhase string

const (
	// WorkspacePhasePending indicates the workspace is being prepared.
	WorkspacePhasePending WorkspacePhase = "pending"

	// WorkspacePhaseReady indicates the workspace is prepared and usable.
	WorkspacePhaseReady WorkspacePhase = "ready"

	// WorkspacePhaseError indicates the workspace preparation failed.
	WorkspacePhaseError WorkspacePhase = "error"
)

// WorkspaceStatus holds the observed state of a workspace.
type WorkspaceStatus struct {
	// Phase is the current lifecycle phase.
	Phase WorkspacePhase `json:"phase"`

	// Path is the filesystem path to the prepared workspace directory.
	// Populated once Phase is WorkspacePhaseReady.
	Path string `json:"path,omitempty"`
}

// Workspace represents a workspace record.
// Identity is Metadata.Name — no UUID.
type Workspace struct {
	// Metadata holds identity and lifecycle fields.
	Metadata ObjectMeta `json:"metadata"`

	// Spec describes the desired configuration.
	Spec WorkspaceSpec `json:"spec"`

	// Status holds the observed state.
	Status WorkspaceStatus `json:"status"`
}

// WorkspaceFilter defines filter criteria for listing workspaces.
type WorkspaceFilter struct {
	// Phase filters by workspace phase. Empty/zero means all phases.
	Phase WorkspacePhase
}
