# S03 Research: ARI Agent Surface — Method Migration

## Summary

S03 is a **high-complexity, high-scope** slice. It replaces the entire external ARI dispatch surface — 9 `session/*` handler functions — with a new `agent/*` surface (10 methods). It requires a new `AgentManager` type, new `pkg/ari/types.go` agent request/response structs, updated `Server.New()` constructor, a new ARI handler dispatch table, and parallel updates to the `agentdctl` CLI and the `agentd` main entry point. The session/* handlers must stay internally accessible for room routing (room/send calls `deliverPrompt`) but must be removed from the external JSON-RPC dispatch table.

The slice is **well-specified** in the existing design docs (ari-spec.md is already the authority doc). The patterns are established (SessionManager → AgentManager mirrors session.go → agent.go). The work is large but not technically novel.

---

## Active Requirements Owned by This Slice

- **R047** — agentd exposes agent/* ARI methods as external surface; session/* is internal only. Agent identified by room+name unique key.
  - Status: partially validated (design docs done in S01; code-level validation is the S03 work)
  - What S03 must prove: `agent/*` methods registered in dispatch table; `session/*` no longer in dispatch; 47 existing tests pass + new agent/* tests pass.

---

## Implementation Landscape

### 1. What Currently Exists

**`pkg/ari/server.go`** (the ARI JSON-RPC server)
- `Server` struct has fields: `manager`, `registry`, `sessions` (agentd.SessionManager), `processes` (agentd.ProcessManager), `runtimeClasses`, `config`, `store`, `baseDir`, `path`
- `Handle()` dispatches on `req.Method` — currently has `session/new`, `session/prompt`, `session/cancel`, `session/stop`, `session/remove`, `session/list`, `session/status`, `session/attach`, `session/detach` plus `workspace/*` and `room/*`
- Key internal helper: `deliverPrompt(ctx, sessionID, text)` — handles auto-start, connect, prompt. Called by both `handleSessionPrompt` and `handleRoomSend`
- `recoveryGuard()` — blocks operational actions during recovery; currently guards: prompt, cancel, workspace/cleanup. Must also guard `agent/prompt` and `agent/cancel`

**`pkg/ari/types.go`** (request/response types)
- Session types: `SessionNewParams`, `SessionNewResult`, `SessionPromptParams`, `SessionPromptResult`, `SessionCancelParams`, `SessionStopParams`, `SessionRemoveParams`, `SessionListParams`, `SessionListResult`, `SessionInfo`, `SessionStatusParams`, `SessionStatusResult`, `SessionAttachParams`, `SessionAttachResult`, `SessionDetachParams`
- Room types: `RoomCreateParams`, `RoomCreateResult`, `RoomStatusParams`, `RoomStatusResult`, `RoomMember` (has `AgentName`, `SessionId`, `State`), `RoomSendParams`, `RoomSendResult`, `RoomDeleteParams`
- **Residual S02 issue**: `SessionInfo.State` docs still mention `paused:warm`/`paused:cold` — must be cleaned up

**`pkg/meta/agent.go`** — S02 delivered: `CreateAgent`, `GetAgent`, `GetAgentByRoomName`, `ListAgents`, `UpdateAgent`, `DeleteAgent` CRUD on `meta.Agent`

**`pkg/meta/models.go`** — `meta.AgentState` (5 constants), `meta.Agent` struct (ID, Room, Name, RuntimeClass, WorkspaceID, Description, SystemPrompt, Labels, State, ErrorMessage, CreatedAt, UpdatedAt). `meta.Session.AgentID` FK field exists.

**`pkg/agentd/session.go`** — `SessionManager` with 5-state machine. No `AgentManager` exists yet — it is explicitly deferred to S03 per D070.

**`pkg/agentd/process.go`** — `ProcessManager.Start(ctx, sessionID)` starts a shim. ProcessManager already understands sessions. S03 must ensure that agent/* methods resolve `agentId → sessionID` when forwarding to ProcessManager.

### 2. What S03 Must Build

#### A. `AgentManager` in `pkg/agentd/agent.go` (new file)

Mirrors `SessionManager` pattern. Wraps `meta.Store`. Provides:
- `Create(ctx, agent) error` — calls `store.CreateAgent`
- `Get(ctx, id) (*meta.Agent, error)` — calls `store.GetAgent`
- `GetByRoomName(ctx, room, name) (*meta.Agent, error)` — calls `store.GetAgentByRoomName`
- `List(ctx, filter) ([]*meta.Agent, error)` — calls `store.ListAgents`
- `UpdateState(ctx, id, state AgentState, errorMessage string) error` — calls `store.UpdateAgent`
- `Delete(ctx, id) error` — enforces `stopped` precondition, calls `store.DeleteAgent`

AgentManager validates `agent/delete` requires stopped state (not delegated to meta.Store). Uses `meta.AgentState` directly (per D070 — AgentManager uses AgentState, SessionManager uses SessionState).

Error types needed:
- `ErrAgentNotFound` — returned by Get/Delete when agent doesn't exist
- `ErrDeleteNotStopped` — returned by Delete when agent is not in stopped state
- `ErrAgentAlreadyExists` — wraps `meta: agent with room=X name=Y already exists`

#### B. Agent Params/Result Types in `pkg/ari/types.go`

New types to add:
```
AgentCreateParams  { Room, Name, Description, RuntimeClass, WorkspaceId, SystemPrompt, Labels }
AgentCreateResult  { AgentId, State }
AgentPromptParams  { AgentId, Prompt }
AgentPromptResult  { StopReason }
AgentCancelParams  { AgentId }
AgentStopParams    { AgentId }
AgentDeleteParams  { AgentId }
AgentRestartParams { AgentId }
AgentListParams    { Room, State, Labels }
AgentListResult    { Agents []AgentInfo }
AgentInfo          { AgentId, Room, Name, Description, RuntimeClass, WorkspaceId, State, Labels, CreatedAt, UpdatedAt }
AgentStatusParams  { AgentId }
AgentStatusResult  { AgentInfo + ShimState *ShimStateInfo + Recovery *AgentRecoveryInfo }
AgentAttachParams  { AgentId }
AgentAttachResult  { SocketPath }
AgentDetachParams  { AgentId }
```

Note: `AgentRecoveryInfo` mirrors `SessionRecoveryInfo` (same shape, different name).

Clean up `SessionInfo.State` comment (remove paused:warm/paused:cold mention — the S02 known issue).

#### C. ARI Dispatch in `pkg/ari/server.go`

**Server struct changes:**
- Add `agents *agentd.AgentManager` field
- Retain `sessions *agentd.SessionManager` (still needed for internal room routing and recovery)
- Add `agentManager` to `New()` constructor

**Dispatch table — `Handle()` method:**
- ADD: `agent/create`, `agent/prompt`, `agent/cancel`, `agent/stop`, `agent/delete`, `agent/restart`, `agent/list`, `agent/status`, `agent/attach`, `agent/detach`
- REMOVE: `session/new`, `session/prompt`, `session/cancel`, `session/stop`, `session/remove`, `session/list`, `session/status`, `session/attach`, `session/detach`
- KEEP: `workspace/*`, `room/*`

**New handlers (non-async):**
- `handleAgentCreate(ctx, conn, req)` — validate room exists, validate workspace exists, generate agentId (uuid), create meta.Agent with state=creating, create meta.Session with state=created (linking agentId), call `store.AcquireWorkspace`. Note: S03 uses **sync** create (state=created immediately after session creation) — async create with background goroutine is S04's responsibility per D063. S03 simplification: agent/create can be synchronous for S03 (returns "created", not "creating") — async bootstrap is S04.
- `handleAgentPrompt(ctx, conn, req)` — look up agent by agentId, find linked session via `store.ListSessions(SessionFilter{AgentID: agentId})`, call `deliverPrompt(sessionID, text)`
- `handleAgentCancel(ctx, conn, req)` — look up session via agentId, call `processes.Connect` → `client.Cancel`
- `handleAgentStop(ctx, conn, req)` — look up session via agentId, call `processes.Stop`, update agent state to stopped
- `handleAgentDelete(ctx, conn, req)` — validate agent is stopped (via AgentManager.Delete), release workspace ref, call `sessions.Delete` for linked session
- `handleAgentRestart(ctx, conn, req)` — deferred to S04 (set unimplemented error for now, or minimal stub)
- `handleAgentList(ctx, conn, req)` — call `store.ListAgents` with filter, return AgentListResult
- `handleAgentStatus(ctx, conn, req)` — get agent, get linked session, get shim state, return AgentStatusResult
- `handleAgentAttach(ctx, conn, req)` — get session via agentId, call `processes.GetProcess`, return socket path
- `handleAgentDetach(ctx, conn, req)` — placeholder, return nil

**Critical internal wiring:** `handleRoomSend` currently resolves `targetAgent` by calling `store.ListSessions(SessionFilter{Room: p.Room})` and matches `s.RoomAgent == p.TargetAgent`. After S03, room membership should resolve via the agents table. But room/send behavior per S06 scope — S03 should update room/send to resolve via agents: `store.GetAgentByRoomName(room, targetAgent)` → get agentId → `store.ListSessions(SessionFilter{AgentID: agentId})` → get sessionID → `deliverPrompt`.

**`recoveryGuard`** — must be extended to also guard `agent/prompt` and `agent/cancel`.

#### D. `cmd/agentdctl` CLI

- Remove `session.go` entirely — or replace `session` subcommand with `agent` subcommand
- Add `cmd/agentdctl/agent.go` with `agent create`, `agent prompt`, `agent cancel`, `agent stop`, `agent delete`, `agent list`, `agent status`, `agent attach` subcommands
- Update `main.go` to register `agentCmd` instead of `sessionCmd`
- Update `daemon.go`: health check currently calls `session/list` — change to `agent/list`
- Room CLI (`room.go`): `room/send` params unchanged (still `room`, `from`, `to`, `message`) — no change needed

#### E. `cmd/agentd/main.go` — minor wiring

- Construct `AgentManager` and pass to `ari.New()`
- Update `ari.New()` signature to accept `agentManager *agentd.AgentManager`

### 3. Key Constraints and Risks

**Constraint — deliverPrompt operates on sessionID not agentId**
The internal `deliverPrompt` helper works with `sessionID`. Agent methods receive `agentId`. S03 must add a lookup step: `agentId → session` via `store.ListSessions(AgentID: agentId)`. If a linked session doesn't exist (agent is in error state or was never bootstrapped), the prompt must fail with a clear error.

**Constraint — session/* handlers removed from external dispatch but RoomSend still needs session resolution**
`room/send` uses `deliverPrompt(sessionID, text)`. After S03, it needs to resolve `agentId → sessionID`. Two options:
1. Keep `store.ListSessions(Room: X)` → match `RoomAgent == targetAgent` (current approach, works but leaks internal detail)
2. Switch to `store.GetAgentByRoomName(room, targetAgent)` → then `store.ListSessions(AgentID: agentId)` (cleaner, uses agent identity)
Option 2 is preferred (aligns with S06 room/status using agents table).

**Risk — S04 scope boundary for async create**
S03 should implement `agent/create` synchronously (immediate "created" state after session creation) to keep scope manageable. Async create with polling is S04. The ari-spec.md shows `agent/create` returning `creating`, but S03 can return `created` immediately as a valid intermediate state (since the state machine allows `creating → created`). The plan should explicitly note this as a known deviation from the final design, intentionally deferred to S04.

**Risk — `Server.New()` constructor signature change**
Adding `agentManager *agentd.AgentManager` to `ari.New()` breaks all callers: `cmd/agentd/main.go` and `pkg/ari/server_test.go` testHarness. This is the highest blast-radius change. Must update both test harnesses (`newTestHarness` and `newSessionTestHarness`).

**Risk — workspace ref tracking for agent lifecycle**
`handleSessionNew` currently calls `store.AcquireWorkspace(workspaceId, sessionId)`. For agents, `handleAgentCreate` should call `store.AcquireWorkspace(workspaceId, agentId)` (using agentId as the ref holder, not sessionId). This is a design decision: workspace refs should be held at the agent level, not session level, because workspace release happens at `agent/delete`. This means the workspace cleanup gate (`store.GetWorkspace refCount > 0`) still works correctly.

**Risk — residual paused:* comment in types.go**
`SessionInfo.State` comment still says `"created", "running", "paused:warm", "paused:cold", "stopped"`. Must fix to `"creating", "created", "running", "stopped", "error"`. Small but must not be forgotten.

**Not in S03 scope:**
- Async create (background goroutine, polling) → S04
- room/status returning agentName/description/runtimeClass/agentState from agents table → S06
- room-mcp-server env var changes (OAR_AGENT_NAME, OAR_AGENT_ID) → S06
- Recovery logic using agent identity → S07
- Event naming (`agent/update`, `agent/stateChange`) → S05

### 4. File Inventory — Files to Create/Modify

| File | Action | What Changes |
|------|--------|-------------|
| `pkg/agentd/agent.go` | CREATE | AgentManager + error types; mirrors session.go pattern |
| `pkg/agentd/agent_test.go` | CREATE | Unit tests for AgentManager (create/get/list/updateState/delete + error cases) |
| `pkg/ari/types.go` | MODIFY | Add Agent* types (create/prompt/cancel/stop/delete/restart/list/status/attach/detach params+results); clean up SessionInfo.State comment |
| `pkg/ari/server.go` | MODIFY | Add `agents` field; update `New()`; replace session/* dispatch with agent/* dispatch; update recoveryGuard scope; rewrite room/send to use agents table |
| `pkg/ari/server_test.go` | MODIFY | Update testHarness constructors; remove session/* tests; add agent/* tests; update room/send tests |
| `cmd/agentdctl/agent.go` | CREATE | `agent` CLI subcommand (create/prompt/cancel/stop/delete/list/status/attach) |
| `cmd/agentdctl/session.go` | DELETE or RETIRE | Remove session/* CLI subcommands |
| `cmd/agentdctl/main.go` | MODIFY | Register agentCmd instead of sessionCmd; update daemon health check |
| `cmd/agentdctl/daemon.go` | MODIFY | Change health check from session/list to agent/list |
| `cmd/agentd/main.go` | MODIFY | Construct AgentManager; pass to ari.New() |

### 5. Natural Task Seams

**T01 — AgentManager (`pkg/agentd/agent.go` + tests)**
Create `AgentManager` wrapping `meta.Store`. Implement Create/Get/GetByRoomName/List/UpdateState/Delete with error types. Unit tests covering: create round-trip, get, list with filters, updateState, delete-requires-stopped, delete-not-found. ~5-6 unit tests. Provides the manager that server.go will consume.

**T02 — Agent types + Server wiring (`pkg/ari/types.go` + `pkg/ari/server.go`)**
Add Agent* request/response types. Update Server struct, `New()` constructor, dispatch table. Implement all 10 agent/* handlers. Update `room/send` to resolve via agents table. Extend `recoveryGuard`. Clean up SessionInfo.State comment. Update all test harnesses in `server_test.go`. Add comprehensive agent/* integration tests.

**T03 — CLI migration (`cmd/agentdctl/agent.go` + `cmd/agentd/main.go`)**
Create `agent.go` CLI. Remove/retire `session.go`. Update `main.go` registration. Update `daemon.go` health check. Update `cmd/agentd/main.go` daemon entry point to wire AgentManager.

### 6. Verification Plan

```bash
# All unit + integration tests pass
go test ./pkg/meta/... ./pkg/agentd/... ./pkg/ari/... -count=1 -timeout 120s

# No session/* methods in ARI dispatch table
grep -c '"session/' pkg/ari/server.go  # should be 0 (or only in comments)

# All agent/* methods present in dispatch
grep -c '"agent/' pkg/ari/server.go    # should be >= 10

# CLI has agent subcommand, not session subcommand
./bin/agentdctl agent --help

# Build succeeds
go build ./...
```

---

## Recommendation

**Decompose into 3 tasks in order:**

1. **T01**: AgentManager (`pkg/agentd/agent.go` + `pkg/agentd/agent_test.go`) — pure unit work, no server changes. Green build at end of T01.
2. **T02**: Agent types + Server wiring + integration tests (`pkg/ari/types.go`, `pkg/ari/server.go`, `pkg/ari/server_test.go`) — largest task; impacts all test harnesses.
3. **T03**: CLI + daemon entry point (`cmd/agentdctl/agent.go`, `cmd/agentdctl/main.go`, `cmd/agentdctl/daemon.go`, `cmd/agentd/main.go`) — mechanical wiring.

T01 is a prerequisite for T02 (server imports AgentManager). T02 is a prerequisite for T03 (CLI imports ari types). Sequential dependency chain: T01 → T02 → T03.

**Critical design decisions for the planner to encode explicitly:**

1. `agent/create` in S03 is **synchronous** — creates Agent record (state=created) and Session record synchronously, returns `{agentId, state:"created"}`. Async bootstrap + `creating` state is S04 work. The state machine supports both `creating` and `created` as valid initial states.

2. Workspace refs are acquired at `agent/create` time using `agentId` as the ref holder (not sessionId). Released at `agent/delete` time. This keeps workspace cleanup semantics at the agent level.

3. `session/*` handlers are fully removed from the dispatch table. The internal helper `deliverPrompt(sessionID, text)` stays (renamed or unexported). `room/send` resolves agent identity via `store.GetAgentByRoomName()` + `store.ListSessions(AgentID: agentId)`.

4. `agentdctl session` subcommand is removed. `agentdctl agent` replaces it. The daemon health check in `daemon.go` switches from `session/list` to `agent/list`.

5. Do **not** implement `agent/restart` in S03 — return `jsonrpc2.CodeMethodNotFound` or a stub `not implemented` error. Restart is S04.
