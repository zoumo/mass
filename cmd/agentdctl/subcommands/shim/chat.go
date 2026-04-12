package shim

import (
	"encoding/json"
	"fmt"
	"strings"

	"charm.land/bubbles/v2/spinner"
	"charm.land/bubbles/v2/textarea"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/open-agent-d/open-agent-d/pkg/tui/chat"
	"github.com/open-agent-d/open-agent-d/third_party/charmbracelet/crush/ui/anim"
	"github.com/open-agent-d/open-agent-d/third_party/charmbracelet/crush/ui/styles"
)

// ── Tea messages ──────────────────────────────────────────────────────────────

type notifMsg struct{ rpcResponse }
type turnEndMsg struct{}
type connClosedMsg struct{}
type connReadyMsg struct {
	c      *client
	notifs <-chan rpcResponse
}
type connErrMsg struct{ err error }
type promptErrMsg struct{ err error }

// stateChangeMsg is sent when the shim reports a runtime/stateChange notification.
type stateChangeMsg struct {
	previous string
	status   string
	reason   string
}

// ── Styles ────────────────────────────────────────────────────────────────────

var (
	styleErr  = lipgloss.NewStyle().Foreground(lipgloss.Color("9"))
	styleDim  = lipgloss.NewStyle().Faint(true)
	styleHelp = lipgloss.NewStyle().Faint(true)
)

const inputAreaHeight = 6 // textarea (3 rows) + divider + help + margin

// ── Model ─────────────────────────────────────────────────────────────────────

type chatModel struct {
	chat    *chat.Chat
	input   textarea.Model
	spinner spinner.Model
	sty     styles.Styles

	sock   string
	client *client
	notifs <-chan rpcResponse

	// Streaming state.
	currentMsg   *shimMessage // mutable message being streamed
	currentMsgID string       // chat item ID for assistant message

	// Tool tracking: tool_call ID → chat item ID, for linking tool_result.
	toolItemIDs map[string]string // toolCall.ID → chat MessageItem ID

	turnCounter int // monotonic counter for generating unique IDs

	agentStatus string // current agent status: "idle", "running", "stopped", "error"

	chatFocused bool
	waiting     bool
	ready       bool
	width       int
	height      int
}

func (m *chatModel) nextID(prefix string) string {
	m.turnCounter++
	return fmt.Sprintf("%s-%d", prefix, m.turnCounter)
}

func newChatModel(sock string) chatModel {
	ta := textarea.New()
	ta.Placeholder = "Type a message… (Enter to send, Shift+Enter for newline)"
	ta.ShowLineNumbers = false
	ta.MaxHeight = 15
	ta.MinHeight = 3
	ta.DynamicHeight = true
	ta.CharLimit = 0

	sp := spinner.New()
	sty := styles.DefaultStyles()

	return chatModel{
		sock:        sock,
		chat:        chat.NewChat(),
		input:       ta,
		spinner:     sp,
		sty:         sty,
		toolItemIDs: make(map[string]string),
	}
}

// ── Init ──────────────────────────────────────────────────────────────────────

func (m chatModel) Init() tea.Cmd {
	return tea.Batch(m.spinner.Tick, connectCmd(m.sock))
}

func connectCmd(sock string) tea.Cmd {
	return func() tea.Msg {
		c, err := dial(sock)
		if err != nil {
			return connErrMsg{fmt.Errorf("connect: %w", err)}
		}
		if _, err := c.call("session/subscribe", nil); err != nil {
			c.close()
			return connErrMsg{fmt.Errorf("session/subscribe: %w", err)}
		}
		return connReadyMsg{c: c, notifs: c.notifs}
	}
}

func waitNotif(ch <-chan rpcResponse) tea.Cmd {
	return func() tea.Msg {
		msg, ok := <-ch
		if !ok {
			return connClosedMsg{}
		}
		if isTurnEndNotification(msg) {
			return turnEndMsg{}
		}
		if msg.Method == "runtime/stateChange" {
			var p runtimeStateChangeParams
			if err := json.Unmarshal(msg.Params, &p); err == nil {
				return stateChangeMsg{
					previous: p.PreviousStatus,
					status:   p.Status,
					reason:   p.Reason,
				}
			}
		}
		return notifMsg{msg}
	}
}

