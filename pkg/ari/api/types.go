// Package api contains the ARI (Agent Runtime Interface) JSON-RPC wire types.
// Domain types (Agent, AgentRun, Workspace) are defined in domain.go and serve
// as both the internal store types and the ARI wire format.
// This file contains shared option types, domain operation params/results,
// and supplementary wire types.
package api

import (
	"time"

	runapi "github.com/zoumo/mass/pkg/agentrun/api"
)

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
// Domain operation params/results (non-CRUD operations with unique params)
// ────────────────────────────────────────────────────────────────────────────

// AgentRunPromptParams is the request params for agentrun/prompt method.
type AgentRunPromptParams struct {
	// Workspace is the workspace name (required).
	Workspace string `json:"workspace"`

	// Name is the agent run name (required).
	Name string `json:"name"`

	// Prompt is an array of ACP ContentBlocks (text, image, audio, etc.) (required).
	Prompt []runapi.ContentBlock `json:"prompt"`
}

// AgentRunPromptResult is the response result for agentrun/prompt method.
type AgentRunPromptResult struct {
	// Accepted is true when the prompt was dispatched to the agent-run.
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

	// Message is an array of ACP ContentBlocks to send (required).
	Message []runapi.ContentBlock `json:"message"`

	// NeedsReply indicates whether the sender expects a reply via workspace message.
	// When true, the delivered prompt includes reply-to and reply-requested=true headers.
	NeedsReply bool `json:"needsReply,omitempty"`
}

// WorkspaceSendResult is the response result for workspace/send method.
type WorkspaceSendResult struct {
	// Delivered is true when the message was dispatched to the target agent-run.
	Delivered bool `json:"delivered"`
}

// ────────────────────────────────────────────────────────────────────────────
// AgentTask types
// ────────────────────────────────────────────────────────────────────────────

// AgentTask is the on-disk task record.
type AgentTask struct {
	ID        string             `json:"id"`
	Assignee  string             `json:"assignee"`
	Attempt   int                `json:"attempt"`
	CreatedAt time.Time          `json:"createdAt"`
	Request   AgentTaskRequest   `json:"request"`
	Completed bool               `json:"completed"`
	Response  *AgentTaskResponse `json:"response,omitempty"`
}

// AgentTaskRequest is the request portion of a task.
type AgentTaskRequest struct {
	Description string   `json:"description"`
	FilePaths   []string `json:"filePaths,omitempty"`
}

// AgentTaskResponse is the response portion of a task (written by agent).
type AgentTaskResponse struct {
	Status      string    `json:"status"`
	Description string    `json:"description"`
	FilePaths   []string  `json:"filePaths,omitempty"`
	UpdatedAt   time.Time `json:"updatedAt"`
}

// AgentRunTaskCreateParams is the request params for agentrun/task/create.
type AgentRunTaskCreateParams struct {
	Workspace   string   `json:"workspace"`
	Name        string   `json:"name"`
	Description string   `json:"description"`
	FilePaths   []string `json:"filePaths,omitempty"`
}

// AgentRunTaskCreateResult is the response result for agentrun/task/create.
type AgentRunTaskCreateResult struct {
	Task     AgentTask `json:"task"`
	TaskPath string    `json:"taskPath"`
}

// AgentRunTaskGetParams is the request params for agentrun/task/get.
type AgentRunTaskGetParams struct {
	Workspace string `json:"workspace"`
	Name      string `json:"name"`
	TaskID    string `json:"taskId"`
}

// AgentRunTaskListParams is the request params for agentrun/task/list.
type AgentRunTaskListParams struct {
	Workspace string `json:"workspace"`
	Name      string `json:"name"`
}

// AgentRunTaskListResult is the response result for agentrun/task/list.
type AgentRunTaskListResult struct {
	Items []AgentTask `json:"items"`
}

// AgentRunTaskRetryParams is the request params for agentrun/task/retry.
type AgentRunTaskRetryParams struct {
	Workspace string `json:"workspace"`
	Name      string `json:"name"`
	TaskID    string `json:"taskId"`
}

// AgentRunTaskRetryResult is the response result for agentrun/task/retry.
type AgentRunTaskRetryResult struct {
	Task     AgentTask `json:"task"`
	TaskPath string    `json:"taskPath"`
}

// ────────────────────────────────────────────────────────────────────────────
// System Info (daemon version and runtime info)
// ────────────────────────────────────────────────────────────────────────────

// SystemInfoParams is the request params for system/info (empty).
type SystemInfoParams struct{}

// SystemInfoResult is the response result for system/info.
type SystemInfoResult struct {
	Version    string `json:"version"`
	GitCommit  string `json:"gitCommit"`
	BuildTime  string `json:"buildTime,omitempty"`
	GoVersion  string `json:"goVersion"`
	Root       string `json:"root"`
	SocketPath string `json:"socketPath"`
	Pid        int    `json:"pid"`
}
