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
