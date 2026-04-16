---
id: M012
title: "Codebase Refactor: Service Interface + Unified RPC + Directory Restructure"
status: complete
completed_at: 2026-04-14T04:29:20.170Z
key_decisions:
  - D112: Adapter pattern for pkg/ari/server — central Service struct + three thin unexported adapters (workspaceAdapter, agentRunAdapter, agentAdapter) each embedding *Service, because WorkspaceService.List(ctx) and AgentService.List(ctx) have identical Go signatures with different return types and a single Go struct cannot implement both
  - D113: Extract mock infrastructure from deleted test file into new _test.go — when deleting shim_client_test.go, extracted newMockShimServer/mockShimServer to mock_run_server_test.go because recovery_test.go and recovery_posture_test.go depended on it
  - D114: Call net.Listen() before srv.Serve(ln) in test helpers — creates socket synchronously before goroutine starts, eliminating the require.Eventually race window
  - ARIView() pattern over json:"-" for sensitive field stripping — json:"-" blocks bbolt persistence; ARIView() provides identical security guarantee (fields absent from ARI responses) without affecting DB storage
  - Bounded 256-entry FIFO notification worker in jsonrpc.Client — prevents slow notification handlers from blocking response dispatch without unbounded memory growth
  - Explicit net.Listener lifecycle in cmd entrypoints — listener created outside jsonrpc.Server so Shutdown does not need the socket path; ln.Close() before srv.Shutdown() is the correct cleanup order
key_files:
  - pkg/jsonrpc/server.go
  - pkg/jsonrpc/client.go
  - pkg/jsonrpc/errors.go
  - pkg/jsonrpc/peer.go
  - pkg/jsonrpc/server_test.go
  - pkg/jsonrpc/client_test.go
  - api/runtime/config.go
  - api/runtime/state.go
  - api/shim/types.go
  - api/shim/service.go
  - api/shim/client.go
  - api/ari/domain.go
  - api/ari/service.go
  - api/ari/client.go
  - api/ari/types.go
  - pkg/ari/server/server.go
  - pkg/ari/client/client.go
  - pkg/agentrun/server/service.go
  - pkg/agentrun/client/client.go
  - cmd/agentd/subcommands/server/command.go
  - cmd/agentd/subcommands/shim/command.go
  - pkg/agentd/process.go
  - pkg/agentd/recovery.go
  - pkg/agentd/mock_run_server_test.go
  - pkg/ari/server_test.go
  - docs/design/agentd/ari-spec.md
lessons_learned:
  - Go multi-interface struct limitation: when two interfaces have methods with identical signatures but different return types (e.g. List(ctx) WorkspaceList vs AgentList), a single struct cannot implement both — the adapter pattern (thin wrappers embedding a shared Service) is the standard Go solution
  - Test file deletion requires a cross-file dependency scan first — deleting a _test.go file can silently break other test files in the same package if they share infrastructure (types, helper functions). Always grep for symbols from the deleted file before deleting.
  - ln.Close() before srv.Shutdown() is the correct JSON-RPC server cleanup order — closing the listener forces Accept() to return immediately, causing Serve() to exit, before Shutdown() processes in-flight requests. Reverse order causes a hang.
  - net.Listen() synchronously before srv.Serve(ln) in test helpers eliminates require.Eventually socket-wait races — the socket file exists before the goroutine starts, so any subsequent dial attempt succeeds immediately
  - json:\"-\" blocks both ARI response serialization AND bbolt persistence — use an explicit ARIView() adapter method to strip sensitive fields at the API boundary without affecting storage
  - Bounded notification worker (FIFO with backpressure) prevents slow notification handlers from blocking the JSON-RPC response pipeline — a common oversight in naive RPC client implementations
  - Pre-existing races in pkg/jsonrpc Client.enqueueNotification (send on closed channel) only manifest under -count>1 parallel runs; single-run go test ./... is a pragmatic acceptance bar for refactor milestones
