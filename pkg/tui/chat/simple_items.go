package chat

// This file provides simple message item types for quick integration.
// These are the original lightweight items used by the shim before
// the full crush-style rendering layer was added. They implement
// MessageItem (list.Item + list.RawRenderable + Identifiable).

import (
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

// ── UserItem ─────────────────────────────────────────────────────────────────

// UserItem renders a user message: "You: <text>".
type UserItem struct {
	cachedMessageItem
	id    string
	text  string
	style lipgloss.Style
}

// NewUserItem creates a new user message item.
func NewUserItem(id, text string, style lipgloss.Style) *UserItem {
	return &UserItem{id: id, text: text, style: style}
}

func (u *UserItem) ID() string { return u.id }

func (u *UserItem) RawRender(width int) string {
	return u.Render(width)
}

func (u *UserItem) Render(width int) string {
	w := min(width, maxTextWidth)
	if s, _, ok := u.getCachedRender(w); ok {
		return s
	}
	prefix := u.style.Render("You: ")
	content := ansi.Wordwrap(prefix+u.text, w, " \t")
	h := strings.Count(content, "\n") + 1
	u.setCachedRender(content, w, h)
	return content
}

// ── AssistantItem ────────────────────────────────────────────────────────────

// AssistantItem renders a streaming agent response. Use [AppendText] to
// accumulate streamed tokens.
type AssistantItem struct {
	cachedMessageItem
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
	a.clearCache()
}

// Text returns the accumulated text.
func (a *AssistantItem) Text() string { return a.text }

func (a *AssistantItem) RawRender(width int) string {
	return a.Render(width)
}

func (a *AssistantItem) Render(width int) string {
	w := min(width, maxTextWidth)
	if s, _, ok := a.getCachedRender(w); ok {
		return s
	}
	prefix := a.style.Render("Agent: ")
	content := ansi.Wordwrap(prefix+a.text, w, " \t")
	h := strings.Count(content, "\n") + 1
	a.setCachedRender(content, w, h)
	return content
}

// ── ThinkingItem ─────────────────────────────────────────────────────────────

// ThinkingItem renders a thinking/reasoning event: "  . <text>".
type ThinkingItem struct {
	cachedMessageItem
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
	t.clearCache()
}

func (t *ThinkingItem) RawRender(width int) string {
	return t.Render(width)
}

func (t *ThinkingItem) Render(width int) string {
	w := min(width, maxTextWidth)
	if s, _, ok := t.getCachedRender(w); ok {
		return s
	}
	content := t.style.Render("  . " + t.text)
	content = ansi.Wordwrap(content, w, " \t")
	h := strings.Count(content, "\n") + 1
	t.setCachedRender(content, w, h)
	return content
}

// ── ToolCallItem ─────────────────────────────────────────────────────────────

// ToolCallItem renders a tool invocation: "  tool <kind>: <title>".
type ToolCallItem struct {
	cachedMessageItem
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

func (tc *ToolCallItem) RawRender(width int) string {
	return tc.Render(width)
}

func (tc *ToolCallItem) Render(width int) string {
	w := min(width, maxTextWidth)
	if s, _, ok := tc.getCachedRender(w); ok {
		return s
	}
	text := tc.kind
	if tc.title != "" {
		text += ": " + tc.title
	}
	content := tc.style.Render("  tool " + text)
	content = ansi.Wordwrap(content, w, " \t")
	h := strings.Count(content, "\n") + 1
	tc.setCachedRender(content, w, h)
	return content
}

// ── ToolResultItem ───────────────────────────────────────────────────────────

// ToolResultItem renders a tool result: "  > <status>".
type ToolResultItem struct {
	cachedMessageItem
	id     string
	status string
	style  lipgloss.Style
}

// NewToolResultItem creates a new tool result message item.
func NewToolResultItem(id, status string, style lipgloss.Style) *ToolResultItem {
	return &ToolResultItem{id: id, status: status, style: style}
}

func (tr *ToolResultItem) ID() string { return tr.id }

func (tr *ToolResultItem) RawRender(width int) string {
	return tr.Render(width)
}

func (tr *ToolResultItem) Render(width int) string {
	w := min(width, maxTextWidth)
	if s, _, ok := tr.getCachedRender(w); ok {
		return s
	}
	content := tr.style.Render("  > " + tr.status)
	content = ansi.Wordwrap(content, w, " \t")
	h := strings.Count(content, "\n") + 1
	tr.setCachedRender(content, w, h)
	return content
}

// ── SystemItem ───────────────────────────────────────────────────────────────

// SystemItem renders a system message (connected, error, canceling, etc.).
type SystemItem struct {
	cachedMessageItem
	id    string
	text  string
	style lipgloss.Style
}

// NewSystemItem creates a new system message item.
func NewSystemItem(id, text string, style lipgloss.Style) *SystemItem {
	return &SystemItem{id: id, text: text, style: style}
}

func (s *SystemItem) ID() string { return s.id }

func (s *SystemItem) RawRender(width int) string {
	return s.Render(width)
}

func (s *SystemItem) Render(width int) string {
	w := min(width, maxTextWidth)
	if r, _, ok := s.getCachedRender(w); ok {
		return r
	}
	content := s.style.Render(s.text)
	content = ansi.Wordwrap(content, w, " \t")
	h := strings.Count(content, "\n") + 1
	s.setCachedRender(content, w, h)
	return content
}
