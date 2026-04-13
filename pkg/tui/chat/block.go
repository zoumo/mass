package chat

import (
	"image/color"
	"strings"

	"charm.land/lipgloss/v2"
)

// BlockConfig defines the rendering parameters for a unified message block.
// All message types (User, Agent, Tool, Think, System, Plan) use this to
// render with consistent visual structure.
type BlockConfig struct {
	// Border: left border character + color. Nil means no border.
	// When nil and Background is also nil, content is indented 2 chars.
	Border *BorderConfig

	// Label: header line text + style (e.g., "[User]", "✓ [Read] params").
	// Nil means no label line.
	Label *LabelConfig

	// Body: the main content string (already rendered/word-wrapped).
	Body string

	// BodyStyle: if non-nil, applied to each body line (e.g., faint for thinking).
	BodyStyle *lipgloss.Style

	// Detail: secondary content below body (e.g., thinking text, tool output).
	// Separated from body by a blank line. Empty string means no detail.
	Detail string

	// DetailStyle: if non-nil, applied to each detail line.
	DetailStyle *lipgloss.Style

	// Background: if non-nil, each line gets this background color + Width padding.
	Background *color.Color

	// Width: content width for background fill. Required when Background is set.
	Width int
}

// BorderConfig defines the left border appearance.
type BorderConfig struct {
	Char  string      // "▌"
	Color color.Color // border color
}

// LabelConfig defines the label line appearance.
type LabelConfig struct {
	Text  string         // rendered label text, e.g., "[User]" or "✓ [Read] params"
	Style lipgloss.Style // style for the label
}

// RenderBlock renders a message block using the unified block structure.
func RenderBlock(cfg BlockConfig) string {
	// 1. Assemble content lines.
	var sections []string

	if cfg.Label != nil && cfg.Label.Text != "" {
		sections = append(sections, cfg.Label.Style.Render(cfg.Label.Text))
	}

	if cfg.Body != "" {
		body := cfg.Body
		if cfg.BodyStyle != nil {
			body = applyStylePerLine(body, *cfg.BodyStyle)
		}
		sections = append(sections, body)
	}

	if cfg.Detail != "" {
		detail := cfg.Detail
		if cfg.DetailStyle != nil {
			detail = applyStylePerLine(detail, *cfg.DetailStyle)
		}
		if len(sections) > 0 {
			sections = append(sections, "") // blank line separator
		}
		sections = append(sections, detail)
	}

	content := strings.Join(sections, "\n")
	if content == "" {
		return ""
	}

	lines := strings.Split(content, "\n")

	// 2. Apply background (User-style block).
	if cfg.Background != nil && cfg.Width > 0 {
		bgStyle := lipgloss.NewStyle().
			Background(*cfg.Background).
			Width(cfg.Width).
			PaddingLeft(1).PaddingRight(1)
		for i, ln := range lines {
			lines[i] = bgStyle.Render(ln)
		}
		return strings.Join(lines, "\n")
	}

	// 3. Apply border or indent.
	if cfg.Border != nil {
		border := lipgloss.NewStyle().Foreground(cfg.Border.Color).Render(cfg.Border.Char)
		for i, ln := range lines {
			lines[i] = border + " " + ln
		}
	} else {
		// No border, no background: indent 2 chars to align with bordered content.
		for i, ln := range lines {
			lines[i] = "  " + ln
		}
	}

	return strings.Join(lines, "\n")
}

// applyStylePerLine applies a lipgloss style to each line individually.
func applyStylePerLine(s string, style lipgloss.Style) string {
	lines := strings.Split(s, "\n")
	for i, ln := range lines {
		lines[i] = style.Render(ln)
	}
	return strings.Join(lines, "\n")
}
