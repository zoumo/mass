---
estimated_steps: 20
estimated_files: 2
skills_used: []
---

# T02: Create ARI server with workspace methods and registry

Create pkg/ari/server.go with JSON-RPC server implementing workspace/prepare, workspace/list, workspace/cleanup. Create pkg/ari/registry.go for workspaceId → metadata tracking. Generate UUIDs for workspace IDs. Wire to WorkspaceManager.Prepare/Cleanup. Handle JSON-RPC errors appropriately.

## Failure Modes

<!-- Q5: JSON-RPC server with dependency on WorkspaceManager -->

| Dependency | On error | On timeout | On malformed response |
|------------|----------|-----------|----------------------|
| WorkspaceManager.Prepare | Return InvalidParams error with WorkspaceError Phase | Return InternalError (context timeout) | Not applicable |
| WorkspaceManager.Cleanup | Return InternalError if refs > 0 or delete fails | Return InternalError (context timeout) | Not applicable |
| UUID generation | Not applicable (no external call) | Not applicable | Not applicable |

## Steps

1. Create pkg/ari/registry.go with package declaration
2. Import sync, define Registry struct with fields: `mu sync.RWMutex`, `workspaces map[string]*WorkspaceMeta` (workspaceId → meta)
3. Define WorkspaceMeta struct with fields: `Id string`, `Name string`, `Path string`, `Spec workspace.WorkspaceSpec`, `Status string`, `RefCount int`
4. Implement NewRegistry() constructor that initializes empty workspaces map
5. Implement Registry methods: Add(id, name, path, spec), Get(id), List() []WorkspaceMeta, Remove(id), Acquire(id) (increment RefCount), Release(id) (decrement, return count)
6. Create pkg/ari/server.go with package declaration
7. Import: context, encoding/json, fmt, log, net, os, path/filepath, sync, github.com/google/uuid, github.com/sourcegraph/jsonrpc2, pkg/workspace, pkg/ari/types
8. Define Server struct with fields: `manager *workspace.WorkspaceManager`, `registry *Registry`, `baseDir string` (workspace root), `path string` (socket path), `mu sync.Mutex`, `listener net.Listener`, `done chan struct{}`
9. Implement New(manager, registry, socketPath, baseDir) constructor
10. Implement Serve() method: create Unix socket listener, accept loop, handleConn for each connection
11. Implement handleConn(nc net.Conn): wrap in jsonrpc2.Conn, use AsyncHandler with connHandler
12. Define connHandler struct with `srv *Server` field
13. Implement connHandler.Handle(ctx, conn, req): switch on req.Method for "workspace/prepare", "workspace/list", "workspace/cleanup"
14. Implement handleWorkspacePrepare(ctx, conn, req): unmarshal WorkspacePrepareParams, generate UUID, generate targetDir under baseDir, call manager.Prepare, add to registry, return WorkspacePrepareResult. On error: return InvalidParams or InternalError.
15. Implement handleWorkspaceList(ctx, conn, req): call registry.List(), return WorkspaceListResult
16. Implement handleWorkspaceCleanup(ctx, conn, req): unmarshal WorkspaceCleanupParams, get workspace from registry, check RefCount > 0 → return error, call manager.Cleanup, remove from registry. On error: return InternalError.
17. Implement helper functions: unmarshalParams, replyError (pattern from pkg/rpc/server.go)
18. Add baseDir default: if empty, use os.TempDir() + "/agentd-workspaces"
19. Run `go build ./pkg/ari/...` to verify compilation

## Must-Haves

- [ ] Registry struct defined with mutex-protected workspaces map
- [ ] WorkspaceMeta struct defined with Id, Name, Path, Spec, Status, RefCount fields
- [ ] Registry.Add, Get, List, Remove, Acquire, Release methods implemented
- [ ] Server struct defined with WorkspaceManager, Registry, baseDir, socket path
- [ ] Server.Serve creates Unix socket and enters accept loop
- [ ] connHandler.Handle routes workspace/prepare, workspace/list, workspace/cleanup
- [ ] workspace/prepare generates UUID, calls manager.Prepare, adds to registry, returns result
- [ ] workspace/list calls registry.List and returns workspaces array
- [ ] workspace/cleanup checks RefCount, calls manager.Cleanup, removes from registry
- [ ] JSON-RPC errors use appropriate codes (InvalidParams, InternalError)
- [ ] UUID generation uses github.com/google/uuid
- [ ] Package compiles without error

## Negative Tests

<!-- Q7: What negative tests prove robustness -->

- **Malformed inputs**: Empty spec, missing workspaceId, invalid JSON — handled by unmarshalParams, returns InvalidParams
- **Error paths**: Prepare failure (bad git URL), Cleanup with refs > 0, Cleanup with nonexistent workspaceId — returns appropriate JSON-RPC errors
- **Boundary conditions**: Empty registry (workspace/list returns empty array), refs exactly at threshold (RefCount = 1 → cleanup fails)

## Verification

go build ./pkg/ari/... compiles without error

## Observability Impact

WorkspaceError Phase field mapped to JSON-RPC error messages (e.g., "prepare-source failed"). Registry state queryable via workspace/list. UUID generation provides traceable workspace identifiers. RefCount visible in WorkspaceInfo for debugging cleanup failures.

## Inputs

- `pkg/workspace/manager.go` — WorkspaceManager.Prepare/Cleanup/Acquire/Release methods
- `pkg/workspace/errors.go` — WorkspaceError type with Phase field
- `pkg/workspace/spec.go` — WorkspaceSpec, Source, SourceType types
- `pkg/rpc/server.go` — JSON-RPC server pattern (jsonrpc2 library usage)
- `pkg/ari/types.go` — Request/response types (from T01)

## Expected Output

- `pkg/ari/server.go` — ARI JSON-RPC server with workspace methods
- `pkg/ari/registry.go` — Workspace registry for tracking metadata