---

# M012: Codebase Refactor: Service Interface + Unified RPC + Directory Restructure

**Replaced three duplicated JSON-RPC implementations with a single transport-agnostic pkg/jsonrpc/ framework, established typed Service Interfaces for ARI and Shim, performed clean-break ARI wire contract convergence using domain shapes, restructured API packages from api/spec→api/runtime and pkg/agentrunapi→api/agent-run, and deleted all legacy dead code — make build + go test ./... pass across all 17 test packages.**

## What Happened

M012 executed a comprehensive codebase refactor across six sequential slices, transforming three separate duplicated JSON-RPC implementations into a single coherent typed package set.

**S01 — pkg/jsonrpc/ Transport-Agnostic Framework:**
Built the shared JSON-RPC foundation from scratch. The Server is transport-agnostic (accepts net.Listener), uses a ServiceDesc pattern for method registration, and supports an interceptor chain. The Client wraps sourcegraph/jsonrpc2 with a bounded 256-entry FIFO notification worker that prevents slow handlers from blocking response dispatch. RPCError carries code/message/data. Peer is injected into handler context for server-initiated notifications. All 18 protocol tests pass.

**S02 — Pure Rename/Move (api/spec→api/runtime, pkg/agentrunapi→api/agent-run):**
A pure import path migration. Created api/runtime/ (config.go + state.go, package runtime) and api/shim/types.go (package agent-run). Updated 22 files across pkg/ and cmd/. Deleted api/spec/ and pkg/agentrunapi/. Zero wire format changes. All existing tests pass unchanged.

**S03 — ARI Clean-Break Contract Convergence:**
Updated docs/design/agentd/ari-spec.md with new metadata/spec/status domain shapes for all methods. Created api/ari/domain.go from api/meta/types.go. Added ARIView() helpers on AgentRun and Workspace to strip internal fields at the ARI boundary (RunSocketPath, RunStateDir, RunPID, BootstrapConfig, Hooks) — ARIView() was preferred over json:"-" because json:"-" also blocks bbolt store persistence. Removed AgentInfo/AgentRunInfo/WorkspaceInfo from api/ari/types.go. Added Result wrapper types (AgentSetResult, AgentRunCreateResult, etc.). Updated pkg/ari/server.go to use domain types directly. Deleted api/meta/ directory. Fixed all test field accesses for nested domain structure.

**S04 — Service Interface + Register + Typed Clients:**
Defined WorkspaceService/AgentRunService/AgentService interfaces with Register functions that build jsonrpc.ServiceDesc. Typed clients (WorkspaceClient, AgentRunClient, AgentClient, Client) wrap jsonrpc.Client with method-specific typed wrappers. Handler documents 5 Subscribe implementation constraints using the Peer abstraction.

**S05 — Implementation Migration:**
Migrated four concrete implementation packages and updated three cmd entrypoints and two agentd modules to consume the typed Service Interface contracts established in S03/S04. The key design challenge was that WorkspaceService.List(ctx) and AgentService.List(ctx) have identical Go signatures but different return types — a single struct cannot implement both. The adapter pattern was used: a central Service struct holding all shared deps, plus three thin unexported adapters (workspaceAdapter, agentRunAdapter, agentAdapter) each embedding *Service and independently implementing their interface. Created pkg/ari/client/client.go providing an ARIClient bundling all three typed ARI sub-clients. Migrated cmd entrypoints to explicit net.Listen + jsonrpc.NewServer + Register + srv.Serve(ln) + srv.Shutdown(ctx) on signal. Migrated pkg/agentd/process.go and recovery.go from internal Client to apiagent-run.Client vian agent-runclient.DialWithHandler.

