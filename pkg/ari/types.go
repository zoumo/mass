// Package ari implements the Agent Runtime Interface (ARI) JSON-RPC server.
// ARI provides methods for workspace management, session lifecycle, and room coordination.
package ari

import (
	"github.com/open-agent-d/open-agent-d/pkg/workspace"
)

// WorkspacePrepareParams is the request params for workspace/prepare method.
// It contains the workspace specification to prepare.
type WorkspacePrepareParams struct {
	// Spec is the OAR Workspace Specification describing how to prepare the workspace.
	Spec workspace.WorkspaceSpec `json:"spec"`
}

// WorkspacePrepareResult is the response result for workspace/prepare method.
// It contains the prepared workspace metadata.
type WorkspacePrepareResult struct {
	// WorkspaceId is the unique identifier assigned to this workspace.
	WorkspaceId string `json:"workspaceId"`

	// Path is the absolute path to the prepared workspace directory.
	Path string `json:"path"`

	// Status is the workspace state. Always "ready" on successful prepare.
	Status string `json:"status"`
}

// WorkspaceListParams is the request params for workspace/list method.
// Currently empty — no filter fields defined for this slice.
type WorkspaceListParams struct{}

// WorkspaceListResult is the response result for workspace/list method.
// It contains the list of all registered workspaces.
type WorkspaceListResult struct {
	// Workspaces is the array of workspace info objects.
	Workspaces []WorkspaceInfo `json:"workspaces"`
}

// WorkspaceInfo describes a single workspace in the registry.
// Returned by workspace/list method.
type WorkspaceInfo struct {
	// WorkspaceId is the unique identifier for this workspace.
	WorkspaceId string `json:"workspaceId"`

	// Name is the workspace name from metadata.
	Name string `json:"name"`

	// Path is the absolute path to the workspace directory.
	Path string `json:"path"`

	// Status is the current workspace state (e.g., "ready", "preparing", "error").
	Status string `json:"status"`

	// Refs is the list of session IDs referencing this workspace.
	// Empty if no active sessions are using this workspace.
	Refs []string `json:"refs"`
}

// WorkspaceCleanupParams is the request params for workspace/cleanup method.
// It identifies which workspace to clean up.
type WorkspaceCleanupParams struct {
	// WorkspaceId is the unique identifier of the workspace to clean up.
	WorkspaceId string `json:"workspaceId"`
}

// =============================================================================
// Session Types
// =============================================================================

// SessionNewParams is the request params for session/new method.
// It contains the specification for creating a new session.
type SessionNewParams struct {
	// WorkspaceId is the workspace to use for this session (required).
	WorkspaceId string `json:"workspaceId"`

	// RuntimeClass is the runtime class for this session (required).
	// Examples: "default", "cuda", "experimental".
	RuntimeClass string `json:"runtimeClass"`

	// Labels are optional key-value metadata for the session.
	Labels map[string]string `json:"labels,omitempty"`

	// Room is the optional room name for multi-agent coordination.
	Room string `json:"room,omitempty"`

	// RoomAgent is the optional agent name/ID within a room.
	RoomAgent string `json:"roomAgent,omitempty"`
}

// SessionNewResult is the response result for session/new method.
// It contains the newly created session identifier and initial state.
type SessionNewResult struct {
	// SessionId is the unique identifier assigned to this session.
	SessionId string `json:"sessionId"`

	// State is the initial session state, always "created" on success.
	State string `json:"state"`
}

// SessionPromptParams is the request params for session/prompt method.
// It contains the prompt text to send to the agent.
type SessionPromptParams struct {
	// SessionId is the unique identifier of the session to prompt (required).
	SessionId string `json:"sessionId"`

	// Text is the prompt message to send to the agent (required).
	Text string `json:"text"`
}

// SessionPromptResult is the response result for session/prompt method.
// It indicates why the prompt processing stopped.
type SessionPromptResult struct {
	// StopReason is the reason the agent stopped processing.
	// Values: "end_turn" (normal completion), "cancelled" (user cancelled),
	// "tool_use" (agent needs tool execution).
	StopReason string `json:"stopReason"`
}

