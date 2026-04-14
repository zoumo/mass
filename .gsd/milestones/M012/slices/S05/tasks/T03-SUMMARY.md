---
id: T03
parent: S05
milestone: M012
key_files:
  - pkg/ari/client/client.go
  - cmd/agentd/subcommands/server/command.go
  - cmd/agentd/subcommands/shim/command.go
  - pkg/agentd/process.go
  - pkg/agentd/recovery.go
  - pkg/ari/server/server.go
  - pkg/ari/server.go
key_decisions:
  - cmd/agentd/subcommands/server/command.go: listener lifecycle is managed explicitly (net.Listen + close) rather than inside jsonrpc.Server, so Shutdown does not need the socket path
  - pkg/ari/client/client.go: ARIClient bundles all three typed sub-clients behind a single Close/DisconnectNotify surface rather than exposing raw jsonrpc.Client
  - process.go/recovery.go: NotificationHandler cast is a no-op because both agentd.NotificationHandler and shimclient.NotificationHandler have the same underlying func type; explicit cast avoids silent compilation errors if types diverge in future
  - RuntimeStatus() keeps value-return API (apishim.RuntimeStatusResult) by dereferencing pointer internally so all callers are unaffected
duration: 
verification_result: passed
completed_at: 2026-04-14T03:01:55.724Z
blocker_discovered: false
---

# T03: Created pkg/ari/client/client.go Dial helper; migrated cmd entrypoints to pkg/ari/server + pkg/shim/server + jsonrpc.Server; migrated pkg/agentd/process.go and recovery.go from internal ShimClient to apishim.ShimClient via pkg/shim/client; make build + go test ./... pass

**Created pkg/ari/client/client.go Dial helper; migrated cmd entrypoints to pkg/ari/server + pkg/shim/server + jsonrpc.Server; migrated pkg/agentd/process.go and recovery.go from internal ShimClient to apishim.ShimClient via pkg/shim/client; make build + go test ./... pass**

## What Happened

**New file: pkg/ari/client/client.go** — Provides a `Dial` helper that returns an `ARIClient` struct bundling all three typed ARI sub-clients (`WorkspaceClient`, `AgentRunClient`, `AgentClient`). Mirrors the pattern established in `pkg/shim/client/client.go`.

**cmd/agentd/subcommands/server/command.go** — Replaced the old monolithic `ari.New(...)` + `srv.Serve()` + `srv.Shutdown()` pattern with: create a `net.Listener` on the Unix socket; create `jsonrpc.NewServer(logger)`; create `ariserver.New(...)` service; call `ariserver.Register(srv, svc)` to wire adapters; start `srv.Serve(ln)` in a goroutine; close listener and call `srv.Shutdown(ctx)` on signal. Renamed the local `store` variable to `metaStore` to avoid shadowing the `store` package import.

**cmd/agentd/subcommands/shim/command.go** — Replaced the old `rpc.New(...)` server (which used sourcegraph/jsonrpc2 directly) with: `shimserver.New(mgr, trans, logPath, logger)` + `jsonrpc.NewServer(logger)` + `apishim.RegisterShimService(srv, svc)` + explicit `net.Listen` + `srv.Serve(ln)`. The socket lifecycle (create listener, close on shutdown) is now managed explicitly so `srv.Shutdown` does not need to know the socket path.

**pkg/agentd/process.go** — Key changes:
- Added import for `apishim "github.com/zoumo/oar/api/shim"` and `shimclient "github.com/zoumo/oar/pkg/shim/client"`; removed old `"github.com/zoumo/oar/api/shim"` unaliased import.
- Changed `ShimProcess.Client` type from `*ShimClient` (internal) to `*apishim.ShimClient`.
- Changed `DialWithHandler(ctx, socketPath, handler)` to `shimclient.DialWithHandler(ctx, socketPath, shimclient.NotificationHandler(handler))`. The function types are structurally identical so the cast is transparent.
- Changed `client.Subscribe(ctx, nil, nil)` to `client.Subscribe(ctx, &apishim.SessionSubscribeParams{})` — fresh start, no afterSeq/fromSeq.
- Changed `client.Status(ctx)` to correctly handle the pointer return `*RuntimeStatusResult`; the `State` field access is unchanged because Go auto-derefs.
- Changed `Connect()` return type from `*ShimClient` to `*apishim.ShimClient`.
- Changed `RuntimeStatus()` return type from `(shim.RuntimeStatusResult, error)` to `(apishim.RuntimeStatusResult, error)`; internally derefs the pointer from the new client so callers see no change.

**pkg/agentd/recovery.go** — Same DialWithHandler migration; updated `Subscribe` call to struct-based params with `FromSeq: &fromSeq`; updated `Load` call from `(ctx, sessionID string)` to `(ctx, &apishim.SessionLoadParams{SessionID: sessionID})`.

**pkg/ari/server/server.go** — Added `apishim "github.com/zoumo/oar/api/shim"` import; changed `client.Prompt(ctx, string)` to `client.Prompt(ctx, &apishim.SessionPromptParams{Prompt: string})` in both the workspace/send and agentrun/prompt handlers (since `Connect()` now returns `*apishim.ShimClient`).

**pkg/ari/server.go** (old monolithic server) — Same `Prompt` call migration as above; added `apishim` import.

**Test updates** — `pkg/agentd/process_test.go`: added `api/shim` import, updated `Prompt` call to struct form. `pkg/agentd/shim_boundary_test.go`: replaced 5x `DialWithHandler` + `Subscribe` with `shimclient.DialWithHandler` + struct-based `Subscribe`. `pkg/ari/server_test.go`: replaced `agentd.Dial` with `shimclient.Dial`, added `shimclient` import.

## Verification

Ran `make build` — exit 0, both binaries (bin/agentd, bin/agentdctl) produced in 7s. Ran `go test ./...` — all packages pass, including integration tests (101s). Zero failures. pkg/ari/client new package compiles and has no test files (expected: it's a thin Dial helper).

## Verification Evidence

| # | Command | Exit Code | Verdict | Duration |
|---|---------|-----------|---------|----------|
| 1 | `make build` | 0 | ✅ pass | 7100ms |
| 2 | `go test ./...` | 0 | ✅ pass | 103000ms |

## Deviations

pkg/agentd/shim_client.go was NOT removed — it still provides the internal Dial/DialWithHandler functions (called from shim_client_test.go) and ParseShimEvent (used in process.go and heavily tested). The task plan only said 'update process.go to use pkg/shim/client', which was accomplished. Removing shim_client.go would break existing tests that directly test its Dial helpers and ParseShimEvent. The old helpers are now superseded for production use but retained for test compatibility.

## Known Issues

None.

## Files Created/Modified

- `pkg/ari/client/client.go`
- `cmd/agentd/subcommands/server/command.go`
- `cmd/agentd/subcommands/shim/command.go`
- `pkg/agentd/process.go`
- `pkg/agentd/recovery.go`
- `pkg/ari/server/server.go`
- `pkg/ari/server.go`
