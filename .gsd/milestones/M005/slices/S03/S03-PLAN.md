# S03: ARI Agent Surface — Method Migration

**Goal:** Replace the external ARI dispatch surface: remove all 9 `session/*` handler methods from the external JSON-RPC dispatch table, add 10 `agent/*` handler methods, introduce `AgentManager` in `pkg/agentd/agent.go`, add Agent request/response types to `pkg/ari/types.go`, update `Server.New()` constructor, rewrite `room/send` to resolve agents via the agents table, and migrate the `agentdctl` CLI from `session` to `agent` subcommands.
**Demo:** After this: TBD

## Tasks
- [x] **T01: Added AgentManager to pkg/agentd with Create/Get/GetByRoomName/List/UpdateState/Delete, domain error types, and 9 passing unit tests** — Create `pkg/agentd/agent.go` with an `AgentManager` type that wraps `meta.Store` and provides Create/Get/GetByRoomName/List/UpdateState/Delete with domain error types. Add `pkg/agentd/agent_test.go` with unit tests covering the full lifecycle and all error paths. AgentManager mirrors the SessionManager pattern established in session.go.

Design constraints:
- AgentManager uses `meta.AgentState` directly (not meta.SessionState) per D070
- Delete enforces `stopped` precondition — returns `ErrDeleteNotStopped` if agent state is not stopped
- Error types: `ErrAgentNotFound`, `ErrDeleteNotStopped`, `ErrAgentAlreadyExists`
- `ErrAgentAlreadyExists` wraps the unique-violation message from `meta.Store.CreateAgent` (checks for `already exists` substring)
- Uses `slog` with `component=agentd.agent` for structured logging (same pattern as session.go)
- `UpdateState` calls `store.UpdateAgent(ctx, id, state, errorMessage, nil)` — no label mutation here
- `Delete` validates stopped state before calling `store.DeleteAgent`
- `Create` sets default state to `AgentStateCreated` if empty (S03 uses synchronous create — state=created immediately, not creating; async creating state is S04)

Unit tests in `pkg/agentd/agent_test.go` must cover:
1. Create round-trip: create → get, verify fields
2. GetByRoomName: create → get by room+name
3. List with state filter
4. List with room filter
5. UpdateState: create → updateState → get, verify state
6. Delete requires stopped: create → updateState(stopped) → delete succeeds
7. Delete protected: create (state=created) → delete returns ErrDeleteNotStopped
8. Get not found: returns nil, nil
9. Delete not found: returns ErrAgentNotFound
  - Estimate: 45m
  - Files: pkg/agentd/agent.go, pkg/agentd/agent_test.go, pkg/agentd/session.go
  - Verify: go test ./pkg/agentd/... -count=1 -timeout 60s -run TestAgent
- [x] **T02: Replaced all 9 session/* dispatch cases with 10 agent/* handlers, rewrote room/send to use the agents table, fixed ON DELETE SET NULL agent/delete ordering bug, and migrated the full test suite to agent/* surface — all tests pass** — This is the largest and highest-risk task in S03. It makes three changes in lockstep:

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
  - Estimate: 2h
  - Files: pkg/ari/types.go, pkg/ari/server.go, pkg/ari/server_test.go
  - Verify: go test ./pkg/ari/... -count=1 -timeout 120s && grep -c '"agent/' pkg/ari/server.go | grep -q '^10' || (echo 'expected 10 agent methods' && false) ; grep -q '"session/new"' pkg/ari/server.go && echo 'FAIL: session/new still in dispatch' || echo 'PASS: session/* removed'
- [x] **T03: Migrated agentdctl CLI from session/* to agent/* subcommands, deleted session.go, extracted shared helpers to helpers.go, updated daemon health check to agent/list, and wired AgentManager into cmd/agentd/main.go** — Three mechanical changes:

1. **`cmd/agentdctl/agent.go`** (new file) — Create `agentCmd` cobra command with subcommands mirroring the session.go pattern:
   - `agent create` — flags: `--room`, `--name`, `--workspace-id`, `--runtime-class`, `--description`, `--system-prompt`; calls `agent/create`; prints agentId
   - `agent list` — flags: `--room`, `--state`; calls `agent/list`; prints JSON
   - `agent status <agent-id>` — calls `agent/status`; prints JSON
   - `agent prompt <agent-id>` — flag: `--text`; calls `agent/prompt`; prints stop reason
   - `agent stop <agent-id>` — calls `agent/stop`
   - `agent delete <agent-id>` — calls `agent/delete`
   - `agent attach <agent-id>` — calls `agent/attach`; prints socket path
   - `agent cancel <agent-id>` — calls `agent/cancel`
   Follow the same patterns as session.go (getClient(), cobra.ExactArgs(1), JSON output via json.Marshal, error handling via cmd.ErrOrStderr()).

2. **`cmd/agentdctl/main.go`** — Replace `rootCmd.AddCommand(sessionCmd)` with `rootCmd.AddCommand(agentCmd)`. Remove any import of session.go symbols. The file `session.go` itself can be deleted (or left in place but with its init/var removed so it compiles without contributing to rootCmd). Simplest approach: delete session.go entirely.

3. **`cmd/agentdctl/daemon.go`** — Change health check from `session/list` to `agent/list`: replace `client.Call("session/list", SessionListParams{}, &SessionListResult{})` with `client.Call("agent/list", AgentListParams{}, &AgentListResult{})`.

4. **`cmd/agentd/main.go`** — Construct `AgentManager` after `SessionManager` and pass it to `ari.New()`:
   ```go
   agents := agentd.NewAgentManager(store)
   // update ari.New() call to include agents parameter
   srv := ari.New(manager, registry, sessions, agents, processes, runtimeClasses, cfg, store, cfg.Socket, cfg.WorkspaceRoot)
   ```
   Import `agentd` is already present. Just add the two lines and update the ari.New() call.

Note: `cmd/agentdctl/session.go` should be deleted so that `sessionCmd` is no longer defined and registered. Verify the file is removed and the build compiles cleanly.
  - Estimate: 45m
  - Files: cmd/agentdctl/agent.go, cmd/agentdctl/session.go, cmd/agentdctl/main.go, cmd/agentdctl/daemon.go, cmd/agentd/main.go
  - Verify: go build ./... && go build -o /tmp/agentdctl ./cmd/agentdctl && /tmp/agentdctl agent --help && ! /tmp/agentdctl --help 2>&1 | grep -q 'session'
