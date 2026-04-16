package component

import (
	"fmt"
	"image/color"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

// ── ThinkingItem ─────────────────────────────────────────────────────────────

// ThinkingItem renders a standalone thinking/reasoning block with [Think] label.
type ThinkingItem struct {
	cachedMessageItem
	id    string
	text  string
	style lipgloss.Style
}

func NewThinkingItem(id, text string, style lipgloss.Style) *ThinkingItem {
	return &ThinkingItem{id: id, text: text, style: style}
}

func (t *ThinkingItem) ID() string { return t.id }

func (t *ThinkingItem) AppendText(s string) {
	t.text += s
	t.clearCache()
}

func (t *ThinkingItem) RawRender(width int) string { return t.Render(width) }

func (t *ThinkingItem) Render(width int) string {
	w := min(width, maxTextWidth)
	if s, _, ok := t.getCachedRender(w); ok {
		return s
	}
	faint := lipgloss.NewStyle().Faint(true)
	content := RenderBlock(BlockConfig{
		Label: &LabelConfig{
			Text:  "[Think]",
			Style: lipgloss.NewStyle().Faint(true).Bold(true),
		},
		Body:      ansi.Wordwrap(t.text, w-2, " \t"), // -2 for indent
		BodyStyle: &faint,
	})
	h := strings.Count(content, "\n") + 1
	t.setCachedRender(content, w, h)
	return content
}

// ── PlanItem ─────────────────────────────────────────────────────────────────

// PlanItem renders a plan update with [Plan] label and blue border.
type PlanItem struct {
	cachedMessageItem
	id      string
	entries []PlanEntry
	color   color.Color
}

// PlanEntry is a single plan step.
type PlanEntry struct {
	ID     string `json:"id"`
	Title  string `json:"title"`
	Status string `json:"status"`
}

func NewPlanItem(id string, entries []PlanEntry, borderColor color.Color) *PlanItem {
	return &PlanItem{id: id, entries: entries, color: borderColor}
}

func (p *PlanItem) ID() string { return p.id }

func (p *PlanItem) RawRender(width int) string { return p.Render(width) }

func (p *PlanItem) Render(width int) string {
	w := min(width, maxTextWidth)
	if s, _, ok := p.getCachedRender(w); ok {
		return s
	}

	var lines []string
	for _, e := range p.entries {
		icon := "○"
		switch e.Status {
		case "completed", "done":
			icon = "✓"
		case "in_progress", "running":
			icon = "●"
		case "failed", "error":
			icon = "×"
		}
		line := fmt.Sprintf("%s %s", icon, e.Title)
		if e.Status != "" && e.Status != "pending" {
			line += lipgloss.NewStyle().Faint(true).Render(" (" + e.Status + ")")
		}
		lines = append(lines, line)
	}

	body := strings.Join(lines, "\n")
	body = ansi.Wordwrap(body, w-4, " \t") // -4 for border + indent

	content := RenderBlock(BlockConfig{
		Border: &BorderConfig{Char: "▌", Color: p.color},
		Label: &LabelConfig{
			Text:  "[Plan]",
			Style: lipgloss.NewStyle().Bold(true).Foreground(p.color),
		},
		Body: body,
	})
	h := strings.Count(content, "\n") + 1
	p.setCachedRender(content, w, h)
	return content
}

// ── SystemItem ───────────────────────────────────────────────────────────────

// SystemItem renders a system message with faint styling.
type SystemItem struct {
	cachedMessageItem
	id    string
	text  string
	style lipgloss.Style
}

func NewSystemItem(id, text string, style lipgloss.Style) *SystemItem {
	return &SystemItem{id: id, text: text, style: style}
}

func (s *SystemItem) ID() string { return s.id }

func (s *SystemItem) RawRender(width int) string { return s.Render(width) }

func (s *SystemItem) Render(width int) string {
	w := min(width, maxTextWidth)
	if r, _, ok := s.getCachedRender(w); ok {
		return r
	}
	faint := lipgloss.NewStyle().Faint(true)
	content := RenderBlock(BlockConfig{
		Body:      ansi.Wordwrap(s.style.Render(s.text), w-2, " \t"),
		BodyStyle: &faint,
	})
	h := strings.Count(content, "\n") + 1
	s.setCachedRender(content, w, h)
	return content
}
