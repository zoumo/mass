# Bubbletea Shim Chat TUI Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace the `runChat()` plain REPL in `massctl shim chat` with a bubbletea TUI that splits conversation history from the input field, shows a spinner while waiting, and color-codes event types.

**Architecture:** Add two new files (`chat.go` for the TUI model, `chat_plain.go` for the existing fallback), modify `command.go` only to delegate `runChat()` to one of them based on `isatty`. All RPC/client code is untouched.

**Tech Stack:** Go 1.24, bubbletea v1.3.x, bubbles v0.20.x (viewport + textinput + spinner), lipgloss v1.1.x, go-isatty v0.0.x

---

## File Map

| File | Action | Responsibility |
|------|--------|----------------|
| `cmd/massctl/subcommands/shim/command.go` | Modify | `runChat()` → dispatch to TUI or plain |
| `cmd/massctl/subcommands/shim/chat_plain.go` | Create | Plain bufio REPL (moved from command.go) |
| `cmd/massctl/subcommands/shim/chat.go` | Create | Bubbletea model + Update + View |
| `go.mod` / `go.sum` | Modify | Add four new dependencies |

---

## Task 1: Add dependencies

**Files:**
- Modify: `go.mod`, `go.sum`

- [ ] **Step 1: Add the four packages**

```bash
cd /Users/jim/code/zoumo/open-agent-runtime
go get github.com/charmbracelet/bubbletea@latest
go get github.com/charmbracelet/bubbles@latest
go get github.com/charmbracelet/lipgloss@latest
go get github.com/mattn/go-isatty@latest
```

- [ ] **Step 2: Verify build still passes**

```bash
make build
```

Expected: binary builds without errors.

- [ ] **Step 3: Commit**

```bash
git add go.mod go.sum
git commit -m "chore: add bubbletea, bubbles, lipgloss, go-isatty dependencies"
```

---

## Task 2: Extract plain REPL to `chat_plain.go`

**Files:**
- Create: `cmd/massctl/subcommands/shim/chat_plain.go`
- Modify: `cmd/massctl/subcommands/shim/command.go`

Move the existing `runChat()` body into a new function `runChatPlain()` in its own file. This preserves the fallback path before any TUI code is written.

- [ ] **Step 1: Create `chat_plain.go`**

```go
package shim

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

// runChatPlain is the original bufio-based REPL, used as a fallback
// when stdout is not a TTY (e.g. pipes, CI).
func runChatPlain(sock string) error {
	c, err := dial(sock)
	if err != nil {
		return err
	}
	defer c.close()

	if _, err := c.call("session/subscribe", nil); err != nil {
		return fmt.Errorf("session/subscribe: %w", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	turnEnd := startNotificationPrinter(ctx, c)

	scanner := bufio.NewScanner(os.Stdin)
	fmt.Println("massctl shim chat — type your message, 'exit' to quit")
	for {
		fmt.Print("\n> ")
		if !scanner.Scan() {
			break
		}
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		if line == "exit" || line == "quit" {
			break
		}
		drainTurnEnd(turnEnd)
		result, err := c.call("session/prompt", map[string]string{"prompt": line})
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			continue
		}
		<-turnEnd
		var pr struct {
			StopReason string `json:"stopReason"`
		}
		_ = json.Unmarshal(result, &pr)
		if pr.StopReason != "" {
			fmt.Fprintf(os.Stderr, "[stop: %s]", pr.StopReason)
		}
	}
	return nil
}
```

Note: you need to add `"context"` to the import list (it is already imported in `command.go` but needs to be added here separately since this is a new file).

The full import block for `chat_plain.go`:

```go
import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
)
```

- [ ] **Step 2: Replace `runChat()` body in `command.go`**

In `command.go`, replace the entire `runChat()` function body. The new `runChat()` will be written in Task 3; for now just make it call `runChatPlain()` so the build stays green:

