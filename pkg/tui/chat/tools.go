package chat

import (
	"fmt"
	"image/color"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/zoumo/oar/third_party/charmbracelet/crush/stringext"
	"github.com/zoumo/oar/third_party/charmbracelet/crush/ui/anim"
	"github.com/zoumo/oar/third_party/charmbracelet/crush/ui/common"
	"github.com/zoumo/oar/third_party/charmbracelet/crush/ui/styles"
)

// responseContextHeight limits the number of lines displayed in tool output.
const responseContextHeight = 10

// toolBodyLeftPaddingTotal represents the padding that should be applied to each tool body.
const toolBodyLeftPaddingTotal = 2

// ToolStatus represents the current state of a tool call.
type ToolStatus int

const (
	ToolStatusAwaitingPermission ToolStatus = iota
	ToolStatusRunning
	ToolStatusSuccess
	ToolStatusError
	ToolStatusCanceled
)

// ToolMessageItem represents a tool call message in the chat UI.
type ToolMessageItem interface {
	MessageItem

	ToolCall() ToolCall
	SetToolCall(tc ToolCall)
	SetResult(res *ToolResult)
	MessageID() string
	SetMessageID(id string)
	SetStatus(status ToolStatus)
	Status() ToolStatus
}

// Compactable is an interface for tool items that can render in a compacted mode.
type Compactable interface {
	SetCompact(compact bool)
}

// SpinningState contains the state passed to SpinningFunc for custom spinning logic.
type SpinningState struct {
	ToolCallData ToolCall
	Result       *ToolResult
	Status       ToolStatus
}

// IsCanceled returns true if the tool status is canceled.
func (s *SpinningState) IsCanceled() bool {
	return s.Status == ToolStatusCanceled
}

// HasResult returns true if the result is not nil.
func (s *SpinningState) HasResult() bool {
	return s.Result != nil
}

// SpinningFunc is a function type for custom spinning logic.
type SpinningFunc func(state SpinningState) bool

// DefaultToolRenderContext implements the default [ToolRenderer] interface.
type DefaultToolRenderContext struct{}

// RenderTool implements the [ToolRenderer] interface.
func (d *DefaultToolRenderContext) RenderTool(sty *styles.Styles, width int, opts *ToolRenderOpts) string {
	return "TODO: Implement Tool Renderer For: " + opts.ToolCallData.Name
}

// ToolRenderOpts contains the data needed to render a tool call.
type ToolRenderOpts struct {
	ToolCallData    ToolCall
	Result          *ToolResult
	Anim            *anim.Anim
	ExpandedContent bool
	Compact         bool
	IsSpinning      bool
	Status          ToolStatus
}

// IsPending returns true if the tool call is still pending.
func (o *ToolRenderOpts) IsPending() bool {
	return !o.ToolCallData.Finished && !o.IsCanceled()
}

// IsCanceled returns true if the tool status is canceled.
func (o *ToolRenderOpts) IsCanceled() bool {
	return o.Status == ToolStatusCanceled
}

// HasResult returns true if the result is not nil.
func (o *ToolRenderOpts) HasResult() bool {
	return o.Result != nil
}

// HasEmptyResult returns true if the result is nil or has empty content.
func (o *ToolRenderOpts) HasEmptyResult() bool {
	return o.Result == nil || o.Result.Content == ""
}

// ToolRenderer represents an interface for rendering tool calls.
type ToolRenderer interface {
	RenderTool(sty *styles.Styles, width int, opts *ToolRenderOpts) string
}

// ToolRendererFunc is a function type that implements the [ToolRenderer] interface.
type ToolRendererFunc func(sty *styles.Styles, width int, opts *ToolRenderOpts) string

// RenderTool implements the ToolRenderer interface.
func (f ToolRendererFunc) RenderTool(sty *styles.Styles, width int, opts *ToolRenderOpts) string {
	return f(sty, width, opts)
}

