# Async Prompt & Room Send

## Context

Currently `agent/prompt` and `room/send` are synchronous — they block the JSON-RPC caller until the agent's entire turn completes (up to 120s timeout). In multi-agent scenarios this causes cascading timeouts: when agent A sends a message to agent B via `room/send`, A's turn blocks waiting for B's full turn, easily exceeding the timeout and putting agents into error state incorrectly.

The fix: make both operations fire-and-forget. The prompt is dispatched to the shim asynchronously, the RPC returns immediately, and results flow back via agent state changes and reply messages (actor model).

## Key Constraint

The agent-shim does **not** support prompt queuing. The ACP SDK's `Connection.handleInbound` dispatches each request in a separate goroutine (`go c.handleInbound(&msg)`), and `AgentSideConnection.handle` calls `agent.Prompt()` without holding a lock over the turn. Two concurrent prompts would race inside the agent implementation.

Therefore: **both `agent/prompt` and `room/send` must reject requests when the target agent is already `running`**. If the caller needs to send a new prompt, it must first call `agent/cancel` to cancel the current turn, then retry.

## Design

### 1. Async `deliverPromptAsync` — new helper in `pkg/ari/server.go`

Instead of calling `client.Prompt(ctx, text)` synchronously, launch the prompt in a goroutine. The goroutine handles state transitions (running -> created/error) when the turn completes.

Important constraints for the helper:

- Do **not** use a bare `context.Background()` for the prompt RPC. The background turn still needs a bounded timeout so agentd does not accumulate stuck goroutines when a shim or ACP agent wedges.
- The timeout should be explicit in the design, and expiry should transition the agent to `error` with a clear error message.
- Cancellation remains a separate control path via `agent/cancel`, but timeout and goroutine cleanup must not depend on callers remembering to cancel manually.

```go
// deliverPromptAsync dispatches a prompt to the shim and returns immediately.
// State transitions (running -> created/error) happen in a background goroutine.
func (h *connHandler) deliverPromptAsync(ctx context.Context, agentID, sessionID, text string) error {
    // Get session, auto-start if needed (same as current deliverPrompt).
    session, err := h.srv.sessions.Get(ctx, sessionID)
    if err != nil { return err }
    if session == nil { return fmt.Errorf("session %q not found", sessionID) }

    if session.State == meta.SessionStateCreated {
        startCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
        _, err := h.srv.processes.Start(startCtx, sessionID)
        cancel()
        if err != nil { return err }
    }

    connectCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
    client, err := h.srv.processes.Connect(connectCtx, sessionID)
    cancel()
    if err != nil { return err }

    // Launch prompt in background goroutine.
    go func() {
        promptCtx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
        defer cancel()
        result, err := client.Prompt(promptCtx, text)
        if err != nil {
            h.srv.agents.UpdateState(context.Background(), agentID, meta.AgentStateError, err.Error())
            return
        }
        // Turn completed — transition back to idle.
        // Do not write lastStopReason via UpdateAgent(labels=...) because that
        // would replace the full labels map rather than merge into it.
        h.srv.agents.UpdateState(context.Background(), agentID, meta.AgentStateCreated, "")
    }()

    return nil
}
```

### 2. Update `handleAgentPrompt` (~line 1102)

Change from synchronous `deliverPrompt` to `deliverPromptAsync`. Return immediately after dispatch.

- **Reject if running:** agent-shim has no prompt queuing; caller must `agent/cancel` first
- Set state to `running`
- Call `deliverPromptAsync` (auto-start + dispatch, no blocking on turn)
- If dispatch fails (session not found, connect fails), set error state and return error
- If dispatch succeeds, return `{accepted: true}` immediately
- Background goroutine handles running -> created/error transition

```go
// Reject if agent is already running — shim does not queue prompts.
// Caller must cancel the current turn first via agent/cancel.
if agent.State == meta.AgentStateRunning {
    replyError(ctx, conn, req.ID, jsonrpc2.CodeInvalidParams,
        "agent is already processing a prompt; cancel it first via agent/cancel")
    return
}
```

### 3. Update `handleRoomSend` (~line 1797)

Same async dispatch pattern. Same running-state guard — **reject if target agent is `running`**.

When the room_send MCP tool gets a rejection, the sending agent sees a tool error like "target agent is busy, try again later" and can decide to retry or move on. This is the correct actor-model behavior — messages are not guaranteed to be accepted if the receiver is busy.

### 4. Update types in `pkg/ari/types.go`

```go
// AgentPromptResult — remove StopReason, add Accepted.
type AgentPromptResult struct {
    Accepted bool `json:"accepted"`
}

// RoomSendResult — remove StopReason (not available in async model).
type RoomSendResult struct {
    Delivered bool `json:"delivered"`
}
```

### 5. Observability: `LastStopReason`

Do **not** store the last turn's stop reason by calling `UpdateAgent(..., labels=...)` with a single-entry map. The current store replaces the full labels document, so that approach would silently discard existing labels.

Recommended options:

- Short term: do not persist `lastStopReason`; rely on shim events / chat transcript for turn outcome.
- Follow-up design: add a dedicated `lastStopReason` field (or equivalent turn outcome field) to the agent model if persistent observability is required.

Until a dedicated field exists, `agent/status` should not pretend to expose `lastStopReason` reliably.

### 6. Update `room-mcp-server` (`cmd/room-mcp-server/main.go`)

- Remove `StopReason` from `ariRoomSendResult`
- Update tool response text: `"Message delivered to {target}. The target agent will process it asynchronously."`
- On rejection (target busy): `"Target agent {target} is busy processing another prompt. Cancel its current turn or try again later."`

### 7. Update CLI `agent prompt` (`cmd/agentdctl/agent.go`)

The prompt command currently blocks until the turn finishes. Update to:
1. Send prompt -> returns immediately with `{accepted: true}`
2. Add `--wait` flag: if set, poll `agent/status` until state is no longer `running`

### 8. Keep synchronous `deliverPrompt` for `session/prompt`

`session/prompt` is the lower-level shim-facing API. Keep it synchronous for backward compatibility. Only `agent/prompt` and `room/send` become async.

## Files to modify

| File | Change |
|------|--------|
| `pkg/ari/server.go` | Add `deliverPromptAsync`, update `handleAgentPrompt` and `handleRoomSend`, add running-state guard to both |
| `pkg/ari/types.go` | Update `AgentPromptResult` and `RoomSendResult` |
| `cmd/room-mcp-server/main.go` | Update `ariRoomSendResult` and response text |
| `cmd/agentdctl/agent.go` | Add `--wait` flag to prompt command |
| `pkg/ari/server_test.go` | Update tests for async behavior |

## Verification

1. **Unit tests:** Update `server_test.go` — `agent/prompt` returns immediately, agent state is `running` right after, then eventually transitions to `created`
2. **Integration tests:** Update `tests/integration/` — prompt tests poll for completion instead of expecting sync result
3. **Manual test:**
   - `ctl agent prompt <id> --text "hello"` returns immediately
   - `ctl agent status <id>` shows `running`
   - After turn completes, `ctl agent status <id>` shows `created`
   - `ctl agent prompt <id> --text "another"` while running -> rejected with "cancel first"
   - Multi-agent: A sends to B via room_send, returns immediately, B processes and replies back to A
