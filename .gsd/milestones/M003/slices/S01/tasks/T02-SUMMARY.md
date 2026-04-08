---
id: T02
parent: S01
milestone: M003
key_files:
  - pkg/ari/server.go
  - pkg/ari/types.go
key_decisions:
  - Guard placed after param unmarshalling but before side-effects so invalid params still get proper errors
  - session/stop intentionally left unguarded for safety
duration: 
verification_result: passed
completed_at: 2026-04-07T17:30:57.937Z
blocker_discovered: false
---

# T02: Added recoveryGuard to block session/prompt and session/cancel during daemon recovery, and wired per-session RecoveryInfo into session/status response

**Added recoveryGuard to block session/prompt and session/cancel during daemon recovery, and wired per-session RecoveryInfo into session/status response**

## What Happened

Added CodeRecoveryBlocked constant (-32001) to pkg/ari/types.go. Created recoveryGuard helper on connHandler in pkg/ari/server.go that checks ProcessManager.IsRecovering() and returns a typed JSON-RPC error when true. Wired the guard into handleSessionPrompt and handleSessionCancel (after param unmarshalling, before side-effects). Left handleSessionStop unguarded for safety and all read-only methods unguarded. Modified handleSessionStatus to look up ShimProcess via GetProcess() and convert agentd.RecoveryInfo to ari.SessionRecoveryInfo in the response.

## Verification

go build ./pkg/ari/... (exit 0), go vet ./pkg/ari/... (exit 0), go test ./pkg/ari/... -count=1 -v (all 10 tests pass, exit 0, 6.3s). Guards are no-ops when recovery phase is idle so existing tests pass unchanged.

## Verification Evidence

| # | Command | Exit Code | Verdict | Duration |
|---|---------|-----------|---------|----------|
| 1 | `go build ./pkg/ari/...` | 0 | ✅ pass | 8300ms |
| 2 | `go vet ./pkg/ari/...` | 0 | ✅ pass | 8300ms |
| 3 | `go test ./pkg/ari/... -count=1 -v` | 0 | ✅ pass | 6292ms |

## Deviations

None.

## Known Issues

None.

## Files Created/Modified

- `pkg/ari/server.go`
- `pkg/ari/types.go`