Find this in `command.go` (lines ~275-320):
```go
func runChat(sock string) error {
	c, err := dial(sock)
	// ... entire body ...
	return nil
}
```

Replace it with:
```go
func runChat(sock string) error {
	return runChatPlain(sock)
}
```

- [ ] **Step 3: Build and verify**

```bash
make build
```

Expected: builds cleanly.

- [ ] **Step 4: Commit**

```bash
git add cmd/massctl/subcommands/shim/chat_plain.go \
        cmd/massctl/subcommands/shim/command.go
git commit -m "refactor(shim): extract plain REPL to chat_plain.go"
```

---

## Task 3: Implement `chat.go` — model, messages, Init

**Files:**
- Create: `cmd/massctl/subcommands/shim/chat.go`

- [ ] **Step 1: Create `chat.go` with model struct and tea messages**

```go
package shim

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textinput"
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

// ── Styles ────────────────────────────────────────────────────────────────────

var (
	styleUser       = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("12"))
	styleAgentLabel = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("10"))
	styleText       = lipgloss.NewStyle()
	styleThinking   = lipgloss.NewStyle().Faint(true)
	styleToolCall   = lipgloss.NewStyle().Foreground(lipgloss.Color("11"))
	styleToolResult = lipgloss.NewStyle().Faint(true)
	styleError      = lipgloss.NewStyle().Foreground(lipgloss.Color("9"))
	styleDivider    = lipgloss.NewStyle().Faint(true)
	styleBorder     = lipgloss.NewStyle().
			Border(lipgloss.NormalBorder(), false, false, false, false).
			BorderForeground(lipgloss.Color("238"))
)

// ── Model ─────────────────────────────────────────────────────────────────────

type chatModel struct {
	viewport viewport.Model
	input    textinput.Model
	spinner  spinner.Model

	sock   string
	client *client
	notifs <-chan rpcResponse

	lines   []string // rendered history lines
	waiting bool     // true while agent turn is in progress
	err     error    // fatal error
	ready   bool     // true after first WindowSizeMsg
	width   int
	height  int
}

func newChatModel(sock string) chatModel {
	ti := textinput.New()
	ti.Placeholder = "Type a message…"
	ti.Focus()

	sp := spinner.New()
	sp.Spinner = spinner.Dot

	return chatModel{
		sock:    sock,
		input:   ti,
		spinner: sp,
	}
}

// ── Init ──────────────────────────────────────────────────────────────────────

func (m chatModel) Init() tea.Cmd {
	return tea.Batch(
		m.spinner.Tick,
		connectCmd(m.sock),
	)
}

func connectCmd(sock string) tea.Cmd {
	return func() tea.Msg {
		c, err := dial(sock)
		if err != nil {
			return connErrMsg{err: fmt.Errorf("connect %s: %w", sock, err)}
		}
		if _, err := c.call("session/subscribe", nil); err != nil {
			c.close()
			return connErrMsg{err: fmt.Errorf("session/subscribe: %w", err)}
		}
		return connReadyMsg{c: c, notifs: c.notifs}
	}
}
```

- [ ] **Step 2: Build check**

```bash
cd /Users/jim/code/zoumo/open-agent-runtime && go build ./cmd/massctl/...
```

Expected: compiles (model exists, Update/View not yet present — add stubs):

Add these stubs at the bottom of `chat.go` temporarily so it compiles:

```go
func (m chatModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) { return m, nil }
func (m chatModel) View() string                            { return "" }

func runChatTUI(sock string) error {
	p := tea.NewProgram(newChatModel(sock), tea.WithAltScreen())
	_, err := p.Run()
	return err
}
```

- [ ] **Step 3: Build check again**

```bash
go build ./cmd/massctl/...
```

Expected: clean compile.

---

## Task 4: Implement `Update()`

**Files:**
- Modify: `cmd/massctl/subcommands/shim/chat.go`

