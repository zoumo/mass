# S07 Research: Recovery & Integration Proof

## Summary

S07 is a **targeted** slice with two distinct deliverables:

1. **Agent state reconciliation in `RecoverSessions`** — the current recovery pass reconnects shim sockets but never touches the `agents` table. After daemon restart, an agent that was `running` may have its session successfully recovered, but `agent/status` would return stale agent state from before the restart. This is the gap addressed by R052.

2. **Integration test migration** — the `tests/integration/` suite uses `session/*` methods that now return `MethodNotFound`. The tests need to be rewritten to use the `agent/*` surface, and a new `TestAgentdRestartRecovery`-equivalent needs to prove agent identity (room+name) survives daemon restart.

All `pkg/...` tests pass green. The integration tests build but would fail at runtime because they call `session/new`, `session/prompt`, etc.

---

## Requirement Coverage

**R052** (active, owner M005/S07): "Recovery operates externally by agent identity (room+name), internally by session/shim handle. Agent identity survives daemon restart."
- Validation target: TestAgentdRestartRecovery equivalent — agent survives restart with same room+name, correct state.

---

## Implementation Landscape

### 1. The Agent State Reconciliation Gap

**File**: `pkg/agentd/recovery.go` — `RecoverSessions`

`RecoverSessions` iterates non-stopped sessions, tries to reconnect to shim sockets, and transitions sessions to `stopped` on failure. It **never** touches the `agents` table.

**Problem**: After daemon restart:
- A session has `agent_id` FK to an `agents` row.
- If shim reconnects → session is `running`, but `agents.state` is whatever it was when daemon died (e.g. `running`, `created`). For `running` agents that stayed running, this happens to be correct — but the agent state is never explicitly reconciled.
- If shim is dead → session gets transitioned to `stopped` via `m.sessions.Transition(...)`, but the parent agent is NOT transitioned to `stopped` or `error`. The agent remains in whatever state it was (e.g. `running`) despite having no live session.

**Root cause**: `ProcessManager` struct has no `AgentManager` field:
```go
type ProcessManager struct {
    registry    *RuntimeClassRegistry
    sessions    *SessionManager
    store       *meta.Store
    config      Config
    // NO agents *AgentManager
    ...
}
```

`NewProcessManager` takes `(registry, sessions, store, cfg)` — no `AgentManager` parameter.

**Fix options**:

**Option A** — Inject `AgentManager` into `ProcessManager` and update agent states inline in `RecoverSessions`:
- Add `agents *AgentManager` field to `ProcessManager` struct
- Update `NewProcessManager` signature: `NewProcessManager(registry, sessions, agents, store, cfg)`
- After session recovery failure: look up session's `AgentID`, call `agents.UpdateState(ctx, agentID, meta.AgentStateError, "shim not recovered after restart")`
- After session recovery success: if session was `running`, look up agent, call `agents.UpdateState(ctx, agentID, meta.AgentStateRunning, "")`
- Must update callers: `cmd/agentd/main.go`, `pkg/ari/server_test.go` (2 harnesses), `pkg/agentd/process_test.go`, `pkg/agentd/recovery_test.go`

**Option B** — Separate `ReconcileAgentStates(ctx, agents)` function, called from `main.go` after `RecoverSessions`:
- Standalone function that queries sessions with non-nil `AgentID`, maps them to agents, updates agent states
- Keeps `ProcessManager` signature stable
- Con: less atomic — a second call could see different state than what recovery saw

**Recommendation**: **Option A** is cleaner. It keeps recovery logic co-located, and the `AgentManager` is already constructed before `ProcessManager` in `main.go`. The signature change affects exactly 5 call sites (all easy to update).

**Agent state reconciliation logic**:
- Session failed to recover (shim unreachable) → `agents.UpdateState(agentID, AgentStateError, "session lost: shim not recovered after daemon restart")`
- Session recovered, shim reports running → `agents.UpdateState(agentID, AgentStateRunning, "")`
- Session recovered, shim reports other (not running, not stopped) → log warning, keep agent state as-is
- Agent has no session (`AgentID` empty) → skip (legacy/orphan session, no agent to update)
- Agent is in `creating` state at restart time → session may not exist yet; the creating goroutine from before the crash is gone; agent should be transitioned to `error` with message "agent lost in creating state after daemon restart"

