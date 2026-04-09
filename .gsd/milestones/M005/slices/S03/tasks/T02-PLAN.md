---
estimated_steps: 75
estimated_files: 3
skills_used: []
---

# T02: Migrate ARI server to agent/* surface

This is the largest and highest-risk task in S03. It makes three changes in lockstep:

1. **`pkg/ari/types.go`** — Add all Agent* request/response types; fix the `SessionInfo.State` comment (remove `paused:warm`/`paused:cold`).

2. **`pkg/ari/server.go`** — Add `agents *agentd.AgentManager` field; update `New()` constructor; replace `session/*` dispatch cases with `agent/*` dispatch cases; implement 10 agent/* handler functions; rewrite `room/send` to use the agents table; extend `recoveryGuard` scope.

3. **`pkg/ari/server_test.go`** — Update both test harnesses (`newTestHarness` and `newSessionTestHarness`) for the new `ari.New()` signature; remove `session/*` tests (or convert them to verify `session/*` now returns `MethodNotFound`); add `agent/*` integration tests.

---

**Types to add to `pkg/ari/types.go`:**
```go
AgentCreateParams  { Room, Name, Description, RuntimeClass, WorkspaceId, SystemPrompt string; Labels map[string]string }
AgentCreateResult  { AgentId, State string }
AgentPromptParams  { AgentId, Prompt string }
AgentPromptResult  { StopReason string }
AgentCancelParams  { AgentId string }
AgentStopParams    { AgentId string }
AgentDeleteParams  { AgentId string }
AgentRestartParams { AgentId string }
AgentListParams    { Room string; State string; Labels map[string]string }
AgentListResult    { Agents []AgentInfo }
AgentInfo          { AgentId, Room, Name, Description, RuntimeClass, WorkspaceId, State string; Labels map[string]string; CreatedAt, UpdatedAt time.Time }
AgentStatusParams  { AgentId string }
AgentStatusResult  { Agent AgentInfo; ShimState *ShimStateInfo; Recovery *AgentRecoveryInfo }
AgentAttachParams  { AgentId string }
AgentAttachResult  { SocketPath string }
AgentDetachParams  { AgentId string }
```
`AgentRecoveryInfo` mirrors `SessionRecoveryInfo` (same fields, different name).
`ShimStateInfo` is already defined in types.go — reuse it.

Fix `SessionInfo.State` comment: change `"created", "running", "paused:warm", "paused:cold", "stopped"` to `"creating", "created", "running", "stopped", "error"`.

---

**Server struct changes:**
- Add `agents *agentd.AgentManager` field
- `New()` gains `agents *agentd.AgentManager` parameter (after `sessions`, before `processes` or at end — pick a consistent position; recommended: after `sessions`)
- All test harness constructors in server_test.go must be updated to pass `agentd.NewAgentManager(store)` to `ari.New()`

---

**Dispatch table changes in `Handle()`:**
- REMOVE: `session/new`, `session/prompt`, `session/cancel`, `session/stop`, `session/remove`, `session/list`, `session/status`, `session/attach`, `session/detach`
- ADD: `agent/create`, `agent/prompt`, `agent/cancel`, `agent/stop`, `agent/delete`, `agent/restart`, `agent/list`, `agent/status`, `agent/attach`, `agent/detach`
- KEEP: `workspace/*`, `room/*`

---

**Handler implementations:**

`handleAgentCreate`: validate room exists → validate workspace exists (via `store.GetWorkspace`) → generate agentId (uuid) → create `meta.Agent{ID: agentId, State: AgentStateCreated, ...}` via `agents.Create` → create `meta.Session{AgentID: agentId, WorkspaceID: workspaceId, State: SessionStateCreated, Room: room, RoomAgent: name, RuntimeClass: runtimeClass}` via `sessions.Create` → call `store.AcquireWorkspace(workspaceId, sessionId)` (NOTE: must use sessionId NOT agentId — workspace_refs.session_id is a FK to sessions(id)) → acquire in-memory registry ref using sessionId → return `AgentCreateResult{AgentId: agentId, State: "created"}`

`handleAgentPrompt`: get agent by agentId → find linked session via `store.ListSessions(AgentID: agentId)` → if no session, return error → call `deliverPrompt(sessionID, prompt)` → return `AgentPromptResult{StopReason}`

`handleAgentCancel`: get agent → find linked session → connect to shim → call cancel (mirrors handleSessionCancel)

`handleAgentStop`: get agent → find linked session → call `processes.Stop` → update agent state to stopped via `agents.UpdateState` → return nil

`handleAgentDelete`: get agent via `agents.Get` → if not found, return error → call `agents.Delete` (enforces stopped precondition via ErrDeleteNotStopped) → find linked session → call `sessions.Delete` → release in-memory registry ref

`handleAgentRestart`: return `jsonrpc2.Error{Code: CodeMethodNotFound, Message: "agent/restart not implemented (see S04)"}` — stub only

`handleAgentList`: unmarshal `AgentListParams` → build `meta.AgentFilter{State, Room}` → call `store.ListAgents` → convert to `[]AgentInfo` → return `AgentListResult`

`handleAgentStatus`: get agent → find linked session → get shim state (call `processes.GetProcess`, handle nil) → return `AgentStatusResult{Agent: agentInfoFromMeta(agent), ShimState: ...}`

`handleAgentAttach`: get agent → find linked session → get process → return socket path

`handleAgentDetach`: return nil (placeholder)

---

**`room/send` rewrite:**
Replace `store.ListSessions(Room: p.Room)` + match-on-RoomAgent with:
```go
agent, err := h.srv.store.GetAgentByRoomName(ctx, p.Room, p.TargetAgent)
// then: store.ListSessions(AgentID: agent.ID)
```
This aligns room/send with the agents table per the design constraint.

---

**`recoveryGuard` extension:**
Add `agent/prompt` and `agent/cancel` to the recovery guard comment; the guard function body is unchanged (it guards ALL callers that call `recoveryGuard()`; update the godoc comment to mention agent methods).

---

**Integration tests to add (in `pkg/ari/server_test.go`):**

For tests that do NOT need a running shim (use `newTestHarness`):
- `TestARIAgentCreateAndList` — create room → prepare workspace → agent/create → agent/list, verify returned
- `TestARIAgentCreateDuplicateName` — same room+name twice → second returns error
- `TestARIAgentCreateMissingRoom` — agent/create with non-existent room → error
- `TestARIAgentStatus` — create → agent/status → correct state returned
- `TestARIAgentDeleteRequiresStopped` — create → agent/delete → error (not stopped)
- `TestARIAgentDeleteAfterStop` — create → agent/stop → agent/delete → succeeds
- `TestARISessionMethodsRemoved` — verify session/new returns MethodNotFound
- `TestARIAgentRestartStub` — verify agent/restart returns MethodNotFound or not-implemented error

For tests that need a running shim (use `newSessionTestHarness`):
- `TestARIAgentPrompt` — create room → prepare workspace → agent/create → agent/prompt → verify stop reason
- `TestARIAgentAttach` — create room → prepare workspace → agent/create → agent/attach → socket path non-empty

Existing room tests (`TestARIRoomSend*`, `TestARIMultiAgentRoundTrip`) that call session/new must be migrated to use agent/create instead — these tests wire up room agents via agent/create in S03.

## Inputs

- ``pkg/agentd/agent.go` — AgentManager, NewAgentManager, ErrAgentNotFound, ErrDeleteNotStopped (from T01)`
- ``pkg/meta/models.go` — meta.AgentState constants, meta.Agent struct, meta.Session.AgentID field`
- ``pkg/meta/agent.go` — Store.GetAgentByRoomName, Store.ListAgents, Store.CreateAgent`
- ``pkg/meta/session.go` — Store.ListSessions with AgentID filter, Store.AcquireWorkspace`
- ``pkg/ari/types.go` — existing Session* types and ShimStateInfo, SessionRecoveryInfo to mirror`
- ``pkg/ari/server.go` — existing Server struct, New(), Handle(), all session/* handlers, deliverPrompt, recoveryGuard, handleRoomSend`
- ``pkg/ari/server_test.go` — newTestHarness, newSessionTestHarness, existing room/session test patterns`

## Expected Output

- ``pkg/ari/types.go` — all Agent* request/response types added; SessionInfo.State comment fixed`
- ``pkg/ari/server.go` — agents field + updated New(); session/* removed from dispatch; 10 agent/* handlers added; room/send uses GetAgentByRoomName; recoveryGuard comment updated`
- ``pkg/ari/server_test.go` — test harnesses updated for new ari.New() signature; agent/* integration tests added; session/* dispatch tests updated to verify MethodNotFound; room tests migrated to use agent/create`

## Verification

go test ./pkg/ari/... -count=1 -timeout 120s && grep -c '"agent/' pkg/ari/server.go | grep -q '^10' || (echo 'expected 10 agent methods' && false) ; grep -q '"session/new"' pkg/ari/server.go && echo 'FAIL: session/new still in dispatch' || echo 'PASS: session/* removed'

## Observability Impact

Failure state exposed: agent not found, linked session not found, recovery-blocked on agent/prompt+cancel all return structured JSON-RPC errors. `AgentManager` uses slog with component=agentd.agent.
