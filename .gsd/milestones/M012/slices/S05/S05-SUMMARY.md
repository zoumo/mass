---
id: S05
parent: M012
milestone: M012
provides:
  - ["pkg/ari/server.Service + Register: typed ARI service wiring for all three ARI interfaces", "pkg/ari/client.ARIClient + Dial: typed ARI client Dial helper", "pkg/shim/server.Service: typed shim service (already present, verified)", "pkg/shim/client: Dial/DialWithHandler/ParseShimEvent helpers (already present, verified)", "cmd entrypoints migrated to pkg/jsonrpc transport + typed service interfaces", "pkg/agentd/process.go + recovery.go migrated to apishim.ShimClient via pkg/shim/client"]
requires:
  []
affects:
  - ["S06 — can now delete legacy packages (pkg/rpc, pkg/ari/server.go monolith, pkg/agentd/shim_client.go internal helpers) since all production callers have been migrated"]
key_files:
  - ["pkg/ari/server/server.go", "pkg/ari/client/client.go", "pkg/shim/server/service.go", "pkg/shim/client/client.go", "cmd/agentd/subcommands/server/command.go", "cmd/agentd/subcommands/shim/command.go", "pkg/agentd/process.go", "pkg/agentd/recovery.go"]
key_decisions:
  - ["Adapter pattern (3 thin unexported adapters embedding *Service) for pkg/ari/server because WorkspaceService.List and AgentService.List have identical Go signatures with different return types — a single struct cannot implement both (D112)", "Explicit net.Listen lifecycle in cmd entrypoints: listener created outside jsonrpc.Server so Shutdown does not need the socket path", "ARIClient bundles all three typed sub-clients (Workspace, AgentRun, Agent) behind a single Close/DisconnectNotify surface", "shimclient.NotificationHandler explicit cast in process.go/recovery.go: both agentd and shimclient handler types are structurally identical but the cast is kept as a future-divergence guard", "pkg/agentd/shim_client.go NOT removed in S05: it backs shim_client_test.go and ParseShimEvent usage — removal deferred to S06 cleanup"]
patterns_established:
  - ["Adapter pattern for multi-interface ARI service registration: central Service struct + thin unexported adapters embedding *Service (K077)", "Explicit net.Listen + jsonrpc.NewServer + Register(srv, svc) + srv.Serve(ln) + srv.Shutdown(ctx) pattern for cmd entrypoints", "Typed Dial helpers (ARIClient, ShimClient) that bundle multiple sub-clients behind a unified Close/DisconnectNotify surface", "Struct-based RPC params throughout (SessionSubscribeParams, SessionPromptParams, SessionLoadParams) — no positional args"]
observability_surfaces:
  - none
drill_down_paths:
  - [".gsd/milestones/M012/slices/S05/tasks/T01-SUMMARY.md", ".gsd/milestones/M012/slices/S05/tasks/T02-SUMMARY.md", ".gsd/milestones/M012/slices/S05/tasks/T03-SUMMARY.md"]
duration: ""
verification_result: passed
completed_at: 2026-04-14T03:13:08.736Z
blocker_discovered: false
---

# S05: Phase 4: Implementation Migration

**Migrated all runtime implementations to typed Service Interface packages: pkg/shim/server, pkg/shim/client, pkg/ari/server (adapter pattern), pkg/ari/client; cmd entrypoints and pkg/agentd updated; make build + go test ./... pass.**

## What Happened

S05 migrated four concrete implementation packages and updated three cmd entrypoints and two agentd modules to consume the typed Service Interface contracts established in S03/S04.

**T01 — pkg/shim/server and pkg/shim/client (pre-existing)**
Both files were already present. `pkg/shim/server/service.go` implements the full `apishim.ShimService` interface (Prompt, Cancel, Load, Subscribe/SubscribeFromSeq, Status, History, Stop) with correct peer-from-context logic for notification push. `pkg/shim/client/client.go` provides `Dial`, `DialWithHandler`, and `ParseShimEvent` helpers. `go build ./pkg/shim/...` passes cleanly.

**T02 — pkg/ari/server/server.go (adapter pattern)**
The plan called for "one struct implementing all 3 interfaces." A design constraint blocked that: `WorkspaceService.List(ctx)` and `AgentService.List(ctx)` have identical Go method signatures but different return types — a single struct cannot satisfy both. The solution is the adapter pattern: a central `Service` struct holds all shared dependencies (WorkspaceManager, Registry, AgentRunManager, ProcessManager, Store, baseDir, logger). Three thin unexported adapters (`workspaceAdapter`, `agentRunAdapter`, `agentAdapter`) each embed `*Service` and independently implement their respective interfaces. A package-level `Register(srv *jsonrpc.Server, svc *Service)` wires all three in one call. All handler logic was ported faithfully from the old jsonrpc2 dispatch style to typed-interface returns, replacing `replyErr`/`replyOK` with `*jsonrpc.RPCError` returns. Shared helpers (`listWorkspaceMembers`, `recordPromptDeliveryFailure`, `buildWorkspaceEnvelope`) live on `*Service` and are available to all adapters via embedding. Compiled cleanly after fixing two struct literal typos.

