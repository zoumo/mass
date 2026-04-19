// Package api contains the ARI protocol types.
// This file defines the domain/store types used internally by mass and
// as ARI wire format (metadata/spec/status shape). These are the types
// returned by ARI methods — internal-only fields are tagged json:"-".
package api

import (
	"encoding/json"
	"time"

	apiruntime "github.com/zoumo/mass/pkg/runtime-spec/api"
)

// ────────────────────────────────────────────────────────────────────────────
// Object system (controller-runtime style)
// ────────────────────────────────────────────────────────────────────────────

// Object is implemented by all ARI domain types (Workspace, AgentRun, Agent).
type Object interface {
	GetObjectMeta() *ObjectMeta
	objectType() string
}

// ObjectList is implemented by all ARI list types (WorkspaceList, AgentRunList, AgentList).
type ObjectList interface {
	objectType() string
}

// ObjectKey identifies a domain object by scoping namespace and name.
type ObjectKey struct {
	// Workspace is the scoping namespace. Empty for global resources (Workspace, Agent).
	Workspace string `json:"workspace,omitempty"`
	// Name is the unique name within the scope.
	Name string `json:"name"`
}

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
// Agent definition
// ────────────────────────────────────────────────────────────────────────────

// AgentSpec describes how to launch an agent process for this named agent definition.
type AgentSpec struct {
	// Disabled controls whether the agent is prevented from creating new agent runs.
	// nil or false means not disabled (agent is usable). true means disabled.
	Disabled *bool `json:"disabled,omitempty"`

	// ClientProtocol selects the communication protocol adapter.
	// Default: "acp".
	ClientProtocol apiruntime.ClientProtocol `json:"clientProtocol,omitempty"`

	// Command is the agent executable.
	Command string `json:"command"`

	// Args are the command-line arguments passed to Command.
	Args []string `json:"args,omitempty"`

	// Env is the list of environment variable overrides applied to the process.
	Env []apiruntime.EnvVar `json:"env,omitempty"`

	// StartupTimeoutSeconds is the maximum time (in seconds) to wait for the
	// agent-run to reach idle state. Nil means use the daemon default.
	StartupTimeoutSeconds *int `json:"startupTimeoutSeconds,omitempty"`
}

// IsDisabled reports whether the agent is disabled.
// A nil Disabled pointer is treated as not disabled (backward-compatible default).
func (s AgentSpec) IsDisabled() bool {
	return s.Disabled != nil && *s.Disabled
}

// Agent represents an agent definition record.
// An Agent is a named, reusable launch configuration (command, args, env, startup timeout).
// It is selected by AgentRun.Spec.Agent when creating a running instance.
// Identity is Metadata.Name — globally unique across all agent definitions.
type Agent struct {
	// Metadata holds identity and lifecycle fields.
	Metadata ObjectMeta `json:"metadata"`

	// Spec describes the desired launch configuration.
	Spec AgentSpec `json:"spec"`
}

func (a *Agent) GetObjectMeta() *ObjectMeta { return &a.Metadata }
func (a *Agent) objectType() string         { return "agent" }

// AgentList holds a list of Agent objects.
type AgentList struct {
	Items []Agent `json:"items"`
}

func (l *AgentList) objectType() string { return "agent" }

// ────────────────────────────────────────────────────────────────────────────
// AgentRun
// ────────────────────────────────────────────────────────────────────────────

// AgentRunSpec describes the desired configuration of an agent run.
type AgentRunSpec struct {
	// Agent is the agent definition name that this run is based on.
	// It references an Agent record by name.
	Agent string `json:"agent"`

	// SystemPrompt is the agent's system prompt for this run.
	SystemPrompt string `json:"systemPrompt,omitempty"`

	// Permissions controls how agent-run handles agent-initiated
	// fs/* and terminal/* requests. Default: ApproveAll.
	Permissions apiruntime.PermissionPolicy `json:"permissions,omitempty"`

	// McpServers is the list of extra MCP services available to the agent in this run.
	// agentd auto-injects the workspace MCP server; this field allows
	// callers to add additional MCP servers per run.
	McpServers []apiruntime.McpServer `json:"mcpServers,omitempty"`

	// Description is a human-readable description of the agent.
	Description string `json:"description,omitempty"`
}

