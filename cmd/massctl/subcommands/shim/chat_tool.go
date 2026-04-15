package shim

import (
	"encoding/json"
	"fmt"
	"strings"

	shimapi "github.com/zoumo/mass/pkg/shim/api"
	"github.com/zoumo/mass/pkg/tui/chat"
)

// buildInput creates a JSON string for the tool's Input field,
// extracting title and locations for display.
func buildInput(title string, locations []shimapi.ToolCallLocation) string {
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

// buildResultContent extracts a displayable string from tool result content blocks.
// Diff blocks are skipped here — they are handled separately by extractDiff
// for structured DiffView rendering. Text and terminal blocks are extracted
// as plain strings. RawOutput is used as a fallback.
func buildResultContent(blocks []shimapi.ToolCallContent, status string, rawOutput any) string {
	var parts []string

	for _, block := range blocks {
		switch {
		case block.Content != nil:
			if block.Content.Content.Text != nil && block.Content.Content.Text.Text != "" {
				parts = append(parts, block.Content.Content.Text.Text)
			}
		case block.Diff != nil:
			// Diff blocks are rendered via DiffView (see extractDiff).
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
		if s := formatRawOutput(rawOutput); s != "" {
			return s
		}
	}

	// Last resort: status string.
	if status != "" {
		return status
	}
	return ""
}

// extractDiff finds the first diff block from content and returns structured data
// for DiffView rendering. Returns nil if no diff block is present.
func extractDiff(blocks []shimapi.ToolCallContent) *chat.ToolResultDiff {
	for _, block := range blocks {
		if block.Diff != nil && (block.Diff.Path != "" || block.Diff.NewText != "") {
			oldText := ""
			if block.Diff.OldText != nil {
				oldText = *block.Diff.OldText
			}
			return &chat.ToolResultDiff{
				Path:    block.Diff.Path,
				OldText: oldText,
				NewText: block.Diff.NewText,
			}
		}
	}
	return nil
}

// formatRawOutput converts a raw output value to a displayable string.
func formatRawOutput(v any) string {
	switch val := v.(type) {
	case string:
		return val
	case nil:
		return ""
	default:
		b, err := json.MarshalIndent(val, "", "  ")
		if err != nil {
			return fmt.Sprintf("%v", val)
		}
		return string(b)
	}
}
