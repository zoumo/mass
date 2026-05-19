package api

// ARI system methods.
const (
	MethodSystemInfo = "system/info"
)

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
	MethodAgentRunCreate    = "agentrun/create"
	MethodAgentRunPrompt    = "agentrun/prompt"
	MethodAgentRunCancel    = "agentrun/cancel"
	MethodAgentRunStop      = "agentrun/stop"
	MethodAgentRunDelete    = "agentrun/delete"
	MethodAgentRunRestart   = "agentrun/restart"
	MethodAgentRunList      = "agentrun/list"
	MethodAgentRunGet       = "agentrun/get"
	MethodAgentRunTaskDo    = "agentrun/task/do"
	MethodAgentRunTaskGet   = "agentrun/task/get"
	MethodAgentRunTaskList  = "agentrun/task/list"
	MethodAgentRunTaskRetry = "agentrun/task/retry"

	// Multi-session lifecycle for a single agentrun. session/new opens
	// an additional ACP session (different cwd, fresh state) without
	// fork+exec a new agent process; session/end releases runtime
	// tracking; session/list enumerates active sessions. Names parallel
	// agentrun/task/* — one resource/verb hierarchy at this layer. See
	// pkg/agentrun/runtime/acp Manager.NewSession for runtime contract.
	MethodAgentRunNewSession   = "agentrun/session/new"
	MethodAgentRunEndSession   = "agentrun/session/end"
	MethodAgentRunListSessions = "agentrun/session/list"
)

// ARI agent definition methods.
const (
	MethodAgentCreate = "agent/create"
	MethodAgentUpdate = "agent/update"
	MethodAgentGet    = "agent/get"
	MethodAgentList   = "agent/list"
	MethodAgentDelete = "agent/delete"
)
