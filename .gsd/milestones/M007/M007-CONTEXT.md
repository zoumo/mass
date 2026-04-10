# M007: OAR Platform Terminal State Refactor — Context

**Gathered:** 2026-04-10
**Status:** Ready for planning

## Project Description

Open Agent Runtime (OAR) is a layered runtime for headless coding agents. agentd manages ACP-speaking agent processes through agent-shim, with ARI as the control interface. This milestone is a full-platform terminal-state refactor: clean cut, no compat layer, no incremental migration.

## Why This Milestone

The current system has three overlapping state systems (spec.Status, meta.AgentState, meta.SessionState), a Room+Workspace dual resource model, and a Session concept that duplicates Agent 1:1. The core contradiction: shim is the ground truth for agent state, but agentd maintains an independent DB state via "guess-then-correct" sync. This causes inconsistency on crashes, confusing API surfaces, and recovery complexity. This refactor cuts to the terminal state in one pass.

## Codebase Brief

### Technology Stack
- Go 1.24, pure Go after this milestone (bbolt replaces SQLite/CGo)
- JSON-RPC 2.0 over Unix domain sockets (ARI)
- ACP protocol via coder/acp-go-sdk
- go.etcd.io/bbolt v1.4.3 (newly added to go.mod)

### Key Modules
- `pkg/meta` — metadata store (full rewrite: bbolt, new models)
- `pkg/spec` — OAR Runtime Spec types (StatusCreated→StatusIdle, StatusError added)
- `pkg/runtime` — shim-side: writes state.json (updated to write "idle")
- `pkg/agentd` — agentd business logic (session.go deleted, agent.go/process.go/recovery.go rewritten)
- `pkg/ari` — ARI JSON-RPC server (types.go + server.go full rewrite)
- `cmd/agentdctl` — CLI (adapted for workspace/name identity)
- `cmd/room-mcp-server` → `cmd/workspace-mcp-server` (renamed + updated)
- `tests/integration` — full rewrite for new (workspace, name) API surface

### Patterns in Use
- Branch-per-slice; squash merge to main per completed slice
- Unit tests in each package (testify suite); integration tests in tests/integration/
- sourcegraph/jsonrpc2 for all JSON-RPC communication
- Existing mockagent binary for integration tests
- K025: macOS socket path ≤ 104 chars — use short /tmp paths in tests

## User-Visible Outcome

### When this milestone is complete, the user can:
- Call `workspace/create {name, source}` → workspace prepared asynchronously, poll `workspace/status` until `phase: ready`
- Call `agent/create {workspace, name, runtimeClass}` → returns `state: creating`; poll until `state: idle`
- Call `agent/prompt {workspace, name, prompt}` → agent processes work
- Call `workspace/send {workspace, from, to, message}` → routes message to target agent
- Restart agentd — agents with `restartPolicy: tryReload` reconnect with session history; `alwaysNew` agents bootstrap fresh
- No Session, Room, AgentState, SessionState, or agentId UUID anywhere in the API

### Entry point / environment
- Entry point: ARI Unix socket (`agentd`), `agentdctl` CLI
- Environment: local dev
- Live dependencies: mockagent (for tests), optional real ACP runtimes

## Completion Class
- Contract complete means: `go test ./...` passes; `go build ./...` passes; no Session/Room/sqlite3/agentId references in codebase
- Integration complete means: TestEndToEndPipeline passes with new workspace/agent lifecycle
- Operational complete means: TestAgentdRestartRecovery passes with tryReload/alwaysNew

## Architectural Decisions

### Single State Enum: spec.Status

**Decision:** Delete meta.AgentState and meta.SessionState. Use spec.Status everywhere. Rename StatusCreated→StatusIdle (value "idle"). Add StatusError (value "error").

**Rationale:** Three state systems create inconsistency and confusion. The shim already uses spec.Status in state.json; unifying to one type eliminates the translation layer.

**Evidence:** recovery.go already reads shim state and overwrites DB state — shim is de facto ground truth. The "created" semantic was ambiguous; "idle" is unambiguous.

**Alternatives Considered:**
- Keep meta.AgentState as alias — rejected (violates "no compat layer" principle)
- Translate at agentd boundary only — rejected (leaves shim writing "created" while agentd reads "idle")

---

### bbolt Replaces SQLite

**Decision:** Replace mattn/go-sqlite3 (CGo) with go.etcd.io/bbolt (pure Go). Delete schema.sql and all migration logic.

