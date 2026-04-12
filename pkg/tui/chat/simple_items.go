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

// ── ThinkingItem ─────────────────────────────────────────────────────────────

// ThinkingItem renders a thinking/reasoning event with [Think] label and faint text.
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
	label := lipgloss.NewStyle().Faint(true).Bold(true).Render("[Think]")
	body := t.style.Render(t.text)
	body = ansi.Wordwrap(body, w, " \t")
	content := label + "\n" + body
	h := strings.Count(content, "\n") + 1
	t.setCachedRender(content, w, h)
	return content
}

// ── SystemItem ───────────────────────────────────────────────────────────────

// SystemItem renders a system message (connected, error, canceling, etc.)
// with [System] prefix.
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
	label := lipgloss.NewStyle().Faint(true).Bold(true).Render("[System]")
	content := label + " " + s.style.Render(s.text)
	content = ansi.Wordwrap(content, w, " \t")
	h := strings.Count(content, "\n") + 1
	s.setCachedRender(content, w, h)
	return content
}