// baseToolMessageItem represents a tool call message that can be displayed in the UI.
type baseToolMessageItem struct {
	*highlightableMessageItem
	*cachedMessageItem
	*focusableMessageItem

	toolRenderer   ToolRenderer
	toolCall       ToolCall
	result         *ToolResult
	messageID      string
	status         ToolStatus
	hasCappedWidth bool
	isCompact      bool
	spinningFunc   SpinningFunc

	sty             *styles.Styles
	anim            *anim.Anim
	expandedContent bool
}

var _ Expandable = (*baseToolMessageItem)(nil)

// newBaseToolMessageItem is the internal constructor for base tool message items.
func newBaseToolMessageItem(
	sty *styles.Styles,
	toolCall ToolCall,
	result *ToolResult,
	toolRenderer ToolRenderer,
	canceled bool,
) *baseToolMessageItem {
	// All tools use capped width by default
	hasCappedWidth := true

	status := ToolStatusRunning
	if canceled {
		status = ToolStatusCanceled
	}

	t := &baseToolMessageItem{
		highlightableMessageItem: defaultHighlighter(sty),
		cachedMessageItem:        &cachedMessageItem{},
		focusableMessageItem:     &focusableMessageItem{},
		sty:                      sty,
		toolRenderer:             toolRenderer,
		toolCall:                 toolCall,
		result:                   result,
		status:                   status,
		hasCappedWidth:           hasCappedWidth,
	}
	t.anim = anim.New(anim.Settings{
		ID:          toolCall.ID,
		Size:        15,
		GradColorA:  sty.Primary,
		GradColorB:  sty.Secondary,
		LabelColor:  sty.FgBase,
		CycleColors: true,
	})

	return t
}

// NewToolMessageItem creates a new [ToolMessageItem] based on the tool call name.
// Since we don't have tool-specific renderers, all tools use the generic renderer.
func NewToolMessageItem(
	sty *styles.Styles,
	messageID string,
	toolCall ToolCall,
	result *ToolResult,
	canceled bool,
) ToolMessageItem {
	item := NewGenericToolMessageItem(sty, toolCall, result, canceled)
	item.SetMessageID(messageID)
	return item
}

// SetCompact implements the Compactable interface.
func (t *baseToolMessageItem) SetCompact(compact bool) {
	t.isCompact = compact
	t.clearCache()
}

// ID returns the unique identifier for this tool message item.
func (t *baseToolMessageItem) ID() string {
	return t.toolCall.ID
}

// StartAnimation starts the tool animation if it should be spinning.
func (t *baseToolMessageItem) StartAnimation() tea.Cmd {
	if !t.isSpinning() {
		return nil
	}
	return t.anim.Start()
}

// Animate progresses the tool animation if it should be spinning.
func (t *baseToolMessageItem) Animate(msg anim.StepMsg) tea.Cmd {
	if !t.isSpinning() {
		return nil
	}
	return t.anim.Animate(msg)
}

// RawRender implements [MessageItem].
func (t *baseToolMessageItem) RawRender(width int) string {
	toolItemWidth := width - MessageLeftPaddingTotal
	if t.hasCappedWidth {
		toolItemWidth = cappedMessageWidth(width)
	}

	content, height, ok := t.getCachedRender(toolItemWidth)
	// if we are spinning or there is no cache rerender
	if !ok || t.isSpinning() {
		content = t.toolRenderer.RenderTool(t.sty, toolItemWidth, &ToolRenderOpts{
			ToolCallData:    t.toolCall,
			Result:          t.result,
			Anim:            t.anim,
			ExpandedContent: t.expandedContent,
			Compact:         t.isCompact,
			IsSpinning:      t.isSpinning(),
			Status:          t.computeStatus(),
		})
		height = lipgloss.Height(content)
		// cache the rendered content
		t.setCachedRender(content, toolItemWidth, height)
	}

	return t.renderHighlighted(content, toolItemWidth, height)
}

