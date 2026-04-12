package shim

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
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
	viewport viewport.Model
	input    textarea.Model
	spinner  spinner.Model

	sock   string
	client *client
	notifs <-chan rpcResponse

	// lines holds rendered conversation history.
	// agentLineIdx is the index of the current streaming agent text line; -1 if none.
	lines        []string
	agentLineIdx int

	waiting bool
	ready   bool
	width   int
}

func newChatModel(sock string) chatModel {
	ta := textarea.New()
	ta.Placeholder = "Type a message… (Enter to send, Alt+Enter for newline)"
	ta.ShowLineNumbers = false
	ta.SetHeight(3)
	ta.Focus()
	// Remove the default border so we control layout ourselves.
	ta.FocusedStyle.Base = lipgloss.NewStyle()
	ta.BlurredStyle.Base = lipgloss.NewStyle()

	sp := spinner.New()
	sp.Spinner = spinner.Dot

	return chatModel{
		sock:         sock,
		input:        ta,
		spinner:      sp,
		agentLineIdx: -1,
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
		_, err := c.call("session/prompt", map[string]string{"prompt": text})
		if err != nil {
			return promptErrMsg{err}
		}
		return nil // completion arrives via turnEndMsg from notifs
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
		if !m.ready {
			m.viewport = viewport.New(msg.Width, vpHeight)
			m.input.SetWidth(msg.Width)
			m.ready = true
		} else {
			m.viewport.Width = msg.Width
			m.viewport.Height = vpHeight
			m.input.SetWidth(msg.Width)
		}
		m.syncViewport()

	case connReadyMsg:
		m.client = msg.c
		m.notifs = msg.notifs
		m.appendLine(styleDim.Render("connected — Esc to cancel, Ctrl+C to quit"))
		m.syncViewport()
		cmds = append(cmds, waitNotif(m.notifs))

	case connErrMsg:
		m.appendLine(styleErr.Render("error: " + msg.err.Error()))
		m.syncViewport()
		return m, tea.Quit

	case connClosedMsg:
		m.appendLine(styleDim.Render("connection closed"))
		m.syncViewport()
		return m, tea.Quit

	case notifMsg:
		m.handleNotif(msg.rpcResponse)
		m.syncViewport()
		cmds = append(cmds, waitNotif(m.notifs))

	case turnEndMsg:
		m.waiting = false
		m.agentLineIdx = -1
		m.input.Focus()
		cmds = append(cmds, waitNotif(m.notifs))

	case promptErrMsg:
		m.appendLine(styleErr.Render("error: " + msg.err.Error()))
		m.waiting = false
		m.agentLineIdx = -1
		m.input.Focus()
		m.syncViewport()
		cmds = append(cmds, waitNotif(m.notifs))

	case spinner.TickMsg:
		var spinCmd tea.Cmd
		m.spinner, spinCmd = m.spinner.Update(msg)
		cmds = append(cmds, spinCmd)

	case tea.KeyMsg:
		switch {
		case msg.Type == tea.KeyCtrlC:
			if m.client != nil {
				m.client.close()
			}
			return m, tea.Quit

		case msg.Type == tea.KeyEsc && m.waiting:
			m.appendLine(styleDim.Render("[cancelling…]"))
			m.syncViewport()
			cmds = append(cmds, cancelPromptCmd(m.client))

		case msg.Type == tea.KeyEnter && !msg.Alt:
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
			m.appendLine(styleUser.Render("You: ") + text)
			m.agentLineIdx = len(m.lines)
			m.appendLine(styleAgent.Render("Agent: "))
			m.syncViewport()
			m.waiting = true
			cmds = append(cmds, sendPromptCmd(m.client, text))

		case msg.Type == tea.KeyEnter && msg.Alt:
			// Alt+Enter: insert newline into textarea.
			if !m.waiting {
				var taCmd tea.Cmd
				m.input, taCmd = m.input.Update(tea.KeyMsg{Type: tea.KeyEnter})
				cmds = append(cmds, taCmd)
			}

		default:
			if !m.waiting {
				var taCmd tea.Cmd
				m.input, taCmd = m.input.Update(msg)
				cmds = append(cmds, taCmd)
			}
		}
	}

	var vpCmd tea.Cmd
	m.viewport, vpCmd = m.viewport.Update(msg)
	cmds = append(cmds, vpCmd)

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
		if m.agentLineIdx >= 0 && m.agentLineIdx < len(m.lines) {
			m.lines[m.agentLineIdx] += pl.Text
		}
	case "thinking":
		var pl textPayload
		_ = json.Unmarshal(p.Event.Payload, &pl)
		// Insert thinking line before the current agent line.
		if m.agentLineIdx >= 0 {
			thinking := styleThink.Render("  · " + pl.Text)
			m.lines = append(m.lines[:m.agentLineIdx],
				append([]string{thinking}, m.lines[m.agentLineIdx:]...)...)
			m.agentLineIdx++
		}
	case "tool_call":
		m.appendAfterAgent(styleTool.Render("  ⚙ " + string(p.Event.Payload)))
	case "tool_result":
		m.appendAfterAgent(styleResult.Render("  ↳ " + string(p.Event.Payload)))
	}
}

func (m *chatModel) appendLine(s string) {
	m.lines = append(m.lines, s)
}

// appendAfterAgent inserts a line after the current agent streaming line
// and advances agentLineIdx past it, seeding a new "Agent: " continuation.
func (m *chatModel) appendAfterAgent(s string) {
	if m.agentLineIdx < 0 || m.agentLineIdx >= len(m.lines) {
		m.appendLine(s)
		return
	}
	after := m.agentLineIdx + 1
	m.lines = append(m.lines[:after], append([]string{s}, m.lines[after:]...)...)
	m.agentLineIdx = after + 1
	if m.agentLineIdx >= len(m.lines) {
		m.lines = append(m.lines, styleAgent.Render("Agent: "))
	}
}

func (m *chatModel) syncViewport() {
	if !m.ready {
		return
	}
	m.viewport.SetContent(strings.Join(m.lines, "\n"))
	m.viewport.GotoBottom()
}

// ── View ──────────────────────────────────────────────────────────────────────

func (m chatModel) View() string {
	if !m.ready {
		return "\n  " + m.spinner.View() + " connecting…\n"
	}

	divider := styleDim.Render(strings.Repeat("─", m.width))

	var bottom string
	if m.waiting {
		bottom = "  " + m.spinner.View() + styleDim.Render(" waiting… (Esc to cancel)")
	} else {
		bottom = m.input.View()
	}

	return m.viewport.View() + "\n" + divider + "\n" + bottom
}

// ── Entry point ───────────────────────────────────────────────────────────────

func runChatTUI(sock string) error {
	p := tea.NewProgram(
		newChatModel(sock),
		tea.WithAltScreen(),
	)
	_, err := p.Run()
	return err
}
