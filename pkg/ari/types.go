// Package ari implements the Agent Runtime Interface (ARI) JSON-RPC server.
// ARI provides methods for workspace management and agent lifecycle.
package ari

import (
	"encoding/json"
	"time"
)

// ────────────────────────────────────────────────────────────────────────────
// Custom JSON-RPC Error Codes
// ────────────────────────────────────────────────────────────────────────────

const (
	// CodeRecoveryBlocked is the JSON-RPC error code returned when an
	// operational action (agent/prompt, agent/cancel) is refused because
	// the daemon is actively recovering agents. Clients should retry once
	// recovery completes (poll agent/status or agent/list to observe
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

	// Members is the list of agents currently using this workspace.
	Members []AgentInfo `json:"members,omitempty"`
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
// Routes a message from one agent to another within a workspace.
type WorkspaceSendParams struct {
	// Workspace is the workspace name (required).
	Workspace string `json:"workspace"`

	// From is the sender agent name (required).
	From string `json:"from"`

	// To is the recipient agent name (required).
	To string `json:"to"`

	// Message is the text to send (required).
	Message string `json:"message"`
}

// WorkspaceSendResult is the response result for workspace/send method.
type WorkspaceSendResult struct {
	// Delivered is true when the message was successfully dispatched.
	Delivered bool `json:"delivered"`
}

// ────────────────────────────────────────────────────────────────────────────
// Agent Types
// ────────────────────────────────────────────────────────────────────────────

// AgentCreateParams is the request params for agent/create method.
type AgentCreateParams struct {
	// Workspace is the workspace this agent belongs to (required).
	Workspace string `json:"workspace"`

	// Name is the agent name, unique within the workspace (required).
	Name string `json:"name"`

	// RuntimeClass is the runtime class for this agent (required).
	RuntimeClass string `json:"runtimeClass"`

	// RestartPolicy controls restart behavior ("never", "on-failure", "always").
	RestartPolicy string `json:"restartPolicy,omitempty"`

	// SystemPrompt is the agent's system prompt (optional).
	SystemPrompt string `json:"systemPrompt,omitempty"`

	// Labels are optional key-value metadata for the agent.
	Labels map[string]string `json:"labels,omitempty"`
}

// AgentCreateResult is the response result for agent/create method.
type AgentCreateResult struct {
	// Workspace is the workspace this agent belongs to.
	Workspace string `json:"workspace"`

	// Name is the agent name.
	Name string `json:"name"`

	// State is the initial agent state ("creating").
	State string `json:"state"`
}

// AgentPromptParams is the request params for agent/prompt method.
type AgentPromptParams struct {
	// Workspace is the workspace this agent belongs to (required).
	Workspace string `json:"workspace"`

	// Name is the agent name (required).
	Name string `json:"name"`

	// Prompt is the prompt message to send to the agent (required).
	Prompt string `json:"prompt"`
}

// AgentPromptResult is the response result for agent/prompt method.
type AgentPromptResult struct {
	// Accepted is true when the prompt was successfully dispatched.
	Accepted bool `json:"accepted"`
}

// AgentCancelParams is the request params for agent/cancel method.
type AgentCancelParams struct {
	// Workspace is the workspace this agent belongs to (required).
	Workspace string `json:"workspace"`

	// Name is the agent name (required).
	Name string `json:"name"`
}

// AgentStopParams is the request params for agent/stop method.
type AgentStopParams struct {
	// Workspace is the workspace this agent belongs to (required).
	Workspace string `json:"workspace"`

	// Name is the agent name (required).
	Name string `json:"name"`
}

// AgentDeleteParams is the request params for agent/delete method.
type AgentDeleteParams struct {
	// Workspace is the workspace this agent belongs to (required).
	Workspace string `json:"workspace"`

	// Name is the agent name (required).
	Name string `json:"name"`
}

// AgentRestartParams is the request params for agent/restart method.
type AgentRestartParams struct {
	// Workspace is the workspace this agent belongs to (required).
	Workspace string `json:"workspace"`

	// Name is the agent name (required).
	Name string `json:"name"`
}

// AgentRestartResult is the response result for agent/restart method.
type AgentRestartResult struct {
	// Workspace is the workspace this agent belongs to.
	Workspace string `json:"workspace"`

	// Name is the agent name.
	Name string `json:"name"`

	// State is the agent state immediately after restart ("creating").
	State string `json:"state"`
}

// AgentStatusParams is the request params for agent/status method.
type AgentStatusParams struct {
	// Workspace is the workspace this agent belongs to (required).
	Workspace string `json:"workspace"`

	// Name is the agent name (required).
	Name string `json:"name"`
}

// AgentStatusResult is the response result for agent/status method.
type AgentStatusResult struct {
	// Agent is the agent metadata.
	Agent AgentInfo `json:"agent"`

	// ShimState is the runtime state of the shim process.
	// Only populated if the agent is running.
	ShimState *ShimStateInfo `json:"shimState,omitempty"`
}

// AgentListParams is the request params for agent/list method.
type AgentListParams struct {
	// Workspace filters by workspace name (optional).
	Workspace string `json:"workspace,omitempty"`

	// State filters by agent state (optional).
	State string `json:"state,omitempty"`

	// Labels is an optional filter to match agents by labels.
	Labels map[string]string `json:"labels,omitempty"`
}

// AgentListResult is the response result for agent/list method.
type AgentListResult struct {
	// Agents is the array of agent info objects.
	Agents []AgentInfo `json:"agents"`
}

// AgentInfo describes a single agent.
// Returned by agent/list and agent/status methods.
type AgentInfo struct {
	// Workspace is the workspace this agent belongs to.
	Workspace string `json:"workspace"`

	// Name is the agent name within the workspace.
	Name string `json:"name"`

	// RuntimeClass is the runtime class for this agent.
	RuntimeClass string `json:"runtimeClass"`

	// State is the current agent state (spec.Status value).
	// Values: "creating", "idle", "running", "stopped", "error".
	State string `json:"state"`

	// ErrorMessage is the error message when state is "error".
	ErrorMessage string `json:"errorMessage,omitempty"`

	// Labels are arbitrary key-value metadata for the agent.
	Labels map[string]string `json:"labels,omitempty"`

	// CreatedAt is the timestamp when the agent was created.
	CreatedAt time.Time `json:"createdAt"`
}

// AgentAttachParams is the request params for agent/attach method.
type AgentAttachParams struct {
	// Workspace is the workspace this agent belongs to (required).
	Workspace string `json:"workspace"`

	// Name is the agent name (required).
	Name string `json:"name"`
}

// AgentAttachResult is the response result for agent/attach method.
type AgentAttachResult struct {
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
