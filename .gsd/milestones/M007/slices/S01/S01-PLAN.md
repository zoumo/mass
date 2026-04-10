# S01: Storage + Model Foundation

**Goal:** Rewrite pkg/meta to use bbolt (pure Go) with new Agent+Workspace models (no Session, Room, AgentState, SessionState); add spec.StatusIdle and spec.StatusError; remove StatusCreated; update pkg/runtime to write "idle"; do a mechanical compilation sweep of all callers (pkg/agentd, pkg/ari, pkg/workspace, cmd/agentd) so that go build ./... is green and zero meta.AgentState/meta.SessionState/go-sqlite3 references remain.
**Demo:** After this: `go test ./pkg/meta/...` passes with new bbolt store; `go build ./...` is green; `rg 'meta.AgentState|meta.SessionState|go-sqlite3' --type go` returns zero matches.

## Must-Haves

- `go test ./pkg/meta/...` passes — bbolt store CRUD for Workspace and Agent
- `go build ./...` succeeds — all non-test files compile
- `rg 'meta\.AgentState|meta\.SessionState|go-sqlite3' --type go` returns zero matches
- `rg 'meta\.Session[^S]' --type go` returns zero matches (no meta.Session usage)
- pkg/meta/schema.sql, session.go, room.go deleted
- spec.StatusIdle = "idle" and spec.StatusError = "error" exist; spec.StatusCreated deleted
- pkg/runtime writes "idle" to state.json after ACP handshake

## Proof Level

- This slice proves: contract — unit tests prove bbolt CRUD contracts; build verifies compilation across all packages

## Integration Closure

S01 delivers a compilable codebase with new storage foundation and type unification. The agentd/process.go behavioral adaptation (shim write authority boundary, RestartPolicy) is left to S02. The ARI handler rewrite is left to S03. This slice is structural and mechanical — no new runtime behavior ships here.

## Verification

- bbolt store logs open/close and CRUD errors via slog.Default() with component="meta.store". No new observability surfaces versus the SQLite store.

## Tasks

- [x] **T01: Rewrite pkg/meta with bbolt store and new Agent+Workspace models** `est:2h`
  Delete all SQLite-based files and replace with a bbolt-backed store using the new object model. The new meta package has two top-level objects: Agent (identity: workspace+name, no UUID) and Workspace (identity: name, no UUID). Agent.Status holds the shim runtime fields (State spec.Status, ShimSocketPath, ShimStateDir, ShimPID, BootstrapConfig). Workspace.Status holds Phase (pending/ready/error) and Path.

bbolt bucket structure:
```
v1/
  workspaces/{name}          → Workspace JSON blob
  agents/{workspace}/{name}  → Agent JSON blob
```

