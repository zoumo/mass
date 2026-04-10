---
id: T01
parent: S03
milestone: M007
key_files:
  - pkg/ari/server.go
  - pkg/ari/server_test.go
key_decisions:
  - Used jsonrpc2.AsyncHandler(s) directly on *Server implementing jsonrpc2.Handler, mirroring pkg/rpc/server.go pattern.
  - workspace/delete maps has-agents store error to CodeRecoveryBlocked (-32001), not-found to -32602.
  - workspace/list returns only registry-tracked workspaces (ready) as specified by plan.
duration: 
verification_result: passed
completed_at: 2026-04-09T21:23:22.532Z
blocker_discovered: false
---

# T01: Replaced server.go stub with real JSON-RPC 2.0 server (Serve/Shutdown/Handle) and all workspace/* handlers with structured slog observability

**Replaced server.go stub with real JSON-RPC 2.0 server (Serve/Shutdown/Handle) and all workspace/* handlers with structured slog observability**

## What Happened

The stub pkg/ari/server.go had no-op Serve/Shutdown and no handler logic. This task replaced it with a fully working implementation: added ln/mu/conns/shutdownCh/logger fields to Server; Serve() applies K014 (removes stale socket file), listens on Unix socket, wraps each accepted connection in jsonrpc2.NewPlainObjectStream/AsyncHandler, tracks connections for graceful Shutdown; Handle() implements jsonrpc2.Handler dispatching workspace/* methods; replyOK/replyErr helpers mirror pkg/rpc pattern. workspace/create writes pending to store, replies immediately, then async goroutine calls manager.Prepare and updates store to ready/error plus registry.Add. workspace/status uses registry fast-path then DB fallback. workspace/list returns registry-tracked workspaces. workspace/delete calls store.DeleteWorkspace (store enforces agent guard), maps errors to -32602/-32001. workspace/send validates fields, recovery guard, DB agent lookup with error-state rejection, shim Connect, fire-and-forget Prompt goroutine. Updated stub test to use Serve() goroutine + Shutdown() pattern to prevent hang.

## Verification

go build ./pkg/ari/... (exit 0), go vet ./pkg/ari/... (exit 0), go test ./pkg/ari/... -count=1 -timeout 30s (10 tests pass, 0.85s). Pre-existing vet failures in integration_test and pkg/rpc_test are unrelated to this task.

## Verification Evidence

| # | Command | Exit Code | Verdict | Duration |
|---|---------|-----------|---------|----------|
| 1 | `go build ./pkg/ari/...` | 0 | ✅ pass | 1200ms |
| 2 | `go vet ./pkg/ari/...` | 0 | ✅ pass | 900ms |
| 3 | `go test ./pkg/ari/... -count=1 -timeout 30s -v` | 0 | ✅ pass | 850ms |

## Deviations

workspace/list returns only registry-tracked (ready) workspaces, not all DB phases — this matches the plan's Registry.List() spec.

## Known Issues

None.

## Files Created/Modified

- `pkg/ari/server.go`
- `pkg/ari/server_test.go`