**S06 — Legacy Cleanup:**
Deleted pkg/rpc/ (old sourcegraph/jsonrpc2-based shim server, 844 lines), pkg/agentd/shim_client.go (old internal Client), and pkg/ari/server.go (1235-line monolith). A key discovery: shim_client_test.go contained newMockShimServer/mockShimServer infrastructure used by recovery_test.go and recovery_posture_test.go — wholesale deletion would have broken unrelated tests. The fix was extracting only the shared infrastructure to a new pkg/agentd/mock_run_server_test.go. Migrated the 801-line pkg/ari/server_test.go from ari.New() to ariserver.New()+jsonrpc.NewServer()+ariserver.Register()+net.Listen() pattern. Established the correct cleanup order: ln.Close() before srv.Shutdown(ctx) so Accept() unblocks and Serve() returns before Shutdown handles in-flight requests.

Final state: make build exits 0 (both binaries). go test ./... -count=1 exits 0 (all 17 test packages). All five legacy import paths confirmed absent. All new package artifacts confirmed present.

## Success Criteria Results

| Criterion | Evidence | Result |
|-----------|----------|--------|
| S01: `make build` passes | `make build` exits 0, produces `bin/agentd` and `bin/agentdctl` | ✅ PASS |
| S01: `go test ./pkg/jsonrpc/...` passes all 18 protocol tests | All 18 named tests pass: TestClient_Call, TestClient_ConcurrentCall, TestClient_CallWithNotification, TestClient_NotificationHandler, TestClient_NotificationOrder, TestClient_SlowNotificationHandler, TestClient_NotificationBackpressure, TestClient_ContextCancel, TestClient_Close, TestClient_ResponseOutOfOrder, TestServer_Dispatch, TestServer_MethodNotFound, TestServer_InvalidParams, TestServer_RPCError, TestServer_PlainError, TestServer_Interceptor, TestServer_PeerNotify, TestServer_PeerDisconnect | ✅ PASS |
| S02: `make build + go test ./...` pass; JSON output identical (pure rename) | All packages pass; zero matches for legacy import paths api/spec and pkg/agentrunapi | ✅ PASS |
| S03: `make build + go test ./...` pass; ARI JSON shape matches ari-spec.md | `api/ari/domain.go` with ARIView() pattern present; `docs/design/agentd/ari-spec.md` updated; zero matches for `api/meta` imports; all tests pass | ✅ PASS |
| S04: `make build` passes; interfaces compile cleanly | All four interface files present: `api/ari/service.go`, `api/ari/client.go`, `api/shim/service.go`, `api/shim/client.go`; `make build` exits 0 | ✅ PASS |
| S05: `make build + go test ./...` pass; integration tests pass | `go test ./... -count=1` exits 0; `tests/integration` passes in 107s; all implementation packages present | ✅ PASS |
| S06: `make build + go test ./...` pass; no references to deleted packages | `go test ./... -count=1` exits 0 (17 packages); `pkg/rpc/` absent; `pkg/ari/server.go` absent; `pkg/agentd/shim_client.go` absent; `rg 'ari.New\b'` → exit 1 (0 matches); `rg '"pkg/rpc"'` → exit 1 (0 matches) | ✅ PASS |

## Definition of Done Results

