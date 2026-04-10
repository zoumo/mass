---
estimated_steps: 26
estimated_files: 1
skills_used: []
---

# T01: Implement ARI server JSON-RPC infrastructure and workspace/* handlers

Replace the server.go stub with a working JSON-RPC server that accepts connections on a Unix socket, dispatches requests to handler functions, and implements all workspace/* methods.

**Server infrastructure:**
1. Add `ln net.Listener`, `mu sync.RWMutex`, `conns map[*jsonrpc2.Conn]struct{}`, `shutdownCh chan struct{}` to Server struct.
2. Implement `Serve()`: net.Listen("unix", s.socketPath), loop Accept() in goroutine, per-connection `jsonrpc2.NewConn(ctx, jsonrpc2.NewPlainObjectStream(nc), jsonrpc2.AsyncHandler(s))`. Track active conns; close on Shutdown.
3. Implement `Shutdown(ctx)`: close listener, close all active conns, wait with ctx timeout.
4. Implement `Handle(ctx, conn, req)` (jsonrpc2.Handler interface): switch on req.Method, dispatch to typed handler functions; unknown methods return jsonrpc2.Error{Code: -32601}.
5. Add `replyOK(ctx, conn, req, result any)` and `replyErr(ctx, conn, req, code int64, msg string)` helpers.

**workspace/create:**
6. Parse WorkspaceCreateParams; validate Name non-empty.
7. Create meta.Workspace in store (phase: pending); if already-exists error return JSON-RPC error.
8. Return WorkspaceCreateResult{Name, Phase:"pending"} immediately.
9. Start goroutine: call manager.Prepare(ctx, wsSpec, targetDir) where targetDir = filepath.Join(s.baseDir, "workspaces", params.Name). On success: store.UpdateWorkspaceStatus → ready + path; registry.Add. On failure: store.UpdateWorkspaceStatus → error.
10. Source for prepare: unmarshal params.Source (json.RawMessage) into workspace.Source; build workspace.WorkspaceSpec{OarVersion:"0.1.0", Metadata:{Name}, Source, Hooks}.

**workspace/status:**
11. Parse WorkspaceStatusParams; look up registry.Get(name). If found, return WorkspaceStatusResult with phase/path from registry.
12. If not in registry, fall back to store.GetWorkspace; return phase from DB. Return -32602 if not found at all.

**workspace/list:**
13. Registry.List() → build []WorkspaceInfo → return WorkspaceListResult.

**workspace/delete:**
14. Parse WorkspaceDeleteParams; store.DeleteWorkspace(ctx, name) — store already rejects if agents exist. registry.Remove(name). Return empty result.

**workspace/send:**
15. Parse WorkspaceSendParams; validate Workspace/From/To/Message non-empty.
16. Recovery guard: if s.processes.IsRecovering() return CodeRecoveryBlocked.
17. Load target agent from store; if nil return -32602; if state==error return -32001 with message "target agent is in error state".
18. Connect to target shim: s.processes.Connect(ctx, params.Workspace, params.To); if error return -32001 "target agent is not running".
19. Kick off `go client.Prompt(context.Background(), params.Message)` (fire-and-forget). Return WorkspaceSendResult{Delivered: true}.

## Inputs

- `pkg/ari/server.go`
- `pkg/ari/types.go`
- `pkg/ari/registry.go`
- `pkg/agentd/process.go`
- `pkg/agentd/agent.go`
- `pkg/meta/models.go`
- `pkg/meta/workspace.go`
- `pkg/workspace/manager.go`
- `pkg/workspace/spec.go`

## Expected Output

- `pkg/ari/server.go`

## Verification

cd /Users/jim/code/zoumo/open-agent-runtime && go build ./pkg/ari/... && go vet ./pkg/ari/...

## Observability Impact

slog INFO on workspace/create (workspace name, phase:pending), workspace prepare goroutine success/failure (workspace name, phase:ready or phase:error, path); workspace/send dispatch (workspace, from, to) and rejection reasons (recovery blocked, error-state target, not running)
