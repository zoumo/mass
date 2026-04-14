---
id: T01
parent: S06
milestone: M012
key_files:
  - pkg/agentd/process.go
  - pkg/agentd/mock_shim_server_test.go
key_decisions:
  - Extracted mock_shim_server_test.go rather than deleting all of shim_client_test.go wholesale, to preserve recovery test infrastructure without carrying the deleted ShimClient's test suite.
duration: 
verification_result: passed
completed_at: 2026-04-14T03:34:05.965Z
blocker_discovered: false
---

# T01: Deleted pkg/rpc, pkg/agentd/shim_client.go, and shim_client_test.go; extracted mock server helper to new file; fixed two bare-symbol references in process.go so make build and go test ./pkg/agentd/... pass clean

**Deleted pkg/rpc, pkg/agentd/shim_client.go, and shim_client_test.go; extracted mock server helper to new file; fixed two bare-symbol references in process.go so make build and go test ./pkg/agentd/... pass clean**

## What Happened


Deleted the three legacy files per plan: the entire `pkg/rpc/` directory (old sourcegraph/jsonrpc2 shim server and its tests), `pkg/agentd/shim_client.go` (old internal ShimClient), and `pkg/agentd/shim_client_test.go` (its test suite).

The plan called for one fix in process.go (`ParseShimEvent` → `shimclient.ParseShimEvent` at line 139), but a build run revealed a second bare reference in the same file: the return type of `buildNotifHandler` was declared as `NotificationHandler` rather than `shimclient.NotificationHandler` (line 132). Both bare symbols had been defined in the deleted `shim_client.go`. Fixed both in a single additional edit.

Deleting `shim_client_test.go` also removed the `newMockShimServer` / `mockShimServer` infrastructure that `recovery_test.go` and `recovery_posture_test.go` depend on. Those tests are unrelated to the old ShimClient; they use the mock to start a real JSON-RPC server for recovery integration scenarios. The fix was to extract only the mock server infrastructure (all types and construction helpers) into a new file `pkg/agentd/mock_shim_server_test.go`, replacing the `shim` import alias with `apishim` (same package, different alias). All ShimClient-specific tests from the old file (dial, prompt, cancel, subscribe, status, history, stop, ParseShimEvent helpers, etc.) were intentionally dropped — they are superseded by pkg/shim/client tests.


## Verification


make build exits 0. go test ./pkg/agentd/... -count=1 exits 0 (7.8 s, all tests pass). rg '"github.com/zoumo/oar/pkg/rpc"' --type go returns zero matches (exit 1 = no matches). pkg/rpc directory no longer exists.


## Verification Evidence

| # | Command | Exit Code | Verdict | Duration |
|---|---------|-----------|---------|----------|
| 1 | `make build` | 0 | ✅ pass | 3900ms |
| 2 | `go test ./pkg/agentd/... -count=1` | 0 | ✅ pass | 7806ms |
| 3 | `rg '"github.com/zoumo/oar/pkg/rpc"' --type go` | 1 | ✅ pass (zero matches) | 50ms |

## Deviations

Two deviations from the written plan:
1. An extra bare-symbol fix was required in process.go beyond the one called out by the plan. The return type of `buildNotifHandler` was `NotificationHandler` (not `shimclient.NotificationHandler`); the plan only mentioned fixing the call-site at line 139 but not the function signature at line 132.
2. `pkg/agentd/shim_client_test.go` contained `newMockShimServer` infrastructure reused by recovery tests; the plan said "delete shim_client_test.go" without noting this dependency. The fix was to extract the infrastructure into `pkg/agentd/mock_shim_server_test.go` rather than simply deleting.

## Known Issues

The ShimClient-specific tests that lived in shim_client_test.go (Dial, Prompt, Cancel, Subscribe, Status, History, Stop, ParseShimEvent helpers) were dropped. Equivalent coverage should exist or be added in pkg/shim/client package tests, but that is out of scope for this slice.

## Files Created/Modified

- `pkg/agentd/process.go`
- `pkg/agentd/mock_shim_server_test.go`