func sendPromptCmd(c *client, text string) tea.Cmd {
	return func() tea.Msg {
		if err := c.send("session/prompt", map[string]string{"prompt": text}); err != nil {
			return promptErrMsg{err}
		}
		return nil
	}
}

func cancelPromptCmd(c *client) tea.Cmd {
	return func() tea.Msg {
		_, _ = c.call("session/cancel", nil)
		return nil
	}
}

func fetchStatusCmd(c *client) tea.Cmd {
	return func() tea.Msg {
		result, err := c.call("runtime/status", nil)
		if err != nil {
			return nil
		}
		var status struct {
			Status string `json:"status"`
		}
		if err := json.Unmarshal(result, &status); err != nil {
			return nil
		}
		return stateChangeMsg{status: status.Status}
	}
}

// ── Update ────────────────────────────────────────────────────────────────────

func (m chatModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmds []tea.Cmd

	switch msg := msg.(type) {

	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		vpHeight := msg.Height - inputAreaHeight
		if vpHeight < 1 {
			vpHeight = 1
		}
		m.chat.SetSize(msg.Width, vpHeight)
		m.input.SetWidth(msg.Width)
		m.ready = true

	case connReadyMsg:
		m.client = msg.c
		m.notifs = msg.notifs
		m.chat.AppendMessages(chat.NewSystemItem(m.nextID("sys"), "connected — tab focus · shift+click select text · ctrl+c quit", styleDim))
		cmds = append(cmds, waitNotif(m.notifs), fetchStatusCmd(m.client))

	case connErrMsg:
		m.chat.AppendMessages(chat.NewSystemItem(m.nextID("sys"), "error: "+msg.err.Error(), styleErr))
		return m, tea.Quit

	case connClosedMsg:
		m.chat.AppendMessages(chat.NewSystemItem(m.nextID("sys"), "connection closed", styleDim))
		return m, tea.Quit

	case notifMsg:
		cmd := m.handleNotif(msg.rpcResponse)
		if cmd != nil {
			cmds = append(cmds, cmd)
		}
		cmds = append(cmds, waitNotif(m.notifs))

	case turnEndMsg:
		if m.currentMsg != nil {
			m.currentMsg.finish(chat.FinishReasonEndTurn)
			m.updateCurrentAssistant()
		}
		m.currentMsg = nil
		m.currentMsgID = ""
		m.waiting = false
		m.chatFocused = false
		cmds = append(cmds, m.input.Focus(), waitNotif(m.notifs))

	case promptErrMsg:
		m.chat.AppendMessages(chat.NewSystemItem(m.nextID("sys"), "error: "+msg.err.Error(), styleErr))
		if m.currentMsg != nil {
			m.currentMsg.finish(chat.FinishReasonError)
			m.updateCurrentAssistant()
		}
		m.currentMsg = nil
		m.currentMsgID = ""
		m.waiting = false
		m.chatFocused = false
		cmds = append(cmds, m.input.Focus(), waitNotif(m.notifs))

	case stateChangeMsg:
		m.agentStatus = msg.status
		// If agent transitions to running and we're not already in waiting state,
		// it means someone else sent a prompt (or we late-joined a running turn).
		if msg.status == "running" && !m.waiting {
			m.waiting = true
			m.input.Blur()
		}
		// If agent returns to idle, ensure we can type.
		if msg.status == "idle" && m.waiting {
			m.waiting = false
			m.chatFocused = false
			cmds = append(cmds, m.input.Focus())
		}

	case anim.StepMsg:
		// Forward animation ticks to the chat (for spinner animations).
		if cmd := m.chat.Animate(msg); cmd != nil {
			cmds = append(cmds, cmd)
		}

	case spinner.TickMsg:
		var spinCmd tea.Cmd
		m.spinner, spinCmd = m.spinner.Update(msg)
		cmds = append(cmds, spinCmd)

	case tea.MouseWheelMsg:
		mouse := msg.Mouse()
		switch mouse.Button {
		case tea.MouseWheelUp:
			m.chat.ScrollBy(-3)
		case tea.MouseWheelDown:
			m.chat.ScrollBy(3)
		}

	case tea.KeyPressMsg:
		cmds = append(cmds, m.handleKey(tea.Key(msg))...)
	}

	return m, tea.Batch(cmds...)
}

