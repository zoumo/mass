package chat

import (
	"encoding/json"
	"fmt"
	"strings"

	shimapi "github.com/zoumo/mass/pkg/shim/api"
	"github.com/zoumo/mass/pkg/tui/component"
)

// DeriveToolKind returns a display name (kind) for the tool.
// Falls back to title or "tool" when kind is empty (e.g., Codex MCP tools).
func DeriveToolKind(kind, title string) string {
	if kind != "" {
		return kind
	}
	// Codex/MCP tools have title like "Tool: server/tool_name".
	if after, ok := strings.CutPrefix(title, "Tool: "); ok {
		return after
	}
	if title != "" {
		return title
	}
	return "tool"
}

// ToolDisplayTitle returns the title to show in tool params.
// When kind is empty and title was used as the kind, returns empty to avoid duplication.
func ToolDisplayTitle(kind, title string) string {
	if kind != "" {
		return title
	}
	// Title was consumed by DeriveToolKind — don't duplicate.
	return ""
}

// BuildInput creates a JSON string for the tool's Input field,
// extracting title and locations for display.
func BuildInput(title string, locations []shimapi.ToolCallLocation) string {
	params := make(map[string]any)
	if title != "" {
		params["title"] = title
	}
	if len(locations) > 0 {
		var paths []string
		for _, loc := range locations {
			s := loc.Path
			if loc.Line != nil {
				s += fmt.Sprintf(":%d", *loc.Line)
			}
			paths = append(paths, s)
		}
		if len(paths) == 1 {
			params["path"] = paths[0]
		} else {
			params["paths"] = paths
		}
	}
	if len(params) == 0 {
		return "{}"
	}
	b, _ := json.Marshal(params)
	return string(b)
}

// BuildResultContent extracts a displayable string from tool result content blocks.
// Diff blocks are skipped here — they are handled separately by ExtractDiff
// for structured DiffView rendering. Text and terminal blocks are extracted
// as plain strings. RawOutput is used as a fallback.
func BuildResultContent(blocks []shimapi.ToolCallContent, status string, rawOutput any) string {
	var parts []string

	for _, block := range blocks {
		switch {
		case block.Content != nil:
			if block.Content.Content.Text != nil && block.Content.Content.Text.Text != "" {
				parts = append(parts, block.Content.Content.Text.Text)
			}
		case block.Diff != nil:
			// Diff blocks are rendered via DiffView (see ExtractDiff).
			// Include a minimal fallback for Content string only.
			if block.Diff.Path != "" {
				parts = append(parts, "diff: "+block.Diff.Path)
			}
		case block.Terminal != nil:
			if block.Terminal.TerminalID != "" {
				parts = append(parts, "terminal: "+block.Terminal.TerminalID)
			}
		}
	}

	if len(parts) > 0 {
		return strings.Join(parts, "\n")
	}

	// Fallback: use rawOutput if no structured content.
	if rawOutput != nil {
		if s := FormatRawOutput(rawOutput); s != "" {
			return s
		}
	}

	return ""
}

// ExtractDiff finds the first diff block from content and returns structured data
// for DiffView rendering. Returns nil if no diff block is present.
func ExtractDiff(blocks []shimapi.ToolCallContent) *component.ToolResultDiff {
	for _, block := range blocks {
		if block.Diff != nil && (block.Diff.Path != "" || block.Diff.NewText != "") {
			oldText := ""
			if block.Diff.OldText != nil {
				oldText = *block.Diff.OldText
			}
			return &component.ToolResultDiff{
				Path:    block.Diff.Path,
				OldText: oldText,
				NewText: block.Diff.NewText,
			}
		}
	}
	return nil
}

// FormatRawOutput converts a raw output value to a displayable string.
// For complex objects (e.g., Codex execute results), extracts meaningful
// content rather than dumping the entire metadata JSON.
func FormatRawOutput(v any) string {
	switch val := v.(type) {
	case string:
		return val
	case nil:
		return ""
	case map[string]any:
		// Codex execute tools include full metadata (command, cwd, duration, etc.).
		// Prefer aggregated_output which contains just the tool's actual output.
		if output, ok := val["aggregated_output"]; ok {
			return FormatRawOutput(output)
		}
		// ACP-style response with content array — extract text blocks.
		if content, ok := val["content"]; ok {
			if arr, ok := content.([]any); ok {
				var texts []string
				for _, item := range arr {
					if m, ok := item.(map[string]any); ok {
						if t, ok := m["text"].(string); ok && t != "" {
							texts = append(texts, t)
						}
					}
				}
				if len(texts) > 0 {
					return strings.Join(texts, "\n")
				}
			}
		}
		// Fallback: full JSON.
		b, err := json.MarshalIndent(val, "", "  ")
		if err != nil {
			return fmt.Sprintf("%v", val)
		}
		return string(b)
	default:
		b, err := json.MarshalIndent(val, "", "  ")
		if err != nil {
			return fmt.Sprintf("%v", val)
		}
		return string(b)
	}
}
