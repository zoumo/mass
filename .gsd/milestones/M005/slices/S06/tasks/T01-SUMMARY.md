---
id: T01
parent: S06
milestone: M005
key_files:
  - pkg/ari/types.go
  - pkg/ari/server.go
  - pkg/ari/server_test.go
key_decisions:
  - handleRoomSend guards on both AgentStateStopped and AgentStateCreating, mirroring handleAgentPrompt
  - room/send calls agents.UpdateState(running) after successful deliverPrompt — agent state becomes canonical
  - handleRoomStatus reads from agents table so Description and RuntimeClass are now surfaced in room/status response
duration: 
verification_result: passed
completed_at: 2026-04-08T21:05:48.948Z
blocker_discovered: false
---

# T01: Replaced session-table lookups in room/status and room/send with agents-table; RoomMember now exposes AgentState/Description/RuntimeClass; room/send sets agent state to running after delivery

**Replaced session-table lookups in room/status and room/send with agents-table; RoomMember now exposes AgentState/Description/RuntimeClass; room/send sets agent state to running after delivery**

## What Happened

Three coordinated changes in pkg/ari/: (1) RoomMember struct in types.go replaced SessionId/State with Description/RuntimeClass/AgentState. (2) handleRoomStatus replaced ListSessions+SessionFilter with agents.List+AgentFilter, building members from agent rows. (3) handleRoomSend replaced the session-state stopped guard with agent.State guards (AgentStateStopped and AgentStateCreating), kept the ListSessions call only for targetSessionID lookup, and added agents.UpdateState(running) after successful deliverPrompt. All test assertions updated from .State to .AgentState on RoomMember across five test functions.

## Verification

go build ./... → clean. go test ./pkg/ari/... -run TestARIRoomLifecycle -v → PASS. go test ./pkg/ari/... -run TestARIRoomSendDelivery -v → PASS (UpdateState(running) visible in logs). go test ./pkg/ari/... -count=1 -timeout 120s → ok 12.1s, zero failures.

## Verification Evidence

| # | Command | Exit Code | Verdict | Duration |
|---|---------|-----------|---------|----------|
| 1 | `go build ./...` | 0 | ✅ pass | 7700ms |
| 2 | `go test ./pkg/ari/... -count=1 -run TestARIRoomLifecycle -v -timeout 120s` | 0 | ✅ pass | 1490ms |
| 3 | `go test ./pkg/ari/... -count=1 -run TestARIRoomSendDelivery -v -timeout 120s` | 0 | ✅ pass | 790ms |
| 4 | `go test ./pkg/ari/... -count=1 -timeout 120s` | 0 | ✅ pass | 12100ms |

## Deviations

None.

## Known Issues

None.

## Files Created/Modified

- `pkg/ari/types.go`
- `pkg/ari/server.go`
- `pkg/ari/server_test.go`