// SessionCancelParams is the request params for session/cancel method.
// It identifies the session to cancel current prompt processing.
type SessionCancelParams struct {
	// SessionId is the unique identifier of the session to cancel (required).
	SessionId string `json:"sessionId"`
}

// SessionStopParams is the request params for session/stop method.
// It identifies the session to stop (shuts down the agent process).
type SessionStopParams struct {
	// SessionId is the unique identifier of the session to stop (required).
	SessionId string `json:"sessionId"`
}

// SessionRemoveParams is the request params for session/remove method.
// It identifies the session to remove from the registry.
type SessionRemoveParams struct {
	// SessionId is the unique identifier of the session to remove (required).
	SessionId string `json:"sessionId"`
}

// SessionListParams is the request params for session/list method.
// It contains optional filters for listing sessions.
type SessionListParams struct {
	// Labels is an optional filter to match sessions by labels.
	Labels map[string]string `json:"labels,omitempty"`
}

// SessionListResult is the response result for session/list method.
// It contains the list of matching sessions.
type SessionListResult struct {
	// Sessions is the array of session info objects.
	Sessions []SessionInfo `json:"sessions"`
}

// SessionInfo describes a single session in the registry.
// Returned by session/list and session/status methods.
type SessionInfo struct {
	// Id is the unique session identifier (UUID).
	Id string `json:"id"`

	// WorkspaceId is the workspace this session uses.
	WorkspaceId string `json:"workspaceId"`

	// RuntimeClass is the runtime class for this session.
	RuntimeClass string `json:"runtimeClass"`

	// State is the current session state.
	// Values: "created", "running", "paused:warm", "paused:cold", "stopped".
	State string `json:"state"`

	// Room is the room name if this session is part of a multi-agent room.
	// Empty string means no room association.
	Room string `json:"room,omitempty"`

	// RoomAgent is the agent name/ID within the room.
	// Empty string means no room agent association.
	RoomAgent string `json:"roomAgent,omitempty"`

	// Labels are arbitrary key-value metadata for the session.
	Labels map[string]string `json:"labels,omitempty"`

	// CreatedAt is the RFC 3339 timestamp when the session was created.
	CreatedAt string `json:"createdAt"`

	// UpdatedAt is the RFC 3339 timestamp when the session was last updated.
	UpdatedAt string `json:"updatedAt"`
}

// SessionStatusParams is the request params for session/status method.
// It identifies the session to query status for.
type SessionStatusParams struct {
	// SessionId is the unique identifier of the session to query (required).
	SessionId string `json:"sessionId"`
}

// SessionStatusResult is the response result for session/status method.
// It contains the session info and optional shim runtime state.
type SessionStatusResult struct {
	// Session is the session metadata from the registry.
	Session SessionInfo `json:"session"`

	// ShimState is the runtime state of the shim process.
	// Only populated if the session is running (state="running").
	ShimState *ShimStateInfo `json:"shimState,omitempty"`
}

// ShimStateInfo describes the runtime state of a shim process.
// Populated in SessionStatusResult when session is running.
type ShimStateInfo struct {
	// Status is the shim process lifecycle status.
	// Values: "creating", "created", "running", "stopped".
	Status string `json:"status"`

	// PID is the OS process ID of the shim process.
	PID int `json:"pid,omitempty"`

	// Bundle is the absolute path to the agent's bundle directory.
	Bundle string `json:"bundle"`

	// ExitCode is the OS exit code of the shim process.
	// Only populated after the process has exited.
	ExitCode *int `json:"exitCode,omitempty"`
}

// SessionAttachParams is the request params for session/attach method.
// It identifies the session to attach to (get shim RPC socket path).
type SessionAttachParams struct {
	// SessionId is the unique identifier of the session to attach (required).
	SessionId string `json:"sessionId"`
}

// SessionAttachResult is the response result for session/attach method.
// It contains the shim RPC socket path for direct communication.
type SessionAttachResult struct {
	// SocketPath is the Unix domain socket path for the shim RPC server.
	SocketPath string `json:"socketPath"`
}

// SessionDetachParams is the request params for session/detach method.
// It identifies the session to detach from.
type SessionDetachParams struct {
	// SessionId is the unique identifier of the session to detach (required).
	SessionId string `json:"sessionId"`
}