package api

// Shim RPC methods (agent-shim ↔ agentd).
const (
	MethodSessionPrompt    = "session/prompt"
	MethodSessionCancel    = "session/cancel"
	MethodSessionLoad      = "session/load"
	MethodSessionSubscribe = "session/subscribe"
	MethodRuntimeStatus    = "runtime/status"
	MethodRuntimeHistory   = "runtime/history"
	MethodRuntimeStop      = "runtime/stop"
)

// Shim notification methods.
const (
	// MethodShimEvent is the unified notification method for all shim events.
	// It replaces the former session/update and runtime/state_change notifications.
	MethodShimEvent = "shim/event"
)

// ARI workspace methods (orchestrator ↔ agentd).
const (
	MethodWorkspaceCreate = "workspace/create"
	MethodWorkspaceStatus = "workspace/status"
	MethodWorkspaceList   = "workspace/list"
	MethodWorkspaceDelete = "workspace/delete"
	MethodWorkspaceSend   = "workspace/send"
)

// ARI agentrun methods.
const (
	MethodAgentRunCreate  = "agentrun/create"
	MethodAgentRunPrompt  = "agentrun/prompt"
	MethodAgentRunCancel  = "agentrun/cancel"
	MethodAgentRunStop    = "agentrun/stop"
	MethodAgentRunDelete  = "agentrun/delete"
	MethodAgentRunRestart = "agentrun/restart"
	MethodAgentRunList    = "agentrun/list"
	MethodAgentRunStatus  = "agentrun/status"
	MethodAgentRunAttach  = "agentrun/attach"
)

// ARI agent definition methods.
const (
	MethodAgentSet    = "agent/set"
	MethodAgentGet    = "agent/get"
	MethodAgentList   = "agent/list"
	MethodAgentDelete = "agent/delete"
)
