package chat

import (
	"strings"

	"charm.land/lipgloss/v2"

	"github.com/open-agent-d/open-agent-d/third_party/charmbracelet/crush/ui/common"
	"github.com/open-agent-d/open-agent-d/third_party/charmbracelet/crush/ui/styles"
)

// UserMessageItem represents a user message in the chat UI.
type UserMessageItem struct {
	*highlightableMessageItem
	*cachedMessageItem
	*focusableMessageItem

	message Message
	sty     *styles.Styles
}

// NewUserMessageItem creates a new UserMessageItem.
func NewUserMessageItem(sty *styles.Styles, message Message) MessageItem {
	return &UserMessageItem{
		highlightableMessageItem: defaultHighlighter(sty),
		cachedMessageItem:        &cachedMessageItem{},
		focusableMessageItem:     &focusableMessageItem{},
		message:                  message,
		sty:                      sty,
	}
}

// RawRender implements [MessageItem].
func (m *UserMessageItem) RawRender(width int) string {
	cappedWidth := cappedMessageWidth(width)

	content, height, ok := m.getCachedRender(cappedWidth)
	// cache hit
	if ok {
		return m.renderHighlighted(content, cappedWidth, height)
	}

	renderer := common.MarkdownRenderer(m.sty, cappedWidth-2) // -2 for block padding

	msgContent := strings.TrimSpace(m.message.Content().Text)
	result, err := renderer.Render(msgContent)
	if err != nil {
		content = msgContent
	} else {
		content = strings.TrimSuffix(result, "\n")
	}

	height = lipgloss.Height(content)
	m.setCachedRender(content, cappedWidth, height)
	return m.renderHighlighted(content, cappedWidth, height)
}

// Render implements list.Item.
func (m *UserMessageItem) Render(width int) string {
	cappedWidth := cappedMessageWidth(width)
	rawContent := m.RawRender(width)

	// Label line
	label := lipgloss.NewStyle().Bold(true).Foreground(m.sty.Primary).Render("[User]")

	// Wrap in a background block (dark gray background with padding)
	blockStyle := lipgloss.NewStyle().
		Background(m.sty.BgBaseLighter).
		Width(cappedWidth).
		Padding(0, 1)

	return blockStyle.Render(label + "\n" + rawContent)
}

// ID implements Identifiable.
func (m *UserMessageItem) ID() string {
	return m.message.GetID()
}
