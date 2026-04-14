// Package api contains the ARI (Agent Runtime Interface) JSON-RPC wire types.
// These are pure request/response parameter and result types for all ARI methods.
// Domain types (Agent, AgentRun, Workspace) are defined in domain.go and serve
// as both the internal store types and the ARI wire format.
package api

import (
	"encoding/json"

	apiruntime "github.com/zoumo/oar/pkg/runtime-spec/api"
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
// Workspace RPC Params / Results
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
	// Workspace is the created workspace domain object (status.phase always "pending").
	Workspace Workspace `json:"workspace"`
}

// WorkspaceStatusParams is the request params for workspace/status method.
type WorkspaceStatusParams struct {
	// Name is the workspace name (required).
	Name string `json:"name"`
}

// WorkspaceStatusResult is the response result for workspace/status method.
type WorkspaceStatusResult struct {
	// Workspace is the current workspace domain object.
	Workspace Workspace `json:"workspace"`

	// Members is the list of agent runs currently using this workspace.
	Members []AgentRun `json:"members"`
}

// WorkspaceListParams is the request params for workspace/list method.
type WorkspaceListParams struct{}

// WorkspaceListResult is the response result for workspace/list method.
type WorkspaceListResult struct {
	// Workspaces is the array of workspace domain objects.
	Workspaces []Workspace `json:"workspaces"`
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
	// Delivered is true when the message was dispatched to the target shim.
	Delivered bool `json:"delivered"`
}

// ────────────────────────────────────────────────────────────────────────────
// AgentRun RPC Params / Results
// ────────────────────────────────────────────────────────────────────────────

// AgentRunCreateParams is the request params for agentrun/create method.
type AgentRunCreateParams struct {
	// Workspace is the workspace name (required).
	Workspace string `json:"workspace"`

	// Name is the agent run name within the workspace (required).
	Name string `json:"name"`

	// Agent is the agent definition name to use (required).
	Agent string `json:"agent"`

	// RestartPolicy controls session continuation on restart.
	// Values: "try_reload" | "always_new" (default: "always_new")
	RestartPolicy string `json:"restartPolicy,omitempty"`

	// SystemPrompt is the bootstrap system prompt for the agent session.
	SystemPrompt string `json:"systemPrompt,omitempty"`

	// Labels are optional key-value metadata.
	Labels map[string]string `json:"labels,omitempty"`
}

// AgentRunCreateResult is the response result for agentrun/create method.
type AgentRunCreateResult struct {
	// AgentRun is the created agent run domain object (status.state always "creating").
	AgentRun AgentRun `json:"agentRun"`
}

// AgentRunPromptParams is the request params for agentrun/prompt method.
type AgentRunPromptParams struct {
	// Workspace is the workspace name (required).
	Workspace string `json:"workspace"`

	// Name is the agent run name (required).
	Name string `json:"name"`

	// Prompt is the text prompt to deliver (required).
	Prompt string `json:"prompt"`
}

// AgentRunPromptResult is the response result for agentrun/prompt method.
type AgentRunPromptResult struct {
	// Accepted is true when the prompt was dispatched to the agent's shim.
	Accepted bool `json:"accepted"`
}

// AgentRunCancelParams is the request params for agentrun/cancel method.
type AgentRunCancelParams struct {
	// Workspace is the workspace name (required).
	Workspace string `json:"workspace"`

	// Name is the agent run name (required).
	Name string `json:"name"`
}

// AgentRunStopParams is the request params for agentrun/stop method.
type AgentRunStopParams struct {
	// Workspace is the workspace name (required).
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
	// AgentRun is the restarted agent run domain object (status.state is "creating").
	AgentRun AgentRun `json:"agentRun"`
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
	// AgentRun is the current agent run domain object.
	AgentRun AgentRun `json:"agentRun"`

	// ShimState is the runtime state of the shim process.
	// Only populated if the agent run has a running shim.
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
	// AgentRuns is the array of agent run domain objects.
	AgentRuns []AgentRun `json:"agentRuns"`
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
// Returned in AgentRunStatusResult.ShimState when the shim is running.
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
// Agent RPC Params / Results
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
	Env []apiruntime.EnvVar `json:"env,omitempty"`

	// StartupTimeoutSeconds is the optional startup timeout in seconds.
	StartupTimeoutSeconds *int `json:"startupTimeoutSeconds,omitempty"`
}

// AgentSetResult is the response result for agent/set method.
type AgentSetResult struct {
	// Agent is the created or updated agent domain object.
	Agent Agent `json:"agent"`
}

// AgentGetParams is the request params for agent/get method.
type AgentGetParams struct {
	// Name is the agent definition name (required).
	Name string `json:"name"`
}

// AgentGetResult is the response result for agent/get method.
type AgentGetResult struct {
	// Agent is the requested agent domain object.
	Agent Agent `json:"agent"`
}

// AgentListParams is the request params for agent/list method.
type AgentListParams struct{}

// AgentListResult is the response result for agent/list method.
type AgentListResult struct {
	// Agents is the array of agent domain objects.
	Agents []Agent `json:"agents"`
}

// AgentDeleteParams is the request params for agent/delete method.
type AgentDeleteParams struct {
	// Name is the agent definition name to delete (required).
	Name string `json:"name"`
}