Replace the stub `Update()` with the full implementation.

- [ ] **Step 1: Replace `Update()` stub**

Remove the stub `Update` and replace with:

```go
func (m chatModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmds []tea.Cmd

	switch msg := msg.(type) {

	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		inputHeight := 2 // prompt line + padding
		vpHeight := msg.Height - inputHeight
		if vpHeight < 1 {
			vpHeight = 1
		}
		if !m.ready {
			m.viewport = viewport.New(msg.Width, vpHeight)
			m.viewport.SetContent(strings.Join(m.lines, "\n"))
			m.viewport.GotoBottom()
			m.input.Width = msg.Width - 2
			m.ready = true
		} else {
			m.viewport.Width = msg.Width
			m.viewport.Height = vpHeight
			m.input.Width = msg.Width - 2
		}

	case connReadyMsg:
		m.client = msg.c
		m.notifs = msg.notifs
		m.appendLine(styleDivider.Render("─── connected ───"))
		m.rebuildViewport()
		cmds = append(cmds, waitNotif(m.notifs))

	case connErrMsg:
		m.appendLine(styleError.Render("error: " + msg.err.Error()))
		m.rebuildViewport()
		return m, tea.Quit

	case notifMsg:
		m.handleNotif(msg.rpcResponse)
		m.rebuildViewport()
		cmds = append(cmds, waitNotif(m.notifs))

	case turnEndMsg:
		m.waiting = false
		m.input.Focus()
		m.appendLine("") // blank separator
		m.rebuildViewport()
		cmds = append(cmds, waitNotif(m.notifs))

	case connClosedMsg:
		m.appendLine(styleDivider.Render("─── connection closed ───"))
		m.rebuildViewport()
		return m, tea.Quit

	case spinner.TickMsg:
		var spinCmd tea.Cmd
		m.spinner, spinCmd = m.spinner.Update(msg)
		cmds = append(cmds, spinCmd)

	case tea.KeyMsg:
		switch msg.Type {
		case tea.KeyCtrlC:
			if m.client != nil {
				m.client.close()
			}
			return m, tea.Quit

		case tea.KeyEnter:
			if m.waiting || m.client == nil {
				break
			}
			text := strings.TrimSpace(m.input.Value())
			if text == "" {
				break
			}
			m.input.Reset()
			m.appendLine(styleUser.Render("You: ") + styleText.Render(text))
			m.appendLine(styleAgentLabel.Render("Agent: "))
			m.rebuildViewport()
			m.waiting = true
			m.input.Blur()
			cmds = append(cmds, sendPromptCmd(m.client, text))

		default:
			if !m.waiting {
				var inputCmd tea.Cmd
				m.input, inputCmd = m.input.Update(msg)
				cmds = append(cmds, inputCmd)
			}
		}
	}

	var vpCmd tea.Cmd
	m.viewport, vpCmd = m.viewport.Update(msg)
	cmds = append(cmds, vpCmd)

	return m, tea.Batch(cmds...)
}

func sendPromptCmd(c *client, text string) tea.Cmd {
	return func() tea.Msg {
		_, err := c.call("session/prompt", map[string]string{"prompt": text})
		if err != nil {
			return connErrMsg{err: fmt.Errorf("session/prompt: %w", err)}
		}
		// turn end arrives via notification channel, not here
		return nil
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
		// Append text to the last "Agent: " line
		if len(m.lines) > 0 {
			m.lines[len(m.lines)-1] += styleText.Render(pl.Text)
		}
	case "thinking":
		var pl textPayload
		_ = json.Unmarshal(p.Event.Payload, &pl)
		m.lines = append(m.lines, styleThinking.Render("  · "+pl.Text))
	case "tool_call":
		m.lines = append(m.lines, styleToolCall.Render("  ⚙ "+string(p.Event.Payload)))
	case "tool_result":
		m.lines = append(m.lines, styleToolResult.Render("  ↳ "+string(p.Event.Payload)))
	case "turn_end":
		// handled by isTurnEndNotification → turnEndMsg
	}
}

func (m *chatModel) appendLine(s string) {
	m.lines = append(m.lines, s)
}

func (m *chatModel) rebuildViewport() {
	m.viewport.SetContent(strings.Join(m.lines, "\n"))
	m.viewport.GotoBottom()
}
```

