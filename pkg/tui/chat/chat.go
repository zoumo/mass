// Package chat provides an interactive chat TUI that communicates with
// a running agent-shim over its Unix socket JSON-RPC interface.
package chat

import (
	"context"
	"fmt"
	"os"
	"runtime/debug"
	"strings"

	"charm.land/bubbles/v2/spinner"
	"charm.land/bubbles/v2/textarea"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	shimapi "github.com/zoumo/mass/pkg/shim/api"
	shimclient "github.com/zoumo/mass/pkg/shim/client"
	"github.com/zoumo/mass/pkg/tui/component"
	"github.com/zoumo/mass/third_party/charmbracelet/crush/ui/anim"
	"github.com/zoumo/mass/third_party/charmbracelet/crush/ui/styles"
)

// ── Tea messages ──────────────────────────────────────────────────────────────

type (
	notifMsg      struct{ ev shimapi.AgentRunEvent }
	turnEndMsg    struct{}
	connClosedMsg struct{}
	connReadyMsg struct {
		sc      *shimclient.ShimClient
		watcher *shimclient.Watcher
	}
)

type (
	connErrMsg   struct{ err error }
	promptErrMsg struct{ err error }
	panicMsg     struct{ err error }
)

// safeCmd wraps a tea.Cmd with panic recovery. If the inner command panics,
// the stack trace is printed to stderr and a panicMsg is returned so the TUI
// can display the error and exit gracefully.
func safeCmd(cmd tea.Cmd) tea.Cmd {
	return func() (result tea.Msg) {
		defer func() {
			if r := recover(); r != nil {
				fmt.Fprintf(os.Stderr, "\n[tea.Cmd] PANIC: %v\n%s\n", r, debug.Stack())
				result = panicMsg{err: fmt.Errorf("panic: %v", r)}
			}
		}()
		return cmd()
	}
}

// stateChangeMsg is sent when the shim reports a runtime_update event with Status.
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
	chat    *component.Chat
	input   textarea.Model
	spinner spinner.Model
	sty     styles.Styles

	sock    string
	client  *shimclient.ShimClient
	watcher *shimclient.Watcher

	// Streaming state.
	currentMsg   *StreamingMessage // mutable message being streamed
	currentMsgID string            // chat item ID for assistant message

	// Tool tracking: tool_call ID → chat item ID, for linking tool_result.
	toolItemIDs map[string]string // toolCall.ID → chat MessageItem ID

	// sentPrompt is true when the most recent prompt was sent from this chat
	// instance. Used to skip the user_message broadcast (we already show it).
	sentPrompt bool

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
		chat:        component.NewChat(),
		input:       ta,
		spinner:     sp,
		sty:         sty,
		toolItemIDs: make(map[string]string),
	}
}

// ── Init ──────────────────────────────────────────────────────────────────────

func (m chatModel) Init() tea.Cmd {
	// NOTE: Do not call m.input.Focus() here — Init() has a value receiver,
	// so the mutation is applied to a copy and discarded. Input focus is
	// established in the first WindowSizeMsg handler instead.
	return tea.Batch(m.spinner.Tick, connectCmd(m.sock))
}

func connectCmd(sock string) tea.Cmd {
	return safeCmd(func() tea.Msg {
		ctx := context.Background()
		sc, err := shimclient.Dial(ctx, sock)
		if err != nil {
			return connErrMsg{fmt.Errorf("connect: %w", err)}
		}
		// WatchEvent returns a Watcher with typed events via ResultChan().
		// The Watcher handles JSON deserialization and watchID filtering internally.
		watcher, err := sc.WatchEvent(ctx, nil)
		if err != nil {
			sc.Close()
			return connErrMsg{fmt.Errorf("session/watch_event: %w", err)}
		}
		return connReadyMsg{sc: sc, watcher: watcher}
	})
}

// waitNotif reads the next event from the Watcher's ResultChan and converts it
// to the appropriate tea.Msg. The Watcher has already deserialized the raw
// JSON-RPC notification into a typed AgentRunEvent, so no JSON parsing is needed.
//
// The loop skips unknown events to avoid returning nil (which would break the
// Bubbletea notification chain — see CLAUDE.md "waitNotif 必须内部循环").
// When ResultChan closes (connection lost, slow consumer eviction, or explicit
// Stop()), connClosedMsg is returned.
func waitNotif(w *shimclient.Watcher) tea.Cmd {
	return safeCmd(func() tea.Msg {
		for {
			ev, ok := <-w.ResultChan()
			if !ok {
				return connClosedMsg{}
			}
			// turn_end → dedicated message so Update can finalize the assistant.
			if ev.Type == shimapi.EventTypeTurnEnd {
				return turnEndMsg{}
			}
			// runtime_update with Status → dedicated message for status bar updates.
			if ev.Type == shimapi.EventTypeRuntimeUpdate {
				if ru, ok := ev.Payload.(shimapi.RuntimeUpdateEvent); ok && ru.Status != nil {
					return stateChangeMsg{
						previous: ru.Status.PreviousStatus,
						status:   ru.Status.Status,
						reason:   ru.Status.Reason,
					}
				}
			}
			return notifMsg{ev: ev}
		}
	})
}

