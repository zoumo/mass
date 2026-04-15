package api

// ARI workspace methods (orchestrator ↔ mass).
const (
	MethodWorkspaceCreate = "workspace/create"
	MethodWorkspaceGet    = "workspace/get"
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
	MethodAgentRunGet     = "agentrun/get"
)

// ARI agent definition methods.
const (
	MethodAgentCreate = "agent/create"
	MethodAgentUpdate = "agent/update"
	MethodAgentGet    = "agent/get"
	MethodAgentList   = "agent/list"
	MethodAgentDelete = "agent/delete"
)