**Rationale:** agentd is single-process with low write concurrency — bbolt's single-writer model is not a constraint. Eliminates CGo build dependency. New object model fits KV storage better than relational tables.

**Evidence:** bbolt v1.4.3 added to go.mod. Bucket structure: `v1/{workspaces/{name}, agents/{workspace}/{name}}`.

**Alternatives Considered:**
- Keep SQLite, adapt schema — rejected (design doc explicitly specifies bbolt; CGo is a real pain)

---

### Workspace Replaces Room+Namespace

**Decision:** Workspace is the single top-level cluster-scoped resource. Serves as both agent grouping boundary and filesystem working directory. Room and Namespace eliminated.

**Rationale:** Room spec was orchestrator-owned desired state; realized Room in agentd was a thin projection. Merging removes a conceptual layer with no behavioral loss.

**Evidence:** D015/D019 established Room ownership model; design doc explicitly supersedes both.

---

### Agent Identity: (workspace, name) — No UUID

**Decision:** All ARI methods identify agents by `{workspace, name}` pair. No opaque agentId UUID.

**Rationale:** UUIDs are not human-meaningful; (workspace, name) provides stable, meaningful identity.

**Evidence:** D061 established agent as external object with room+name; this milestone converts room→workspace.

**Alternatives Considered:**
- Single `--agent workspace/name` flag — rejected by user (use separate --workspace + --name flags)

---

### Shim Write Authority: Absolute Boundary

**Decision:** After shim bootstrap, agentd NEVER writes idle/running/stopped/error directly to DB. Only "creating" (at create/restart) and pre-shim "error" are written by agentd. All post-bootstrap state comes through runtime/stateChange notifications.

**Rationale:** "Guess-then-correct" pattern causes DB/shim inconsistency on crashes. Absolute boundary eliminates the class. DB used as fast admission gate only.

**Evidence:** recovery.go already does "read shim state → overwrite DB" — formalizing this as the only path is a natural extension.

**Alternatives Considered:**
- DB as primary, shim as secondary — rejected (crashes prove shim is the real truth)

---

### RestartPolicy: tryReload vs alwaysNew

**Decision:** Agent.Spec.RestartPolicy governs recovery. tryReload: reads ACP sessionId from shim state file, calls session/load, falls back silently to alwaysNew on any failure. alwaysNew: always starts fresh. Full tryReload semantics implemented (not skeleton).

**Rationale:** Some ACP runtimes support session/load for conversation continuity. RestartPolicy makes this per-agent configurable.

**Alternatives Considered:**
- Skeleton-only tryReload — rejected by user (full attempt required)

---

### CLI Identity Flags

**Decision:** agentdctl uses `--workspace` and `--name` as separate flags for agent identity.

**Rationale:** Matches (workspace, name) data model; kubectl-style familiar. Confirmed by user.

---

### workspace-mcp-server Rename

**Decision:** Rename cmd/room-mcp-server/ to cmd/workspace-mcp-server/. OAR_ROOM_NAME→OAR_WORKSPACE_NAME. Calls workspace/send + workspace/status. Binary name: workspace-mcp-server.

**Rationale:** Directory name should match binary purpose. Confirmed by user.

---

### workspace/send In Scope

**Decision:** workspace/send is in scope for M007. Routes prompt from one agent to another within the same workspace. Rejected if target agent is in error state.

**Rationale:** Replaces room/send. User confirmed in scope during discussion Round 3.

## Interface Contracts

### ARI Method Surface (new)

```
workspace/create    {name, source, hooks?, labels?}              → {name, phase}
workspace/status    {name}                                        → {name, phase, path?, members[]}
workspace/list      {}                                            → {workspaces[]}
workspace/delete    {name}                                        → {} (blocked if agents exist)
workspace/send      {workspace, from, to, message}               → {delivered}

agent/create        {workspace, name, runtimeClass, restartPolicy?, systemPrompt?, labels?} → {workspace, name, state:"creating"}
agent/prompt        {workspace, name, prompt}                    → {accepted}
agent/cancel        {workspace, name}                            → {}
agent/stop          {workspace, name}                            → {}
agent/delete        {workspace, name}                            → {} (requires stopped/error)
agent/restart       {workspace, name}                            → {} (requires stopped/error)
agent/list          {workspace, state?}                          → {agents[]}
agent/status        {workspace, name}                            → AgentInfo
agent/attach        {workspace, name}                            → event stream

Events: agent/update, agent/stateChange
```

