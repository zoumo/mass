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
	styleUser   = lipgloss.NewStyle().Bold(true)
	styleAgent  = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("10"))
	styleThink  = lipgloss.NewStyle().Faint(true)
	styleTool   = lipgloss.NewStyle().Foreground(lipgloss.Color("11"))
	styleResult = lipgloss.NewStyle().Faint(true)
	styleErr    = lipgloss.NewStyle().Foreground(lipgloss.Color("9"))
	styleDim    = lipgloss.NewStyle().Faint(true)
)

const inputAreaHeight = 5 // textarea (3 rows) + divider + bottom hint

// ── Model ─────────────────────────────────────────────────────────────────────

type chatModel struct {
	chat    *chat.Chat
	input   textarea.Model
	spinner spinner.Model

	sock   string
	client *client
	notifs <-chan rpcResponse

	currentAssistantID string // ID of the current streaming assistant item
	turnCounter        int    // monotonic counter for generating unique IDs

	waiting bool
	ready   bool
	width   int
}

func (m *chatModel) nextID(prefix string) string {
	m.turnCounter++
	return fmt.Sprintf("%s-%d", prefix, m.turnCounter)
}

func newChatModel(sock string) chatModel {
	ta := textarea.New()
	ta.Placeholder = "Type a message… (Enter to send, Shift+Enter for newline)"
	ta.ShowLineNumbers = false
	ta.MaxHeight = 5
	ta.CharLimit = 0

	sp := spinner.New()

	return chatModel{
		sock:    sock,
		chat:    chat.New(),
		input:   ta,
		spinner: sp,
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
		m.chat.Append(chat.NewSystemItem(m.nextID("sys"), "connected — Esc to cancel, Ctrl+C to quit", styleDim))
		cmds = append(cmds, waitNotif(m.notifs))

	case connErrMsg:
		m.chat.Append(chat.NewSystemItem(m.nextID("sys"), "error: "+msg.err.Error(), styleErr))
		return m, tea.Quit

	case connClosedMsg:
		m.chat.Append(chat.NewSystemItem(m.nextID("sys"), "connection closed", styleDim))
		return m, tea.Quit

	case notifMsg:
		m.handleNotif(msg.rpcResponse)
		cmds = append(cmds, waitNotif(m.notifs))

	case turnEndMsg:
		m.waiting = false
		m.currentAssistantID = ""
		cmds = append(cmds, m.input.Focus(), waitNotif(m.notifs))

	case promptErrMsg:
		m.chat.Append(chat.NewSystemItem(m.nextID("sys"), "error: "+msg.err.Error(), styleErr))
		m.waiting = false
		m.currentAssistantID = ""
		cmds = append(cmds, m.input.Focus(), waitNotif(m.notifs))

	case spinner.TickMsg:
		var spinCmd tea.Cmd
		m.spinner, spinCmd = m.spinner.Update(msg)
		cmds = append(cmds, spinCmd)

	case tea.KeyPressMsg:
		key := tea.Key(msg)
		switch {
		case key.Code == tea.KeyEscape && key.Mod == 0:
			if m.waiting {
				m.chat.Append(chat.NewSystemItem(m.nextID("sys"), "[cancelling…]", styleDim))
				cmds = append(cmds, cancelPromptCmd(m.client))
			} else {
				// Not waiting — quit
				if m.client != nil {
					m.client.close()
				}
				return m, tea.Quit
			}

		case key.Mod&tea.ModCtrl != 0 && key.Code == 'c':
			if m.client != nil {
				m.client.close()
			}
			return m, tea.Quit

		case key.Code == tea.KeyEnter && key.Mod&tea.ModShift != 0:
			// Shift+Enter: insert newline into textarea.
			if !m.waiting {
				var taCmd tea.Cmd
				m.input, taCmd = m.input.Update(msg)
				cmds = append(cmds, taCmd)
			}

		case key.Code == tea.KeyEnter && key.Mod == 0:
			// Plain Enter: send message.
			if m.waiting || m.client == nil {
				break
			}
			text := strings.TrimSpace(m.input.Value())
			if text == "" {
				break
			}
			m.input.Reset()
			m.input.Blur()

			m.chat.Append(chat.NewUserItem(m.nextID("user"), text, styleUser))

			id := m.nextID("assistant")
			m.currentAssistantID = id
			m.chat.Append(chat.NewAssistantItem(id, styleAgent))

			m.waiting = true
			cmds = append(cmds, sendPromptCmd(m.client, text))

		case key.Code == tea.KeyPgUp:
			m.chat.ScrollBy(-m.chat.Height())

		case key.Code == tea.KeyPgDown:
			m.chat.ScrollBy(m.chat.Height())

		default:
			if !m.waiting {
				var taCmd tea.Cmd
				m.input, taCmd = m.input.Update(msg)
				cmds = append(cmds, taCmd)
			}
		}
	}

	return m, tea.Batch(cmds...)
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
		m.chat.Update(m.currentAssistantID, func(item chat.MessageItem) {
			if a, ok := item.(*chat.AssistantItem); ok {
				a.AppendText(pl.Text)
			}
		})

	case "thinking":
		var pl textPayload
		_ = json.Unmarshal(p.Event.Payload, &pl)
		m.chat.Append(chat.NewThinkingItem(m.nextID("think"), pl.Text, styleThink))

	case "tool_call":
		var pl struct {
			ID    string `json:"id"`
			Kind  string `json:"kind"`
			Title string `json:"title"`
		}
		_ = json.Unmarshal(p.Event.Payload, &pl)
		m.chat.Append(chat.NewToolCallItem(m.nextID("tc"), pl.Kind, pl.Title, styleTool))

		// Start a new assistant item for text that may follow the tool call.
		id := m.nextID("assistant")
		m.currentAssistantID = id
		m.chat.Append(chat.NewAssistantItem(id, styleAgent))

	case "tool_result":
		var pl struct {
			ID     string `json:"id"`
			Status string `json:"status"`
		}
		_ = json.Unmarshal(p.Event.Payload, &pl)
		m.chat.Append(chat.NewToolResultItem(m.nextID("tr"), pl.Status, styleResult))
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
		bottom = "  " + m.spinner.View() + styleDim.Render(" waiting… (Esc to cancel)")
	} else {
		bottom = m.input.View()
	}

	v := tea.NewView(chatView + "\n" + divider + "\n" + bottom)
	v.AltScreen = true
	return v
}

// ── Entry point ───────────────────────────────────────────────────────────────

func runChatTUI(sock string) error {
	p := tea.NewProgram(newChatModel(sock))
	_, err := p.Run()
	return err
}
