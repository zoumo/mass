// Package chat provides an interactive chat TUI that communicates with
// a running agent-run over its Unix socket JSON-RPC interface.
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

	runapi "github.com/zoumo/mass/pkg/agentrun/api"
	runclient "github.com/zoumo/mass/pkg/agentrun/client"
	apiruntime "github.com/zoumo/mass/pkg/runtime-spec/api"
	"github.com/zoumo/mass/pkg/tui/component"
	"github.com/zoumo/mass/pkg/watch"
	"github.com/zoumo/mass/third_party/charmbracelet/crush/ui/anim"
	"github.com/zoumo/mass/third_party/charmbracelet/crush/ui/styles"
)

// ── Tea messages ──────────────────────────────────────────────────────────────

type (
	notifMsg      struct{ ev runapi.AgentRunEvent }
	turnEndMsg    struct{}
	connClosedMsg struct{}
	connReadyMsg  struct {
		sc *runclient.Client
	}
	watchStoppedMsg struct{} // WatchClient stopped (ctx canceled), no reconnect needed
)

type (
	connErrMsg     struct{ err error }
	promptErrMsg   struct{ err error }
	setModelResult struct{ modelID string }
	panicMsg       struct{ err error }
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

// stateChangeMsg is sent when the agent-run reports a runtime_update event with Status.
type stateChangeMsg struct {
	previous string
	status   string
	reason   string
	seq      int // event sequence number, used to distinguish replay vs live
}

// initialStatusMsg is sent by fetchStatusCmd. It is a separate type from
// stateChangeMsg to prevent fetchStatusCmd from spawning a duplicate waitNotif
// chain — only stateChangeMsg (from the event stream) re-schedules waitNotif.
type initialStatusMsg struct {
	status            string
	availableCommands []apiruntime.AvailableCommand
	currentModel      string
	availableModels   []apiruntime.ModelInfo
}

// agentCommandsMsg is sent when RuntimeUpdateEvent carries AvailableCommands.
type agentCommandsMsg struct {
	commands []runapi.AvailableCommand
}

// ── Styles ────────────────────────────────────────────────────────────────────

var (
	styleErr  = lipgloss.NewStyle().Foreground(lipgloss.Color("9"))
	styleDim  = lipgloss.NewStyle().Faint(true)
	styleHelp = lipgloss.NewStyle().Faint(true)
)

const inputAreaHeight = 7 // divider + header + divider + input(1 line) + divider + statusbar(2 lines)

// ── Model ─────────────────────────────────────────────────────────────────────

type chatModel struct {
	chat    *component.Chat
	input   textarea.Model
	spinner spinner.Model
	sty     styles.Styles

	sock          string
	workspaceName string
	agentName     string
	client        *runclient.Client
	watcher       *watch.RetryWatcher[runapi.AgentRunEvent]

	// Streaming state.
	currentMsg   *StreamingMessage // mutable message being streamed
	currentMsgID string            // chat item ID for assistant message

	// Tool tracking: tool_call ID → chat item ID, for linking tool_result.
	toolItemIDs map[string]string // toolCall.ID → chat MessageItem ID

	// sentPrompt is true when the most recent prompt was sent from this chat
	// instance. Used to skip the user_message broadcast (we already show it).
	sentPrompt bool

	turnCounter int // monotonic counter for generating unique IDs

	agentStatus     string // current agent status: "idle", "running", "stopped", "error"
	agentCommands   []agentCommand
	currentModel    string                 // current model ID from session state
	availableModels []apiruntime.ModelInfo // model list from session state, used for /model completion

	// liveSeq is the cursor snapshot taken when initialStatusMsg is received.
	// Events with seq <= liveSeq are historical replay; events with seq > liveSeq are live.
	// Set once when initialStatusMsg is processed.
	liveSeq int
	// initialStatusApplied is true once initialStatusMsg has been processed.
	// After this point, historical stateChangeMsgs (seq <= liveSeq) that would
	// set status to "running" are ignored — they cannot reflect current state.
	initialStatusApplied bool

	// watchReading is true once the first waitNotif has been started.
	// On RPC reconnects (connReadyMsg), we skip adding a second waitNotif
	// because the existing goroutine is still alive on wc.Events().
	watchReading bool

	completion       *completionState
	completionArgCmd string // non-empty when completion is showing args for this command name

	history     *History
	infoMessage *infoMessage

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

// cleanup closes the RPC client and cancels the WatchClient context.
// Must be called on all quit paths to avoid leaking goroutines.
func (m *chatModel) cleanup() {
	if m.client != nil {
		_ = m.client.Close()
		m.client = nil
	}
	if m.watcher != nil {
		m.watcher.Stop()
		m.watcher = nil
	}
}

func newChatModel(opts ChatTUIOptions) chatModel {
	ta := textarea.New()
	ta.Placeholder = "Type a message…"
	ta.ShowLineNumbers = false
	ta.MaxHeight = 15
	ta.MinHeight = 1
	ta.DynamicHeight = true
	ta.CharLimit = 0

	sp := spinner.New()
	sty := styles.DefaultStyles()

	watcher := watch.NewRetryWatcher(
		context.Background(),
		runclient.NewWatchFunc(opts.SocketPath),
		-1,
		func(ev runapi.AgentRunEvent) int { return ev.Seq },
		4096,
	)

	return chatModel{
		sock:          opts.SocketPath,
		workspaceName: opts.WorkspaceName,
		agentName:     opts.AgentName,
		chat:          component.NewChat(),
		input:         ta,
		spinner:       sp,
		sty:           sty,
		completion:    newCompletionState(&sty),
		history:       NewHistory(100),
		toolItemIDs:   make(map[string]string),
		watcher:       watcher,
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
	return reconnectCmd(sock)
}

// reconnectCmd dials the agent-run socket and returns the RPC client.
// The event stream is handled independently by WatchClient.
func reconnectCmd(sock string) tea.Cmd {
	return safeCmd(func() tea.Msg {
		ctx := context.Background()
		sc, err := runclient.Dial(ctx, sock)
		if err != nil {
			return connErrMsg{fmt.Errorf("connect: %w", err)}
		}
		return connReadyMsg{sc: sc}
	})
}

// waitNotif reads the next event from the WatchClient's Events channel and
// converts it to the appropriate tea.Msg. The WatchClient handles reconnection
// automatically, so watchStoppedMsg is only returned when the channel is closed
// (i.e., the WatchClient context was canceled). connClosedMsg is returned only
// by watchDisconnect (RPC disconnect), to trigger a reconnect.
//
// The loop skips unrecognized runtime_update subtypes via continue;
// all other events are always mapped to a non-nil tea.Msg.
func waitNotif(watcher *watch.RetryWatcher[runapi.AgentRunEvent]) tea.Cmd {
	return safeCmd(func() tea.Msg {
		for {
			inner, ok := <-watcher.ResultChan()
			if !ok {
				return watchStoppedMsg{}
			}
			// turn_end → dedicated message so Update can finalize the assistant.
			if inner.Type == runapi.EventTypeTurnEnd {
				return turnEndMsg{}
			}
			// runtime_update with Status → dedicated message for status bar updates.
			if inner.Type == runapi.EventTypeRuntimeUpdate {
				if ru, ok := inner.Payload.(runapi.RuntimeUpdateEvent); ok {
					if ru.Phase != nil {
						return stateChangeMsg{
							previous: ru.Phase.PreviousPhase,
							status:   ru.Phase.Phase,
							reason:   ru.Phase.Reason,
							seq:      inner.Seq,
						}
					}
					if ru.AvailableCommands != nil {
						return agentCommandsMsg{commands: ru.AvailableCommands.Commands}
					}
				}
				// Skip unrecognized runtime_update subtypes.
				continue
			}
			return notifMsg{ev: inner}
		}
	})
}

// watchDisconnect returns a tea.Cmd that blocks until the agent-run connection
// drops, then emits connClosedMsg.
func watchDisconnect(sc *runclient.Client) tea.Cmd {
	return func() tea.Msg {
		<-sc.DisconnectNotify()
		return connClosedMsg{}
	}
}

func sendPromptCmd(sc *runclient.Client, text string) tea.Cmd {
	return safeCmd(func() tea.Msg {
		if err := sc.SendPrompt(context.Background(), &runapi.SessionPromptParams{
			Prompt: []runapi.ContentBlock{runapi.TextBlock(text)},
		}); err != nil {
			return promptErrMsg{err}
		}
		return nil
	})
}

func cancelPromptCmd(sc *runclient.Client) tea.Cmd {
	return safeCmd(func() tea.Msg {
		_ = sc.Cancel(context.Background())
		return nil
	})
}

func fetchStatusCmd(sc *runclient.Client) tea.Cmd {
	return safeCmd(func() tea.Msg {
		result, err := sc.Status(context.Background())
		if err != nil {
			return nil
		}
		msg := initialStatusMsg{status: string(result.State.Phase)}
		if result.State.Session != nil {
			msg.availableCommands = result.State.Session.AvailableCommands
			if result.State.Session.Models != nil {
				msg.currentModel = result.State.Session.Models.CurrentModelId
				msg.availableModels = result.State.Session.Models.AvailableModels
			}
		}
		return msg
	})
}

// ── Update ────────────────────────────────────────────────────────────────────

func (m chatModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmds []tea.Cmd

	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.recalcViewport()
		m.input.SetWidth(msg.Width)
		if !m.ready {
			// First WindowSizeMsg: focus the input here because Init() has a
			// value receiver — m.input.Focus() in Init mutates a copy that is
			// discarded, leaving the textarea unfocused.
			m.ready = true
			cmds = append(cmds, m.input.Focus())
		}

	case connReadyMsg:
		if m.client != nil {
			_ = m.client.Close()
		}
		m.client = msg.sc
		m.liveSeq = 0                  // reset for re-snapshot on next initialStatusMsg
		m.initialStatusApplied = false // reset so replayed stateChangeMsg events are not filtered during reconnect
		cmds = append(cmds, fetchStatusCmd(m.client), watchDisconnect(m.client))
		if !m.watchReading {
			m.watchReading = true
			cmds = append(cmds, waitNotif(m.watcher))
		}

	case panicMsg:
		m.chat.AppendMessages(component.NewSystemItem(m.nextID("sys"), "fatal: "+msg.err.Error(), styleErr))
		m.cleanup()
		return m, tea.Quit

	case connErrMsg:
		m.chat.AppendMessages(component.NewSystemItem(m.nextID("sys"), "error: "+msg.err.Error(), styleErr))
		m.cleanup()
		return m, tea.Quit

	case connClosedMsg:
		m.chat.AppendMessages(component.NewSystemItem(m.nextID("sys"), "reconnecting...", styleDim))
		return m, reconnectCmd(m.sock)

	case watchStoppedMsg:
		// WatchClient stopped (ctx canceled); no reconnect, no action needed.
		return m, nil

	case notifMsg:
		cmd := m.handleNotif(msg.ev)
		if cmd != nil {
			cmds = append(cmds, cmd)
		}
		cmds = append(cmds, waitNotif(m.watcher))

	case turnEndMsg:
		// Finalize the streaming assistant message for UI rendering only.
		// Do NOT touch m.waiting here — state is driven solely by
		// runtime_update{status} events (stateChangeMsg). The matching
		// stateChangeMsg{idle} will clear waiting and re-focus the input.
		if m.currentMsg != nil {
			m.currentMsg.Finish(component.FinishReasonEndTurn)
			if cmd := m.updateCurrentAssistant(); cmd != nil {
				cmds = append(cmds, cmd)
			}
		}
		m.currentMsg = nil
		m.currentMsgID = ""
		cmds = append(cmds, waitNotif(m.watcher))

	case setModelResult:
		m.currentModel = msg.modelID
		m.chat.AppendMessages(component.NewSystemItem(m.nextID("sys"),
			fmt.Sprintf("model switched to %s", msg.modelID), styleDim))

	case promptErrMsg:
		m.chat.AppendMessages(component.NewSystemItem(m.nextID("sys"), "error: "+msg.err.Error(), styleErr))
		if m.currentMsg != nil {
			m.currentMsg.Finish(component.FinishReasonError)
			if cmd := m.updateCurrentAssistant(); cmd != nil {
				cmds = append(cmds, cmd)
			}
		}
		m.currentMsg = nil
		m.currentMsgID = ""
		m.waiting = false
		m.chatFocused = false
		cmds = append(cmds, m.input.Focus(), waitNotif(m.watcher))

	case stateChangeMsg:
		isHistorical := m.liveSeq > 0 && msg.seq <= m.liveSeq
		// Historical replay events that arrive after initialStatusMsg was applied
		// must not override the authoritative current status. Specifically, a
		// historical "running" event from an incomplete past turn (e.g. crash
		// with no matching "idle") would permanently stuck the TUI in waiting
		// mode even though the agent is actually idle right now.
		if isHistorical && m.initialStatusApplied {
			cmds = append(cmds, waitNotif(m.watcher))
			break
		}
		m.agentStatus = msg.status
		if msg.status == string(apiruntime.PhaseRunning) && !m.waiting {
			m.waiting = true
			m.input.Blur()
		}
		if msg.status == string(apiruntime.PhaseIdle) && m.waiting {
			m.waiting = false
			m.chatFocused = false
			cmds = append(cmds, m.input.Focus())
		}
		cmds = append(cmds, waitNotif(m.watcher)) // keep the notification chain alive

	case initialStatusMsg:
		// Initial status from fetchStatusCmd — authoritative current state.
		// Snapshot the current cursor as the live boundary: events with seq <= liveSeq
		// are historical replay; events with seq > liveSeq are live.
		// Do NOT re-schedule waitNotif (that would create a duplicate chain).
		m.liveSeq = m.watcher.Cursor()
		m.agentStatus = msg.status
		m.currentModel = msg.currentModel
		m.availableModels = msg.availableModels
		m.updateAgentCommands(msg.availableCommands)
		m.initialStatusApplied = true
		switch msg.status {
		case string(apiruntime.PhaseRunning):
			if !m.waiting {
				m.waiting = true
				m.input.Blur()
			}
		default:
			// idle / stopped / error — override any waiting state set by
			// historical replay events that arrived before this message.
			if m.waiting {
				m.waiting = false
				m.chatFocused = false
				cmds = append(cmds, m.input.Focus())
			}
		}

	case agentCommandsMsg:
		m.updateAgentCommands(msg.commands)
		cmds = append(cmds, waitNotif(m.watcher))

	case anim.StepMsg:
		// Forward animation ticks to the chat (for spinner animations).
		if cmd := m.chat.Animate(msg); cmd != nil {
			cmds = append(cmds, cmd)
		}

	case spinner.TickMsg:
		var spinCmd tea.Cmd
		m.spinner, spinCmd = m.spinner.Update(msg)
		cmds = append(cmds, spinCmd)

	case infoExpiredMsg:
		m.infoMessage = nil

	case tea.MouseClickMsg:
		mouse := msg.Mouse()
		if mouse.Button == tea.MouseLeft && mouse.Mod&tea.ModShift == 0 {
			m.chat.HandleMouseClick(mouse.X, mouse.Y)
		}

	case tea.MouseWheelMsg:
		mouse := msg.Mouse()
		switch mouse.Button {
		case tea.MouseWheelUp:
			cmds = append(cmds, m.chat.ScrollByAndAnimate(-3))
		case tea.MouseWheelDown:
			cmds = append(cmds, m.chat.ScrollByAndAnimate(3))
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
		m.cleanup()
		cmds = append(cmds, tea.Quit)
		return cmds

	case key.Code == tea.KeyTab && key.Mod == 0:
		// When completion is active, Tab confirms the selected entry.
		if !m.waiting && m.completion.Active() {
			if entry := m.completion.Selected(); entry != nil {
				if m.completionArgCmd != "" {
					// Arg completion: confirm the argument value and close popup.
					m.input.SetValue("/" + m.completionArgCmd + " " + entry.Name)
					m.completionArgCmd = ""
					m.completion.Deactivate()
					m.recalcViewport()
				} else {
					// Command completion: fill command name and stay open for args.
					m.input.SetValue("/" + entry.Name + " ")
					m.syncCompletion()
				}
			}
			return cmds
		}
		// Otherwise toggle editor ↔ chat focus.
		m.chatFocused = !m.chatFocused
		if m.chatFocused {
			m.input.Blur()
			m.chat.Focus()
			m.chat.SelectLastInView()
		} else {
			m.chat.Blur()
			cmds = append(cmds, m.input.Focus())
		}
		return cmds

	case key.Code == tea.KeyEscape && key.Mod == 0:
		if m.completion.Active() {
			// Esc dismisses completion and clears the input.
			m.completion.Deactivate()
			m.input.Reset()
			m.recalcViewport()
		} else if m.chatFocused {
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
		switch key.Code {
		case 'j', tea.KeyDown:
			m.chat.SelectNext()
			cmds = append(cmds, m.chat.ScrollToSelectedAndAnimate())
		case 'k', tea.KeyUp:
			m.chat.SelectPrev()
			cmds = append(cmds, m.chat.ScrollToSelectedAndAnimate())
		case 'd':
			cmds = append(cmds, m.chat.ScrollByAndAnimate(m.chat.Height()/2))
		case 'u':
			cmds = append(cmds, m.chat.ScrollByAndAnimate(-m.chat.Height()/2))
		case ' ', tea.KeyEnter:
			// Space/Enter toggles expand on selected tool item.
			m.chat.ToggleExpandedSelectedItem()
		case 'f', tea.KeyPgDown:
			cmds = append(cmds, m.chat.ScrollByAndAnimate(m.chat.Height()))
		case 'b', tea.KeyPgUp:
			cmds = append(cmds, m.chat.ScrollByAndAnimate(-m.chat.Height()))
		case 'g', tea.KeyHome:
			cmds = append(cmds, m.chat.ScrollToTopAndAnimate())
		case 'G', tea.KeyEnd:
			cmds = append(cmds, m.chat.ScrollToBottomAndAnimate())
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
		// When completion is active, Enter confirms the selected item (same as Tab)
		// rather than executing — so the user can add args before sending.
		if m.completion.Active() {
			if entry := m.completion.Selected(); entry != nil {
				if m.completionArgCmd != "" {
					// Arg completion: confirm and close popup (ready to send).
					m.input.SetValue("/" + m.completionArgCmd + " " + entry.Name)
					m.completionArgCmd = ""
					m.completion.Deactivate()
					m.recalcViewport()
				} else {
					// Command completion: fill name, stay open for args.
					m.input.SetValue("/" + entry.Name + " ")
					m.syncCompletion()
				}
			}
			break
		}
		text := strings.TrimSpace(m.input.Value())
		if text == "" {
			break
		}

		// Slash command interception — before sending to agent.
		if cmd, args, found := parseSlashCommand(text); found {
			m.input.Reset()
			m.completion.Deactivate()
			m.recalcViewport()
			m.history.Push(text)
			return cmd.Handler(m, args)
		}

		// Agent command interception — dynamic commands from runtime.
		if _, found := m.matchAgentCommand(text); found {
			m.input.Reset()
			m.completion.Deactivate()
			m.recalcViewport()
			m.history.Push(text)
			return cmdAgentCommand(m, text)
		}

		m.input.Reset()
		m.completion.Deactivate()
		m.recalcViewport()
		m.input.Blur()

		// Re-enable follow mode so the user sees their message and the response.
		m.chat.ScrollToBottom()

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

		// Do NOT set m.waiting = true here — state is driven solely by the
		// incoming stateChangeMsg{running} from runtime_update. Setting it
		// optimistically here would create a dual-driver and cause races.
		m.sentPrompt = true
		m.toolItemIDs = make(map[string]string) // reset tool tracking
		m.history.Push(text)
		cmds = append(cmds, sendPromptCmd(m.client, text))

	case key.Code == tea.KeyUp && !m.waiting:
		if m.completion.Active() {
			m.completion.SelectPrev()
		} else if m.input.Line() == 0 {
			m.history.SaveDraft(m.input.Value())
			if text, ok := m.history.Prev(); ok {
				m.input.SetValue(text)
			}
		}

	case key.Code == tea.KeyDown && !m.waiting:
		if m.completion.Active() {
			m.completion.SelectNext()
		} else if m.input.Line() == m.input.LineCount()-1 {
			if text, ok := m.history.Next(); ok {
				m.input.SetValue(text)
			}
		}

	case key.Code == tea.KeyPgUp:
		cmds = append(cmds, m.chat.ScrollByAndAnimate(-m.chat.Height()))

	case key.Code == tea.KeyPgDown:
		cmds = append(cmds, m.chat.ScrollByAndAnimate(m.chat.Height()))

	default:
		if !m.waiting {
			var taCmd tea.Cmd
			m.input, taCmd = m.input.Update(tea.KeyPressMsg(key))
			cmds = append(cmds, taCmd)
			m.syncCompletion()
		}
	}

	return cmds
}

// syncCompletion checks the current input value and activates/updates/deactivates
// the completion popup accordingly.
func (m *chatModel) syncCompletion() {
	text := m.input.Value()
	if strings.HasPrefix(text, "/") && !m.waiting {
		cmdPart, argPart, hasSpace := strings.Cut(text[1:], " ")
		if !hasSpace {
			// Still typing the command name — show command completion.
			m.completionArgCmd = ""
			if !m.completion.Active() {
				m.completion.Activate(m.buildAllCompletionEntries())
			}
			m.completion.UpdateFilter(cmdPart)
			m.recalcViewport()
			return
		}
		// Command name is complete; check for argument completion.
		cmd := lookupCommand(strings.ToLower(cmdPart))
		if cmd != nil && cmd.ArgCompleter != nil {
			entries := cmd.ArgCompleter(m, argPart)
			if len(entries) > 0 {
				if m.completionArgCmd != cmd.Name {
					// Entering arg mode (or switched to a different command).
					m.completionArgCmd = cmd.Name
					m.completion.Activate(entries)
				}
				m.completion.UpdateFilter(argPart)
				m.recalcViewport()
				return
			}
		}
	}
	// No applicable completion.
	m.completionArgCmd = ""
	if m.completion.Active() {
		m.completion.Deactivate()
		m.recalcViewport()
	}
}

// recalcViewport adjusts the chat viewport height to account for the completion area.
func (m *chatModel) recalcViewport() {
	if m.width == 0 || m.height == 0 {
		return
	}
	completionHeight := m.completion.Height()
	if completionHeight > 0 {
		completionHeight++ // +1 for the divider above the completion list
	}
	vpHeight := max(m.height-inputAreaHeight-completionHeight, 1)
	m.chat.SetSize(m.width, vpHeight)
}

// buildAllCompletionEntries builds a unified list of all commands for completion.
func (m *chatModel) buildAllCompletionEntries() []completionEntry {
	entries := make([]completionEntry, 0, len(commandRegistry)+len(m.agentCommands))
	for _, cmd := range commandRegistry {
		entries = append(entries, completionEntry{
			Name:        cmd.Name,
			Description: cmd.Description,
			Category:    completionCategory(cmd.Category),
		})
	}
	for _, ac := range m.agentCommands {
		entries = append(entries, completionEntry{
			Name:        ac.Name,
			Description: ac.Description,
			Category:    categoryAgent,
		})
	}
	return entries
}

func (m *chatModel) updateCurrentAssistant() tea.Cmd {
	if m.currentMsgID == "" {
		return nil
	}
	item := m.chat.MessageItem(m.currentMsgID)
	if item == nil {
		return nil
	}
	if a, ok := item.(*component.AssistantMessageItem); ok {
		a.SetMessage(m.currentMsg)
	}
	if m.chat.Follow() {
		return m.chat.ScrollToBottomAndAnimate()
	}
	return nil
}

// ensureCurrentMsg creates an assistant message if we don't have one yet.
// This happens when we connect to an agent-run mid-turn (late join).
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
func contentBlockText(cb runapi.ContentBlock) string {
	if cb.Text != nil {
		return cb.Text.Text
	}
	return ""
}

func (m *chatModel) handleNotif(ev runapi.AgentRunEvent) tea.Cmd {
	// runtime_update events are handled by waitNotif; skip here.
	if ev.Type == runapi.EventTypeRuntimeUpdate {
		return nil
	}

	var cmds []tea.Cmd

	switch pl := ev.Payload.(type) {
	case runapi.ContentEvent:
		switch ev.Type {
		case runapi.EventTypeUserMessage:
			// User prompt broadcast. Skip if we sent this prompt (already shown).
			if m.sentPrompt {
				m.sentPrompt = false
				break
			}
			if text := contentBlockText(pl.Content); text != "" {
				userMsg := NewFinishedStreamingMessage(m.nextID("user"), component.RoleUser, text)
				m.chat.AppendMessages(component.NewUserMessageItem(&m.sty, userMsg))
			}

		case runapi.EventTypeAgentMessage:
			if cmd := m.ensureCurrentMsg(); cmd != nil {
				cmds = append(cmds, cmd)
			}
			m.currentMsg.AppendText(contentBlockText(pl.Content))
			if cmd := m.updateCurrentAssistant(); cmd != nil {
				cmds = append(cmds, cmd)
			}

		case runapi.EventTypeAgentThinking:
			if cmd := m.ensureCurrentMsg(); cmd != nil {
				cmds = append(cmds, cmd)
			}
			m.currentMsg.AppendThinking(contentBlockText(pl.Content))
			if cmd := m.updateCurrentAssistant(); cmd != nil {
				cmds = append(cmds, cmd)
			}
		}

	case runapi.ToolCallEvent:
		// Finish current assistant text before tool.
		if m.currentMsg != nil {
			m.currentMsg.Finish(component.FinishReasonToolUse)
			if cmd := m.updateCurrentAssistant(); cmd != nil {
				cmds = append(cmds, cmd)
			}
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

	case runapi.ToolResultEvent:
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
					switch pl.Status {
					case "completed":
						ti.SetStatus(component.ToolStatusSuccess)
					case "error":
						ti.SetStatus(component.ToolStatusError)
					}
				}
			}
		}
		if m.chat.Follow() {
			cmds = append(cmds, m.chat.ScrollToBottomAndAnimate())
		}

	case runapi.PlanEvent:
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
	div := styleDim.Render(strings.Repeat("─", m.width))
	cursor := -1
	if m.watcher != nil {
		cursor = m.watcher.Cursor()
	}
	header := renderHeader(m.workspaceName, m.agentName, m.agentStatus, m.currentModel, cursor, m.width)

	var input string
	if m.waiting {
		input = "  " + m.spinner.View() + m.renderStatusLine()
	} else {
		input = m.input.View()
	}

	statusBar := m.renderHelp()

	// Layout:
	//   chat
	//   ──────  (divider)
	//   header
	//   ──────  (divider)
	//   input   [+ completion area if active]
	//   ──────  (divider)
	//   status bar
	content := chatView + "\n" + div + "\n" + header + "\n" + div + "\n" + input
	if m.completion.Active() {
		content += "\n" + div + "\n" + m.completion.Render(m.width)
	}
	content += "\n" + div + "\n" + statusBar

	v := tea.NewView(content)
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
	case string(apiruntime.PhaseRunning):
		styled = styleStatusRunning.Render("● running")
	case string(apiruntime.PhaseRestarting):
		styled = styleStatusRunning.Render("● restarting")
	case string(apiruntime.PhaseIdle):
		styled = styleStatusIdle.Render("● idle")
	case string(apiruntime.PhaseError):
		styled = styleStatusError.Render("● error")
	case string(apiruntime.PhaseStopped):
		styled = styleStatusStopped.Render("● stopped")
	default:
		styled = styleDim.Render("● " + status)
	}

	hint := ""
	switch status {
	case string(apiruntime.PhaseRunning):
		hint = styleDim.Render(" — ctrl+x to cancel")
	case string(apiruntime.PhaseRestarting):
		hint = styleDim.Render(" — restarting")
	case string(apiruntime.PhaseIdle):
		hint = styleDim.Render(" — ready for input")
	case string(apiruntime.PhaseError):
		hint = styleDim.Render(" — agent error, check logs")
	case string(apiruntime.PhaseStopped):
		hint = styleDim.Render(" — agent stopped")
	}

	return " " + styled + hint
}

func (m chatModel) renderHelp() string {
	h := styleHelp

	switch {
	case m.completion.Active():
		line1 := h.Render(" ↑/↓ select · tab confirm · esc close")
		line2 := h.Render(" ctrl+c quit")
		return line1 + "\n" + line2
	case m.chatFocused:
		line1 := h.Render(" j/k select · space expand · d/u half-page · g/G top/bottom")
		line2 := h.Render(" tab editor · esc back · ctrl+c quit")
		return line1 + "\n" + line2
	default:
		line1 := h.Render(" enter send · shift+enter newline · / commands")
		line2 := h.Render(" tab chat · ctrl+x cancel · ctrl+c quit")
		if m.infoMessage != nil {
			info := " " + lipgloss.NewStyle().Foreground(lipgloss.Color("10")).Render(m.infoMessage.text)
			return line1 + "\n" + info
		}
		return line1 + "\n" + line2
	}
}

// ── Entry point ───────────────────────────────────────────────────────────────

// ChatTUIOptions holds configuration for RunChatTUI.
type ChatTUIOptions struct {
	SocketPath    string
	WorkspaceName string
	AgentName     string
}

// RunChatTUI launches the interactive chat TUI connected to the given agent-run socket.
func RunChatTUI(opts ChatTUIOptions) error {
	p := tea.NewProgram(newChatModel(opts))
	_, err := p.Run()
	return err
}
