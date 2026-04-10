---
estimated_steps: 27
estimated_files: 9
skills_used: []
---

# T01: Rewrite pkg/meta with bbolt store and new Agent+Workspace models

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

## Inputs

- `pkg/meta/models.go`
- `pkg/meta/store.go`
- `pkg/meta/workspace.go`
- `pkg/meta/agent.go`
- `pkg/meta/schema.sql`
- `pkg/meta/session.go`
- `pkg/meta/room.go`
- `go.mod`

## Expected Output

- `pkg/meta/models.go`
- `pkg/meta/store.go`
- `pkg/meta/workspace.go`
- `pkg/meta/agent.go`
- `pkg/meta/store_test.go`
- `pkg/meta/workspace_test.go`
- `pkg/meta/agent_test.go`
- `go.mod`
- `go.sum`

## Verification

cd /Users/jim/code/zoumo/open-agent-runtime && go test ./pkg/meta/... -count=1 -timeout 30s && ! rg 'mattn/go-sqlite3|meta\.AgentState|meta\.SessionState' --type go pkg/meta/

## Observability Impact

bbolt store logs open/close/error events via slog to help diagnose startup failures