**Special case — agents stuck in `creating`**: When daemon restarts, any agent in `creating` state has a lost background goroutine. Its session may or may not exist. If session exists and shim is alive, the normal recovery applies. If no session exists (goroutine hadn't created it yet) or shim is dead, the agent should be marked `error`. This needs to be handled separately by listing agents in `creating` state and marking them as `error` if no recoverable session is found.

### 2. Integration Test Migration

**Files to migrate**: `tests/integration/session_test.go`, `tests/integration/restart_test.go`, `tests/integration/concurrent_test.go`, `tests/integration/e2e_test.go`

All use `session/*` methods (MethodNotFound), `ari.SessionNewResult`, `ari.SessionStatusResult`, `ari.SessionPromptResult`.

**New test to write**: `TestAgentdRestartRecovery` in `tests/integration/restart_test.go` (rewrite):
- Phase 1: Start agentd, create room, prepare workspace, `agent/create` agent-A + agent-B (use `pollAgentUntilReady`)
- Prompt both agents via `agent/prompt`
- Phase 2: Kill agent-B's shim, stop agentd
- Phase 3: Restart agentd with same config+metaDB
- Phase 4: Verify agent-A recoverable by room+name via `agent/status` → state is `created` or `running`, with recovery metadata
- Phase 5: Verify agent-B has state `error` (shim lost) 
- Phase 6: Post-restart `agent/prompt` to agent-A succeeds
- Phase 7: Cleanup (agent/stop, agent/delete, room/delete, workspace/cleanup)

The key R052 proof: after restart, `agent/status` accepts `agentId` OR (new) room+name should resolve to the same agent. Currently agent/status only takes `agentId` — this is fine since the agentId is stable (UUID persisted in DB). R052's "external by room+name" means: given room+name, you can reconstruct agentId via `agent/list` filtering, then use agentId for subsequent operations. The identity (room+name) maps to a stable agentId across restarts.

**Existing integration test helpers available**:
- `setupAgentdTest` in `session_test.go` — starts agentd binary with mockagent, returns client+cleanup
- `setupAgentdTestWithRuntimeClass` in `real_cli_test.go` — same but with custom runtime class YAML
- `startAgentd` / `stopAgentd` in `restart_test.go` — for multi-phase restart tests
- `waitForSocket` in `e2e_test.go` — polls for socket file

**New helpers needed**:
- `pollAgentStatus(t, client, agentId, wantState, timeout)` — polls `agent/status` until state matches
- `agentCreateViaClient(t, client, room, name, runtimeClass, workspaceId)` — calls `agent/create` via `ari.Client`
- `agentPromptViaClient(t, client, agentId, text)` — calls `agent/prompt`

**ARI client note**: `pkg/ari/client.go` has `Client.Call(method, params, result)` — generic enough. The integration tests pass params as `map[string]interface{}` so no special types needed.

**Session test files that need migration** (optional for this slice, but part of "test migration"):
- `session_test.go` — 4 tests using session/* methods; need new agent/* equivalents
- `concurrent_test.go` — uses session/* for concurrent session tests; need agent/* equivalents
- `e2e_test.go` — uses session/* for end-to-end pipeline test; rewrite as agent e2e
- `restart_test.go` — the R052 test; needs full rewrite

**Scoping note**: The slice goal says "migrate all integration tests to agent/* surface." However, given the S07 risk is "low" and the requirement is specifically R052, the planner should scope:
- **Must-have**: `TestAgentdRestartRecovery` rewrite (R052 proof)
- **Must-have**: Agent state reconciliation in `RecoverSessions`
- **Should-have**: `restart_test.go` cleaned up (remove session/* helpers)
- **Nice-to-have**: Other integration test migration (session_test.go, etc.)

The other integration tests (`session_test.go`, `concurrent_test.go`, `e2e_test.go`) test session/* methods that now always return MethodNotFound — they will fail if run. The question is whether to:
a) Delete them (they test a removed surface)
b) Rewrite them with agent/* equivalents
c) Mark them as known-failing

**Recommendation**: Rewrite `restart_test.go` (required for R052). For the others, rewrite the e2e test to use agents (valuable), and delete or skip the session lifecycle tests (surface removed). The concurrent test can be adapted to use agents. But scope tightly to avoid over-expanding S07 — the "low risk" rating means it should be focused.

---

## Key Files

| File | Role | Change needed |
|------|------|---------------|
| `pkg/agentd/recovery.go` | `RecoverSessions` — session-level recovery | Add agent state reconciliation after session recovery/failure |
| `pkg/agentd/process.go` | `ProcessManager` struct + `NewProcessManager` | Add `agents *AgentManager` field and parameter |
| `pkg/agentd/recovery_test.go` | Unit tests for RecoverSessions | Add tests for agent state reconciliation |
| `tests/integration/restart_test.go` | `TestAgentdRestartRecovery` | Full rewrite using agent/* surface |
| `cmd/agentd/main.go` | `NewProcessManager` call site | Update call signature (add `agents` arg) |
| `pkg/ari/server_test.go` | `newTestHarness` + `newSessionTestHarness` | Update 2 `NewProcessManager` call sites |
| `pkg/agentd/process_test.go` | `NewProcessManager` call site | Update call signature |
| `tests/integration/e2e_test.go` | End-to-end test | Rewrite to use agent/* (recommended) |
| `tests/integration/session_test.go` | Session lifecycle tests | Delete or skip (surface removed) |

---

## Recovery Flow After Fix

```
RecoverSessions(ctx):
  for each non-stopped session:
    err := recoverSession(ctx, session)
    if err:
      // existing: mark session stopped
      sessions.Transition(ctx, session.ID, SessionStateStopped)
      // NEW: mark parent agent error
      if session.AgentID != "":
        agents.UpdateState(ctx, session.AgentID, AgentStateError, 
            "session lost: shim not recovered after daemon restart")
    else:
      // existing: set RecoveryInfo on shimProc
      // NEW: reconcile agent state
      if session.AgentID != "" && shimState.Status == spec.StatusRunning:
        agents.UpdateState(ctx, session.AgentID, AgentStateRunning, "")

  // NEW: fix agents stuck in "creating" after restart
  // (their bootstrap goroutine was killed with the daemon)
  for each agent in AgentStateCreating:
    session := findLinkedSession(agent.ID)
    if session == nil || session.State == SessionStateStopped:
      agents.UpdateState(ctx, agent.ID, AgentStateError, 
          "agent bootstrap lost: daemon restarted during creating phase")
```

For the "creating" cleanup, this is best done as a separate step in `RecoverSessions` (or a helper called from it), after the main session recovery loop completes.

---

## Test Pattern for `TestAgentdRestartRecovery`

The test MUST:
1. Use real `agentd` binary (integration test, not unit test)
2. Use real `agent-shim` + `mockagent` binaries
3. Create agents via `agent/create`, wait via `pollAgentStatus`
4. Kill shim-B before restarting agentd (same pattern as existing restart_test.go)
5. After restart, verify via `agent/status`:
   - Agent-A: state == "running" (or "created" if shim recovered but agent state not yet updated to running)
   - Agent-B: state == "error" (shim lost → fail-closed)
6. Verify agent identity preserved: agent-A has same room+name as before restart
7. Post-restart prompt to agent-A succeeds

**New `pollAgentStatus` helper** (vs existing `pollAgentUntilReady` in `server_test.go`):
```go
// waitForAgentState polls agent/status until state matches or timeout.
func waitForAgentState(t, client, agentId, wantState, timeout) ari.AgentStatusResult
```

---

## Verification Commands

```bash
# Unit tests: recovery reconciliation
go test ./pkg/agentd/... -count=1 -run TestRecoverSessions -v -timeout 60s

# Full package suite (must stay green)
go test ./pkg/... -count=1 -timeout 120s

# Integration test (requires built binaries)
make build  # or: go build ./cmd/...
go test ./tests/integration/... -count=1 -run TestAgentdRestartRecovery -v -timeout 120s
```

---

## Ordering / Task Decomposition

**T01 — Agent state reconciliation in RecoverSessions** (pkg/agentd)
- Modify `ProcessManager` struct: add `agents *AgentManager` field
- Update `NewProcessManager(registry, sessions, agents, store, cfg)` signature
- Update `RecoverSessions`: after session failure → update parent agent to error; after session success + shim running → update agent to running
- Add "creating cleanup" pass: mark stuck-creating agents as error
- Update all `NewProcessManager` call sites (5 files)
- Add unit tests in `recovery_test.go` covering: agent error on session failure, agent running on session success, creating-state cleanup
- **Verify**: `go test ./pkg/agentd/... -count=1 -timeout 60s`; `go test ./pkg/ari/... -count=1 -timeout 120s`; `go build ./...`

**T02 — Integration test migration: restart_test.go rewrite** (tests/integration)
- Rewrite `TestAgentdRestartRecovery` to use `agent/create`, `agent/prompt`, `agent/status`, `agent/stop`, `agent/delete` via room+name
- Add `waitForAgentState` and `agentCreateViaClient` helpers
- Remove or rewrite `waitForSessionState` (old session-scoped helper)
- Remove old session/* helper functions in restart_test.go
- **Verify**: `go test ./tests/integration/... -count=1 -run TestAgentdRestartRecovery -v -timeout 120s`

**T03 — Integration test migration: other tests** (tests/integration, optional)
- Rewrite or remove `session_test.go`, `e2e_test.go`, `concurrent_test.go`
- These use session/* methods that always return MethodNotFound
- Scope: delete session_test.go session/* tests; rewrite e2e_test.go to use agents; rewrite concurrent_test.go to use agents
- **Verify**: `go test ./tests/integration/... -count=1 -timeout 120s`

---

## Risks

- **Creating-state cleanup**: The logic to detect "stuck in creating" agents requires listing agents by state from the `agents` table. `meta.AgentFilter` has a `State` field (`AgentState`), so this is `store.ListAgents(ctx, &meta.AgentFilter{State: meta.AgentStateCreating})`. The agents table has `GetAgentByRoomName` and `ListAgents` — both available.
- **No `agents` parameter in `NewProcessManager` callers**: 5 files need updating. The most fragile is `pkg/ari/server_test.go` which has two harnesses (`newTestHarness` + `newSessionTestHarness`). Both already create `agentd.NewAgentManager(store)` — they just need to pass it to `NewProcessManager`.
- **Integration test binary dependencies**: `tests/integration/` requires pre-built `bin/agentd`, `bin/agent-shim`, `bin/mockagent`. The `Makefile` should have a `make build` target. Check if CI builds these.
- **macOS socket path limit**: Existing tests already handle this with `/tmp/oar-<pid>-<counter>.sock` pattern. Reuse this in the new restart test.

---

## Forward Intelligence for Planner

1. **`RecoverSessions` agent state update needs access to both session AND agent** — the session has `AgentID` (FK to agents table), so after recovering/failing a session, `session.AgentID != ""` check is sufficient to identify which agent to update.

2. **The `creating` cleanup is a second-phase pass** — it happens AFTER the main session recovery loop because you need to know which agents were successfully recovered before marking the rest as error. A simple pattern: after the loop, call `store.ListAgents(ctx, &meta.AgentFilter{State: meta.AgentStateCreating})` and for each, check if its session was recovered; if not, mark error.

3. **`AgentManager.UpdateState` has no transition validation** (confirmed in S04/D075). This means calling `UpdateState(agentID, AgentStateRunning, "")` on an agent that's already `running` is safe — it's an upsert.

4. **The integration test restart pattern is already proven** in `restart_test.go` (Phase 1-3 pattern). The new test just changes from `session/new+prompt` to `agent/create+prompt` and from `session/status` to `agent/status`. Reuse `startAgentd`, `stopAgentd`, `waitForSocket` helpers verbatim.

5. **`ari.Client.Call` is generic** — takes method string + `interface{}` params. The integration tests don't need to import `ari.AgentCreateResult` etc.; they can use `map[string]interface{}` for params and define local structs for results, or just use `map[string]interface{}` for results too. Look at how existing integration tests decode results.

6. **`pkg/ari/types.go`** has all the ARI types (`AgentCreateResult`, `AgentStatusResult`, etc.) — the integration tests can import and use these directly for cleaner assertions.

7. **After T01, all `pkg/...` tests must still pass** — the `NewProcessManager` signature change is the main risk. There are 5 call sites; the planner should list all 5 explicitly in the task plan.
