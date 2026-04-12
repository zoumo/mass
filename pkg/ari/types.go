// Package ari implements the Agent Runtime Interface (ARI) JSON-RPC server.
// ARI provides methods for workspace management and agent lifecycle.
package ari

import (
	"encoding/json"
	"time"

	"github.com/open-agent-d/open-agent-d/pkg/spec"
)

// ────────────────────────────────────────────────────────────────────────────
// Custom JSON-RPC Error Codes
// ────────────────────────────────────────────────────────────────────────────

const (
	// CodeRecoveryBlocked is the JSON-RPC error code returned when an
	// operational action (agentrun/prompt, agentrun/cancel) is refused because
	// the daemon is actively recovering agents. Clients should retry once
	// recovery completes (poll agentrun/status or agentrun/list to observe
	// the daemon leaving the recovering phase).
	CodeRecoveryBlocked int64 = -32001
)

// ────────────────────────────────────────────────────────────────────────────
// Workspace Types
// ────────────────────────────────────────────────────────────────────────────

// WorkspaceCreateParams is the request params for workspace/create method.
type WorkspaceCreateParams struct {
	// Name is the unique workspace name (required).
	Name string `json:"name"`

	// Source is the workspace source specification (git/emptyDir/local).
	Source json.RawMessage `json:"source,omitempty"`

	// Labels are optional key-value metadata for the workspace.
	Labels map[string]string `json:"labels,omitempty"`
}

// WorkspaceCreateResult is the response result for workspace/create method.
type WorkspaceCreateResult struct {
	// Name is the workspace name.
	Name string `json:"name"`

	// Phase is the initial workspace phase ("pending").
	Phase string `json:"phase"`
}

// WorkspaceStatusParams is the request params for workspace/status method.
type WorkspaceStatusParams struct {
	// Name is the workspace name (required).
	Name string `json:"name"`
}

// WorkspaceStatusResult is the response result for workspace/status method.
type WorkspaceStatusResult struct {
	// Name is the workspace name.
	Name string `json:"name"`

	// Phase is the current workspace phase ("pending", "ready", "error").
	Phase string `json:"phase"`

	// Path is the absolute path to the prepared workspace directory.
	Path string `json:"path,omitempty"`

	// Members is the list of agent runs currently using this workspace.
	Members []AgentRunInfo `json:"members,omitempty"`
}

// WorkspaceListParams is the request params for workspace/list method.
type WorkspaceListParams struct{}

// WorkspaceListResult is the response result for workspace/list method.
type WorkspaceListResult struct {
	// Workspaces is the array of workspace info objects.
	Workspaces []WorkspaceInfo `json:"workspaces"`
}

// WorkspaceInfo describes a single workspace in the registry.
// Returned by workspace/list and workspace/status methods.
type WorkspaceInfo struct {
	// Name is the unique workspace name.
	Name string `json:"name"`

	// Phase is the current workspace phase ("pending", "ready", "error").
	Phase string `json:"phase"`

	// Path is the absolute path to the workspace directory.
	Path string `json:"path,omitempty"`
}

// WorkspaceDeleteParams is the request params for workspace/delete method.
type WorkspaceDeleteParams struct {
	// Name is the workspace name to delete (required).
	Name string `json:"name"`
}

// WorkspaceSendParams is the request params for workspace/send method.
// Routes a message from one agent run to another within a workspace.
type WorkspaceSendParams struct {
	// Workspace is the workspace name (required).
	Workspace string `json:"workspace"`

	// From is the sender agent name (required).
	From string `json:"from"`

	// To is the recipient agent name (required).
	To string `json:"to"`

	// Message is the text to send (required).
	Message string `json:"message"`

	// NeedsReply indicates whether the sender expects a reply via workspace message.
	// When true, the delivered prompt includes reply-to and reply-requested=true headers.
	NeedsReply bool `json:"needsReply,omitempty"`
}

// WorkspaceSendResult is the response result for workspace/send method.
type WorkspaceSendResult struct {
	// Delivered is true when the message was successfully dispatched.
	Delivered bool `json:"delivered"`
}

// ────────────────────────────────────────────────────────────────────────────
// AgentRun Types
// ────────────────────────────────────────────────────────────────────────────

