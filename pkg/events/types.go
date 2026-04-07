package events

import acp "github.com/coder/acp-go-sdk"

// Event is a sealed interface for all typed events produced by the Translator.
// The unexported discriminator method prevents external implementations.
type Event interface {
	eventType() string
}

// TextEvent carries a streamed text chunk from the agent.
type TextEvent struct {
	Text string `json:"text"`
}

func (TextEvent) eventType() string { return "text" }

// ThinkingEvent carries a streamed thinking/reasoning chunk from the agent.
type ThinkingEvent struct {
	Text string `json:"text"`
}

func (ThinkingEvent) eventType() string { return "thinking" }

// ToolCallEvent signals that the agent invoked a tool.
type ToolCallEvent struct {
	ID    string `json:"id"`
	Kind  string `json:"kind"`
	Title string `json:"title"`
}

func (ToolCallEvent) eventType() string { return "tool_call" }

// ToolResultEvent carries the outcome of a tool invocation.
type ToolResultEvent struct {
	ID     string `json:"id"`
	Status string `json:"status"`
}

func (ToolResultEvent) eventType() string { return "tool_result" }

// FileWriteEvent reports a file-write side-channel event from the ACP client.
type FileWriteEvent struct {
	Path    string `json:"path"`
	Allowed bool   `json:"allowed"`
}

func (FileWriteEvent) eventType() string { return "file_write" }

// FileReadEvent reports a file-read side-channel event from the ACP client.
type FileReadEvent struct {
	Path    string `json:"path"`
	Allowed bool   `json:"allowed"`
}

func (FileReadEvent) eventType() string { return "file_read" }

// CommandEvent reports a shell-command side-channel event from the ACP client.
type CommandEvent struct {
	Command string `json:"command"`
	Allowed bool   `json:"allowed"`
}

func (CommandEvent) eventType() string { return "command" }

// PlanEvent carries an updated plan from the agent session.
type PlanEvent struct {
	Entries []acp.PlanEntry `json:"entries"`
}

func (PlanEvent) eventType() string { return "plan" }

// UserMessageEvent carries a streamed text chunk echoed from the user's prompt.
// ACP agents echo the incoming prompt back as UserMessageChunk notifications.
type UserMessageEvent struct {
	Text string `json:"text"`
}

func (UserMessageEvent) eventType() string { return "user_message" }

// TurnStartEvent signals the start of an agent turn.
type TurnStartEvent struct{}

func (TurnStartEvent) eventType() string { return "turn_start" }

// TurnEndEvent signals the end of an agent turn with a stop reason.
type TurnEndEvent struct {
	StopReason string `json:"stopReason"`
}

func (TurnEndEvent) eventType() string { return "turn_end" }

// ErrorEvent is emitted when an unknown or malformed event variant is encountered.
type ErrorEvent struct {
	Msg string `json:"message"`
}

func (ErrorEvent) eventType() string { return "error" }