// Render renders the tool message item at the given width.
// Uses the unified Block component with colored left border.
func (t *baseToolMessageItem) Render(width int) string {
	// Pick border color based on status.
	var borderColor color.Color
	switch t.computeStatus() {
	case ToolStatusSuccess:
		borderColor = t.sty.GreenDark
	case ToolStatusError:
		borderColor = t.sty.Error
	default:
		borderColor = t.sty.FgMuted
	}

	// Build label from icon + tool name + params (the header line).
	raw := t.RawRender(width)
	// RawRender returns header + body. Split to extract header as label
	// and body as detail.
	header, detail := splitToolHeaderBody(raw)

	return RenderBlock(BlockConfig{
		Border: &BorderConfig{Char: "▌", Color: borderColor},
		Label: &LabelConfig{
			Text:  header,
			Style: lipgloss.NewStyle(),
		},
		Detail: detail,
	})
}

// splitToolHeaderBody splits RawRender output into header (first line)
// and body (remaining lines after the blank separator).
func splitToolHeaderBody(raw string) (header, body string) {
	parts := strings.SplitN(raw, "\n\n", 2)
	header = parts[0]
	if len(parts) > 1 {
		body = parts[1]
	}
	return header, body
}

// ToolCall returns the tool call associated with this message item.
func (t *baseToolMessageItem) ToolCall() ToolCall {
	return t.toolCall
}

// SetToolCall sets the tool call associated with this message item.
func (t *baseToolMessageItem) SetToolCall(tc ToolCall) {
	t.toolCall = tc
	t.clearCache()
}

// SetResult sets the tool result associated with this message item.
func (t *baseToolMessageItem) SetResult(res *ToolResult) {
	t.result = res
	t.clearCache()
}

// MessageID returns the ID of the message containing this tool call.
func (t *baseToolMessageItem) MessageID() string {
	return t.messageID
}

// SetMessageID sets the ID of the message containing this tool call.
func (t *baseToolMessageItem) SetMessageID(id string) {
	t.messageID = id
}

// SetStatus sets the tool status.
func (t *baseToolMessageItem) SetStatus(status ToolStatus) {
	t.status = status
	t.clearCache()
}

// Status returns the current tool status.
func (t *baseToolMessageItem) Status() ToolStatus {
	return t.status
}

// computeStatus computes the effective status considering the result.
func (t *baseToolMessageItem) computeStatus() ToolStatus {
	if t.result != nil {
		if t.result.IsError {
			return ToolStatusError
		}
		return ToolStatusSuccess
	}
	return t.status
}

// isSpinning returns true if the tool should show animation.
func (t *baseToolMessageItem) isSpinning() bool {
	if t.spinningFunc != nil {
		return t.spinningFunc(SpinningState{
			ToolCallData: t.toolCall,
			Result:       t.result,
			Status:       t.status,
		})
	}
	return !t.toolCall.Finished && t.status != ToolStatusCanceled
}

// SetSpinningFunc sets a custom function to determine if the tool should spin.
func (t *baseToolMessageItem) SetSpinningFunc(fn SpinningFunc) {
	t.spinningFunc = fn
}

// ToggleExpanded toggles the expanded state.
func (t *baseToolMessageItem) ToggleExpanded() bool {
	t.expandedContent = !t.expandedContent
	t.clearCache()
	return t.expandedContent
}

// HandleMouseClick implements MouseClickable.
func (t *baseToolMessageItem) HandleMouseClick(btn ansi.MouseButton, x, y int) bool {
	return btn == ansi.MouseLeft
}

// ── Tool rendering helpers ──────────────────────────────────────────────────

// pendingTool renders a tool that is still in progress with an animation.
func pendingTool(sty *styles.Styles, name string, a *anim.Anim, nested bool) string {
	icon := sty.Tool.IconPending.Render()
	nameStyle := sty.Tool.NameNormal
	if nested {
		nameStyle = sty.Tool.NameNested
	}
	toolName := nameStyle.Render(name)

	var animView string
	if a != nil {
		animView = a.Render()
	}

	return fmt.Sprintf("%s %s %s", icon, toolName, animView)
}

