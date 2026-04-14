package api

// EventType* and Category* constants — moved from github.com/zoumo/oar/api.

// Event type identifiers used in shim/event payloads.
const (
	EventTypeText        = "text"
	EventTypeThinking    = "thinking"
	EventTypeToolCall    = "tool_call"
	EventTypeToolResult  = "tool_result"
	EventTypePlan        = "plan"
	EventTypeUserMessage = "user_message"
	EventTypeTurnStart   = "turn_start"
	EventTypeTurnEnd     = "turn_end"
	EventTypeError       = "error"

	// New event types mirroring previously discarded ACP SessionUpdate branches.
	EventTypeAvailableCommands = "available_commands"
	EventTypeCurrentMode       = "current_mode"
	EventTypeConfigOption      = "config_option"
	EventTypeSessionInfo       = "session_info"
	EventTypeUsage             = "usage"

	// EventTypeStateChange is a runtime category event for process lifecycle transitions.
	EventTypeStateChange = "state_change"
)

// Event category identifiers for ShimEvent.Category.
const (
	// CategorySession covers all ACP SessionUpdate translated events.
	CategorySession = "session"
	// CategoryRuntime covers runtime/process lifecycle events (state_change).
	CategoryRuntime = "runtime"
)
