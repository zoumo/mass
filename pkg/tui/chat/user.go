package chat

import (
	"strings"

	"charm.land/lipgloss/v2"

	"github.com/zoumo/oar/third_party/charmbracelet/crush/ui/common"
	"github.com/zoumo/oar/third_party/charmbracelet/crush/ui/styles"
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
	bg := m.sty.BgBaseLighter
	return RenderBlock(BlockConfig{
		Label: &LabelConfig{
			Text:  "[User]",
			Style: lipgloss.NewStyle().Bold(true).Foreground(m.sty.Primary),
		},
		Body:       m.RawRender(width),
		Background: &bg,
		Width:      cappedWidth,
	})
}

// ID implements Identifiable.
func (m *UserMessageItem) ID() string {
	return m.message.GetID()
}