### bbolt Bucket Contract

```
v1
  workspaces/{name}          → Workspace JSON
  agents/{workspace}/{name}  → Agent JSON
```

### spec.Status Values (new)

```
"creating"  — bootstrap in progress (agentd writes)
"idle"      — was "created"; ACP handshake done, ready for prompt (shim writes)
"running"   — processing a turn (shim writes)
"stopped"   — process exited (shim writes; or agentd writes when shim confirmed dead)
"error"     — failure (shim writes post-bootstrap; agentd writes pre-bootstrap)
```

### Agent Object

```go
type Agent struct {
    Metadata ObjectMeta   // {Name, Workspace, Labels, CreatedAt, UpdatedAt}
    Spec     AgentSpec    // {RuntimeClass, RestartPolicy, Description, SystemPrompt}
    Status   AgentStatus  // {State, ErrorMessage, ShimSocketPath, ShimStateDir, ShimPID, BootstrapConfig}
}
```

### Workspace Object

```go
type Workspace struct {
    Metadata ObjectMeta       // {Name, Labels, CreatedAt, UpdatedAt}
    Spec     WorkspaceSpec    // {Source, Hooks?}
    Status   WorkspaceStatus  // {Phase: pending/ready/error, Path}
}
```

## Error Handling Strategy

1. **bbolt open failure** — daemon exits with clear error; no partial init
2. **workspace not ready on agent/create** — JSON-RPC error "workspace not ready: phase=pending"
3. **agent already exists (same workspace+name)** — JSON-RPC error "agent already exists"
4. **shim stale-state mismatch** — DB fast gate allows, shim rejects → agentd syncs DB from shim state immediately, returns shim's result
5. **tryReload: session/load failure or state file missing** — silent fallback to alwaysNew; logged at Info
6. **workspace/delete with live agents** — scan agents bucket; return error "workspace has active agents"
7. **bbolt write transaction failure** — log, return internal error; DB remains consistent

## Final Integrated Acceptance

To call this milestone complete, we must prove:
- `TestEndToEndPipeline` passes: workspace/create → agent/create → agent/prompt → agent/stop → agent/delete → workspace/delete
- `TestAgentdRestartRecovery` passes: daemon restart reconnects live shims; dead shims handled per RestartPolicy
- `go test ./...` → all unit tests pass
- `golangci-lint run ./...` → 0 issues
- `rg 'meta\.AgentState|meta\.SessionState|go-sqlite3|schema\.sql' --type go` → zero matches

## Testing Requirements