// toolEarlyStateContent handles error/canceled/pending states before content rendering.
func toolEarlyStateContent(sty *styles.Styles, opts *ToolRenderOpts, width int) (string, bool) {
	var msg string
	switch opts.Status {
	case ToolStatusError:
		msg = toolErrorContent(sty, opts.Result, width)
	case ToolStatusCanceled:
		msg = sty.Tool.StateCancelled.Render("Canceled.")
	case ToolStatusAwaitingPermission:
		msg = sty.Tool.StateWaiting.Render("Requesting permission...")
	case ToolStatusRunning:
		msg = sty.Tool.StateWaiting.Render("Waiting for tool response...")
	default:
		return "", false
	}
	return msg, true
}

// toolErrorContent formats an error message with ERROR tag.
func toolErrorContent(sty *styles.Styles, result *ToolResult, width int) string {
	if result == nil {
		return ""
	}
	errContent := strings.ReplaceAll(result.Content, "\n", " ")
	errTag := sty.Tool.ErrorTag.Render("ERROR")
	tagWidth := lipgloss.Width(errTag)
	errContent = ansi.Truncate(errContent, width-tagWidth-3, "...")
	return fmt.Sprintf("%s %s", errTag, sty.Tool.ErrorMessage.Render(errContent))
}

// toolIcon returns the status icon for a tool call based on its status.
func toolIcon(sty *styles.Styles, status ToolStatus) string {
	switch status {
	case ToolStatusSuccess:
		return sty.Tool.IconSuccess.String()
	case ToolStatusError:
		return sty.Tool.IconError.String()
	case ToolStatusCanceled:
		return sty.Tool.IconCancelled.String()
	default:
		return sty.Tool.IconPending.String()
	}
}

// toolParamList formats tool parameters as "main (key=value, ...)" with truncation.
func toolParamList(sty *styles.Styles, params []string, width int) string {
	const minSpaceForMainParam = 30
	if len(params) == 0 {
		return ""
	}

	mainParam := params[0]

	// Build key=value pairs from remaining params (consecutive key, value pairs).
	var kvPairs []string
	for i := 1; i+1 < len(params); i += 2 {
		if params[i+1] != "" {
			kvPairs = append(kvPairs, fmt.Sprintf("%s=%s", params[i], params[i+1]))
		}
	}

	output := mainParam
	if len(kvPairs) > 0 {
		partsStr := strings.Join(kvPairs, ", ")
		if remaining := width - lipgloss.Width(partsStr) - 3; remaining >= minSpaceForMainParam {
			output = fmt.Sprintf("%s (%s)", mainParam, partsStr)
		}
	}

	if width >= 0 {
		output = ansi.Truncate(output, width, "...")
	}
	return sty.Tool.ParamMain.Render(output)
}

// toolHeader builds the tool header line: "icon ToolName params..."
func toolHeader(sty *styles.Styles, status ToolStatus, name string, width int, nested bool, params ...string) string {
	icon := toolIcon(sty, status)
	nameStyle := sty.Tool.NameNormal
	if nested {
		nameStyle = sty.Tool.NameNested
	}
	toolName := nameStyle.Render(name)
	prefix := fmt.Sprintf("%s [%s] ", icon, toolName)
	prefixWidth := lipgloss.Width(prefix)
	remainingWidth := width - prefixWidth
	paramsStr := toolParamList(sty, params, remainingWidth)
	return prefix + paramsStr
}

// toolOutputPlainContent renders plain text with optional expansion support.
func toolOutputPlainContent(sty *styles.Styles, content string, width int, expanded bool) string {
	content = stringext.NormalizeSpace(content)
	lines := strings.Split(content, "\n")

	maxLines := responseContextHeight
	if expanded {
		maxLines = len(lines) // Show all
	}

	var out []string
	for i, ln := range lines {
		if i >= maxLines {
			break
		}
		ln = " " + ln
		if lipgloss.Width(ln) > width {
			ln = ansi.Truncate(ln, width, "...")
		}
		out = append(out, sty.Tool.ContentLine.Width(width).Render(ln))
	}

	wasTruncated := len(lines) > responseContextHeight

	if !expanded && wasTruncated {
		out = append(out, sty.Tool.ContentTruncation.
			Width(width).
			Render(fmt.Sprintf(assistantMessageTruncateFormat, len(lines)-responseContextHeight)))
	}

	return strings.Join(out, "\n")
}