Steps:
1. Delete pkg/meta/session.go, pkg/meta/room.go, pkg/meta/schema.sql, pkg/meta/session_test.go, pkg/meta/room_test.go, pkg/meta/integration_test.go.
2. Rewrite pkg/meta/models.go: define ObjectMeta{Name, Labels, CreatedAt, UpdatedAt}, AgentSpec{RuntimeClass, RestartPolicy, Description, SystemPrompt}, AgentStatus{State spec.Status, ErrorMessage, ShimSocketPath, ShimStateDir, ShimPID int, BootstrapConfig json.RawMessage}, Agent{Metadata ObjectMeta (+ Workspace field), Spec AgentSpec, Status AgentStatus}, WorkspaceSpec{Source json.RawMessage, Hooks json.RawMessage}, WorkspacePhase type (pending/ready/error), WorkspaceStatus{Phase WorkspacePhase, Path string}, Workspace{Metadata ObjectMeta, Spec WorkspaceSpec, Status WorkspaceStatus}, AgentFilter{Workspace, State spec.Status}, WorkspaceFilter{Phase WorkspacePhase}. Remove AgentState, SessionState, Session, Room, WorkspaceStatusActive/Inactive/Deleted, WorkspaceRef, CommunicationMode.
3. Rewrite pkg/meta/store.go: open bbolt at path, create v1/workspaces and v1/agents/{workspace} nested buckets, expose Close(). Use bbolt.Open(path, 0600, &bbolt.Options{Timeout: 5*time.Second}). On Open, run initBuckets() in an Update tx.
4. Rewrite pkg/meta/workspace.go: CreateWorkspace(ctx, *Workspace), GetWorkspace(ctx, name string), ListWorkspaces(ctx, *WorkspaceFilter), UpdateWorkspaceStatus(ctx, name string, status WorkspaceStatus), DeleteWorkspace(ctx, name string). Use bbolt.Update/View transactions with JSON marshalling. Key = []byte(name) in v1/workspaces bucket.
5. Rewrite pkg/meta/agent.go: CreateAgent(ctx, *Agent), GetAgent(ctx, workspace, name string), ListAgents(ctx, *AgentFilter), UpdateAgentStatus(ctx, workspace, name string, status AgentStatus), DeleteAgent(ctx, workspace, name string). Use nested bucket: bucket v1/agents then sub-bucket per workspace, key = name. ListAgents: if filter.Workspace != empty, scan only that workspace's sub-bucket; otherwise cursor over all sub-buckets.
6. Update go.mod: remove require github.com/mattn/go-sqlite3; change go.etcd.io/bbolt from indirect to direct. Run `go mod tidy`.
7. Write pkg/meta/store_test.go: test Open/Close, bucket creation, path validation.
8. Write pkg/meta/workspace_test.go: TestCreateWorkspace, TestGetWorkspace_NotFound, TestListWorkspaces, TestUpdateWorkspaceStatus, TestDeleteWorkspace, TestDeleteWorkspace_NotFound, TestDeleteWorkspace_WithAgents (should fail or succeed based on impl).
9. Write pkg/meta/agent_test.go: TestCreateAgent, TestGetAgent_NotFound, TestGetAgent_ByWorkspaceName, TestListAgents_AllWorkspaces, TestListAgents_FilterByWorkspace, TestListAgents_FilterByState, TestUpdateAgentStatus, TestDeleteAgent, TestCreateAgent_DuplicateRejected.

Constraints:
- bbolt requires all writes in Update tx, reads in View tx. No raw DB access outside transactions.
- Agent identity is (workspace, name); no UUID field.
- Workspace identity is name; no ID UUID field.
- WorkspaceStatus.Phase: "pending" = being prepared, "ready" = usable, "error" = failed.
- spec.Status is imported from pkg/spec for agent state values.
- All times stored as RFC3339 strings in JSON (json.Marshal handles this for time.Time).
- AgentStatus.BootstrapConfig is json.RawMessage; store as base64-safe JSON string.
- DeleteWorkspace should return an error if any agents exist in that workspace (scan agents/{workspace} bucket).
- Use pkg/spec.Status type for AgentStatus.State (import pkg/spec).
  - Files: `pkg/meta/models.go`, `pkg/meta/store.go`, `pkg/meta/workspace.go`, `pkg/meta/agent.go`, `pkg/meta/store_test.go`, `pkg/meta/workspace_test.go`, `pkg/meta/agent_test.go`, `go.mod`, `go.sum`
  - Verify: cd /Users/jim/code/zoumo/open-agent-runtime && go test ./pkg/meta/... -count=1 -timeout 30s && ! rg 'mattn/go-sqlite3|meta\.AgentState|meta\.SessionState' --type go pkg/meta/

- [x] **T02: Add spec.StatusIdle + spec.StatusError; update pkg/runtime to write idle** `est:30m`
  Delete spec.StatusCreated and add spec.StatusIdle (value "idle") and spec.StatusError (value "error") to pkg/spec/state_types.go. Update all callers within pkg/spec and pkg/runtime.

Steps:
1. Edit pkg/spec/state_types.go:
   - Replace `StatusCreated Status = "created"` with `StatusIdle Status = "idle"` (keep comment updated: ACP handshake done, ready for prompt)
   - Add `StatusError Status = "error"` after StatusStopped
   - Update inline docs
2. Edit pkg/runtime/runtime.go:
   - Replace `spec.StatusCreated` → `spec.StatusIdle` (appears in Create() after handshake success, and in Prompt() when resetting to idle after turn ends). Check all occurrences: `grep -n StatusCreated pkg/runtime/runtime.go`
3. Edit pkg/spec/state_test.go: replace StatusCreated → StatusIdle in test assertions.
4. Edit pkg/runtime/runtime_test.go: replace StatusCreated → StatusIdle in test assertions.

