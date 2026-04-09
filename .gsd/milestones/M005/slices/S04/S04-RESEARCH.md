# S04 Research: Agent Lifecycle — Async Create, Stop/Delete Separation, Restart

## Summary

S04 is a **targeted research** slice. The technology is fully understood (Go, jsonrpc2, goroutines), the codebase pattern is clear from S03, and the work is precisely scoped: replace three stubs (async create, real restart) and wire the `OAR_AGENT_NAME`/`OAR_AGENT_ID` env vars. No new third-party libraries needed.

## Requirement Owned

**R048** — `agent/create` uses async semantics: returns `creating` state immediately, bootstrap completes in background. Callers poll `agent/status` for `created`/`error`.

## What Exists After S03

### What's Done

| Location | State |
|---|---|
| `pkg/agentd/agent.go` — `AgentManager` | Complete with Create/Get/GetByRoomName/List/UpdateState/Delete |
| `pkg/ari/server.go` — `handleAgentCreate` | **Synchronous stub** — returns `created` immediately with no background bootstrap |
| `pkg/ari/server.go` — `handleAgentRestart` | **Stub** — returns `MethodNotFound` (code comment: "see S04") |
| `pkg/ari/server.go` — `handleAgentStop` | Working — calls `processes.Stop(session.ID)` then `agents.UpdateState(stopped)` |
| `pkg/ari/server.go` — `handleAgentDelete` | Working — enforces stopped precondition, pre-fetches session before ON DELETE SET NULL |
| `pkg/meta/models.go` | `AgentStateCreating`, `AgentStateCreated`, `AgentStateRunning`, `AgentStateStopped`, `AgentStateError` all defined |
| `pkg/agentd/session.go` — `validTransitions` | `stopped → creating` already defined (restart bootstrap path) |
| `pkg/agentd/process.go` — `ProcessManager.Start` | Full bootstrap: bundle, fork, wait-socket, connect, subscribe, transition to running |
| `pkg/ari/server_test.go` — `TestARIAgentRestartStub` | Tests that restart returns MethodNotFound — S04 replaces this with a real test |
| `cmd/agentdctl/agent.go` | 8 subcommands — no `restart` subcommand yet |

### Key Gaps (S04 must fill)

1. **`handleAgentCreate` async**: currently `agent.State = AgentStateCreated` synchronously. Must change to `AgentStateCreating` returned immediately, background goroutine does: `sessions.Create` + `processes.Start` + on success → `agents.UpdateState(created)` + on failure → `agents.UpdateState(error, errMsg)`.

2. **`handleAgentRestart`**: currently `MethodNotFound` stub. Real implementation: validate stopped/error state, find/delete linked session, create new session record, transition agent to `creating`, launch background goroutine (same as create bootstrap), transition to `created`/`error` when done.

3. **`generateConfig` env vars**: `OAR_SESSION_ID` passed to room-mcp-server should become `OAR_AGENT_ID` (agent-level identity) and `OAR_AGENT_NAME` per M005 context spec. Decision says env vars are `OAR_AGENT_NAME`, `OAR_AGENT_ID`, `OAR_ROOM_NAME` (replacing `OAR_SESSION_ID`). This only touches `process.go:generateConfig`. **Note: S06 is the room-mcp-server rewrite, but the env var rename affects process.go which is S04 scope.**

4. **Tests**: `TestARIAgentRestartStub` must be replaced with `TestARIAgentRestartAsync` testing the real restart flow. A new `TestARIAgentCreateAsync` must verify: create returns `creating` → poll status → transitions to `created`.

## Implementation Landscape

### Files That Change

| File | Changes |
|---|---|
| `pkg/ari/server.go` | `handleAgentCreate`: async bootstrap; `handleAgentRestart`: real impl |
| `pkg/agentd/process.go` | `generateConfig`: rename `OAR_SESSION_ID` → `OAR_AGENT_ID`, add `OAR_AGENT_NAME` |
| `pkg/ari/server_test.go` | Replace `TestARIAgentRestartStub` with real restart test; add `TestARIAgentCreateAsync` |
| `cmd/agentdctl/agent.go` | Add `agentRestartCmd` subcommand |

### Files That Don't Change