func (m *chatModel) handleKey(key tea.Key) []tea.Cmd {
	var cmds []tea.Cmd

	switch {
	case key.Mod&tea.ModCtrl != 0 && key.Code == 'c':
		if m.client != nil {
			m.client.close()
		}
		cmds = append(cmds, tea.Quit)
		return cmds

	case key.Code == tea.KeyTab && key.Mod == 0:
		m.chatFocused = !m.chatFocused
		if m.chatFocused {
			m.input.Blur()
			m.chat.Focus()
		} else {
			m.chat.Blur()
			cmds = append(cmds, m.input.Focus())
		}
		return cmds

	case key.Code == tea.KeyEscape && key.Mod == 0:
		if m.waiting {
			m.chat.AppendMessages(chat.NewSystemItem(m.nextID("sys"), "[cancelling…]", styleDim))
			cmds = append(cmds, cancelPromptCmd(m.client))
		} else if m.chatFocused {
			m.chatFocused = false
			m.chat.Blur()
			cmds = append(cmds, m.input.Focus())
		}
		return cmds
	}

	if m.chatFocused {
		switch {
		case key.Code == 'j' || key.Code == tea.KeyDown:
			m.chat.ScrollBy(1)
		case key.Code == 'k' || key.Code == tea.KeyUp:
			m.chat.ScrollBy(-1)
		case key.Code == 'd':
			m.chat.ScrollBy(m.chat.Height() / 2)
		case key.Code == 'u':
			m.chat.ScrollBy(-m.chat.Height() / 2)
		case key.Code == 'f' || key.Code == tea.KeyPgDown || key.Code == ' ':
			m.chat.ScrollBy(m.chat.Height())
		case key.Code == 'b' || key.Code == tea.KeyPgUp:
			m.chat.ScrollBy(-m.chat.Height())
		case key.Code == 'g' || key.Code == tea.KeyHome:
			m.chat.ScrollToTop()
		case key.Code == 'G' || key.Code == tea.KeyEnd:
			m.chat.ScrollToBottom()
		}
		return cmds
	}

	switch {
	case key.Code == tea.KeyEnter && key.Mod&tea.ModShift != 0:
		if !m.waiting {
			var taCmd tea.Cmd
			m.input, taCmd = m.input.Update(tea.KeyPressMsg(key))
			cmds = append(cmds, taCmd)
		}

	case key.Code == tea.KeyEnter && key.Mod == 0:
		if m.waiting || m.client == nil {
			break
		}
		text := strings.TrimSpace(m.input.Value())
		if text == "" {
			break
		}
		m.input.Reset()
		m.input.Blur()

		userMsg := newShimMessage(m.nextID("user"), chat.RoleUser)
		userMsg.text = text
		userMsg.finished = true
		m.chat.AppendMessages(chat.NewUserMessageItem(&m.sty, userMsg))

		assistantMsg := newShimMessage(m.nextID("assistant"), chat.RoleAssistant)
		m.currentMsg = assistantMsg
		m.currentMsgID = assistantMsg.id
		item := chat.NewAssistantMessageItem(&m.sty, assistantMsg)
		m.chat.AppendMessages(item)

		// Start the spinner animation for the new assistant item.
		if a, ok := item.(*chat.AssistantMessageItem); ok {
			if cmd := a.StartAnimation(); cmd != nil {
				cmds = append(cmds, cmd)
			}
		}

		m.waiting = true
		m.toolItemIDs = make(map[string]string) // reset tool tracking
		cmds = append(cmds, sendPromptCmd(m.client, text))

	case key.Code == tea.KeyPgUp:
		m.chat.ScrollBy(-m.chat.Height())

	case key.Code == tea.KeyPgDown:
		m.chat.ScrollBy(m.chat.Height())

	default:
		if !m.waiting {
			var taCmd tea.Cmd
			m.input, taCmd = m.input.Update(tea.KeyPressMsg(key))
			cmds = append(cmds, taCmd)
		}
	}

	return cmds
}