Constraints:
- Do NOT add a StatusCreated alias — no compat layer.
- The JSON value written to state.json changes from "created" to "idle" — this is intentional per D085.
- Other files outside pkg/spec and pkg/runtime that use StatusCreated will be fixed in T03/T04 (they're in packages being adapted there).
  - Files: `pkg/spec/state_types.go`, `pkg/runtime/runtime.go`, `pkg/spec/state_test.go`, `pkg/runtime/runtime_test.go`
  - Verify: cd /Users/jim/code/zoumo/open-agent-runtime && go build ./pkg/spec/... ./pkg/runtime/... && ! rg 'StatusCreated' --type go pkg/spec/ pkg/runtime/ && go test ./pkg/spec/... ./pkg/runtime/... -count=1 -timeout 30s

- [x] **T03: pkg/agentd compilation sweep — delete SessionManager, adapt to new meta.Agent model** `est:2h`
  Adapt pkg/agentd to compile with the new meta package (no Session, no Room, no AgentState, no SessionState). Delete SessionManager (it wrapped meta.Session which no longer exists). Adapt ProcessManager to use Agent identity (workspace, name) instead of session UUID. This is a mechanical adaptation — behavioral correctness (shim write authority, RestartPolicy) is left to S02.

Steps:
1. DELETE pkg/agentd/session.go and pkg/agentd/session_test.go entirely.

2. REWRITE pkg/agentd/agent.go:
   - Remove meta.AgentState; use spec.Status everywhere.
   - Change identity: Agent identified by (workspace, name) not UUID.
   - ErrDeleteNotStopped.State becomes spec.Status type.
   - ErrAgentAlreadyExists: Room → Workspace field.
   - AgentManager.Create(ctx, *meta.Agent): set default status.State=spec.StatusCreating.
   - AgentManager.Get(ctx, workspace, name string): call store.GetAgent(ctx, workspace, name).
   - AgentManager.GetByWorkspaceName → rename from GetByRoomName; params (workspace, name).
   - AgentManager.List(ctx, *meta.AgentFilter): call store.ListAgents.
   - AgentManager.UpdateStatus(ctx, workspace, name string, status meta.AgentStatus): call store.UpdateAgentStatus.
   - AgentManager.Delete(ctx, workspace, name string): check state is stopped/error then call store.DeleteAgent.
   - Remove all meta.AgentState references.

3. REWRITE pkg/agentd/process.go (mechanical — same logic, new types):
   - Remove `sessions *SessionManager` field from ProcessManager.
   - ProcessManager.NewProcessManager: remove sessions param.
   - ShimProcess: rename SessionID to AgentKey (type string, value = workspace+"/"+name).
   - ProcessManager.processes map key = workspace+"/"+name composite string.
   - Start(ctx, workspace, name string) instead of Start(ctx, sessionID string):
     * Fetch agent via m.agents.Get(ctx, workspace, name)
     * Check agent.Status.State == spec.StatusCreating (not SessionStateCreated)
     * generateConfig takes *meta.Agent
     * createBundle takes *meta.Agent
   - State transitions: replace `m.sessions.Transition(ctx, id, meta.SessionStateRunning)` with `m.agents.UpdateStatus(ctx, workspace, name, meta.AgentStatus{State: spec.StatusRunning})`
   - Stopped transition: `m.agents.UpdateStatus(ctx, workspace, name, meta.AgentStatus{State: spec.StatusStopped})`
   - generateConfig(*meta.Agent, *RuntimeClass): agent.Metadata.Name for config name; no Room/session fields; agent.Spec.SystemPrompt for systemPrompt
   - createBundle(*meta.Agent, spec.Config): use agent.Metadata.Workspace+agent.Metadata.Name as dir name fragment
   - forkShim: adapt similarly
   - After successful Start: call store.UpdateAgentStatus to set ShimSocketPath, ShimStateDir, ShimPID
   - Remove all meta.SessionStateXXX references; replace with spec.StatusXXX
   - Remove any room-related MCP injection logic (no more Room concept)

4. REWRITE pkg/agentd/recovery.go (mechanical — same logic, new types):
   - Replace `m.store.ListSessions(ctx, nil)` with `m.store.ListAgents(ctx, nil)` (nil filter = all agents)
   - Filter out terminal agents: skip spec.StatusStopped and spec.StatusError
   - Replace session.ShimSocketPath → agent.Status.ShimSocketPath
   - Replace session.ShimStateDir → agent.Status.ShimStateDir
   - Replace session.ShimPID → agent.Status.ShimPID
   - Replace m.sessions.Transition(ctx, id, stopped) → m.agents.UpdateStatus(ctx, ws, name, meta.AgentStatus{State: spec.StatusStopped})
   - Replace agent ID from UUID to (workspace, name) pair; ShimProcess.AgentKey = workspace+"/"+name
   - Update m.processes map to use workspace+"/"+name key
   - recoveredAgentIDs map: key = workspace+"/"+name

5. UPDATE pkg/agentd/agent_test.go, process_test.go, recovery_test.go, shim_client_test.go: just make them compile (fix type references; do not delete them). Replace meta.AgentState → spec.Status, meta.SessionState → spec.Status, SessionStateCreated → spec.StatusIdle, AgentStateCreated → spec.StatusIdle, AgentStateRunning → spec.StatusRunning, etc. Fix any GetByRoomName → GetByWorkspaceName, sessions.XXX removed.

6. UPDATE pkg/agentd/shim_client.go if it references spec.StatusCreated → spec.StatusIdle (grep first).

Constraints:
- The ShimProcess.AgentKey composite string format is workspace+"/"+name (matches bbolt key path convention).
- Do NOT implement shim write authority boundary (agentd still writes state directly — S02 fixes this).
- Do NOT implement RestartPolicy — that's S02.
- ProcessManager.NewProcessManager signature changes: remove sessions *SessionManager param.
- All meta.SessionState*, meta.AgentState*, meta.Session references must be eliminated.
- pkg/agentd/session.go and session_test.go are fully deleted.
  - Files: `pkg/agentd/agent.go`, `pkg/agentd/process.go`, `pkg/agentd/recovery.go`, `pkg/agentd/agent_test.go`, `pkg/agentd/process_test.go`, `pkg/agentd/recovery_test.go`, `pkg/agentd/shim_client_test.go`, `pkg/agentd/shim_client.go`
  - Verify: cd /Users/jim/code/zoumo/open-agent-runtime && go build ./pkg/agentd/... && ! rg 'SessionManager|meta\.AgentState|meta\.SessionState|meta\.Session[^S]' --type go pkg/agentd/

- [x] **T04: pkg/ari + pkg/workspace + cmd/agentd compilation sweep — final green build** `est:1.5h`
  Adapt the remaining callers (pkg/ari, pkg/workspace, cmd/agentd) to compile with the new meta and agentd types. pkg/ari/server.go is rewritten from scratch in S03 — for this task, replace it with a minimal compilable stub (same Serve/Shutdown interface, empty implementations). This avoids 1663 lines of mechanical adaptation that will be discarded in S03.

Steps:
1. REWRITE pkg/ari/registry.go:
   - WorkspaceMeta: replace Id UUID with Name string as key; keep Path, Spec, Status, RefCount, Refs fields but Refs is []string of agent keys (workspace+"/"+name).
   - Registry.workspaces map key = name (not UUID).
   - Add(name, path, spec), Get(name), Remove(name), Acquire(name, agentKey), Release(name, agentKey).
   - RebuildFromDB(store *meta.Store): use store.ListWorkspaces(ctx, nil) (new API, no UUID, no ListWorkspaceRefs). For each workspace, populate Registry entry from ws.Metadata.Name, ws.Status.Path, ws.Status.Phase == "ready". No RefCount from DB needed (not tracked in new model).
   - Update registry_test.go to compile with new WorkspaceMeta shape.

2. REWRITE pkg/ari/types.go:
   - Remove all Session* types (SessionNewParams, SessionListParams, SessionStatusResult, etc.).
   - Remove Room* types.
   - WorkspaceCreateParams: {Name, Source json.RawMessage, Labels map[string]string}.
   - WorkspaceStatusResult: {Name, Phase, Path string, Members []AgentInfo}.
   - WorkspaceListResult: {Workspaces []WorkspaceInfo}.
   - WorkspaceInfo: {Name, Phase, Path string}.
   - AgentCreateParams: {Workspace, Name, RuntimeClass, RestartPolicy, SystemPrompt, Labels}.
   - AgentPromptParams: {Workspace, Name, Prompt string}.
   - AgentStopParams, AgentDeleteParams, AgentRestartParams, AgentCancelParams, AgentStatusParams, AgentListParams: use Workspace+Name fields (no AgentId UUID).
   - AgentInfo: {Workspace, Name, RuntimeClass, State string (= spec.Status value), ErrorMessage, Labels, CreatedAt}.
   - AgentListResult: {Agents []AgentInfo}.
   - WorkspaceSendParams: {Workspace, From, To, Message string}.
   - WorkspaceSendResult: {Delivered bool}.
   - Keep CodeRecoveryBlocked constant.
   - Remove AgentState field (was string) from old types.

3. REPLACE pkg/ari/server.go with a minimal compilable stub:
   - Keep package ari declaration and imports of sourcegraph/jsonrpc2, agentd, meta, workspace.
   - Define Server struct with fields: manager *workspace.WorkspaceManager, registry *Registry, agents *agentd.AgentManager, processes *agentd.ProcessManager, runtimeClasses *agentd.RuntimeClassRegistry, config agentd.Config, store *meta.Store, socketPath string, baseDir string.
   - func New(manager *workspace.WorkspaceManager, registry *Registry, agents *agentd.AgentManager, processes *agentd.ProcessManager, runtimeClasses *agentd.RuntimeClassRegistry, config agentd.Config, store *meta.Store, socketPath, baseDir string) *Server — note: no sessions param.
   - func (s *Server) Serve() error — returns nil (stub; real impl in S03).
   - func (s *Server) Shutdown(ctx context.Context) error — returns nil (stub).
   - Add comment: // TODO(S03): full handler implementation
   - This file will be fully replaced in S03.

4. UPDATE pkg/ari/client.go and pkg/ari/client_test.go: keep existing API but fix any references to deleted types (Session* → Agent*, remove Room* references).

5. UPDATE pkg/workspace/manager.go:
   - InitRefCounts(store *meta.Store): use store.ListWorkspaces(ctx, &meta.WorkspaceFilter{Phase: meta.WorkspacePhaseReady}) — adapt to new meta.WorkspaceFilter shape.
   - Update import if meta.WorkspaceStatusActive → meta.WorkspacePhaseReady.

6. UPDATE cmd/agentd/main.go:
   - Remove sessions := agentd.NewSessionManager(store) and related code.
   - Update ari.New(...) call: remove sessions param (new Server constructor doesn't take it).
   - Remove registry.RebuildFromDB call or adapt it to new signature (RebuildFromDB(store)).
   - Keep manager.InitRefCounts(store) call.
   - Keep processes.RecoverSessions(ctx) call.

7. Fix any remaining compilation errors: run `go build ./...` and fix each error in sequence.

Constraints:
- pkg/ari/server.go stub MUST NOT import unused packages (will cause compile error). Only import what the stub actually uses.
- pkg/ari/server_test.go will have compile errors after server.go is stubbed — that is acceptable for S01 (go build ./... skips test files).
- The ari.New() signature change cascades to cmd/agentd/main.go — must update both in sync.
- pkg/workspace/manager.go only uses meta.WorkspaceFilter and meta.Store — small change.
- After all changes: `go build ./...` must be green; `go test ./pkg/meta/...` must pass.
  - Files: `pkg/ari/registry.go`, `pkg/ari/registry_test.go`, `pkg/ari/types.go`, `pkg/ari/server.go`, `pkg/ari/client.go`, `pkg/workspace/manager.go`, `cmd/agentd/main.go`
  - Verify: cd /Users/jim/code/zoumo/open-agent-runtime && go build ./... && go test ./pkg/meta/... -count=1 -timeout 30s && ! rg 'meta\.AgentState|meta\.SessionState|go-sqlite3|SessionManager' --type go pkg/ari/ pkg/workspace/ cmd/

## Files Likely Touched

- pkg/meta/models.go
- pkg/meta/store.go
- pkg/meta/workspace.go
- pkg/meta/agent.go
- pkg/meta/store_test.go
- pkg/meta/workspace_test.go
- pkg/meta/agent_test.go
- go.mod
- go.sum
- pkg/spec/state_types.go
- pkg/runtime/runtime.go
- pkg/spec/state_test.go
- pkg/runtime/runtime_test.go
- pkg/agentd/agent.go
- pkg/agentd/process.go
- pkg/agentd/recovery.go
- pkg/agentd/agent_test.go
- pkg/agentd/process_test.go
- pkg/agentd/recovery_test.go
- pkg/agentd/shim_client_test.go
- pkg/agentd/shim_client.go
- pkg/ari/registry.go
- pkg/ari/registry_test.go
- pkg/ari/types.go
- pkg/ari/server.go
- pkg/ari/client.go
- pkg/workspace/manager.go
- cmd/agentd/main.go
