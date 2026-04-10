---
estimated_steps: 49
estimated_files: 7
skills_used: []
---

# T04: pkg/ari + pkg/workspace + cmd/agentd compilation sweep — final green build

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

## Inputs

- `pkg/meta/models.go`
- `pkg/meta/store.go`
- `pkg/meta/workspace.go`
- `pkg/agentd/agent.go`
- `pkg/agentd/process.go`
- `pkg/ari/registry.go`
- `pkg/ari/types.go`
- `pkg/ari/server.go`
- `pkg/ari/client.go`
- `pkg/workspace/manager.go`
- `cmd/agentd/main.go`

## Expected Output

- `pkg/ari/registry.go`
- `pkg/ari/types.go`
- `pkg/ari/server.go`
- `pkg/ari/client.go`
- `pkg/workspace/manager.go`
- `cmd/agentd/main.go`

## Verification

cd /Users/jim/code/zoumo/open-agent-runtime && go build ./... && go test ./pkg/meta/... -count=1 -timeout 30s && ! rg 'meta\.AgentState|meta\.SessionState|go-sqlite3|SessionManager' --type go pkg/ari/ pkg/workspace/ cmd/