- [ ] **Step 2: Build check**

```bash
go build ./cmd/massctl/...
```

Expected: clean compile.

---

## Task 5: Implement `View()`

**Files:**
- Modify: `cmd/massctl/subcommands/shim/chat.go`

Replace the stub `View()` with the full render.

- [ ] **Step 1: Replace `View()` stub**

```go
func (m chatModel) View() string {
	if !m.ready {
		return "\n  Connecting…\n"
	}

	vpView := m.viewport.View()

	// Build input line
	var inputLine string
	if m.waiting {
		inputLine = "  " + m.spinner.View() + " waiting…"
	} else {
		inputLine = "> " + m.input.View()
	}

	divider := styleDivider.Render(strings.Repeat("─", m.width))

	return fmt.Sprintf("%s\n%s\n%s", vpView, divider, inputLine)
}
```

- [ ] **Step 2: Build check**

```bash
go build ./cmd/massctl/...
```

Expected: clean compile.

---

## Task 6: Wire isatty dispatch in `command.go` and update `runChatTUI`

**Files:**
- Modify: `cmd/massctl/subcommands/shim/command.go`
- Modify: `cmd/massctl/subcommands/shim/chat.go` (finalize `runChatTUI`)

- [ ] **Step 1: Update `runChat()` in `command.go`**

Replace:
```go
func runChat(sock string) error {
	return runChatPlain(sock)
}
```

With:
```go
func runChat(sock string) error {
	if isatty.IsTerminal(os.Stdin.Fd()) {
		return runChatTUI(sock)
	}
	return runChatPlain(sock)
}
```

Add the import at the top of `command.go`:
```go
"github.com/mattn/go-isatty"
```

- [ ] **Step 2: Finalize `runChatTUI` in `chat.go`**

The stub `runChatTUI` is already correct from Task 3:
```go
func runChatTUI(sock string) error {
	p := tea.NewProgram(newChatModel(sock), tea.WithAltScreen())
	_, err := p.Run()
	return err
}
```

No change needed here.

- [ ] **Step 3: Full build**

```bash
make build
```

Expected: binary builds cleanly.

- [ ] **Step 4: Commit everything**

```bash
git add cmd/massctl/subcommands/shim/chat.go \
        cmd/massctl/subcommands/shim/chat_plain.go \
        cmd/massctl/subcommands/shim/command.go \
        go.mod go.sum
git commit -m "feat(shim): bubbletea TUI for massctl shim chat"
```

---

## Task 7: Smoke test

Manual verification — no automated test needed per spec.

- [ ] **Step 1: Start a test agent and get its socket path**

Using the existing `massctl` e2e setup or any running `mass` instance. The socket path will be something like `/tmp/mass-e2e-XXXXX/bundles/ws-agent/shim.sock`.

- [ ] **Step 2: Run chat in TUI mode (TTY)**

```bash
./bin/mass-ctl shim chat --socket <socket-path>
```

Verify:
- Viewport renders at top, input `> ` at bottom
- Typing and pressing Enter sends the message
- Spinner appears while agent is responding
- Agent reply streams into the viewport
- `tool_call` lines appear in yellow, `thinking` in dim gray
- `PgUp`/`PgDn` scrolls history
- `Ctrl+C` exits cleanly

- [ ] **Step 3: Run in plain mode (non-TTY)**

```bash
echo "hello" | ./bin/mass-ctl shim chat --socket <socket-path>
```

Verify: falls back to the plain REPL output (no TUI).
