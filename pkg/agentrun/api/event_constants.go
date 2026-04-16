package api

// EventType* constants — moved from github.com/zoumo/mass/api.

// Event type identifiers used in runtime/event_update payloads.
const (
	EventTypeAgentMessage  = "agent_message"
	EventTypeAgentThinking = "agent_thinking"
	EventTypeToolCall    = "tool_call"
	EventTypeToolResult  = "tool_result"
	EventTypePlan        = "plan"
	EventTypeUserMessage = "user_message"
	EventTypeTurnStart   = "turn_start"
	EventTypeTurnEnd     = "turn_end"
	EventTypeError       = "error"

	// EventTypeRuntimeUpdate is the merged event type for runtime status changes
	// and session metadata updates (replaces state_change, available_commands,
	// current_mode, config_option, session_info, usage).
	EventTypeRuntimeUpdate = "runtime_update"
)

// Content block streaming status values.
const (
	BlockStatusStart     = "start"
	BlockStatusStreaming  = "streaming"
	BlockStatusEnd       = "end"
)
