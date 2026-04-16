package chat

import (
	"time"

	"github.com/zoumo/mass/pkg/tui/component"
)

// StreamingMessage is a mutable implementation of [component.Message] used by
// the chat TUI. It accumulates streaming content (text, thinking) and tracks
// tool calls / results so that crush-style rendering items can re-render as
// data arrives.
type StreamingMessage struct {
	id   string
	role component.MessageRole

	text     string
	thinking string

	isThinking bool
	finished   bool
	reason     component.FinishReason
	finishMsg  string

	toolCalls   []component.ToolCall
	toolResults []component.ToolResult

	thinkingStart time.Time
	thinkingEnd   time.Time

	summary bool
}

// NewStreamingMessage creates a new empty streaming message.
func NewStreamingMessage(id string, role component.MessageRole) *StreamingMessage {
	return &StreamingMessage{id: id, role: role}
}

// NewFinishedStreamingMessage creates a finished message with text content.
// Used for user messages that arrive fully formed.
func NewFinishedStreamingMessage(id string, role component.MessageRole, text string) *StreamingMessage {
	return &StreamingMessage{id: id, role: role, text: text, finished: true}
}

func (m *StreamingMessage) GetID() string                  { return m.id }
func (m *StreamingMessage) GetRole() component.MessageRole { return m.role }
func (m *StreamingMessage) Content() component.ContentBlock {
	return component.ContentBlock{Text: m.text}
}

func (m *StreamingMessage) ReasoningContent() component.ReasoningBlock {
	return component.ReasoningBlock{Thinking: m.thinking}
}
func (m *StreamingMessage) IsThinking() bool                     { return m.isThinking }
func (m *StreamingMessage) IsFinished() bool                     { return m.finished }
func (m *StreamingMessage) FinishReason() component.FinishReason { return m.reason }
func (m *StreamingMessage) FinishPart() *component.FinishPart {
	if !m.finished {
		return nil
	}
	return &component.FinishPart{
		Reason:  m.reason,
		Time:    time.Now().Unix(),
		Message: m.finishMsg,
	}
}
func (m *StreamingMessage) ToolCalls() []component.ToolCall     { return m.toolCalls }
func (m *StreamingMessage) ToolResults() []component.ToolResult { return m.toolResults }
func (m *StreamingMessage) ThinkingDuration() time.Duration {
	if m.thinkingEnd.IsZero() {
		if m.thinkingStart.IsZero() {
			return 0
		}
		return time.Since(m.thinkingStart)
	}
	return m.thinkingEnd.Sub(m.thinkingStart)
}
func (m *StreamingMessage) IsSummaryMessage() bool { return m.summary }

// AppendText adds streaming text content.
func (m *StreamingMessage) AppendText(s string) {
	m.text += s
	if m.isThinking {
		m.isThinking = false
		m.thinkingEnd = time.Now()
	}
}

// AppendThinking adds streaming thinking content.
func (m *StreamingMessage) AppendThinking(s string) {
	m.thinking += s
	if !m.isThinking {
		m.isThinking = true
		if m.thinkingStart.IsZero() {
			m.thinkingStart = time.Now()
		}
	}
}

// Finish marks the message as finished.
func (m *StreamingMessage) Finish(reason component.FinishReason) {
	m.finished = true
	m.reason = reason
	m.isThinking = false
	if !m.thinkingStart.IsZero() && m.thinkingEnd.IsZero() {
		m.thinkingEnd = time.Now()
	}
}
