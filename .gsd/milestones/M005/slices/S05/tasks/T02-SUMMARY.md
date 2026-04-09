---
id: T02
parent: S05
milestone: M005
key_files:
  - pkg/rpc/server.go
  - pkg/rpc/server_test.go
key_decisions:
  - Event count per prompt is 6 not 7 — mock agent WriteTextFile does not emit an ACP SessionNotification
  - NotifyTurnEnd always fires in handlePrompt using stopReason='error' fallback before conditional replyError
  - turn_start fires before mgr.Prompt giving it the lowest seq; turn_end fires after giving it the highest among session/update events
duration: 
verification_result: passed
completed_at: 2026-04-08T20:30:09.236Z
blocker_discovered: false
---

# T02: Wired NotifyTurnStart/NotifyTurnEnd into handlePrompt and updated RPC integration tests from 4-event to 6-event model with turn field assertions

**Wired NotifyTurnStart/NotifyTurnEnd into handlePrompt and updated RPC integration tests from 4-event to 6-event model with turn field assertions**

## What Happened

Modified handlePrompt in pkg/rpc/server.go to call NotifyTurnStart before mgr.Prompt and NotifyTurnEnd always after (even on error) using a stopReason variable. Updated all three CleanBreakSurface subtests to expect 6 events per prompt (the task plan assumed 7, but the mock agent's WriteTextFile emits no ACP SessionNotification — only 2 text events appear inside the turn). Added TurnId/StreamSeq assertions: turn_start at live[0] has StreamSeq=0, all session/update events share the same TurnId, turn_end at live[5] has StreamSeq=3. All 20 RPC test cases pass; all 8 packages pass.

## Verification

go test ./pkg/rpc/... -count=1 -v: all 20 cases PASS. go test ./pkg/... -count=1: all 8 packages OK. grep -c 'collect(4' pkg/rpc/server_test.go: 0 occurrences.

## Verification Evidence

| # | Command | Exit Code | Verdict | Duration |
|---|---------|-----------|---------|----------|
| 1 | `go test ./pkg/rpc/... -count=1 -v 2>&1 | grep -E 'PASS|FAIL|---'` | 0 | ✅ pass | 13500ms |
| 2 | `go test ./pkg/... -count=1` | 0 | ✅ pass | 19400ms |
| 3 | `grep -c 'collect(4' pkg/rpc/server_test.go` | 1 | ✅ pass | 50ms |

## Deviations

Task plan assumed 7 events per prompt (with a file_write event at seq+2). Actual count is 6 — WriteTextFile in acpClient does a direct OS write without emitting any ACP SessionNotification, so no file_write event enters the subscriber stream. Adjusted: collect(7,→6), lastSeq 6→5, turn_end StreamSeq 4→3, stateChange indices adjusted accordingly.

## Known Issues

None.

## Files Created/Modified

- `pkg/rpc/server.go`
- `pkg/rpc/server_test.go`
