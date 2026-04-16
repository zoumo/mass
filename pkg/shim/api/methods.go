package api

// Shim RPC methods (agent-shim ↔ mass).
const (
	MethodSessionPrompt    = "session/prompt"
	MethodSessionCancel    = "session/cancel"
	MethodSessionLoad      = "session/load"
	MethodRuntimeWatchEvent = "runtime/watch_event"
	MethodSessionSetModel   = "session/set_model"
	MethodRuntimeStatus     = "runtime/status"
	MethodRuntimeStop       = "runtime/stop"
)

// Shim notification methods.
const (
	// MethodRuntimeEventUpdate is the unified notification method for all runtime events.
	// It replaces the former session/update and runtime/state_change notifications.
	MethodRuntimeEventUpdate = "runtime/event_update"
)
