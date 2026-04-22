package component

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/zoumo/mass/third_party/charmbracelet/crush/ui/anim"
	"github.com/zoumo/mass/third_party/charmbracelet/crush/ui/common"
	"github.com/zoumo/mass/third_party/charmbracelet/crush/ui/styles"
)

const (
	assistantMessageTruncateFormat = "... (%d lines hidden) [click or space to expand]"
	maxCollapsedThinkingHeight     = 10
)

// AssistantMessageItem represents an assistant message in the chat UI.
type AssistantMessageItem struct {
	*highlightableMessageItem
	*cachedMessageItem
	*focusableMessageItem

	message           Message
	sty               *styles.Styles
	anim              *anim.Anim
	thinkingExpanded  bool
	thinkingBoxHeight int

	// fullRenderCache caches the complete Render() output (body + thinking + border).
	// Keyed by width. Cleared alongside cachedMessageItem on content changes.
	fullRenderCache map[int]string
}

// NewAssistantMessageItem creates a new AssistantMessageItem.
func NewAssistantMessageItem(sty *styles.Styles, message Message) MessageItem {
	a := &AssistantMessageItem{
		highlightableMessageItem: defaultHighlighter(sty),
		cachedMessageItem:        &cachedMessageItem{},
		focusableMessageItem:     &focusableMessageItem{},
		message:                  message,
		sty:                      sty,
	}
	a.anim = anim.New(anim.Settings{
		ID:          a.ID(),
		Size:        15,
		GradColorA:  sty.Primary,
		GradColorB:  sty.Secondary,
		LabelColor:  sty.FgBase,
		CycleColors: true,
	})
	return a
}

func (a *AssistantMessageItem) StartAnimation() tea.Cmd {
	if !a.isSpinning() {
		return nil
	}
	return a.anim.Start()
}

func (a *AssistantMessageItem) Animate(msg anim.StepMsg) tea.Cmd {
	if !a.isSpinning() {
		return nil
	}
	return a.anim.Animate(msg)
}

func (a *AssistantMessageItem) ID() string {
	return a.message.GetID()
}

// RawRender implements [MessageItem].
func (a *AssistantMessageItem) RawRender(width int) string {
	cappedWidth := cappedMessageWidth(width)

	var spinner string
	if a.isSpinning() {
		spinner = a.renderSpinning()
	}

	content, height, ok := a.getCachedRender(cappedWidth)
	if !ok {
		content = a.renderTextContent(cappedWidth)
		height = lipgloss.Height(content)
		a.setCachedRender(content, cappedWidth, height)
	}

	highlighted := a.renderHighlighted(content, cappedWidth, height)
	if spinner != "" {
		if highlighted != "" {
			highlighted += "\n\n"
		}
		return highlighted + spinner
	}
	return highlighted
}

func (a *AssistantMessageItem) clearCache() {
	a.cachedMessageItem.clearCache()
	a.fullRenderCache = nil
}

// Render implements list.Item.
func (a *AssistantMessageItem) Render(width int) string {
	// Spinning: show just the animation, no block.
	if a.isSpinning() {
		return "  " + a.RawRender(width)
	}

	cappedWidth := cappedMessageWidth(width)

	if a.fullRenderCache != nil {
		if cached, ok := a.fullRenderCache[cappedWidth]; ok {
			return cached
		}
	}

	body := a.renderTextContent(cappedWidth)
	if strings.TrimSpace(body) == "" {
		return ""
	}

	var detail string
	thinkingFaint := lipgloss.NewStyle().Faint(true)
	thinking := strings.TrimSpace(a.message.ReasoningContent().Thinking)
	if thinking != "" {
		detail = a.renderThinkingText(thinking, cappedWidth)
	}

	result := RenderBlock(BlockConfig{
		Border:      &BorderConfig{Char: "▌", Color: a.sty.Primary},
		Body:        body,
		Detail:      detail,
		DetailStyle: &thinkingFaint,
	})

	if a.fullRenderCache == nil {
		a.fullRenderCache = make(map[int]string)
	}
	a.fullRenderCache[cappedWidth] = result
	return result
}

