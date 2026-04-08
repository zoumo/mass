// Package ari implements the Agent Runtime Interface (ARI) JSON-RPC server.
// ARI provides methods for workspace management, session lifecycle, and room coordination.
package ari

import (
	"time"

	"github.com/open-agent-d/open-agent-d/pkg/workspace"
)

// ────────────────────────────────────────────────────────────────────────────
// Custom JSON-RPC Error Codes
// ────────────────────────────────────────────────────────────────────────────

const (
	// CodeRecoveryBlocked is the JSON-RPC error code returned when an
	// operational action (session/prompt, session/cancel) is refused because
	// the daemon is actively recovering sessions. Clients should retry once
	// recovery completes (poll session/status or session/list to observe
	// the daemon leaving the recovering phase).
	CodeRecoveryBlocked int64 = -32001
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
	// Values: "creating", "created", "running", "stopped", "error".
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

	// Recovery holds per-session recovery metadata. Only populated when the
	// session was recovered after a daemon restart. Nil for sessions that
	// were started normally.
	Recovery *SessionRecoveryInfo `json:"recovery,omitempty"`
}

// SessionRecoveryInfo describes the result of a recovery attempt for a
// session. Surfaced through session/status so operators can distinguish
// healthy sessions from recovered ones.
type SessionRecoveryInfo struct {
	// Recovered indicates whether the session was successfully recovered.
	Recovered bool `json:"recovered"`

	// RecoveredAt is the wall-clock time when recovery completed.
	// Nil if recovery has not completed yet.
	RecoveredAt *time.Time `json:"recoveredAt,omitempty"`

	// Outcome is the recovery result: "recovered", "failed", or "pending".
	Outcome string `json:"outcome"`
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

// =============================================================================
// Room Types
// =============================================================================

// RoomCreateParams is the request params for room/create method.
// It contains the specification for creating a new room.
type RoomCreateParams struct {
	// Name is the unique room name (required).
	Name string `json:"name"`

	// Labels are optional key-value metadata for the room.
	Labels map[string]string `json:"labels,omitempty"`

	// Communication holds optional communication settings.
	Communication *RoomCommunication `json:"communication,omitempty"`
}

// RoomCommunication describes the communication mode for a room.
type RoomCommunication struct {
	// Mode is the communication mode: "mesh", "star", or "isolated".
	// Defaults to "mesh" if empty.
	Mode string `json:"mode,omitempty"`
}

// RoomCreateResult is the response result for room/create method.
// It contains the newly created room metadata.
type RoomCreateResult struct {
	// Name is the room name.
	Name string `json:"name"`

	// CommunicationMode is the resolved communication mode.
	CommunicationMode string `json:"communicationMode"`

	// CreatedAt is the RFC 3339 timestamp when the room was created.
	CreatedAt string `json:"createdAt"`
}

// RoomStatusParams is the request params for room/status method.
// It identifies the room to query status for.
type RoomStatusParams struct {
	// Name is the room name (required).
	Name string `json:"name"`
}

// RoomStatusResult is the response result for room/status method.
// It contains the room metadata and realized member list.
type RoomStatusResult struct {
	// Name is the room name.
	Name string `json:"name"`

	// Labels are the room's key-value metadata.
	Labels map[string]string `json:"labels,omitempty"`

	// CommunicationMode is the room's communication mode.
	CommunicationMode string `json:"communicationMode"`

	// Members is the list of sessions currently associated with the room.
	Members []RoomMember `json:"members"`

	// CreatedAt is the RFC 3339 timestamp when the room was created.
	CreatedAt string `json:"createdAt"`

	// UpdatedAt is the RFC 3339 timestamp when the room was last updated.
	UpdatedAt string `json:"updatedAt"`
}

// RoomMember describes a session that is part of a room.
type RoomMember struct {
	// AgentName is the agent name/ID within the room.
	AgentName string `json:"agentName"`

	// SessionId is the unique session identifier.
	SessionId string `json:"sessionId"`

	// State is the current session state.
	State string `json:"state"`
}

// RoomSendParams is the request params for room/send method.
// It routes a message from one agent to another within a room.
type RoomSendParams struct {
	// Room is the room name (required).
	Room string `json:"room"`

	// TargetAgent is the agent name to deliver the message to (required).
	TargetAgent string `json:"targetAgent"`

	// Message is the text to send (required).
	Message string `json:"message"`

	// SenderAgent is the name of the agent sending the message (optional, for attribution).
	SenderAgent string `json:"senderAgent,omitempty"`

	// SenderId is the session ID of the sender (optional, for tracing).
	SenderId string `json:"senderId,omitempty"`
}

// RoomSendResult is the response result for room/send method.
// It indicates whether the message was delivered.
type RoomSendResult struct {
	// Delivered is true when the prompt was successfully delivered.
	Delivered bool `json:"delivered"`

	// StopReason is the reason the target agent stopped processing.
	StopReason string `json:"stopReason,omitempty"`
}

// RoomDeleteParams is the request params for room/delete method.
// It identifies the room to delete.
type RoomDeleteParams struct {
	// Name is the room name (required).
	Name string `json:"name"`
}

// =============================================================================
// Agent Types
// =============================================================================

// AgentCreateParams is the request params for agent/create method.
// It contains the specification for creating a new agent.
type AgentCreateParams struct {
	// Room is the room this agent belongs to (required).
	Room string `json:"room"`

	// Name is the agent name, unique within the room (required).
	Name string `json:"name"`

	// Description is a human-readable description of the agent (optional).
	Description string `json:"description,omitempty"`

	// RuntimeClass is the runtime class for this agent (required).
	RuntimeClass string `json:"runtimeClass"`

	// WorkspaceId is the workspace this agent uses (required).
	WorkspaceId string `json:"workspaceId"`

	// SystemPrompt is the agent's system prompt (optional).
	SystemPrompt string `json:"systemPrompt,omitempty"`

	// Labels are optional key-value metadata for the agent.
	Labels map[string]string `json:"labels,omitempty"`
}

// AgentCreateResult is the response result for agent/create method.
// It contains the newly created agent identifier and initial state.
type AgentCreateResult struct {
	// AgentId is the unique identifier assigned to this agent.
	AgentId string `json:"agentId"`

	// State is the initial agent state, always "created" on success.
	State string `json:"state"`
}

// AgentPromptParams is the request params for agent/prompt method.
// It contains the prompt text to send to the agent.
type AgentPromptParams struct {
	// AgentId is the unique identifier of the agent to prompt (required).
	AgentId string `json:"agentId"`

	// Prompt is the prompt message to send to the agent (required).
	Prompt string `json:"prompt"`
}

// AgentPromptResult is the response result for agent/prompt method.
// It indicates why the prompt processing stopped.
type AgentPromptResult struct {
	// StopReason is the reason the agent stopped processing.
	// Values: "end_turn" (normal completion), "cancelled" (user cancelled),
	// "tool_use" (agent needs tool execution).
	StopReason string `json:"stopReason"`
}

// AgentCancelParams is the request params for agent/cancel method.
// It identifies the agent to cancel current prompt processing.
type AgentCancelParams struct {
	// AgentId is the unique identifier of the agent to cancel (required).
	AgentId string `json:"agentId"`
}

// AgentStopParams is the request params for agent/stop method.
// It identifies the agent to stop (shuts down the agent process).
type AgentStopParams struct {
	// AgentId is the unique identifier of the agent to stop (required).
	AgentId string `json:"agentId"`
}

// AgentDeleteParams is the request params for agent/delete method.
// It identifies the agent to delete from the registry.
type AgentDeleteParams struct {
	// AgentId is the unique identifier of the agent to delete (required).
	AgentId string `json:"agentId"`
}

// AgentRestartParams is the request params for agent/restart method.
// It identifies the agent to restart.
type AgentRestartParams struct {
	// AgentId is the unique identifier of the agent to restart (required).
	AgentId string `json:"agentId"`
}

// AgentRestartResult is the response for agent/restart.
// The agent transitions to "creating" immediately; poll agent/status
// until state is "created" (or "error") to confirm bootstrap completion.
type AgentRestartResult struct {
	// AgentId is the unique identifier of the restarted agent.
	AgentId string `json:"agentId"`

	// State is the agent state immediately after restart is initiated.
	// Always "creating" on a successful restart request.
	State string `json:"state"`
}

// AgentListParams is the request params for agent/list method.
// It contains optional filters for listing agents.
type AgentListParams struct {
	// Room filters by room name (optional).
	Room string `json:"room,omitempty"`

	// State filters by agent state (optional).
	State string `json:"state,omitempty"`

	// Labels is an optional filter to match agents by labels.
	Labels map[string]string `json:"labels,omitempty"`
}

// AgentListResult is the response result for agent/list method.
// It contains the list of matching agents.
type AgentListResult struct {
	// Agents is the array of agent info objects.
	Agents []AgentInfo `json:"agents"`
}

// AgentInfo describes a single agent in the registry.
// Returned by agent/list and agent/status methods.
type AgentInfo struct {
	// AgentId is the unique agent identifier (UUID).
	AgentId string `json:"agentId"`

	// Room is the room this agent belongs to.
	Room string `json:"room"`

	// Name is the agent name within the room.
	Name string `json:"name"`

	// Description is a human-readable description of the agent.
	Description string `json:"description,omitempty"`

	// RuntimeClass is the runtime class for this agent.
	RuntimeClass string `json:"runtimeClass"`

	// WorkspaceId is the workspace this agent uses.
	WorkspaceId string `json:"workspaceId"`

	// State is the current agent state.
	// Values: "creating", "created", "running", "stopped", "error".
	State string `json:"state"`

	// ErrorMessage is the error message when state is "error".
	ErrorMessage string `json:"errorMessage,omitempty"`

	// Labels are arbitrary key-value metadata for the agent.
	Labels map[string]string `json:"labels,omitempty"`

	// CreatedAt is the timestamp when the agent was created.
	CreatedAt time.Time `json:"createdAt"`

	// UpdatedAt is the timestamp when the agent was last updated.
	UpdatedAt time.Time `json:"updatedAt"`
}

// AgentStatusParams is the request params for agent/status method.
// It identifies the agent to query status for.
type AgentStatusParams struct {
	// AgentId is the unique identifier of the agent to query (required).
	AgentId string `json:"agentId"`
}

// AgentStatusResult is the response result for agent/status method.
// It contains the agent info and optional shim runtime state.
type AgentStatusResult struct {
	// Agent is the agent metadata from the registry.
	Agent AgentInfo `json:"agent"`

	// ShimState is the runtime state of the shim process.
	// Only populated if the agent's linked session is running.
	ShimState *ShimStateInfo `json:"shimState,omitempty"`

	// Recovery holds per-session recovery metadata.
	// Only populated when the session was recovered after a daemon restart.
	Recovery *AgentRecoveryInfo `json:"recovery,omitempty"`
}

// AgentRecoveryInfo describes the result of a recovery attempt for an agent.
// Mirrors SessionRecoveryInfo. Surfaced through agent/status.
type AgentRecoveryInfo struct {
	// Recovered indicates whether the agent's session was successfully recovered.
	Recovered bool `json:"recovered"`

	// RecoveredAt is the wall-clock time when recovery completed.
	// Nil if recovery has not completed yet.
	RecoveredAt *time.Time `json:"recoveredAt,omitempty"`

	// Outcome is the recovery result: "recovered", "failed", or "pending".
	Outcome string `json:"outcome"`
}

// AgentAttachParams is the request params for agent/attach method.
// It identifies the agent to attach to (get shim RPC socket path).
type AgentAttachParams struct {
	// AgentId is the unique identifier of the agent to attach (required).
	AgentId string `json:"agentId"`
}

// AgentAttachResult is the response result for agent/attach method.
// It contains the shim RPC socket path for direct communication.
type AgentAttachResult struct {
	// SocketPath is the Unix domain socket path for the shim RPC server.
	SocketPath string `json:"socketPath"`
}

// AgentDetachParams is the request params for agent/detach method.
// It identifies the agent to detach from.
type AgentDetachParams struct {
	// AgentId is the unique identifier of the agent to detach (required).
	AgentId string `json:"agentId"`
}