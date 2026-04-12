package chat

import (
	"fmt"
	"image"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/open-agent-d/open-agent-d/third_party/charmbracelet/crush/ui/list"
	"github.com/open-agent-d/open-agent-d/third_party/charmbracelet/crush/ui/styles"
)

// MessageLeftPaddingTotal is the total width that is taken up by the border +
// padding. We also cap the width so text is readable to the maxTextWidth(120).
const MessageLeftPaddingTotal = 2

// maxTextWidth is the maximum width text messages can be.
const maxTextWidth = 120

// Identifiable is an interface for items that can provide a unique identifier.
type Identifiable interface {
	ID() string
}

// Animatable is an interface for items that support animation.
type Animatable interface {
	StartAnimation() interface{ /* tea.Cmd */ }
}

// Expandable is an interface for items that can be expanded or collapsed.
type Expandable interface {
	// ToggleExpanded toggles the expanded state of the item. It returns
	// whether the item is now expanded.
	ToggleExpanded() bool
}

// MessageItem represents a [Message] item that can be displayed in the
// UI and be part of a [list.List] identifiable by a unique ID.
type MessageItem interface {
	list.Item
	list.RawRenderable
	Identifiable
}

// HighlightableMessageItem is a message item that supports highlighting.
type HighlightableMessageItem interface {
	MessageItem
	list.Highlightable
}

// FocusableMessageItem is a message item that supports focus.
type FocusableMessageItem interface {
	MessageItem
	list.Focusable
}

// highlightableMessageItem is a base struct for message items that support
// text selection highlighting.
type highlightableMessageItem struct {
	startLine   int
	startCol    int
	endLine     int
	endCol      int
	highlighter list.Highlighter
}

var _ list.Highlightable = (*highlightableMessageItem)(nil)

// isHighlighted returns true if the item has a highlight range set.
func (h *highlightableMessageItem) isHighlighted() bool {
	return h.startLine != -1 || h.endLine != -1
}

// renderHighlighted highlights the content if necessary.
func (h *highlightableMessageItem) renderHighlighted(content string, width, height int) string {
	if !h.isHighlighted() {
		return content
	}
	area := image.Rect(0, 0, width, height)
	return list.Highlight(content, area, h.startLine, h.startCol, h.endLine, h.endCol, h.highlighter)
}

// SetHighlight implements list.Highlightable.
func (h *highlightableMessageItem) SetHighlight(startLine int, startCol int, endLine int, endCol int) {
	// Adjust columns for the style's left inset (border + padding) since we
	// highlight the content only.
	offset := MessageLeftPaddingTotal
	h.startLine = startLine
	h.startCol = max(0, startCol-offset)
	h.endLine = endLine
	if endCol >= 0 {
		h.endCol = max(0, endCol-offset)
	} else {
		h.endCol = endCol
	}
}

// Highlight implements list.Highlightable.
func (h *highlightableMessageItem) Highlight() (startLine int, startCol int, endLine int, endCol int) {
	return h.startLine, h.startCol, h.endLine, h.endCol
}

func defaultHighlighter(sty *styles.Styles) *highlightableMessageItem {
	return &highlightableMessageItem{
		startLine:   -1,
		startCol:    -1,
		endLine:     -1,
		endCol:      -1,
		highlighter: list.ToHighlighter(sty.TextSelection),
	}
}

// cachedMessageItem caches rendered message content to avoid re-rendering.
type cachedMessageItem struct {
	// rendered is the cached rendered string
	rendered string
	// width and height are the dimensions of the cached render
	width  int
	height int
}

// getCachedRender returns the cached render if it exists for the given width.
func (c *cachedMessageItem) getCachedRender(width int) (string, int, bool) {
	if c.width == width && c.rendered != "" {
		return c.rendered, c.height, true
	}
	return "", 0, false
}

// setCachedRender sets the cached render.
func (c *cachedMessageItem) setCachedRender(rendered string, width, height int) {
	c.rendered = rendered
	c.width = width
	c.height = height
}

// clearCache clears the cached render.
func (c *cachedMessageItem) clearCache() {
	c.rendered = ""
	c.width = 0
	c.height = 0
}

// focusableMessageItem is a base struct for message items that can be focused.
type focusableMessageItem struct {
	focused bool
}