- `pkg/agentd/agent.go` — `AgentManager` is complete as-is. `Create()` accepts initial state `creating` (the validation already allows it: `session.State != meta.SessionStateCreating && session.State != meta.SessionStateCreated` — wait, that's for sessions, not agents). For agents: `AgentManager.Create()` sets default state to `AgentStateCreated` if empty. **The planner must ensure the handler passes `AgentStateCreating` explicitly** in the async create path so the default doesn't override it.
- `pkg/agentd/session.go` — `validTransitions` already has `stopped → creating`. No changes needed.
- `pkg/meta/models.go`, `pkg/meta/agent.go` — all states already defined.

## Async Create Pattern

The pattern to follow is `ProcessManager.Start` + background goroutine, analogous to how recovery works (`RecoverSessions` launches goroutines for each session). The blueprint:

```
handleAgentCreate:
  1. Validate params, room, workspace (same as today)
  2. Create agent with state=creating (not created!)
  3. Create linked session with state=creating
  4. AcquireWorkspace (same as today)
  5. Reply immediately with {agentId, state:"creating"}
  6. go func() {
       err := processes.Start(ctx, session.ID)
       if err != nil:
         agents.UpdateState(agentId, error, err.Error())
         sessions.Transition(sessionId, error)
       else:
         agents.UpdateState(agentId, created, "")
         // sessions.Start already sets session to running
         // but for create semantics, agent transitions to created (not running)
         // The session is running but the agent is "ready" = created
     }()
```

**Critical design point**: After `processes.Start` succeeds, the agent transitions to `created` (not `running`), because `created` means "bootstrap complete, ready for prompts". Running means "currently processing a prompt". This matches the state machine in `session.go`: `created → running (on prompt)`.

**Context for background goroutine**: Use `context.Background()` (not the request context) so the bootstrap continues after the RPC returns. Add a timeout (30s for socket, total 45s budget). The goroutine should have structured logging via the server's process manager logger.

## Restart Pattern

`agent/restart` is: stopped/error agent → re-bootstrap from existing state directory.

```
handleAgentRestart:
  1. Get agent — validate exists
  2. Validate state is stopped or error (only restartable states)
  3. Find linked session (may be nil if fully deleted)
  4. If linked session exists: delete it (sessions.Delete + release workspace ref)
  5. Create new session record linked to agentId (new sessionId)
  6. AcquireWorkspace for new session
  7. agents.UpdateState(agentId, creating, "")
  8. Reply immediately with {} or {agentId, state:"creating"}
  9. go func() { processes.Start(ctx, newSessionId) → agents.UpdateState(created or error) }()
```

**Why delete old session?** Restart creates a fresh shim process. The old session row (stopped) cannot be reused because `ProcessManager.Start` requires `session.State == SessionStateCreated`. The old session must be deleted and a new one created.

**validTransitions check**: `stopped → creating` is already in the transition map. The session deletion doesn't go through the state machine (it's a delete, not a transition). The new session starts in `creating`.

## State Machine Flow Post-S04

```
agent/create:  → agent.state = creating (DB)
               → background: processes.Start (session: creating→running)
               → on success: agent.state = created (DB)
               → on failure: agent.state = error (DB)

agent/prompt:  (requires created state)
               → processes.Start auto-start (session: created→running)
               → agent.state = running

agent/stop:    → processes.Stop
               → agent.state = stopped

agent/delete:  (requires stopped)
               → agents.Delete + sessions.Delete + registry.Release

agent/restart: (requires stopped or error)
               → sessions.Delete(old) + registry.Release
               → sessions.Create(new, creating state)
               → AcquireWorkspace(new session)
               → agent.state = creating
               → background: processes.Start → agent.state = created / error
```

## Session State Machine Alignment

After S04, `deliverPrompt` still checks `session.State == meta.SessionStateCreated` to decide whether to auto-start. After async create, the session reaches `running` when `processes.Start` completes. The agent reaches `created`. So when `handleAgentPrompt` is called, the session will typically be `running` (not `created`) — the shim is already up! This means the auto-start path in `deliverPrompt` won't trigger for async-created agents that completed bootstrap. The auto-start path only fires if the session is `created` (bootstrap not yet run) — which won't happen after async create succeeds.