// AgentRunCreateParams is the request params for agentrun/create method.
type AgentRunCreateParams struct {
	// Workspace is the workspace this agent run belongs to (required).
	Workspace string `json:"workspace"`

	// Name is the agent run name, unique within the workspace (required).
	Name string `json:"name"`

	// Agent is the agent definition name to use for this run (required).
	// References an Agent record by name.
	Agent string `json:"agent"`

	// RestartPolicy controls restart behavior ("never", "on-failure", "always").
	RestartPolicy string `json:"restartPolicy,omitempty"`

	// SystemPrompt is the agent's system prompt (optional).
	SystemPrompt string `json:"systemPrompt,omitempty"`

	// Labels are optional key-value metadata for the agent run.
	Labels map[string]string `json:"labels,omitempty"`
}

// AgentRunCreateResult is the response result for agentrun/create method.
type AgentRunCreateResult struct {
	// Workspace is the workspace this agent run belongs to.
	Workspace string `json:"workspace"`

	// Name is the agent run name.
	Name string `json:"name"`

	// State is the initial agent run state ("creating").
	State string `json:"state"`
}

// AgentRunPromptParams is the request params for agentrun/prompt method.
type AgentRunPromptParams struct {
	// Workspace is the workspace this agent run belongs to (required).
	Workspace string `json:"workspace"`

	// Name is the agent run name (required).
	Name string `json:"name"`

	// Prompt is the prompt message to send to the agent (required).
	Prompt string `json:"prompt"`
}

// AgentRunPromptResult is the response result for agentrun/prompt method.
type AgentRunPromptResult struct {
	// Accepted is true when the prompt was successfully dispatched.
	Accepted bool `json:"accepted"`
}

// AgentRunCancelParams is the request params for agentrun/cancel method.
type AgentRunCancelParams struct {
	// Workspace is the workspace this agent run belongs to (required).
	Workspace string `json:"workspace"`

	// Name is the agent run name (required).
	Name string `json:"name"`
}

// AgentRunStopParams is the request params for agentrun/stop method.
type AgentRunStopParams struct {
	// Workspace is the workspace this agent run belongs to (required).
	Workspace string `json:"workspace"`

	// Name is the agent run name (required).
	Name string `json:"name"`
}

// AgentRunDeleteParams is the request params for agentrun/delete method.
type AgentRunDeleteParams struct {
	// Workspace is the workspace this agent run belongs to (required).
	Workspace string `json:"workspace"`

	// Name is the agent run name (required).
	Name string `json:"name"`
}

// AgentRunRestartParams is the request params for agentrun/restart method.
type AgentRunRestartParams struct {
	// Workspace is the workspace this agent run belongs to (required).
	Workspace string `json:"workspace"`

	// Name is the agent run name (required).
	Name string `json:"name"`
}

// AgentRunRestartResult is the response result for agentrun/restart method.
type AgentRunRestartResult struct {
	// Workspace is the workspace this agent run belongs to.
	Workspace string `json:"workspace"`

	// Name is the agent run name.
	Name string `json:"name"`

	// State is the agent run state immediately after restart ("creating").
	State string `json:"state"`
}

// AgentRunStatusParams is the request params for agentrun/status method.
type AgentRunStatusParams struct {
	// Workspace is the workspace this agent run belongs to (required).
	Workspace string `json:"workspace"`

	// Name is the agent run name (required).
	Name string `json:"name"`
}

// AgentRunStatusResult is the response result for agentrun/status method.
type AgentRunStatusResult struct {
	// AgentRun is the agent run metadata.
	AgentRun AgentRunInfo `json:"agentRun"`

	// ShimState is the runtime state of the shim process.
	// Only populated if the agent run is running.
	ShimState *ShimStateInfo `json:"shimState,omitempty"`
}

// AgentRunListParams is the request params for agentrun/list method.
type AgentRunListParams struct {
	// Workspace filters by workspace name (optional).
	Workspace string `json:"workspace,omitempty"`

	// State filters by agent run state (optional).
	State string `json:"state,omitempty"`

	// Labels is an optional filter to match agent runs by labels.
	Labels map[string]string `json:"labels,omitempty"`
}

// AgentRunListResult is the response result for agentrun/list method.
type AgentRunListResult struct {
	// AgentRuns is the array of agent run info objects.
	AgentRuns []AgentRunInfo `json:"agentRuns"`
}