// toolOutputCodeContent renders code with syntax highlighting and line numbers.
func toolOutputCodeContent(sty *styles.Styles, path, content string, offset, width int, expanded bool) string {
	content = stringext.NormalizeSpace(content)

	lines := strings.Split(content, "\n")
	maxLines := responseContextHeight
	if expanded {
		maxLines = len(lines)
	}

	displayLines := lines
	if len(lines) > maxLines {
		displayLines = lines[:maxLines]
	}

	bg := sty.Tool.ContentCodeBg
	highlighted, _ := common.SyntaxHighlight(sty, strings.Join(displayLines, "\n"), path, bg)
	highlightedLines := strings.Split(highlighted, "\n")

	maxLineNumber := len(displayLines) + offset
	maxDigits := getDigits(maxLineNumber)
	numFmt := fmt.Sprintf("%%%dd", maxDigits)

	bodyWidth := width - toolBodyLeftPaddingTotal
	codeWidth := bodyWidth - maxDigits

	var out []string
	for i, ln := range highlightedLines {
		lineNum := sty.Tool.ContentLineNumber.Render(fmt.Sprintf(numFmt, i+1+offset))
		ln = ansi.Truncate(ln, codeWidth-sty.Tool.ContentCodeLine.GetHorizontalPadding(), "...")
		codeLine := sty.Tool.ContentCodeLine.
			Width(codeWidth).
			Render(ln)
		out = append(out, lipgloss.JoinHorizontal(lipgloss.Left, lineNum, codeLine))
	}

	if len(lines) > maxLines && !expanded {
		out = append(out, sty.Tool.ContentCodeTruncation.
			Width(width).
			Render(fmt.Sprintf(assistantMessageTruncateFormat, len(lines)-maxLines)),
		)
	}

	return sty.Tool.Body.Render(strings.Join(out, "\n"))
}

// toolOutputImageContent renders image data with size info.
func toolOutputImageContent(sty *styles.Styles, data, mediaType string) string {
	dataSize := len(data) * 3 / 4
	sizeStr := formatSize(dataSize)
	return sty.Tool.Body.Render(fmt.Sprintf(
		"%s %s %s %s",
		sty.Tool.ResourceLoadedText.Render("Loaded Image"),
		sty.Tool.ResourceLoadedIndicator.Render(styles.ArrowRightIcon),
		sty.Tool.MediaType.Render(mediaType),
		sty.Tool.ResourceSize.Render(sizeStr),
	))
}

// joinToolParts joins header and body with a blank line separator.
func joinToolParts(header, body string) string {
	return strings.Join([]string{header, "", body}, "\n")
}

// looksLikeMarkdown checks if content appears to be markdown by looking for
// common markdown patterns.
func looksLikeMarkdown(content string) bool {
	patterns := []string{
		"# ",  // headers
		"## ", // headers
		"**",  // bold
		"```", // code fence
		"- ",  // unordered list
		"1. ", // ordered list
		"> ",  // blockquote
		"---", // horizontal rule
		"***", // horizontal rule
	}
	for _, p := range patterns {
		if strings.Contains(content, p) {
			return true
		}
	}
	return false
}

// getDigits returns the number of digits in a number.
func getDigits(n int) int {
	if n == 0 {
		return 1
	}
	if n < 0 {
		n = -n
	}
	digits := 0
	for n > 0 {
		n /= 10
		digits++
	}
	return digits
}

// formatSize formats byte size into human readable format.
func formatSize(bytes int) string {
	const (
		kb = 1024
		mb = kb * 1024
	)
	switch {
	case bytes >= mb:
		return fmt.Sprintf("%.1f MB", float64(bytes)/float64(mb))
	case bytes >= kb:
		return fmt.Sprintf("%.1f KB", float64(bytes)/float64(kb))
	default:
		return fmt.Sprintf("%d B", bytes)
	}
}
