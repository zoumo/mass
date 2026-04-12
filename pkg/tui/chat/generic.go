package chat

import (
	"encoding/json"
	"strings"

	"github.com/zoumo/oar/third_party/charmbracelet/crush/stringext"
	"github.com/zoumo/oar/third_party/charmbracelet/crush/ui/styles"
)

// GenericToolMessageItem is a message item that represents an unknown tool call.
type GenericToolMessageItem struct {
	*baseToolMessageItem
}

var _ ToolMessageItem = (*GenericToolMessageItem)(nil)

// NewGenericToolMessageItem creates a new [GenericToolMessageItem].
func NewGenericToolMessageItem(
	sty *styles.Styles,
	toolCall ToolCall,
	result *ToolResult,
	canceled bool,
) ToolMessageItem {
	return &GenericToolMessageItem{
		baseToolMessageItem: newBaseToolMessageItem(sty, toolCall, result, &GenericToolRenderContext{}, canceled),
	}
}

// GenericToolRenderContext renders unknown/generic tool messages.
type GenericToolRenderContext struct{}

// RenderTool implements the [ToolRenderer] interface.
func (g *GenericToolRenderContext) RenderTool(sty *styles.Styles, width int, opts *ToolRenderOpts) string {
	cappedWidth := cappedMessageWidth(width)
	name := genericPrettyName(opts.ToolCallData.Name)

	if opts.IsPending() {
		return pendingTool(sty, name, opts.Anim, opts.Compact)
	}

	var params map[string]any
	if err := json.Unmarshal([]byte(opts.ToolCallData.Input), &params); err != nil {
		return toolErrorContent(sty, &ToolResult{Content: "Invalid parameters"}, cappedWidth)
	}

	var toolParams []string
	for k, v := range params {
		switch val := v.(type) {
		case string:
			toolParams = append(toolParams, val)
		default:
			toolParams = append(toolParams, k)
		}
	}

	header := toolHeader(sty, opts.Status, name, cappedWidth, opts.Compact, toolParams...)
	if opts.Compact {
		return header
	}

	if earlyState, ok := toolEarlyStateContent(sty, opts, cappedWidth); ok {
		return joinToolParts(header, earlyState)
	}

	if !opts.HasResult() || opts.Result.Content == "" {
		return header
	}

	bodyWidth := cappedWidth - toolBodyLeftPaddingTotal

	// Handle image data.
	if opts.Result.Data != "" && strings.HasPrefix(opts.Result.MIMEType, "image/") {
		body := sty.Tool.Body.Render(toolOutputImageContent(sty, opts.Result.Data, opts.Result.MIMEType))
		return joinToolParts(header, body)
	}

	// Try to parse result as JSON for pretty display.
	var result json.RawMessage
	var body string
	if err := json.Unmarshal([]byte(opts.Result.Content), &result); err == nil {
		prettyResult, err := json.MarshalIndent(result, "", "  ")
		if err == nil {
			body = sty.Tool.Body.Render(toolOutputCodeContent(sty, "result.json", string(prettyResult), 0, bodyWidth, opts.ExpandedContent))
		} else {
			body = sty.Tool.Body.Render(toolOutputPlainContent(sty, opts.Result.Content, bodyWidth, opts.ExpandedContent))
		}
	} else if looksLikeMarkdown(opts.Result.Content) {
		body = sty.Tool.Body.Render(toolOutputCodeContent(sty, "result.md", opts.Result.Content, 0, bodyWidth, opts.ExpandedContent))
	} else {
		body = sty.Tool.Body.Render(toolOutputPlainContent(sty, opts.Result.Content, bodyWidth, opts.ExpandedContent))
	}

	return joinToolParts(header, body)
}

// genericPrettyName converts a snake_case or kebab-case tool name to a
// human-readable title case name.
func genericPrettyName(name string) string {
	name = strings.ReplaceAll(name, "_", " ")
	name = strings.ReplaceAll(name, "-", " ")
	return stringext.Capitalize(name)
}
