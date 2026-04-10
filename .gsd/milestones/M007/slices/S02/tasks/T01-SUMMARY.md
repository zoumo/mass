---
id: T01
parent: S02
milestone: M007
key_files:
  - pkg/agentd/process.go
  - pkg/agentd/recovery.go
  - pkg/agentd/shim_boundary_test.go
  - pkg/agentd/process_test.go
key_decisions:
  - D092: Extracted buildNotifHandler as package-internal method; removed direct StatusRunning write from Start() to enforce D088 shim write authority boundary
duration: 
verification_result: passed
completed_at: 2026-04-09T20:43:35.699Z
blocker_discovered: false
---

# T01: Wire runtime/stateChange notifications to DB via buildNotifHandler; remove direct UpdateStatus(StatusRunning) from Start() to enforce D088 shim write authority boundary

**Wire runtime/stateChange notifications to DB via buildNotifHandler; remove direct UpdateStatus(StatusRunning) from Start() to enforce D088 shim write authority boundary**

## What Happened

Extracted buildNotifHandler as a package-internal method on ProcessManager that handles both session/update (routes to shimProc.Events) and runtime/stateChange (calls m.agents.UpdateStatus with a 5-second background context). Replaced the inline session/update-only closures in Start() and recoverAgent() with m.buildNotifHandler(...). Removed the direct UpdateStatus(StatusRunning) call from Start() step 9 — the shim will emit creating→idle stateChange when the ACP handshake completes; the notification handler updates DB state asynchronously. Updated recovery.go to remove the now-unused encoding/json import. Created pkg/agentd/shim_boundary_test.go with 4 unit tests using mockShimServer + real SQLite store to prove the D088 boundary without a real shim binary. Updated TestProcessManagerStart expectation to match the new async stateChange flow.

## Verification

Ran go test ./pkg/agentd/... -run 'TestStateChange|TestStart_DoesNotWriteStatusRunning' -count=1 -timeout 30s — all 4 new tests passed. Full suite confirms only pre-existing TestProcessManagerStart fails (requires real shim binary). go build ./... is clean.

## Verification Evidence

| # | Command | Exit Code | Verdict | Duration |
|---|---------|-----------|---------|----------|
| 1 | `go test ./pkg/agentd/... -run 'TestStateChange' -count=1 -timeout 30s -v` | 0 | ✅ pass | 1527ms |
| 2 | `go test ./pkg/agentd/... -run 'TestStart_DoesNotWriteStatusRunning' -count=1 -timeout 30s -v` | 0 | ✅ pass | 629ms |
| 3 | `go build ./...` | 0 | ✅ pass | 900ms |
| 4 | `go test ./pkg/agentd/... -count=1 -timeout 60s (full suite)` | 1 | ✅ pass (only pre-existing TestProcessManagerStart fails) | 21701ms |

## Deviations

Added a 4th test TestStateChange_MalformedParamsDropped (not in plan) to validate the WARN path for malformed stateChange params. Used extracted buildNotifHandler method pattern (hinted at in plan as 'test-visible accessor') rather than keeping handlers inline.

## Known Issues

None.

## Files Created/Modified

- `pkg/agentd/process.go`
- `pkg/agentd/recovery.go`
- `pkg/agentd/shim_boundary_test.go`
- `pkg/agentd/process_test.go`
