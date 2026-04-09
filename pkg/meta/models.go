// Package meta provides metadata storage for OAR session/workspace/room records.
// It uses SQLite for persistence with transaction support.
package meta

import (
	"encoding/json"
	"time"
)

// AgentState defines the possible states of an agent.
type AgentState string

const (
	// AgentStateCreating indicates the agent is being provisioned.
	AgentStateCreating AgentState = "creating"

	// AgentStateCreated indicates the agent has been provisioned but not yet started.
	AgentStateCreated AgentState = "created"

	// AgentStateRunning indicates the agent is actively running.
	AgentStateRunning AgentState = "running"

	// AgentStateStopped indicates the agent has been stopped (terminal state).
	AgentStateStopped AgentState = "stopped"

	// AgentStateError indicates the agent encountered an unrecoverable error.
	AgentStateError AgentState = "error"
)

// Agent represents an agent definition record.
// An agent is a named, room-scoped entity that can be instantiated as sessions.
type Agent struct {
	// ID is the unique agent identifier (UUID).
	ID string `json:"id"`

	// Room is the room this agent belongs to.
	Room string `json:"room"`

	// Name is the agent name, unique within the room.
	Name string `json:"name"`

	// RuntimeClass is the runtime class for this agent (e.g., "default", "cuda").
	RuntimeClass string `json:"runtimeClass"`

	// WorkspaceID is the workspace this agent uses.
	WorkspaceID string `json:"workspaceId"`

	// Description is a human-readable description of the agent.
	Description string `json:"description,omitempty"`

	// SystemPrompt is the agent's system prompt.
	SystemPrompt string `json:"systemPrompt,omitempty"`

	// Labels are arbitrary key-value metadata for the agent.
	// Stored as JSON in the database.
	Labels map[string]string `json:"labels,omitempty"`

	// State is the current agent state.
	State AgentState `json:"state"`

	// ErrorMessage is the error message if state is AgentStateError.
	ErrorMessage string `json:"errorMessage,omitempty"`

	// CreatedAt is the timestamp when the agent was created.
	CreatedAt time.Time `json:"createdAt"`

	// UpdatedAt is the timestamp when the agent was last updated.
	UpdatedAt time.Time `json:"updatedAt"`
}

// emptyJSON is the default empty JSON object used for bootstrap_config.
var emptyJSON = json.RawMessage("{}")

// SessionState defines the possible states of a session.
type SessionState string

const (
	// SessionStateCreating indicates a session that is being provisioned.
	SessionStateCreating SessionState = "creating"

	// SessionStateCreated indicates a newly created session that has not started.
	SessionStateCreated SessionState = "created"

	// SessionStateRunning indicates an actively running session.
	SessionStateRunning SessionState = "running"

	// SessionStateStopped indicates a stopped session.
	SessionStateStopped SessionState = "stopped"

	// SessionStateError indicates a session that encountered an unrecoverable error (terminal).
	SessionStateError SessionState = "error"
)

// WorkspaceStatus defines the possible statuses of a workspace.
type WorkspaceStatus string

const (
	// WorkspaceStatusActive indicates an active workspace.
	WorkspaceStatusActive WorkspaceStatus = "active"

	// WorkspaceStatusInactive indicates an inactive workspace.
	WorkspaceStatusInactive WorkspaceStatus = "inactive"

	// WorkspaceStatusDeleted indicates a deleted workspace.
	WorkspaceStatusDeleted WorkspaceStatus = "deleted"
)

// CommunicationMode defines the communication mode for a room.
type CommunicationMode string

const (
	// CommunicationModeMesh indicates mesh mode (all agents see all messages).
	CommunicationModeMesh CommunicationMode = "mesh"

	// CommunicationModeStar indicates star mode (agents communicate through a central coordinator).
	CommunicationModeStar CommunicationMode = "star"

	// CommunicationModeIsolated indicates isolated mode (agents have no inter-agent communication).
	CommunicationModeIsolated CommunicationMode = "isolated"
)

