package shim

import (
	"encoding/json"
	"fmt"
	"strings"

	shimapi "github.com/zoumo/mass/pkg/shim/api"
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
func buildResultContent(blocks []shimapi.ToolCallContent, status string) string {
	var parts []string

	// Extract text from content blocks.
	for _, block := range blocks {
		switch {
		case block.Content != nil:
			if block.Content.Content.Text != nil && block.Content.Content.Text.Text != "" {
				parts = append(parts, block.Content.Content.Text.Text)
			}
		case block.Diff != nil:
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

	// Fallback to status.
	if status != "" {
		return status
	}
	return ""
}