func (m *chatModel) updateCurrentAssistant() {
	if m.currentMsgID == "" {
		return
	}
	item := m.chat.MessageItem(m.currentMsgID)
	if item == nil {
		return
	}
	if a, ok := item.(*chat.AssistantMessageItem); ok {
		a.SetMessage(m.currentMsg)
	}
	if m.chat.Follow() {
		m.chat.ScrollToBottom()
	}
}

// ensureCurrentMsg creates an assistant message if we don't have one yet.
// This happens when we connect to a shim mid-turn (late join).
func (m *chatModel) ensureCurrentMsg() tea.Cmd {
	if m.currentMsg != nil {
		return nil
	}
	msg := newShimMessage(m.nextID("assistant"), chat.RoleAssistant)
	m.currentMsg = msg
	m.currentMsgID = msg.id
	item := chat.NewAssistantMessageItem(&m.sty, msg)
	m.chat.AppendMessages(item)
	if a, ok := item.(*chat.AssistantMessageItem); ok {
		return a.StartAnimation()
	}
	return nil
}

func (m *chatModel) handleNotif(msg rpcResponse) tea.Cmd {
	if msg.Method != "session/update" {
		return nil
	}
	var p sessionUpdateParams
	if err := json.Unmarshal(msg.Params, &p); err != nil {
		return nil
	}

	var cmds []tea.Cmd

	switch p.Event.Type {
	case "text":
		var pl textPayload
		_ = json.Unmarshal(p.Event.Payload, &pl)
		if cmd := m.ensureCurrentMsg(); cmd != nil {
			cmds = append(cmds, cmd)
		}
		m.currentMsg.appendText(pl.Text)
		m.updateCurrentAssistant()

	case "thinking":
		var pl textPayload
		_ = json.Unmarshal(p.Event.Payload, &pl)
		if cmd := m.ensureCurrentMsg(); cmd != nil {
			cmds = append(cmds, cmd)
		}
		m.currentMsg.appendThinking(pl.Text)
		m.updateCurrentAssistant()

	case "tool_call":
		var pl struct {
			ID    string `json:"id"`
			Kind  string `json:"kind"`
			Title string `json:"title"`
		}
		_ = json.Unmarshal(p.Event.Payload, &pl)

		// Finish current assistant text before tool.
		if m.currentMsg != nil {
			m.currentMsg.finish(chat.FinishReasonToolUse)
			m.updateCurrentAssistant()
		}

		// Add tool call item and track its ID for later result linking.
		toolItemID := m.nextID("tc")
		tc := chat.ToolCall{ID: pl.ID, Name: pl.Kind, Input: fmt.Sprintf(`{"title":%q}`, pl.Title)}
		toolItem := chat.NewToolMessageItem(&m.sty, toolItemID, tc, nil, false)
		if ti, ok := toolItem.(chat.ToolMessageItem); ok {
			ti.SetStatus(chat.ToolStatusRunning)
		}
		m.chat.AppendMessages(toolItem)
		m.toolItemIDs[pl.ID] = toolItemID

		// Start new assistant message for text after tool.
		newMsg := newShimMessage(m.nextID("assistant"), chat.RoleAssistant)
		m.currentMsg = newMsg
		m.currentMsgID = newMsg.id
		item := chat.NewAssistantMessageItem(&m.sty, newMsg)
		m.chat.AppendMessages(item)
		if a, ok := item.(*chat.AssistantMessageItem); ok {
			if cmd := a.StartAnimation(); cmd != nil {
				cmds = append(cmds, cmd)
			}
		}

	case "tool_result":
		var pl struct {
			ID     string `json:"id"`
			Status string `json:"status"`
		}
		_ = json.Unmarshal(p.Event.Payload, &pl)

		status := chat.ToolStatusSuccess
		if pl.Status == "error" {
			status = chat.ToolStatusError
		}

		// Find the tool call item and update its result/status.
		if itemID, ok := m.toolItemIDs[pl.ID]; ok {
			if item := m.chat.MessageItem(itemID); item != nil {
				if ti, ok := item.(chat.ToolMessageItem); ok {
					ti.SetStatus(status)
					ti.SetResult(&chat.ToolResult{
						ToolCallID: pl.ID,
						Content:    pl.Status,
					})
				}
			}
		} else {
			// Late join: tool_call was before we connected. Show as standalone.
			toolItemID := m.nextID("tc")
			tc := chat.ToolCall{ID: pl.ID, Name: pl.ID} // use ID as name fallback
			result := &chat.ToolResult{ToolCallID: pl.ID, Content: pl.Status}
			toolItem := chat.NewToolMessageItem(&m.sty, toolItemID, tc, result, false)
			if ti, ok := toolItem.(chat.ToolMessageItem); ok {
				ti.SetStatus(status)
			}
			m.chat.AppendMessages(toolItem)
		}
		if m.chat.Follow() {
			m.chat.ScrollToBottom()
		}
	}

	if len(cmds) > 0 {
		return tea.Batch(cmds...)
	}
	return nil
}