// watchDisconnect returns a tea.Cmd that blocks until the shim connection
// drops, then emits connClosedMsg.
func watchDisconnect(sc *shimclient.ShimClient) tea.Cmd {
	return func() tea.Msg {
		<-sc.DisconnectNotify()
		return connClosedMsg{}
	}
}

func sendPromptCmd(sc *shimclient.ShimClient, text string) tea.Cmd {
	return safeCmd(func() tea.Msg {
		if err := sc.SendPrompt(context.Background(), &shimapi.SessionPromptParams{Prompt: text}); err != nil {
			return promptErrMsg{err}
		}
		return nil
	})
}

func cancelPromptCmd(sc *shimclient.ShimClient) tea.Cmd {
	return safeCmd(func() tea.Msg {
		_ = sc.Cancel(context.Background())
		return nil
	})
}

func fetchStatusCmd(sc *shimclient.ShimClient) tea.Cmd {
	return safeCmd(func() tea.Msg {
		result, err := sc.Status(context.Background())
		if err != nil {
			return nil
		}
		return stateChangeMsg{status: string(result.State.Status)}
	})
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
		if !m.ready {
			// First WindowSizeMsg: focus the input here because Init() has a
			// value receiver — m.input.Focus() in Init mutates a copy that is
			// discarded, leaving the textarea unfocused.
			m.ready = true
			cmds = append(cmds, m.input.Focus())
		}

	case connReadyMsg:
		m.client = msg.sc
		m.watcher = msg.watcher
		m.chat.AppendMessages(component.NewSystemItem(m.nextID("sys"), "connected — tab focus · shift+click select text · ctrl+c quit", styleDim))
		cmds = append(cmds, waitNotif(m.watcher), fetchStatusCmd(m.client), watchDisconnect(m.client))

	case panicMsg:
		m.chat.AppendMessages(component.NewSystemItem(m.nextID("sys"), "fatal: "+msg.err.Error(), styleErr))
		return m, tea.Quit

	case connErrMsg:
		m.chat.AppendMessages(component.NewSystemItem(m.nextID("sys"), "error: "+msg.err.Error(), styleErr))
		return m, tea.Quit

	case connClosedMsg:
		m.chat.AppendMessages(component.NewSystemItem(m.nextID("sys"), "connection closed", styleDim))
		return m, tea.Quit

	case notifMsg:
		cmd := m.handleNotif(msg.ev)
		if cmd != nil {
			cmds = append(cmds, cmd)
		}
		cmds = append(cmds, waitNotif(m.watcher))

	case turnEndMsg:
		if m.currentMsg != nil {
			m.currentMsg.Finish(component.FinishReasonEndTurn)
			m.updateCurrentAssistant()
		}
		m.currentMsg = nil
		m.currentMsgID = ""
		m.waiting = false
		m.chatFocused = false
		cmds = append(cmds, m.input.Focus(), waitNotif(m.watcher))

	case promptErrMsg:
		m.chat.AppendMessages(component.NewSystemItem(m.nextID("sys"), "error: "+msg.err.Error(), styleErr))
		if m.currentMsg != nil {
			m.currentMsg.Finish(component.FinishReasonError)
			m.updateCurrentAssistant()
		}
		m.currentMsg = nil
		m.currentMsgID = ""
		m.waiting = false
		m.chatFocused = false
		cmds = append(cmds, m.input.Focus(), waitNotif(m.watcher))

	case stateChangeMsg:
		m.agentStatus = msg.status
		if msg.status == "running" && !m.waiting {
			m.waiting = true
			m.input.Blur()
		}
		if msg.status == "idle" && m.waiting {
			m.waiting = false
			m.chatFocused = false
			cmds = append(cmds, m.input.Focus())
		}
		cmds = append(cmds, waitNotif(m.watcher)) // keep the notification chain alive

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

	case tea.PasteMsg:
		// Forward paste events to textarea.
		if !m.waiting && !m.chatFocused {
			var taCmd tea.Cmd
			m.input, taCmd = m.input.Update(msg)
			cmds = append(cmds, taCmd)
		}

	case tea.PasteStartMsg, tea.PasteEndMsg:
		// Forward bracketed paste markers to textarea.
		if !m.waiting && !m.chatFocused {
			var taCmd tea.Cmd
			m.input, taCmd = m.input.Update(msg)
			cmds = append(cmds, taCmd)
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
			m.client.Close()
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
		// Esc only switches focus, never cancels. Use Ctrl+X to cancel.
		if m.chatFocused {
			m.chatFocused = false
			m.chat.Blur()
			cmds = append(cmds, m.input.Focus())
		}
		return cmds

	case key.Mod&tea.ModCtrl != 0 && key.Code == 'x':
		// Ctrl+X: explicit cancel of running turn.
		if m.waiting && m.client != nil {
			m.chat.AppendMessages(component.NewSystemItem(m.nextID("sys"), "[canceling…]", styleDim))
			cmds = append(cmds, cancelPromptCmd(m.client))
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
		case key.Code == ' ':
			// Space toggles expand on selected tool item, or scrolls if not expandable.
			m.chat.ToggleExpandedSelectedItem()
		case key.Code == 'f' || key.Code == tea.KeyPgDown:
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
		// Shift+Enter: insert newline.
		if !m.waiting {
			m.input.InsertRune('\n')
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

		userMsg := NewFinishedStreamingMessage(m.nextID("user"), component.RoleUser, text)
		m.chat.AppendMessages(component.NewUserMessageItem(&m.sty, userMsg))

		assistantMsg := NewStreamingMessage(m.nextID("assistant"), component.RoleAssistant)
		m.currentMsg = assistantMsg
		m.currentMsgID = assistantMsg.id
		item := component.NewAssistantMessageItem(&m.sty, assistantMsg)
		m.chat.AppendMessages(item)

		// Start the spinner animation for the new assistant item.
		if a, ok := item.(*component.AssistantMessageItem); ok {
			if cmd := a.StartAnimation(); cmd != nil {
				cmds = append(cmds, cmd)
			}
		}

		m.waiting = true
		m.sentPrompt = true
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
	if a, ok := item.(*component.AssistantMessageItem); ok {
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
	msg := NewStreamingMessage(m.nextID("assistant"), component.RoleAssistant)
	m.currentMsg = msg
	m.currentMsgID = msg.id
	item := component.NewAssistantMessageItem(&m.sty, msg)
	m.chat.AppendMessages(item)
	if a, ok := item.(*component.AssistantMessageItem); ok {
		return a.StartAnimation()
	}
	return nil
}

// contentBlockText extracts the text string from a ContentBlock for display.
func contentBlockText(cb shimapi.ContentBlock) string {
	if cb.Text != nil {
		return cb.Text.Text
	}
	return ""
}

func (m *chatModel) handleNotif(ev shimapi.AgentRunEvent) tea.Cmd {
	// runtime_update events are handled by waitNotif; skip here.
	if ev.Type == shimapi.EventTypeRuntimeUpdate {
		return nil
	}

	var cmds []tea.Cmd

	switch pl := ev.Payload.(type) {
	case shimapi.ContentEvent:
		switch ev.Type {
		case shimapi.EventTypeUserMessage:
			// User prompt broadcast. Skip if we sent this prompt (already shown).
			if m.sentPrompt {
				m.sentPrompt = false
				break
			}
			if text := contentBlockText(pl.Content); text != "" {
				userMsg := NewFinishedStreamingMessage(m.nextID("user"), component.RoleUser, text)
				m.chat.AppendMessages(component.NewUserMessageItem(&m.sty, userMsg))
			}

		case shimapi.EventTypeAgentMessage:
			if cmd := m.ensureCurrentMsg(); cmd != nil {
				cmds = append(cmds, cmd)
			}
			m.currentMsg.AppendText(contentBlockText(pl.Content))
			m.updateCurrentAssistant()

		case shimapi.EventTypeAgentThinking:
			if cmd := m.ensureCurrentMsg(); cmd != nil {
				cmds = append(cmds, cmd)
			}
			m.currentMsg.AppendThinking(contentBlockText(pl.Content))
			m.updateCurrentAssistant()
		}

	case shimapi.ToolCallEvent:
		// Finish current assistant text before tool.
		if m.currentMsg != nil {
			m.currentMsg.Finish(component.FinishReasonToolUse)
			m.updateCurrentAssistant()
		}

		// Build input from event data for display.
		toolItemID := m.nextID("tc")
		kind := DeriveToolKind(pl.Kind, pl.Title)
		input := BuildInput(ToolDisplayTitle(pl.Kind, pl.Title), pl.Locations)
		tc := component.ToolCall{ID: pl.ID, Name: kind, Input: input, Finished: true}

		// ToolCallEvent's Content/RawOutput carry the tool's execution result.
		// Pre-populate the result so it displays immediately without waiting
		// for the separate ToolResultEvent (which may add/override later).
		var initResult *component.ToolResult
		resultContent := BuildResultContent(pl.Content, pl.Status, pl.RawOutput)
		diffData := ExtractDiff(pl.Content)
		if resultContent != "" || diffData != nil {
			initResult = &component.ToolResult{ToolCallID: pl.ID, Content: resultContent, Diff: diffData}
		}

		toolItem := component.NewToolMessageItem(&m.sty, toolItemID, tc, initResult, false)
		// Our tool_call event means the tool was already invoked. Set initial
		// status to Success to avoid showing "Waiting for tool response...".
		// The actual status will be overwritten when tool_result arrives.
		toolItem.SetStatus(component.ToolStatusSuccess)
		m.chat.AppendMessages(toolItem)
		// Store the ACP tool call ID (not the internal tc-N ID) because
		// Chat.MessageItem() indexes items by item.ID(), which for tool items
		// returns toolCall.ID (the ACP ID).
		m.toolItemIDs[pl.ID] = tc.ID

		// Create new assistant message for post-tool content.
		// If no text arrives, it renders as just the spinner animation
		// (no [Agent] label for empty messages — handled in Render).
		newMsg := NewStreamingMessage(m.nextID("assistant"), component.RoleAssistant)
		m.currentMsg = newMsg
		m.currentMsgID = newMsg.id
		item := component.NewAssistantMessageItem(&m.sty, newMsg)
		m.chat.AppendMessages(item)
		if a, ok := item.(*component.AssistantMessageItem); ok {
			if cmd := a.StartAnimation(); cmd != nil {
				cmds = append(cmds, cmd)
			}
		}

	case shimapi.ToolResultEvent:
		// Find the matching tool call item and merge updates.
		// ACP sends multiple tool_result events per tool (metadata, intermediate,
		// completed). Only update fields when the new event carries actual data
		// to avoid overwriting previously populated values with empty ones.
		if itemID, ok := m.toolItemIDs[pl.ID]; ok {
			if item := m.chat.MessageItem(itemID); item != nil {
				if ti, ok := item.(component.ToolMessageItem); ok {
					// Update title/kind/locations from result if available.
					if pl.Title != "" || pl.Kind != "" || len(pl.Locations) > 0 {
						tc := ti.ToolCall()
						if pl.Title != "" {
							tc.Input = BuildInput(pl.Title, pl.Locations)
						}
						if pl.Kind != "" {
							tc.Name = pl.Kind
						}
						ti.SetToolCall(tc)
					}
					// Only update result when there's actual content.
					// Preserve existing diff when new event only carries
					// plain text (e.g. "has been updated successfully").
					resultContent := BuildResultContent(pl.Content, pl.Status, pl.RawOutput)
					diffData := ExtractDiff(pl.Content)
					if resultContent != "" || diffData != nil {
						// If a previous event already set a diff and this
						// event has none, keep the existing diff.
						if diffData == nil {
							if prev := ti.Result(); prev != nil && prev.Diff != nil {
								diffData = prev.Diff
							}
						}
						ti.SetResult(&component.ToolResult{
							ToolCallID: pl.ID,
							Content:    resultContent,
							Diff:       diffData,
						})
					}
					// Update status only for terminal states.
					if pl.Status == "completed" {
						ti.SetStatus(component.ToolStatusSuccess)
					} else if pl.Status == "error" {
						ti.SetStatus(component.ToolStatusError)
					}
				}
			}
		}
		if m.chat.Follow() {
			m.chat.ScrollToBottom()
		}

	case shimapi.PlanEvent:
		if len(pl.Entries) > 0 {
			chatEntries := make([]component.PlanEntry, len(pl.Entries))
			for i, e := range pl.Entries {
				chatEntries[i] = component.PlanEntry{
					Title:  e.Content,
					Status: string(e.Status),
				}
			}
			m.chat.AppendMessages(component.NewPlanItem(m.nextID("plan"), chatEntries, m.sty.Info))
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
		hint = styleDim.Render(" — ctrl+x to cancel")
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
		keys = append(keys, "enter send", "shift+enter newline", "tab chat", "ctrl+x cancel")
	}
	keys = append(keys, "shift+click select text", "ctrl+c quit")
	return styleHelp.Render(" " + strings.Join(keys, " · "))
}

// ── Entry point ───────────────────────────────────────────────────────────────

// RunChatTUI launches the interactive chat TUI connected to the given shim socket.
func RunChatTUI(sock string) error {
	p := tea.NewProgram(newChatModel(sock))
	_, err := p.Run()
	return err
}