**T03 — pkg/ari/client, cmd entrypoints, pkg/agentd migration**
Created `pkg/ari/client/client.go` providing an `ARIClient` struct bundling all three typed ARI sub-clients behind a single `Close`/`DisconnectNotify` surface. Updated `cmd/agentd/subcommands/server/command.go` to use explicit `net.Listen` + `jsonrpc.NewServer` + `ariserver.Register` + `srv.Serve(ln)` + `srv.Shutdown(ctx)` on signal — listener lifecycle is explicit so Shutdown doesn't need the socket path. Updated `cmd/agentd/subcommands/shim/command.go` from the old sourcegraph/jsonrpc2 handler to `shimserver.New` + `apishim.RegisterShimService` + the same explicit listener pattern. Migrated `pkg/agentd/process.go` and `recovery.go` from internal ShimClient to `apishim.ShimClient` via `shimclient.DialWithHandler`; Subscribe calls updated to struct-based params; `RuntimeStatus()` derefs the pointer from the new client internally so callers see no API change. Updated `pkg/ari/server/server.go` to use struct-based `Prompt` call. Updated test files (`process_test.go`, `shim_boundary_test.go`, `server_test.go`) to use the new client types and params structs.

Note: `pkg/agentd/shim_client.go` was NOT removed — it still backs `shim_client_test.go` and its `ParseShimEvent` is used in `process.go` and heavily tested. Removal was out of scope for S05.

**Verification:** `make build` exit 0 (both binaries, 7s). `go test ./... -count=1` — all 18 test packages pass including integration tests (107s). A pre-existing intermittent `panic: send on closed channel` in `pkg/jsonrpc/client.go:115` appears only under `-count=3` due to a race between `Close()` and the sourcegraph/jsonrpc2 `readMessages` goroutine; it does not reproduce on a standard single-count run and is not caused by S05 changes.

## Verification

make build exit 0 (7s, both binaries). go test ./... -count=1 exit 0 (all 18 packages pass, integration tests 107s).

## Requirements Advanced

None.

## Requirements Validated

None.

## New Requirements Surfaced

None.

## Requirements Invalidated or Re-scoped

None.

## Deviations

["T02: plan called for 'one struct implementing all 3 interfaces'; adapted to adapter pattern because WorkspaceService.List and AgentService.List have identical Go signatures with different return types", "T03: pkg/agentd/shim_client.go NOT removed — retained for test compatibility; full removal deferred to S06"]

## Known Limitations

["pkg/agentd/shim_client.go (internal Dial/DialWithHandler/ParseShimEvent) still present — superseded for production use but retained for test compatibility; removal scoped to S06", "Pre-existing pkg/jsonrpc Client.notifCh race (send-on-closed-channel under -count>1 parallel runs) not fixed in S05 — documented as K078"]

## Follow-ups

["S06: Delete pkg/rpc/ (legacy sourcegraph/jsonrpc2-based server), pkg/ari/server.go (old monolithic handler), pkg/agentd/shim_client.go internal Dial/DialWithHandler helpers; verify no remaining references to deleted packages", "pkg/jsonrpc Client.notifCh race (K078): guard send with select{case notifCh <- msg: default: log.Warn} or recover() to eliminate the send-on-closed-channel panic under parallel test load"]

## Files Created/Modified

- `pkg/ari/server/server.go` — New: adapter-pattern Service + workspaceAdapter + agentRunAdapter + agentAdapter implementing all three ARI service interfaces; Register() wires all three with jsonrpc.Server
- `pkg/ari/client/client.go` — New: ARIClient bundling WorkspaceClient, AgentRunClient, AgentClient behind Dial/Close/DisconnectNotify
- `cmd/agentd/subcommands/server/command.go` — Migrated from ari.New + srv.Serve to explicit net.Listen + jsonrpc.NewServer + ariserver.Register + srv.Serve(ln) + srv.Shutdown
- `cmd/agentd/subcommands/shim/command.go` — Migrated from rpc.New (sourcegraph/jsonrpc2 direct) to shimserver.New + apishim.RegisterShimService + explicit listener + srv.Serve
- `pkg/agentd/process.go` — ShimProcess.Client changed from internal *ShimClient to *apishim.ShimClient; DialWithHandler migrated to shimclient; Subscribe/Status calls updated to struct-based params
- `pkg/agentd/recovery.go` — DialWithHandler + Subscribe + Load calls migrated to shimclient and struct-based params
- `pkg/ari/server.go` — Updated Prompt call to struct-based params (apishim.SessionPromptParams)
- `pkg/agentd/process_test.go` — Updated Prompt call to struct-based params
- `pkg/agentd/shim_boundary_test.go` — Replaced internal DialWithHandler + Subscribe with shimclient equivalents
- `pkg/ari/server_test.go` — Replaced agentd.Dial with shimclient.Dial; added shimclient import