// SetFocused implements list.Focusable.
func (f *focusableMessageItem) SetFocused(focused bool) {
	f.focused = focused
}

// AssistantInfoID returns a stable ID for assistant info items.
func AssistantInfoID(messageID string) string {
	return fmt.Sprintf("%s:assistant-info", messageID)
}

// AssistantInfoItem renders model info and response time after assistant completes.
type AssistantInfoItem struct {
	*cachedMessageItem

	id      string
	message Message
	sty     *styles.Styles
}

// NewAssistantInfoItem creates a new AssistantInfoItem.
func NewAssistantInfoItem(sty *styles.Styles, message Message) MessageItem {
	return &AssistantInfoItem{
		cachedMessageItem: &cachedMessageItem{},
		id:                AssistantInfoID(message.GetID()),
		message:           message,
		sty:               sty,
	}
}

// ID implements Identifiable.
func (a *AssistantInfoItem) ID() string {
	return a.id
}

// RawRender implements list.RawRenderable.
func (a *AssistantInfoItem) RawRender(width int) string {
	innerWidth := max(0, width-MessageLeftPaddingTotal)
	content, _, ok := a.getCachedRender(innerWidth)
	if !ok {
		content = a.renderContent(innerWidth)
		height := lipgloss.Height(content)
		a.setCachedRender(content, innerWidth, height)
	}
	return content
}

// Render implements list.Item.
func (a *AssistantInfoItem) Render(width int) string {
	prefix := a.sty.Chat.Message.SectionHeader.Render()
	lines := strings.Split(a.RawRender(width), "\n")
	for i, line := range lines {
		lines[i] = prefix + line
	}
	return strings.Join(lines, "\n")
}

func (a *AssistantInfoItem) renderContent(width int) string {
	finishData := a.message.FinishPart()
	if finishData == nil {
		return ""
	}
	return ""
}

// cappedMessageWidth returns the maximum width for message content for readability.
func cappedMessageWidth(availableWidth int) int {
	return min(availableWidth-MessageLeftPaddingTotal, maxTextWidth)
}

// ExtractMessageItems extracts [MessageItem]s from a [Message]. It
// returns all parts of the message as [MessageItem]s.
//
// For assistant messages with tool calls, pass a toolResults map to link results.
// Use BuildToolResultMap to create this map from all messages in a session.
func ExtractMessageItems(sty *styles.Styles, msg Message, toolResults map[string]ToolResult) []MessageItem {
	switch msg.GetRole() {
	case RoleUser:
		return []MessageItem{NewUserMessageItem(sty, msg)}
	case RoleAssistant:
		var items []MessageItem
		if ShouldRenderAssistantMessage(msg) {
			items = append(items, NewAssistantMessageItem(sty, msg))
		}
		for _, tc := range msg.ToolCalls() {
			var result *ToolResult
			if tr, ok := toolResults[tc.ID]; ok {
				result = &tr
			}
			items = append(items, NewToolMessageItem(
				sty,
				msg.GetID(),
				tc,
				result,
				msg.FinishReason() == FinishReasonCanceled,
			))
		}
		return items
	}
	return []MessageItem{}
}

// ShouldRenderAssistantMessage determines if an assistant message should be rendered.
//
// In some cases the assistant message only has tools so we do not want to render an
// empty message.
func ShouldRenderAssistantMessage(msg Message) bool {
	content := strings.TrimSpace(msg.Content().Text)
	thinking := strings.TrimSpace(msg.ReasoningContent().Thinking)
	isError := msg.FinishReason() == FinishReasonError
	isCancelled := msg.FinishReason() == FinishReasonCanceled
	hasToolCalls := len(msg.ToolCalls()) > 0
	return !hasToolCalls || content != "" || thinking != "" || msg.IsThinking() || isError || isCancelled
}

// BuildToolResultMap creates a map of tool call IDs to their results from a list of messages.
// Tool result messages (role == RoleTool) contain the results that should be linked
// to tool calls in assistant messages.
func BuildToolResultMap(messages []Message) map[string]ToolResult {
	resultMap := make(map[string]ToolResult)
	for _, msg := range messages {
		if msg.GetRole() == RoleTool {
			for _, result := range msg.ToolResults() {
				if result.ToolCallID != "" {
					resultMap[result.ToolCallID] = result
				}
			}
		}
	}
	return resultMap
}
