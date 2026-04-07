---
id: T02
parent: S03
milestone: M002
key_files:
  - pkg/agentd/recovery.go
  - pkg/agentd/recovery_test.go
  - cmd/agentd/main.go
key_decisions:
  - Recovered shims watched via DisconnectNotify rather than Cmd.Wait since daemon did not fork them
  - Recovery is fail-closed per D012/D029 — connect failure marks session stopped
  - RecoverSessions returns nil even when individual sessions fail; only systemic DB errors returned
duration: 
verification_result: passed
completed_at: 2026-04-07T15:33:40.702Z
blocker_discovered: false
---

# T02: Added RecoverSessions startup pass that reconnects to live shims, replays history, resumes subscriptions, and marks dead shims stopped; wired into daemon startup and fixed shutdown timeout bug

**Added RecoverSessions startup pass that reconnects to live shims, replays history, resumes subscriptions, and marks dead shims stopped; wired into daemon startup and fixed shutdown timeout bug**

## What Happened

Created pkg/agentd/recovery.go with RecoverSessions(ctx) method on ProcessManager that lists non-terminal sessions, connects to persisted shim sockets via DialWithHandler, calls runtime/status + runtime/history + session/subscribe to reconcile and resume event delivery, and registers recovered shims in the processes map. Dead shims are marked stopped (fail-closed per D012/D029). Added watchRecoveredProcess goroutine that watches via DisconnectNotify for recovered shims without a Cmd handle. Created 6 unit tests covering live shim, dead shim, no sessions, stopped sessions, mixed scenarios, and missing socket path. Wired recovery into cmd/agentd/main.go before ARI server start. Fixed shutdown timeout bug (30 nanoseconds → 30*time.Second).

## Verification

go test ./pkg/agentd -count=1 -run TestRecoverSessions -v: 6 PASS. go build ./cmd/agentd: clean build. rg '30\*time\.Second' cmd/agentd/main.go: both timeouts correct. go test ./pkg/agentd -count=1 -v: all 53 tests pass.

## Verification Evidence

| # | Command | Exit Code | Verdict | Duration |
|---|---------|-----------|---------|----------|
| 1 | `go test ./pkg/agentd -count=1 -run TestRecoverSessions -v` | 0 | ✅ pass | 7200ms |
| 2 | `go build ./cmd/agentd` | 0 | ✅ pass | 7800ms |
| 3 | `rg '30\*time\.Second' cmd/agentd/main.go` | 0 | ✅ pass | 50ms |
| 4 | `go test ./pkg/agentd -count=1 -v` | 0 | ✅ pass | 10800ms |

## Deviations

Added 3 extra test cases beyond the 3 specified in plan. Renamed createTestSession to createRecoveryTestSession to avoid name collision.

## Known Issues

None.

## Files Created/Modified

- `pkg/agentd/recovery.go`
- `pkg/agentd/recovery_test.go`
- `cmd/agentd/main.go`