// AgentRunInfo describes a single agent run.
// Returned by agentrun/list and agentrun/status methods.
type AgentRunInfo struct {
	// Workspace is the workspace this agent run belongs to.
	Workspace string `json:"workspace"`

	// Name is the agent run name within the workspace.
	Name string `json:"name"`

	// Agent is the agent definition name used by this run.
	Agent string `json:"agent"`

	// State is the current agent run state (spec.Status value).
	// Values: "creating", "idle", "running", "stopped", "error".
	State string `json:"state"`

	// ErrorMessage is the error message when state is "error".
	ErrorMessage string `json:"errorMessage,omitempty"`

	// Labels are arbitrary key-value metadata for the agent run.
	Labels map[string]string `json:"labels,omitempty"`

	// CreatedAt is the timestamp when the agent run was created.
	CreatedAt time.Time `json:"createdAt"`
}

// AgentRunAttachParams is the request params for agentrun/attach method.
type AgentRunAttachParams struct {
	// Workspace is the workspace this agent run belongs to (required).
	Workspace string `json:"workspace"`

	// Name is the agent run name (required).
	Name string `json:"name"`
}

// AgentRunAttachResult is the response result for agentrun/attach method.
type AgentRunAttachResult struct {
	// SocketPath is the Unix domain socket path for the shim RPC server.
	SocketPath string `json:"socketPath"`
}

// ────────────────────────────────────────────────────────────────────────────
// Shim State
// ────────────────────────────────────────────────────────────────────────────

// ShimStateInfo describes the runtime state of a shim process.
type ShimStateInfo struct {
	// Status is the shim process lifecycle status.
	Status string `json:"status"`

	// PID is the OS process ID of the shim process.
	PID int `json:"pid,omitempty"`

	// Bundle is the absolute path to the agent's bundle directory.
	Bundle string `json:"bundle"`

	// ExitCode is the OS exit code of the shim process.
	// Only populated after the process has exited.
	ExitCode *int `json:"exitCode,omitempty"`
}

// ────────────────────────────────────────────────────────────────────────────
// Agent Types (agent definition CRUD)
// ────────────────────────────────────────────────────────────────────────────

// AgentSetParams is the request params for agent/set method.
type AgentSetParams struct {
	// Name is the unique agent definition name (required).
	Name string `json:"name"`

	// Command is the ACP agent executable (required).
	Command string `json:"command"`

	// Args are command-line arguments for the agent (optional).
	Args []string `json:"args,omitempty"`

	// Env is the list of environment variable overrides (optional).
	Env []spec.EnvVar `json:"env,omitempty"`

	// StartupTimeoutSeconds is the optional startup timeout in seconds.
	StartupTimeoutSeconds *int `json:"startupTimeoutSeconds,omitempty"`
}

// AgentInfo describes a single agent definition entity.
// Returned by agent/set, agent/get, and agent/list methods.
type AgentInfo struct {
	// Name is the unique agent definition name.
	Name string `json:"name"`

	// Command is the ACP agent executable.
	Command string `json:"command"`

	// Args are command-line arguments for the agent.
	Args []string `json:"args,omitempty"`

	// Env is the list of environment variable overrides.
	Env []spec.EnvVar `json:"env,omitempty"`

	// StartupTimeoutSeconds is the optional startup timeout in seconds.
	StartupTimeoutSeconds *int `json:"startupTimeoutSeconds,omitempty"`

	// CreatedAt is the timestamp when the agent definition was created.
	CreatedAt time.Time `json:"createdAt"`

	// UpdatedAt is the timestamp when the agent definition was last updated.
	UpdatedAt time.Time `json:"updatedAt"`
}

// AgentGetParams is the request params for agent/get method.
type AgentGetParams struct {
	// Name is the agent definition name (required).
	Name string `json:"name"`
}

// AgentGetResult is the response result for agent/get method.
type AgentGetResult struct {
	// Agent is the requested agent definition info.
	Agent AgentInfo `json:"agent"`
}

// AgentListParams is the request params for agent/list method.
type AgentListParams struct{}

// AgentListResult is the response result for agent/list method.
type AgentListResult struct {
	// Agents is the array of agent definition info objects.
	Agents []AgentInfo `json:"agents"`
}

// AgentDeleteParams is the request params for agent/delete method.
type AgentDeleteParams struct {
	// Name is the agent definition name to delete (required).
	Name string `json:"name"`
}