// AgentRunStatus holds the observed runtime state of an agent run.
// Internal fields (run socket, state dir, PID, bootstrap config) must be
// persisted in the store (json tags present) but stripped via ARIView()
// before sending over the wire.
type AgentRunStatus struct {
	// State is the current lifecycle status of the agent.
	State apiruntime.Status `json:"state"`

	// ErrorMessage is a non-empty error description when State is apiruntime.StatusError.
	ErrorMessage string `json:"errorMessage,omitempty"`

	// Run holds the runtime state of the agent-run process.
	// Populated in Get responses when the agent-run is running; nil otherwise.
	// Contains SocketPath so callers no longer need agentrun/attach.
	Run *RunStateInfo `json:"run,omitempty"`

	// RunSocketPath is the Unix socket path for the agent-run's RPC endpoint.
	// Persisted in store; stripped by ARIView().
	RunSocketPath string `json:"runSocketPath,omitempty"`

	// RunStateDir is the absolute path to the agent-run's state directory.
	// Persisted in store; stripped by ARIView().
	RunStateDir string `json:"runStateDir,omitempty"`

	// RunPID is the OS process ID of the agent-run process.
	// Persisted in store; stripped by ARIView().
	RunPID int `json:"runPid,omitempty"`

	// BootstrapConfig is the JSON-serialized config used to start this agent run.
	// Persisted in store; stripped by ARIView().
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

func (a *AgentRun) GetObjectMeta() *ObjectMeta { return &a.Metadata }
func (a *AgentRun) objectType() string         { return "agentrun" }

// AgentRunList holds a list of AgentRun objects.
type AgentRunList struct {
	Items []AgentRun `json:"items"`
}

func (l *AgentRunList) objectType() string { return "agentrun" }

// AgentRunFilter defines filter criteria for listing agent runs.
type AgentRunFilter struct {
	// Workspace filters by workspace name. Empty means all workspaces.
	Workspace string

	// State filters by agent status. Empty/zero means all states.
	State apiruntime.Status
}

// ────────────────────────────────────────────────────────────────────────────
// Workspace
// ────────────────────────────────────────────────────────────────────────────

// WorkspaceSpec describes the desired configuration of a workspace.
type WorkspaceSpec struct {
	// Source is the source specification (git/emptyDir/local), stored as raw JSON.
	Source json.RawMessage `json:"source,omitempty"`

	// Hooks is the lifecycle hooks specification.
	// Persisted in store; not exposed in ARI wire responses.
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

func (w *Workspace) GetObjectMeta() *ObjectMeta { return &w.Metadata }
func (w *Workspace) objectType() string         { return "workspace" }

// WorkspaceList holds a list of Workspace objects.
type WorkspaceList struct {
	Items []Workspace `json:"items"`
}

func (l *WorkspaceList) objectType() string { return "workspace" }

// WorkspaceFilter defines filter criteria for listing workspaces.
type WorkspaceFilter struct {
	// Phase filters by workspace phase. Empty/zero means all phases.
	Phase WorkspacePhase
}

// ────────────────────────────────────────────────────────────────────────────
// ARI view helpers — strip internal-only fields before sending over the wire
// ────────────────────────────────────────────────────────────────────────────

// ARIView returns an AgentRun with internal-only fields zeroed for wire transmission.
func (a AgentRun) ARIView() AgentRun {
	a.Status.RunSocketPath = ""
	a.Status.RunStateDir = ""
	a.Status.RunPID = 0
	a.Status.BootstrapConfig = nil
	return a
}

// ARIView returns a Workspace with internal-only fields zeroed.
// Use this when building ARI wire responses (workspace/status, workspace/list, etc.).
func (w Workspace) ARIView() Workspace {
	w.Spec.Hooks = nil
	return w
}
