package chat

import (
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

// ── UserItem ─────────────────────────────────────────────────────────────────

// UserItem renders a user message: "You: <text>".
type UserItem struct {
	cachedItem
	id    string
	text  string
	style lipgloss.Style
}

// NewUserItem creates a new user message item.
func NewUserItem(id, text string, style lipgloss.Style) *UserItem {
	return &UserItem{id: id, text: text, style: style}
}

func (u *UserItem) ID() string { return u.id }

func (u *UserItem) Render(width int) string {
	w := cappedWidth(width)
	if s, _, ok := u.get(w); ok {
		return s
	}
	prefix := u.style.Render("You: ")
	content := ansi.Wordwrap(prefix+u.text, w, " \t")
	h := strings.Count(content, "\n") + 1
	u.set(content, w, h)
	return content
}

// ── AssistantItem ────────────────────────────────────────────────────────────

// AssistantItem renders a streaming agent response. Use [AppendText] to
// accumulate streamed tokens.
type AssistantItem struct {
	cachedItem
	id    string
	text  string
	style lipgloss.Style
}

// NewAssistantItem creates a new assistant message item.
func NewAssistantItem(id string, style lipgloss.Style) *AssistantItem {
	return &AssistantItem{id: id, style: style}
}

func (a *AssistantItem) ID() string { return a.id }

// AppendText appends streamed text and invalidates the render cache.
func (a *AssistantItem) AppendText(s string) {
	a.text += s
	a.clear()
}

// Text returns the accumulated text.
func (a *AssistantItem) Text() string { return a.text }

func (a *AssistantItem) Render(width int) string {
	w := cappedWidth(width)
	if s, _, ok := a.get(w); ok {
		return s
	}
	prefix := a.style.Render("Agent: ")
	content := ansi.Wordwrap(prefix+a.text, w, " \t")
	h := strings.Count(content, "\n") + 1
	a.set(content, w, h)
	return content
}

// ── ThinkingItem ─────────────────────────────────────────────────────────────

// ThinkingItem renders a thinking/reasoning event: "  · <text>".
type ThinkingItem struct {
	cachedItem
	id    string
	text  string
	style lipgloss.Style
}

// NewThinkingItem creates a new thinking message item.
func NewThinkingItem(id, text string, style lipgloss.Style) *ThinkingItem {
	return &ThinkingItem{id: id, text: text, style: style}
}

func (t *ThinkingItem) ID() string { return t.id }

// AppendText appends streamed thinking text and invalidates the render cache.
func (t *ThinkingItem) AppendText(s string) {
	t.text += s
	t.clear()
}

func (t *ThinkingItem) Render(width int) string {
	w := cappedWidth(width)
	if s, _, ok := t.get(w); ok {
		return s
	}
	content := t.style.Render("  · " + t.text)
	content = ansi.Wordwrap(content, w, " \t")
	h := strings.Count(content, "\n") + 1
	t.set(content, w, h)
	return content
}

// ── ToolCallItem ─────────────────────────────────────────────────────────────

// ToolCallItem renders a tool invocation: "  ⚙ <kind>: <title>".
type ToolCallItem struct {
	cachedItem
	id    string
	kind  string
	title string
	style lipgloss.Style
}

// NewToolCallItem creates a new tool call message item.
func NewToolCallItem(id, kind, title string, style lipgloss.Style) *ToolCallItem {
	return &ToolCallItem{id: id, kind: kind, title: title, style: style}
}

func (tc *ToolCallItem) ID() string { return tc.id }

func (tc *ToolCallItem) Render(width int) string {
	w := cappedWidth(width)
	if s, _, ok := tc.get(w); ok {
		return s
	}
	text := tc.kind
	if tc.title != "" {
		text += ": " + tc.title
	}
	content := tc.style.Render("  ⚙ " + text)
	content = ansi.Wordwrap(content, w, " \t")
	h := strings.Count(content, "\n") + 1
	tc.set(content, w, h)
	return content
}

// ── ToolResultItem ───────────────────────────────────────────────────────────

// ToolResultItem renders a tool result: "  ↳ <status>".
type ToolResultItem struct {
	cachedItem
	id     string
	status string
	style  lipgloss.Style
}

// NewToolResultItem creates a new tool result message item.
func NewToolResultItem(id, status string, style lipgloss.Style) *ToolResultItem {
	return &ToolResultItem{id: id, status: status, style: style}
}

func (tr *ToolResultItem) ID() string { return tr.id }

func (tr *ToolResultItem) Render(width int) string {
	w := cappedWidth(width)
	if s, _, ok := tr.get(w); ok {
		return s
	}
	content := tr.style.Render("  ↳ " + tr.status)
	content = ansi.Wordwrap(content, w, " \t")
	h := strings.Count(content, "\n") + 1
	tr.set(content, w, h)
	return content
}

// ── SystemItem ───────────────────────────────────────────────────────────────

// SystemItem renders a system message (connected, error, cancelling, etc.).
type SystemItem struct {
	cachedItem
	id    string
	text  string
	style lipgloss.Style
}

// NewSystemItem creates a new system message item.
func NewSystemItem(id, text string, style lipgloss.Style) *SystemItem {
	return &SystemItem{id: id, text: text, style: style}
}

func (s *SystemItem) ID() string { return s.id }

func (s *SystemItem) Render(width int) string {
	w := cappedWidth(width)
	if r, _, ok := s.get(w); ok {
		return r
	}
	content := s.style.Render(s.text)
	content = ansi.Wordwrap(content, w, " \t")
	h := strings.Count(content, "\n") + 1
	s.set(content, w, h)
	return content
}