// Session represents an agent runtime session record.
// A session is created when an agent starts and tracks its state throughout
// its lifetime.
type Session struct {
	// ID is the unique session identifier (UUID).
	ID string `json:"id"`

	// RuntimeClass is the runtime class for this session (e.g., "default", "cuda").
	RuntimeClass string `json:"runtimeClass"`

	// WorkspaceID is the workspace this session uses.
	WorkspaceID string `json:"workspaceId"`

	// Room is the room name if this session is part of a multi-agent room.
	// Empty string means no room association.
	Room string `json:"room,omitempty"`

	// RoomAgent is the agent name/ID within the room.
	// Empty string means no room agent association.
	RoomAgent string `json:"roomAgent,omitempty"`

	// AgentID is the optional reference to the parent agent.
	// Empty string means no agent association (legacy sessions).
	AgentID string `json:"agentId,omitempty"`

	// Labels are arbitrary key-value metadata for the session.
	// Stored as JSON in the database.
	Labels map[string]string `json:"labels,omitempty"`

	// State is the current session state.
	State SessionState `json:"state"`

	// BootstrapConfig is the JSON-serialized config used to start this session.
	// Stored as a JSON blob so the schema stays stable as config fields evolve.
	// Empty/nil means no bootstrap config recorded yet.
	BootstrapConfig json.RawMessage `json:"bootstrapConfig,omitempty"`

	// ShimSocketPath is the Unix socket path for the shim's RPC endpoint.
	// Used during recovery to reconnect to a still-alive shim.
	ShimSocketPath string `json:"shimSocketPath,omitempty"`

	// ShimStateDir is the absolute path to the shim's state directory.
	// Contains the event log and other shim-local state.
	ShimStateDir string `json:"shimStateDir,omitempty"`

	// ShimPID is the OS process ID of the shim process.
	// Used during recovery to check if the shim is still alive.
	ShimPID int `json:"shimPid,omitempty"`

	// CreatedAt is the timestamp when the session was created.
	CreatedAt time.Time `json:"createdAt"`

	// UpdatedAt is the timestamp when the session was last updated.
	UpdatedAt time.Time `json:"updatedAt"`
}

// Workspace represents a workspace preparation record.
// A workspace is created from a workspace spec (git clone, empty dir, or local).
type Workspace struct {
	// ID is the unique workspace identifier (UUID).
	ID string `json:"id"`

	// Name is the workspace name (from workspace spec metadata).
	Name string `json:"name"`

	// Path is the filesystem path to the workspace directory.
	Path string `json:"path"`

	// Source is the source specification (git/emptyDir/local).
	// Stored as JSON in the database.
	Source json.RawMessage `json:"source"`

	// Status is the current workspace status.
	Status WorkspaceStatus `json:"status"`

	// RefCount is the number of sessions using this workspace.
	RefCount int `json:"refCount"`

	// CreatedAt is the timestamp when the workspace was created.
	CreatedAt time.Time `json:"createdAt"`

	// UpdatedAt is the timestamp when the workspace was last updated.
	UpdatedAt time.Time `json:"updatedAt"`
}

// Room represents a communication room for multi-agent coordination.
// Agents in the same room can coordinate through shared communication.
type Room struct {
	// Name is the unique room name (primary key).
	Name string `json:"name"`

	// Labels are arbitrary key-value metadata for the room.
	// Stored as JSON in the database.
	Labels map[string]string `json:"labels,omitempty"`

	// CommunicationMode is how agents in the room communicate.
	CommunicationMode CommunicationMode `json:"communicationMode"`

	// CreatedAt is the timestamp when the room was created.
	CreatedAt time.Time `json:"createdAt"`

	// UpdatedAt is the timestamp when the room was last updated.
	UpdatedAt time.Time `json:"updatedAt"`
}

// WorkspaceRef represents a reference from a session to a workspace.
// Used for tracking workspace ref counts.
type WorkspaceRef struct {
	// ID is the auto-increment primary key.
	ID int64 `json:"id"`

	// WorkspaceID is the workspace being referenced.
	WorkspaceID string `json:"workspaceId"`

	// SessionID is the session holding the reference.
	SessionID string `json:"sessionId"`

	// CreatedAt is the timestamp when the reference was created.
	CreatedAt time.Time `json:"createdAt"`
}

// labelsToJSON converts a labels map to JSON bytes.
// Returns an empty JSON object '{}' if labels is nil or empty.
func labelsToJSON(labels map[string]string) []byte {
	if labels == nil || len(labels) == 0 {
		return []byte("{}")
	}
	data, _ := json.Marshal(labels)
	return data
}

// labelsFromJSON parses JSON bytes into a labels map.
// Returns nil if the JSON is empty or invalid.
func labelsFromJSON(data []byte) map[string]string {
	if len(data) == 0 || string(data) == "{}" {
		return nil
	}
	var labels map[string]string
	if err := json.Unmarshal(data, &labels); err != nil {
		return nil
	}
	return labels
}