// ── View ──────────────────────────────────────────────────────────────────────

func (m chatModel) View() tea.View {
	if !m.ready {
		return tea.NewView("\n  " + m.spinner.View() + " connecting…\n")
	}

	chatView := m.chat.Render()
	divider := styleDim.Render(strings.Repeat("─", m.width))

	var bottom string
	if m.waiting {
		bottom = "  " + m.spinner.View() + m.renderStatusLine()
	} else {
		bottom = m.input.View()
	}

	help := m.renderHelp()

	v := tea.NewView(chatView + "\n" + divider + "\n" + bottom + "\n" + help)
	v.AltScreen = true
	v.MouseMode = tea.MouseModeCellMotion
	return v
}

var (
	styleStatusRunning = lipgloss.NewStyle().Foreground(lipgloss.Color("10")) // green
	styleStatusIdle    = lipgloss.NewStyle().Foreground(lipgloss.Color("12")) // blue
	styleStatusError   = lipgloss.NewStyle().Foreground(lipgloss.Color("9"))  // red
	styleStatusStopped = lipgloss.NewStyle().Faint(true)
)

func (m chatModel) renderStatusLine() string {
	status := m.agentStatus
	if status == "" {
		status = "unknown"
	}

	var styled string
	switch status {
	case "running":
		styled = styleStatusRunning.Render("● running")
	case "idle":
		styled = styleStatusIdle.Render("● idle")
	case "error":
		styled = styleStatusError.Render("● error")
	case "stopped":
		styled = styleStatusStopped.Render("● stopped")
	default:
		styled = styleDim.Render("● " + status)
	}

	hint := ""
	switch status {
	case "running":
		hint = styleDim.Render(" — esc to cancel")
	case "idle":
		hint = styleDim.Render(" — ready for input")
	case "error":
		hint = styleDim.Render(" — agent error, check logs")
	case "stopped":
		hint = styleDim.Render(" — agent stopped")
	}

	return " " + styled + hint
}

func (m chatModel) renderHelp() string {
	var keys []string
	if m.chatFocused {
		keys = append(keys, "j/k scroll", "d/u half-page", "g/G top/bottom", "tab editor", "esc back")
	} else {
		keys = append(keys, "enter send", "shift+enter newline", "tab chat", "esc cancel")
	}
	keys = append(keys, "shift+click select text", "ctrl+c quit")
	return styleHelp.Render(" " + strings.Join(keys, " · "))
}

// ── Entry point ───────────────────────────────────────────────────────────────

func runChatTUI(sock string) error {
	p := tea.NewProgram(newChatModel(sock))
	_, err := p.Run()
	return err
}