**This is correct**: async create bootstraps the shim immediately, so by the time `agent/prompt` arrives, the session is `running` (shim is up, no auto-start needed).

**Edge case**: if someone calls `agent/prompt` while the agent is still in `creating` state, `handleAgentPrompt` should reject it (agent not ready). The current code will fail at `linkedSessionForAgent` returning the `creating` session, then `deliverPrompt` will fail because session state is `creating` (not `created` or `running`). **This is the correct behavior** — the caller should poll `agent/status` until `created`.

## Env Var Rename

`generateConfig` in `process.go` currently injects `OAR_SESSION_ID`. Per M005 context:
- `OAR_SESSION_ID` → remove (internal)
- `OAR_AGENT_ID` = the `meta.Session.AgentID` (the agent's UUID)
- `OAR_AGENT_NAME` = the agent's name within the room
- `OAR_ROOM_NAME` = already present

The session has `session.AgentID` and `session.RoomAgent` (= agent name). So `generateConfig` adds:
```go
{Name: "OAR_AGENT_ID",   Value: session.AgentID},
{Name: "OAR_AGENT_NAME", Value: session.RoomAgent},
```
replacing the `OAR_SESSION_ID` line.

**Note**: `OAR_SESSION_ID` removal may break `room-mcp-server` (S06 scope). For S04, add `OAR_AGENT_ID`/`OAR_AGENT_NAME` and keep `OAR_SESSION_ID` as a deprecated alias (remove in S06). **Or**: S06's room-mcp-server rewrite will handle this. The safe S04 approach is to add the new vars without removing the old one — S06 removes the deprecated `OAR_SESSION_ID` when room-mcp-server is rewritten.

## Test Strategy

**R048 validation target**: "Integration test: create returns creating → poll status → transitions to created or error"

`TestARIAgentCreateAsync` (uses `newSessionTestHarness`):
1. `agent/create` → assert result.State == "creating"
2. Poll `agent/status` in a loop (max 30s) until state != "creating"
3. Assert final state == "created"
4. Assert no error

`TestARIAgentRestartAsync` (replaces `TestARIAgentRestartStub`, uses `newSessionTestHarness`):
1. create → prompt (auto-start) → stop agent
2. `agent/restart` → returns immediately (state should transition to creating)
3. Poll `agent/status` until state != "creating" → assert state == "created"
4. Verify agent responds to a second prompt
5. Stop + delete cleanup

`TestARIAgentCreateAsyncErrorState` (optional, tests failure path):
- Use an invalid runtimeClass to cause bootstrap failure
- Assert final state == "error" with non-empty error message

## Seams (Task Division)

**T01 — Async create in `handleAgentCreate`**
- File: `pkg/ari/server.go`
- Change `handleAgentCreate` to return state `creating`, launch background goroutine
- Add `TestARIAgentCreateAsync` test
- **Verify**: `go test ./pkg/ari/... -run TestARIAgentCreateAsync` passes; existing tests still pass; `TestARIAgentCreateAndList` check: initial state returns `creating` now (test must be updated to accept `creating`)

**T02 — Real `handleAgentRestart`**
- File: `pkg/ari/server.go`
- Replace MethodNotFound stub with real implementation
- File: `pkg/ari/server_test.go` — replace `TestARIAgentRestartStub` with `TestARIAgentRestartAsync`
- File: `cmd/agentdctl/agent.go` — add `agentRestartCmd`
- **Verify**: `go test ./pkg/ari/... -run TestARIAgentRestart` passes; `go build ./...` clean

**T03 — Env var alignment in `generateConfig`**
- File: `pkg/agentd/process.go`
- Add `OAR_AGENT_ID`, `OAR_AGENT_NAME`; keep `OAR_SESSION_ID` as deprecated alias
- **Verify**: `go build ./...` clean; existing tests pass (no behavioral change if old var retained)

## Key Constraints and Gotchas

1. **`AgentManager.Create()` default state**: `agent.go:Create()` sets `agent.State = AgentStateCreated` if empty. The handler must explicitly set `agent.State = AgentStateCreating` before calling `agents.Create()`.

2. **Background goroutine context**: Must use `context.Background()` with a bounded timeout (e.g., 60s) — not the request context which closes when the RPC returns.

3. **`deliverPrompt` state check**: Uses `session.State == meta.SessionStateCreated` to trigger auto-start. After async create, the session is `running`. This is correct — no auto-start needed. But `handleAgentPrompt` doesn't check agent state before calling `deliverPrompt`. If the agent is in `creating` state, the session is also in `creating` state — `deliverPrompt` will fail with "session X is in state creating (must be 'created' to start)". This produces a reasonable error. Consider adding an explicit check in `handleAgentPrompt`: if agent.State == creating, return "agent not ready" error.

4. **Session-level state during async create**: The session starts in `creating`. `ProcessManager.Start` checks `session.State != meta.SessionStateCreated` and returns an error — **this is a problem**: the session needs to be in `created` state when `Start` is called. Two options:
   - Create session in `creating`, then transition to `created` before calling `Start` (inside background goroutine)
   - Create session directly in `created` state (simpler — no intermediate `creating` state for the session)
   
   **Recommended**: Create session in `created` state in the background goroutine (after the handler has already returned `creating`). The agent-level `creating` state is the external observable state; the internal session state can start at `created` since ProcessManager.Start requires it.

5. **ON DELETE SET NULL for restart**: When `handleAgentRestart` deletes the old session before creating a new one, the agent row's `sessions.agent_id` FK is SET NULL. The agent row itself is not deleted. After creating the new session, the new session's `agent_id` FK will point to the same agent. This is the correct sequence.

6. **Test `TestARIAgentCreateAndList`** currently asserts `result.State == "created"`. After async create, it will be `"creating"`. This test (in `newTestHarness`, no real runtime) must be updated to accept `"creating"`.

7. **`TestARIAgentStatus`** similarly asserts `"created"` — must be updated.

8. **`TestARIAgentDeleteRequiresStopped`** — after async create with no runtime, the agent stays `creating`. The delete precondition check in `AgentManager.Delete` checks `AgentStateStopped`. A `creating` agent is not stopped, so delete should fail. The test currently creates with `"default"` (no real runtime) and asserts delete fails — this still works since `creating != stopped`. No change needed.

9. **Grep count "agent/"**: `grep -c '"agent/' pkg/ari/server.go` currently 10. Adding restart implementation doesn't add new dispatch cases (restart is already dispatched). Count stays 10.

## Verification Commands

```bash
# Full test suite
go test ./pkg/ari/... -count=1 -timeout 120s
go test ./pkg/agentd/... -count=1 -timeout 60s

# Async create integration test
go test ./pkg/ari/... -run TestARIAgentCreateAsync -v -timeout 60s

# Restart integration test
go test ./pkg/ari/... -run TestARIAgentRestartAsync -v -timeout 90s

# Build check
go build ./...

# CLI restart subcommand
go build -o /tmp/agentdctl ./cmd/agentdctl
/tmp/agentdctl agent restart --help

# State machine: agent/create returns creating
# (test assertion: result.State == "creating")
```

## Skills Discovered

None needed — pure Go, no unfamiliar libraries.

## Forward Intelligence for Planner

- **The hardest part is the goroutine/context design**: background goroutine must not be tied to the request context. See `process.go:forkShim` for the established pattern (uses `exec.Command` not `exec.CommandContext` for the same reason).
- **Session state trick**: Create session in `created` state (not `creating`) inside the background goroutine, so `ProcessManager.Start`'s precondition is satisfied without a separate transition step.
- **Test update scope is larger than it looks**: at least `TestARIAgentCreateAndList`, `TestARIAgentStatus`, `TestARIAgentRestartStub` need updating. The planner should audit all test assertions for `"created"` as initial state.
- **`deliverPrompt` + `creating` agent**: Add a guard in `handleAgentPrompt` — if `agent.State == creating`, return "agent is still being provisioned, poll agent/status" with CodeInvalidParams. This prevents confusing errors from the session-layer.
- **Restart idempotency**: If restart is called on an agent already in `creating` state (background goroutine running), should it error or be idempotent? Simplest correct answer: return an error "agent is already being provisioned". The state machine already handles this implicitly — `creating` is not in the allowed states for restart.
