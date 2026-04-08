---
id: T02
parent: S03
milestone: M004
key_files:
  - pkg/ari/server_test.go
key_decisions:
  - Added testing.Short() guard for this test per task plan specification
duration: 
verification_result: passed
completed_at: 2026-04-08T06:27:00.025Z
blocker_discovered: false
---

# T02: Added TestARIRoomTeardownGuards proving room/delete fails with active members, session/remove fails on running sessions, and both succeed after proper stop sequence

**Added TestARIRoomTeardownGuards proving room/delete fails with active members, session/remove fails on running sessions, and both succeed after proper stop sequence**

## What Happened

Wrote TestARIRoomTeardownGuards in pkg/ari/server_test.go. The test creates a room with 2 agents, auto-starts one via room/send, then proves 3 ordering constraints: (1) room/delete returns CodeInvalidParams with "active member" when a session is running, (2) session/remove returns CodeInvalidParams with "active" (ErrDeleteProtected) on a running session, (3) both operations succeed after all sessions are stopped. Test passes in 1.26s, full ARI suite passes with no regressions.

## Verification

Ran `go test ./pkg/ari/ -count=1 -v -run TestARIRoomTeardownGuards -timeout 120s` — PASS (1.26s). All 3 ordering constraints verified. Full short suite `go test ./pkg/ari/ -count=1 -short -timeout 120s` — PASS (8.2s), no regressions.

## Verification Evidence

| # | Command | Exit Code | Verdict | Duration |
|---|---------|-----------|---------|----------|
| 1 | `go test ./pkg/ari/ -count=1 -v -run TestARIRoomTeardownGuards -timeout 120s` | 0 | ✅ pass | 2423ms |
| 2 | `go test ./pkg/ari/ -count=1 -short -timeout 120s` | 0 | ✅ pass | 8237ms |

## Deviations

None.

## Known Issues

None.

## Files Created/Modified

- `pkg/ari/server_test.go`