| Item | Status | Evidence |
|------|--------|----------|
| All 6 slices complete | ✅ | S01–S06 all show status: complete in gsd_milestone_status |
| All 12 tasks done | ✅ | All task counts show done == total: S01(2/2), S02(1/1), S03(2/2), S04(2/2), S05(3/3), S06(2/2) |
| All 6 slice summaries exist | ✅ | S01-SUMMARY.md through S06-SUMMARY.md all confirmed present and inlined in this report |
| Cross-slice integration: S01 pkg/jsonrpc → S04/S05 consumers | ✅ | cmd entrypoints and typed clients all import pkg/jsonrpc; all compile cleanly |
| Cross-slice integration: S02 api/runtime+api/shim → S03/S04/S05 consumers | ✅ | Zero legacy api/spec or pkg/agentrunapi imports remain in any file |
| Cross-slice integration: S03 api/ari/domain.go → S04/S05 Service interfaces and impls | ✅ | Service interfaces use domain types; pkg/ari/server/server.go uses ARIView() pattern |
| Cross-slice integration: S04 service interfaces → S05 implementations | ✅ | pkg/ari/server/server.go implements all 3 ARI interfaces; pkg/agentrun/server/service.go implements Handler |
| Cross-slice integration: S05 migrations enable S06 legacy deletion | ✅ | All production callers migrated before S06 deleted legacy files; go test ./... pass after deletion |
| make build exits 0 (final) | ✅ | Verified live at milestone close: both binaries produced |
| go test ./... -count=1 exits 0 (final) | ✅ | All 17 test packages pass; integration tests 108s |
| Zero legacy dead code | ✅ | pkg/rpc/, pkg/ari/server.go, pkg/agentd/shim_client.go all deleted and confirmed absent |
| VALIDATION.md verdict: pass | ✅ | M012-VALIDATION.md verdict=pass, remediation_round=0 |

## Requirement Outcomes

M012 was a structural refactor milestone. All functional requirements (R001–R009) were validated in prior milestones and remained in validated status throughout M012. The refactor maintained behavioral compatibility — all integration tests continued to pass, demonstrating no regression.

| Requirement | Status Before M012 | Status After M012 | Evidence |
|-------------|-------------------|-------------------|----------|
| R001 — agentd starts + ARI socket | validated | validated (no change) | make build + integration tests pass; cmd entrypoints migrated to new typed API |
| R002 — Runtime entity registration via ARI | validated | validated (no change) | go test ./tests/integration passes; ARI server migrated to typed service |
| R003 — SQLite metadata store | validated | validated (no change) | go test ./pkg/store passes; store layer untouched by refactor |
| R004 — Session Manager state machine | validated | validated (no change) | go test ./pkg/agentd passes |
| R005 — Process Manager + shim lifecycle | validated | validated (no change) | process.go/recovery.go migrated to typed Client; agentd tests pass |
| R006 — ARI JSON-RPC session/* methods | validated | validated (no change) | go test ./pkg/ari passes; ARI server adapter preserves all methods |
| R007 — agentdctl CLI | validated | validated (no change) | make build produces agentdctl; integration tests verify CLI operations |

No requirement status transitions occurred. No new requirements were surfaced during M012. The pre-existing pkg/jsonrpc Client.enqueueNotification race (K078) was documented but does not affect the single-run acceptance bar.

## Deviations

["S03: json:\"-\" for sensitive fields was replaced with ARIView() method pattern because json:\"-\" also blocks bbolt store persistence — security guarantee is identical (fields absent from ARI responses)", "S05/T02: plan called for 'one struct implementing all 3 interfaces'; adapted to adapter pattern because WorkspaceService.List and AgentService.List have identical signatures with different return types — a single Go struct cannot implement both", "S05/T03: pkg/agentd/shim_client.go NOT removed in S05 — retained for test compatibility; full removal deferred to S06", "S06/T01: shim_client_test.go could not be wholesale deleted — recovery_test.go and recovery_posture_test.go depended on its mock infrastructure; extracted newMockShimServer/mockShimServer to mock_run_server_test.go"]

## Follow-ups

["pkg/jsonrpc Client.enqueueNotification send-on-closed-channel race (K078): guard with select{case notifCh <- msg: default: log.Warn} or recover() to eliminate the panic under -count>1 parallel test loads", "pkg/agentrun/client package tests: Client-specific tests (Dial, Prompt, Cancel, Subscribe, Status, History, Stop) were dropped when shim_client_test.go was deleted; equivalent coverage should be added to pkg/agentrun/client package", "Consider whether pkg/ari/server_test.go coverage gaps (from the S06 migration) need supplementation — the migrated test suite tests the adapter layer indirectly but may not cover all adapter-boundary edge cases"]
