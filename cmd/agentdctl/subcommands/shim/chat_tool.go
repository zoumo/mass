package shim

import (
	"encoding/json"
	"fmt"
	"strings"
)

// toolEventPayload is a unified parser for both tool_call and tool_result
// event payloads. Both share the same rich structure.
type toolEventPayload struct {
	ID     string `json:"id"`
	Kind   string `json:"kind"`
	Title  string `json:"title"`
	Status string `json:"status,omitempty"`

	// Content is an array of content/diff/terminal blocks.
	Content []toolContentBlock `json:"content,omitempty"`

	// Locations are file paths associated with the tool call.
	Locations []toolLocation `json:"locations,omitempty"`

	// RawInput/RawOutput carry the original tool input/output.
	RawInput  json.RawMessage `json:"rawInput,omitempty"`
	RawOutput json.RawMessage `json:"rawOutput,omitempty"`
}

type toolContentBlock struct {
	Type string `json:"type"` // "content", "diff", "terminal"

	// content variant
	Content *struct {
		Type string `json:"type"` // "text", "image", etc.
		Text string `json:"text,omitempty"`
	} `json:"content,omitempty"`

	// diff variant
	Path    string  `json:"path,omitempty"`
	OldText *string `json:"oldText,omitempty"`
	NewText string  `json:"newText,omitempty"`

	// terminal variant
	TerminalID string `json:"terminalId,omitempty"`
}

type toolLocation struct {
	Path string `json:"path"`
	Line *int   `json:"line,omitempty"`
}

// buildInput creates a JSON string for the tool's Input field,
// extracting title and locations for display.
func (p *toolEventPayload) buildInput() string {
	params := make(map[string]any)
	if p.Title != "" {
		params["title"] = p.Title
	}
	if len(p.Locations) > 0 {
		var paths []string
		for _, loc := range p.Locations {
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

// buildResultContent extracts a displayable string from the tool result.
func (p *toolEventPayload) buildResultContent() string {
	var parts []string

	// Extract text from content blocks.
	for _, c := range p.Content {
		switch c.Type {
		case "content":
			if c.Content != nil && c.Content.Text != "" {
				parts = append(parts, c.Content.Text)
			}
		case "diff":
			if c.Path != "" {
				parts = append(parts, "diff: "+c.Path)
			}
		case "terminal":
			if c.TerminalID != "" {
				parts = append(parts, "terminal: "+c.TerminalID)
			}
		}
	}

	if len(parts) > 0 {
		return strings.Join(parts, "\n")
	}

	// Fallback to status.
	if p.Status != "" {
		return p.Status
	}
	return ""
}