// renderTextContent renders only the text content (no thinking).
func (a *AssistantMessageItem) renderTextContent(width int) string {
	var parts []string

	content := strings.TrimSpace(a.message.Content().Text)
	if content != "" {
		renderer := common.MarkdownRenderer(a.sty, width)
		result, err := renderer.Render(content)
		if err != nil {
			parts = append(parts, content)
		} else {
			parts = append(parts, strings.TrimSuffix(result, "\n"))
		}
	}

	if a.message.IsFinished() {
		switch a.message.FinishReason() {
		case FinishReasonCanceled:
			parts = append(parts, a.sty.Base.Italic(true).Render("Canceled"))
		case FinishReasonError:
			parts = append(parts, a.renderError(width))
		}
	}

	return strings.Join(parts, "\n")
}

// renderThinkingText renders thinking content for the Detail section.
func (a *AssistantMessageItem) renderThinkingText(thinking string, width int) string {
	renderer := common.PlainMarkdownRenderer(a.sty, width)
	rendered, err := renderer.Render(thinking)
	if err != nil {
		rendered = thinking
	}
	rendered = strings.TrimSpace(rendered)

	lines := strings.Split(rendered, "\n")
	total := len(lines)
	if !a.thinkingExpanded && total > maxCollapsedThinkingHeight {
		lines = lines[total-maxCollapsedThinkingHeight:]
		hint := fmt.Sprintf(assistantMessageTruncateFormat, total-maxCollapsedThinkingHeight)
		lines = append([]string{hint, ""}, lines...)
	}
	a.thinkingBoxHeight = len(lines)

	result := strings.Join(lines, "\n")

	if !a.message.IsThinking() || len(a.message.ToolCalls()) > 0 {
		duration := a.message.ThinkingDuration()
		if duration.String() != "0s" {
			footer := a.sty.Chat.Message.ThinkingFooterTitle.Render("Thought for ") +
				a.sty.Chat.Message.ThinkingFooterDuration.Render(duration.String())
			result += "\n" + footer
		}
	}

	return result
}

func (a *AssistantMessageItem) renderSpinning() string {
	if a.message.IsThinking() {
		a.anim.SetLabel("Thinking")
	} else if a.message.IsSummaryMessage() {
		a.anim.SetLabel("Summarizing")
	}
	return a.anim.Render()
}

func (a *AssistantMessageItem) renderError(width int) string {
	finishPart := a.message.FinishPart()
	if finishPart == nil {
		return ""
	}
	errTag := a.sty.Chat.Message.ErrorTag.Render("ERROR")
	truncated := ansi.Truncate(finishPart.Message, width-2-lipgloss.Width(errTag), "...")
	title := fmt.Sprintf("%s %s", errTag, a.sty.Chat.Message.ErrorTitle.Render(truncated))
	details := a.sty.Chat.Message.ErrorDetails.Width(width - 2).Render(finishPart.Details)
	return fmt.Sprintf("%s\n\n%s", title, details)
}

func (a *AssistantMessageItem) isSpinning() bool {
	isThinking := a.message.IsThinking()
	isFinished := a.message.IsFinished()
	hasContent := strings.TrimSpace(a.message.Content().Text) != ""
	hasThinking := strings.TrimSpace(a.message.ReasoningContent().Thinking) != ""
	hasToolCalls := len(a.message.ToolCalls()) > 0
	return (isThinking || !isFinished) && !hasContent && !hasThinking && !hasToolCalls
}

func (a *AssistantMessageItem) SetMessage(message Message) tea.Cmd {
	wasSpinning := a.isSpinning()
	a.message = message
	a.clearCache()
	if !wasSpinning && a.isSpinning() {
		return a.StartAnimation()
	}
	return nil
}

func (a *AssistantMessageItem) ToggleExpanded() {
	a.thinkingExpanded = !a.thinkingExpanded
	a.clearCache()
}

func (a *AssistantMessageItem) HandleMouseClick(btn ansi.MouseButton, x, y int) bool {
	if btn != ansi.MouseLeft {
		return false
	}
	if a.thinkingBoxHeight > 0 && y < a.thinkingBoxHeight {
		a.ToggleExpanded()
		return true
	}
	return false
}