- **S01:** Unit tests for all bbolt CRUD ops (pkg/meta/*_test.go fully rewritten)
- **S02:** Unit tests for AgentManager, ProcessManager shim boundary; recovery unit tests for both RestartPolicies
- **S03:** ARI handler tests over Unix socket; workspace/send routing test
- **S04:** Build verification; CLI smoke tests; workspace-mcp-server build
- **S05:** Full integration tests with mockagent; restart recovery; lint gate

## Acceptance Criteria

**S01:**
1. `go test ./pkg/meta/...` passes — Workspace+Agent CRUD with bbolt
2. `go build ./...` succeeds — all packages compile with new types
3. `schema.sql`, `session.go`, `room.go` deleted; zero references remain
4. `spec.StatusIdle` (value "idle") used everywhere; pkg/runtime writes "idle" to state.json
5. `meta.AgentState` and `meta.SessionState` types do not exist

**S02:**
1. `go test ./pkg/agentd/...` passes — AgentManager CRUD, shim boundary enforcement
2. Recovery unit test: tryReload attempts session/load and falls back on failure; alwaysNew skips
3. deliverPromptAsync gates on StatusIdle, syncs from shim on mismatch
4. `pkg/agentd/session.go` deleted; no Session type in agentd
5. No direct post-bootstrap state writes in process.go

**S03:**
1. `workspace/create` → `{phase:"pending"}`; `workspace/status` → `{phase:"ready", path:"..."}`
2. `agent/create` → `{state:"creating"}`; `agent/status` → `{state:"idle"}` after bootstrap
3. `agent/prompt` rejected for creating/stopped/error state
4. `workspace/send` routes to target agent; rejected for error-state target
5. No agentId UUID in any ARI request/response

**S04:**
1. `agentdctl workspace create --name w --source-type emptyDir` works
2. `agentdctl agent create --workspace w --name a --runtime-class mockagent` works
3. `go build ./cmd/workspace-mcp-server` succeeds; OAR_WORKSPACE_NAME env var used
4. `go build ./...` — no cmd/room-mcp-server references

**S05:**
1. `TestEndToEndPipeline` passes with new pipeline
2. `TestAgentdRestartRecovery` passes with new recovery model
3. `go test ./tests/integration/... -v -timeout 120s` — all non-skip tests pass
4. `golangci-lint run ./...` → 0 issues

## Risks and Unknowns

- **Compilation continuity S01→S02** — S01 must include mechanical renames in pkg/agentd+pkg/ari to keep build green; must be scoped so S01 doesn't become a mega-slice
- **session/load ACP SDK support** — coder/acp-go-sdk v0.6.3 may not expose session/load; tryReload must fall back cleanly; verify during S02 planning
- **workspace/send routing** — must handle auto-start semantics correctly (same deliverPrompt helper pattern as room/send)

## Existing Codebase / Prior Art

- `docs/plan/unified-state-design.md` — **authoritative design spec for this entire milestone**
- `pkg/meta/store.go` — current SQLite store; reference for Open/Close pattern
- `pkg/agentd/recovery.go` — current recovery logic; rewrite target for RestartPolicy
- `pkg/agentd/process.go` — shim boundary enforcement goes here
- `pkg/ari/server.go` — full handler rewrite; deliverPromptAsync pattern to preserve
- `tests/integration/e2e_test.go` — rewrite target for new pipeline
- `tests/integration/restart_test.go` — rewrite target for new recovery
- `cmd/room-mcp-server/main.go` — rename + update target

> See `.gsd/DECISIONS.md` for all architectural and pattern decisions.

## Relevant Requirements

- R047 — Workspace as unified resource (S01+S03)
- R048 — (workspace, name) identity (S01+S03)
- R049 — spec.Status single enum (S01)
- R050 — bbolt backend (S01)
- R051 — Shim write authority (S02)
- R052 — RestartPolicy (S02)
- R053 — workspace/send (S03)
- R054 — workspace-mcp-server (S04)
- R044 — Hardening follow-on (S02 covers restart/recovery portion)

## Scope

### In Scope
- pkg/meta full rewrite (bbolt, new models, delete Session/Room/WorkspaceRef)
- pkg/spec StatusIdle + StatusError; pkg/runtime state.json value "idle"
- pkg/agentd full adaptation (no Session, shim write authority, RestartPolicy recovery)
- pkg/ari full rewrite (workspace/* + agent/* + workspace/send)
- cmd/agentdctl (--workspace --name flags)
- cmd/room-mcp-server → cmd/workspace-mcp-server
- tests/integration rewritten for new API
- docs/design/agentd/agentd.md + ari-spec.md replaced
- golangci-lint 0 issues maintained

### Out of Scope / Non-Goals
- Codex end-to-end validation (deferred per D014)
- Multi-agent orchestration beyond workspace/send
- Data migration from old SQLite — fresh start only
- No compat layer: no deprecated aliases, no migration code, no old API preserved

## Technical Constraints

- No compat layer — confirmed by user ("终态直切，无兼容层")
- macOS socket path ≤ 104 chars (K025) — short /tmp paths in all tests
- bbolt single writer — acceptable for single-process daemon
- go.etcd.io/bbolt v1.4.3 already in go.mod

## Integration Points

- `coder/acp-go-sdk` — ACP protocol; session/load for tryReload in recovery
- `sourcegraph/jsonrpc2` — ARI JSON-RPC transport (unchanged)
- `modelcontextprotocol/go-sdk` — workspace-mcp-server (unchanged)
- `mockagent` binary — integration test ACP agent (unchanged)

## Ecosystem Notes

- bbolt v1.4.3: maintained fork of BoltDB. Pure Go, ACID, single-writer B+tree. Sub-millisecond reads, serialized writes. Appropriate for agentd's single-process workload.
- coder/acp-go-sdk v0.6.3: verify session/load method exists before implementing tryReload; if absent, tryReload degrades to alwaysNew.
- golangci-lint v2 clean posture established in M006 — maintain throughout.

## Open Questions

- Does coder/acp-go-sdk v0.6.3 expose session/load? — Verify during S02 planning; if not, tryReload is purely fallback with graceful degradation.
