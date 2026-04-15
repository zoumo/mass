# Design: Bubbletea TUI for `massctl shim chat`

**Date**: 2026-04-12  
**Scope**: Replace `runChat()` implementation with a bubbletea TUI; no changes to RPC layer.

---

## Goals

- Split input and output into distinct areas so streaming agent text never interrupts the input field
- Show a spinner while waiting for the agent to respond
- Color-code event types (tool calls yellow, tool results dim, thinking dim)
- Support `PgUp/PgDn` scrolling through conversation history
- Degrade gracefully to the existing plain-text REPL in non-TTY environments

## Non-Goals

- Multiline input (single-line `textinput`, Enter to send — matching Claude Code UX)
- Mouse support
- Theming or configuration
- Changes to `prompt`, `state`, `history`, or `stop` subcommands

---

## File Layout

```
cmd/massctl/subcommands/shim/
  command.go      — existing; runChat() delegates to runChatTUI() or runChatPlain()
  chat.go         — NEW: bubbletea model, messages, Update/View logic
  chat_plain.go   — NEW: existing bufio REPL, renamed from the inline runChat() body
```

Keeping TUI code in a separate file avoids growing `command.go` and makes the fallback path easy to read independently.

---

## Layout

```
┌─────────────────────────────────────────┐
│  (viewport — scrollable history)        │
│                                         │
│  You: hello                             │
│                                         │
│  Agent: here is my answer               │
│                                         │
│  ⚙ bash  ···                            │
│  ↳ exit 0                               │
│                                         │
│  Agent: ⠸                               │  ← spinner shown while waiting
│                                         │
├─────────────────────────────────────────┤
│ > █                                     │  ← textinput, Enter to send
└─────────────────────────────────────────┘
```

The viewport takes `termHeight - 3` rows; the input area is 2 rows (border + input line).

---

## Model

```go
type chatModel struct {
    viewport  viewport.Model
    input     textinput.Model
    spinner   spinner.Model
    sock      string       // unix socket path
    client    *client      // RPC client, nil until connected
    notifs    <-chan rpcResponse
    lines     []string     // rendered history lines (lipgloss-styled)
    waiting   bool         // true while agent turn is in progress
    err       error        // terminal error, causes program to quit
    ready     bool         // true after first WindowSizeMsg
}
```

---

## Messages

```go
type notifMsg     struct{ rpcResponse }
type turnEndMsg   struct{}
type connClosedMsg struct{}
type connReadyMsg  struct{ c *client; notifs <-chan rpcResponse }
type connErrMsg    struct{ err error }
```

---

## State Machine

```
init → connecting → idle ⇄ waiting → (Ctrl+C) → quit
                                ↓ connClosed/err → quit
```

- **connecting**: `Init()` fires a `tea.Cmd` that dials the socket and calls `session/subscribe`; spinner shown
- **idle**: input focused, spinner hidden; `Enter` sends prompt and transitions to `waiting`
- **waiting**: input blurred/dimmed, spinner shown after last agent line; `notifMsg` events appended to viewport
- **quit**: `tea.Quit` returned from `Update`

---

## Event Rendering

| Event type   | Style                          |
|--------------|--------------------------------|
| `text`       | white, no prefix               |
| `thinking`   | dim gray, prefix `· `          |
| `tool_call`  | yellow, prefix `⚙ `            |
| `tool_result`| dim gray, prefix `↳ `          |
| `turn_end`   | blank separator line           |

Each `session/update` notification appends one styled string to `lines`; viewport content is rebuilt with `strings.Join(lines, "\n")` and `viewport.GotoBottom()` called.

---

## Notification Bridge

```go
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
```

After each `notifMsg` is handled in `Update`, another `waitNotif` command is returned to keep the loop alive.

---

## isatty Fallback

```go
// in command.go
func runChat(sock string) error {
    if !isatty.IsTerminal(os.Stdin.Fd()) {
        return runChatPlain(sock)
    }
    return runChatTUI(sock)
}
```

`runChatPlain` is the existing `bufio.Scanner` loop, moved verbatim to `chat_plain.go`.

---

## New Dependencies

| Module | Version | Purpose |
|--------|---------|---------|
| `github.com/charmbracelet/bubbletea` | v1.3.x | TUI event loop |
| `github.com/charmbracelet/bubbles` | v0.20.x | viewport, textinput, spinner |
| `github.com/charmbracelet/lipgloss` | v1.1.x | styling |
| `github.com/mattn/go-isatty` | v0.0.x | TTY detection |

---

## Error Handling

- Dial failure: show error line in viewport, quit after 2 s
- `session/subscribe` failure: same
- Mid-conversation RPC error: append red error line to viewport, return to idle (don't quit)
- `connClosedMsg`: append "connection closed" line, quit

---

## Testing

Manual smoke test via `massctl shim chat --socket <path>`. No unit tests in this change — the bubbletea `teatest` library can be added in a follow-up if needed.
