// Package api contains the ARI (Agent Runtime Interface) JSON-RPC wire types.
// Domain types (Agent, AgentRun, Workspace) are defined in domain.go and serve
// as both the internal store types and the ARI wire format.
// This file contains shared option types, domain operation params/results,
// and supplementary wire types.
package api

// ────────────────────────────────────────────────────────────────────────────
// Custom JSON-RPC Error Codes
// ────────────────────────────────────────────────────────────────────────────

const (
	// CodeRecoveryBlocked is the JSON-RPC error code returned when an
	// operational action (agentrun/prompt, agentrun/cancel) is refused because
	// the daemon is actively recovering agents. Clients should retry once
	// recovery completes (poll agentrun/get or agentrun/list to observe
	// the daemon leaving the recovering phase).
	CodeRecoveryBlocked int64 = -32001
)

// ────────────────────────────────────────────────────────────────────────────
// Shared List Options (controller-runtime style)
// ────────────────────────────────────────────────────────────────────────────

// ListOptions configures filtering for List operations.
type ListOptions struct {
	// FieldSelector filters by field values.
	// Supported fields depend on the resource type:
	//   Workspace: "phase"
	//   AgentRun:  "workspace", "state"
	//   Agent:     (none)
	FieldSelector map[string]string `json:"fieldSelector,omitempty"`

	// Labels filters by label key-value pairs.
	Labels map[string]string `json:"labels,omitempty"`
}

// ListOption is a functional option for List operations.
type ListOption func(*ListOptions)

// ApplyListOptions applies all ListOption functions and returns the resulting ListOptions.
func ApplyListOptions(opts ...ListOption) ListOptions {
	var o ListOptions
	for _, fn := range opts {
		fn(&o)
	}
	return o
}

// WithField sets a single field selector entry.
func WithField(field, value string) ListOption {
	return func(o *ListOptions) {
		if o.FieldSelector == nil {
			o.FieldSelector = make(map[string]string)
		}
		o.FieldSelector[field] = value
	}
}

// InWorkspace filters agent runs by workspace name.
func InWorkspace(ws string) ListOption { return WithField("workspace", ws) }

// WithState filters agent runs by state.
func WithState(state string) ListOption { return WithField("state", state) }

// WithPhase filters workspaces by phase.
func WithPhase(phase string) ListOption { return WithField("phase", phase) }

// WithLabels filters by label key-value pairs.
func WithLabels(labels map[string]string) ListOption {
	return func(o *ListOptions) {
		o.Labels = labels
	}
}

// ────────────────────────────────────────────────────────────────────────────
// Shim State
// ────────────────────────────────────────────────────────────────────────────

// ShimStateInfo describes the runtime state of a shim process.
// Populated in AgentRun.Status.Shim when the shim is running.
type ShimStateInfo struct {
	// Status is the shim process lifecycle status.
	Status string `json:"status"`

	// PID is the OS process ID of the shim process.
	PID int `json:"pid,omitempty"`

	// Bundle is the absolute path to the agent's bundle directory.
	Bundle string `json:"bundle"`

	// SocketPath is the Unix domain socket path for the shim's RPC endpoint.
	SocketPath string `json:"socketPath,omitempty"`

	// ExitCode is the OS exit code of the shim process.
	// Only populated after the process has exited.
	ExitCode *int `json:"exitCode,omitempty"`
}

// ────────────────────────────────────────────────────────────────────────────
// Domain operation params/results (non-CRUD operations with unique params)
// ────────────────────────────────────────────────────────────────────────────

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
