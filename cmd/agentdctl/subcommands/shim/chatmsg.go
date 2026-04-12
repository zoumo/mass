package shim

import (
	"time"

	"github.com/open-agent-d/open-agent-d/pkg/tui/chat"
)

// shimMessage is a mutable implementation of [chat.Message] used by the shim
// chat TUI. It accumulates streaming content (text, thinking) and tracks
// tool calls / results so that crush-style rendering items can re-render as
// data arrives.
type shimMessage struct {
	id   string
	role chat.MessageRole

	text     string
	thinking string

	isThinking bool
	finished   bool
	reason     chat.FinishReason
	finishMsg  string

	toolCalls   []chat.ToolCall
	toolResults []chat.ToolResult

	thinkingStart time.Time
	thinkingEnd   time.Time

	summary bool
}

func newShimMessage(id string, role chat.MessageRole) *shimMessage {
	return &shimMessage{id: id, role: role}
}

func (m *shimMessage) GetID() string           { return m.id }
func (m *shimMessage) GetRole() chat.MessageRole { return m.role }
func (m *shimMessage) Content() chat.ContentBlock {
	return chat.ContentBlock{Text: m.text}
}
func (m *shimMessage) ReasoningContent() chat.ReasoningBlock {
	return chat.ReasoningBlock{Thinking: m.thinking}
}
func (m *shimMessage) IsThinking() bool         { return m.isThinking }
func (m *shimMessage) IsFinished() bool         { return m.finished }
func (m *shimMessage) FinishReason() chat.FinishReason { return m.reason }
func (m *shimMessage) FinishPart() *chat.FinishPart {
	if !m.finished {
		return nil
	}
	return &chat.FinishPart{
		Reason:  m.reason,
		Time:    time.Now().Unix(),
		Message: m.finishMsg,
	}
}
func (m *shimMessage) ToolCalls() []chat.ToolCall     { return m.toolCalls }
func (m *shimMessage) ToolResults() []chat.ToolResult { return m.toolResults }
func (m *shimMessage) ThinkingDuration() time.Duration {
	if m.thinkingEnd.IsZero() {
		if m.thinkingStart.IsZero() {
			return 0
		}
		return time.Since(m.thinkingStart)
	}
	return m.thinkingEnd.Sub(m.thinkingStart)
}
func (m *shimMessage) IsSummaryMessage() bool { return m.summary }

// appendText adds streaming text content.
func (m *shimMessage) appendText(s string) {
	m.text += s
	if m.isThinking {
		m.isThinking = false
		m.thinkingEnd = time.Now()
	}
}

// appendThinking adds streaming thinking content.
func (m *shimMessage) appendThinking(s string) {
	m.thinking += s
	if !m.isThinking {
		m.isThinking = true
		if m.thinkingStart.IsZero() {
			m.thinkingStart = time.Now()
		}
	}
}

// finish marks the message as finished.
func (m *shimMessage) finish(reason chat.FinishReason) {
	m.finished = true
	m.reason = reason
	m.isThinking = false
	if !m.thinkingStart.IsZero() && m.thinkingEnd.IsZero() {
		m.thinkingEnd = time.Now()
	}
}
