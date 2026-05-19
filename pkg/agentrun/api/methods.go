package api

// Run RPC methods (agent-run ↔ mass).
const (
	MethodSessionPrompt     = "session/prompt"
	MethodSessionCancel     = "session/cancel"
	MethodSessionLoad       = "session/load"
	MethodRuntimeWatchEvent = "runtime/watch_event"
	MethodSessionSetModel   = "session/set_model"
	MethodRuntimePhase      = "runtime/status"
	MethodRuntimeStop       = "runtime/stop"

	// Multi-session RPCs — open / end additional ACP sessions on a long-
	// lived agent process so callers can multiplex tasks without
	// fork+exec per task. See pkg/agentrun/runtime/acp/runtime.go's
	// NewSession / EndSession for the underlying runtime contract.
	MethodSessionNew  = "session/new"
	MethodSessionEnd  = "session/end"
	MethodSessionList = "session/list"
)

// Run notification methods.
const (
	// MethodRuntimeEventUpdate is the unified notification method for all runtime events.
	// It replaces the former session/update and runtime/state_change notifications.
	MethodRuntimeEventUpdate = "runtime/event_update"
)
