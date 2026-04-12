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
	sty     styles.Styles // crush styles for message rendering

	sock   string
	client *client
	notifs <-chan rpcResponse

	// Current streaming state: we maintain a mutable shimMessage that gets
	// passed to the crush-style AssistantMessageItem via SetMessage().
	currentMsg   *shimMessage // the mutable message being streamed
	currentMsgID string       // the chat item ID for the assistant message

	turnCounter int // monotonic counter for generating unique IDs

	chatFocused bool // true when chat area has focus (for vim scrolling)
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
		sock:    sock,
		chat:    chat.NewChat(),
		input:   ta,
		spinner: sp,
		sty:     sty,
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
		m.chat.AppendMessages(chat.NewSystemItem(m.nextID("sys"), "connected — tab to switch focus, ctrl+c to quit", styleDim))
		cmds = append(cmds, waitNotif(m.notifs))

	case connErrMsg:
		m.chat.AppendMessages(chat.NewSystemItem(m.nextID("sys"), "error: "+msg.err.Error(), styleErr))
		return m, tea.Quit

	case connClosedMsg:
		m.chat.AppendMessages(chat.NewSystemItem(m.nextID("sys"), "connection closed", styleDim))
		return m, tea.Quit

	case notifMsg:
		m.handleNotif(msg.rpcResponse)
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
	// ── Global keys ──────────────────────────────────────────────────────
	case key.Mod&tea.ModCtrl != 0 && key.Code == 'c':
		if m.client != nil {
			m.client.close()
		}
		cmds = append(cmds, tea.Quit)
		return cmds

	case key.Code == tea.KeyTab && key.Mod == 0:
		// Toggle focus between chat and editor.
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
			// Switch back to editor.
			m.chatFocused = false
			m.chat.Blur()
			cmds = append(cmds, m.input.Focus())
		}
		return cmds
	}

	// ── Chat-focused keys (vim-style scrolling) ─────────────────────────
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

	// ── Editor-focused keys ─────────────────────────────────────────────
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

		// Create user message.
		userMsg := newShimMessage(m.nextID("user"), chat.RoleUser)
		userMsg.text = text
		userMsg.finished = true
		m.chat.AppendMessages(chat.NewUserMessageItem(&m.sty, userMsg))

		// Create assistant message (will be updated as tokens stream in).
		assistantMsg := newShimMessage(m.nextID("assistant"), chat.RoleAssistant)
		m.currentMsg = assistantMsg
		m.currentMsgID = assistantMsg.id
		m.chat.AppendMessages(chat.NewAssistantMessageItem(&m.sty, assistantMsg))

		m.waiting = true
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

// updateCurrentAssistant calls SetMessage on the current assistant item
// to trigger a re-render with the latest message state.
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

func (m *chatModel) handleNotif(msg rpcResponse) {
	if msg.Method != "session/update" {
		return
	}
	var p sessionUpdateParams
	if err := json.Unmarshal(msg.Params, &p); err != nil {
		return
	}
	switch p.Event.Type {
	case "text":
		var pl textPayload
		_ = json.Unmarshal(p.Event.Payload, &pl)
		if m.currentMsg != nil {
			m.currentMsg.appendText(pl.Text)
			m.updateCurrentAssistant()
		}

	case "thinking":
		var pl textPayload
		_ = json.Unmarshal(p.Event.Payload, &pl)
		if m.currentMsg != nil {
			m.currentMsg.appendThinking(pl.Text)
			m.updateCurrentAssistant()
		}

	case "tool_call":
		var pl struct {
			ID    string `json:"id"`
			Kind  string `json:"kind"`
			Title string `json:"title"`
		}
		_ = json.Unmarshal(p.Event.Payload, &pl)

		// Finish current assistant text before tool, start fresh after.
		if m.currentMsg != nil {
			m.currentMsg.finish(chat.FinishReasonToolUse)
			m.updateCurrentAssistant()
		}

		// Add tool call as a generic tool message.
		toolMsg := newShimMessage(m.nextID("tool"), chat.RoleTool)
		tc := chat.ToolCall{ID: pl.ID, Name: pl.Kind, Input: fmt.Sprintf(`{"title":%q}`, pl.Title)}
		toolMsg.toolCalls = []chat.ToolCall{tc}
		m.chat.AppendMessages(chat.NewToolMessageItem(&m.sty, toolMsg.id, tc, nil, false))

		// Create new assistant message for text after tool.
		newMsg := newShimMessage(m.nextID("assistant"), chat.RoleAssistant)
		m.currentMsg = newMsg
		m.currentMsgID = newMsg.id
		m.chat.AppendMessages(chat.NewAssistantMessageItem(&m.sty, newMsg))

	case "tool_result":
		var pl struct {
			ID     string `json:"id"`
			Status string `json:"status"`
		}
		_ = json.Unmarshal(p.Event.Payload, &pl)

		// Add as system message for now (tool result rendering requires
		// linking to the original tool call which we don't track).
		m.chat.AppendMessages(chat.NewSystemItem(
			m.nextID("tr"),
			fmt.Sprintf("  ↳ %s", pl.Status),
			styleDim,
		))
	}
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
		bottom = "  " + m.spinner.View() + styleDim.Render(" waiting… (esc cancel)")
	} else {
		bottom = m.input.View()
	}

	help := m.renderHelp()

	v := tea.NewView(chatView + "\n" + divider + "\n" + bottom + "\n" + help)
	v.AltScreen = true
	v.MouseMode = tea.MouseModeCellMotion
	return v
}

func (m chatModel) renderHelp() string {
	var keys []string
	if m.chatFocused {
		keys = append(keys, "j/k scroll", "d/u half-page", "g/G top/bottom", "tab editor", "esc back")
	} else {
		keys = append(keys, "enter send", "shift+enter newline", "tab chat", "esc cancel")
	}
	keys = append(keys, "ctrl+c quit")
	return styleHelp.Render(" " + strings.Join(keys, " · "))
}

// ── Entry point ───────────────────────────────────────────────────────────────

func runChatTUI(sock string) error {
	p := tea.NewProgram(newChatModel(sock))
	_, err := p.Run()
	return err
}
