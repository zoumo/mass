// Package component provides chat UI components replicating charmbracelet/crush's
// internal/ui/chat rendering layer.
//
// Instead of depending on crush's internal/message package, this package defines
// its own Message interface containing only the methods that the rendering code
// actually calls.
package component

import "time"

// MessageRole identifies the role of a message sender.
type MessageRole string

const (
	RoleAssistant MessageRole = "assistant"
	RoleUser      MessageRole = "user"
	RoleSystem    MessageRole = "system"
	RoleTool      MessageRole = "tool"
)

// FinishReason describes why a message generation finished.
type FinishReason string

const (
	FinishReasonEndTurn          FinishReason = "end_turn"
	FinishReasonMaxTokens        FinishReason = "max_tokens"
	FinishReasonToolUse          FinishReason = "tool_use"
	FinishReasonCanceled         FinishReason = "canceled"
	FinishReasonError            FinishReason = "error"
	FinishReasonPermissionDenied FinishReason = "permission_denied"
	FinishReasonUnknown          FinishReason = "unknown"
)

// ContentBlock holds the text content of a message.
type ContentBlock struct {
	Text string
}

// ReasoningBlock holds the thinking/reasoning content of a message.
type ReasoningBlock struct {
	Thinking string
}

// ToolCall represents a tool invocation within a message.
type ToolCall struct {
	ID               string `json:"id"`
	Name             string `json:"name"`
	Input            string `json:"input"`
	ProviderExecuted bool   `json:"provider_executed"`
	Finished         bool   `json:"finished"`
}

// ToolResult represents the result of a tool invocation.
type ToolResult struct {
	ToolCallID string `json:"tool_call_id"`
	Name       string `json:"name"`
	Content    string `json:"content"`
	Data       string `json:"data"`
	MIMEType   string `json:"mime_type"`
	Metadata   string `json:"metadata"`
	IsError    bool   `json:"is_error"`

	// Diff holds structured diff data for file-change tool results.
	// When set, the rendering layer uses DiffView instead of plain text.
	Diff *ToolResultDiff `json:"-"`
}

// ToolResultDiff holds the before/after content for rendering a file diff.
type ToolResultDiff struct {
	Path    string // file path
	OldText string // content before the change (empty for new files)
	NewText string // content after the change
}

// FinishPart contains metadata about why a message finished.
type FinishPart struct {
	Reason  FinishReason
	Time    int64
	Message string
	Details string
}

// Message is the minimal interface required by the chat rendering layer.
// Implementations provide the data for user, assistant, and tool messages.
type Message interface {
	// GetID returns the unique identifier for this message.
	GetID() string
	// GetRole returns the message role.
	GetRole() MessageRole

	// Content returns the text content block.
	Content() ContentBlock
	// ReasoningContent returns the thinking/reasoning block.
	ReasoningContent() ReasoningBlock

	// IsThinking returns true if the message is still in the thinking phase.
	IsThinking() bool
	// IsFinished returns true if the message has a finish part.
	IsFinished() bool
	// FinishReason returns the reason the message finished.
	FinishReason() FinishReason
	// FinishPart returns the finish metadata, or nil if not yet finished.
	FinishPart() *FinishPart

	// ToolCalls returns the tool calls in this message.
	ToolCalls() []ToolCall
	// ToolResults returns the tool results in this message (for tool role).
	ToolResults() []ToolResult

	// ThinkingDuration returns how long the thinking phase lasted.
	ThinkingDuration() time.Duration

	// IsSummaryMessage returns true if this is a summary/condensed message.
	IsSummaryMessage() bool
}
