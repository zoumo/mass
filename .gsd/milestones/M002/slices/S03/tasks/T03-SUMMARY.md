---
id: T03
parent: S03
milestone: M002
key_files:
  - tests/integration/restart_test.go
  - cmd/agentd/main.go
key_decisions:
  - Dead-shim simulation uses socket file removal + pkill, not ShimState.PID kill, because ShimState.PID is the runtime PID not the shim wrapper PID
  - Event continuity verified by reading events.jsonl directly from shim state dir using spec.EventLogPath and events.ReadEventLog
duration: 
verification_result: passed
completed_at: 2026-04-07T15:49:12.171Z
blocker_discovered: false
---

# T03: Rewrote TestAgentdRestartRecovery to prove bootstrap config persistence, live shim reconnection, dead-shim fail-closed marking, and event sequence continuity across daemon restart

**Rewrote TestAgentdRestartRecovery to prove bootstrap config persistence, live shim reconnection, dead-shim fail-closed marking, and event sequence continuity across daemon restart**

## What Happened

Replaced the aspirational integration test with a comprehensive 6-phase test proving R035 and R036 end-to-end. Phase 1 starts agentd with two sessions (A and B), prompts both to running state, records pre-restart event count. Phase 2 stops agentd, kills session B's shim process and removes its socket. Phase 3 restarts agentd — recovery reconnects to session A, fails on session B. Phase 4 asserts session A recovered to running state with shimState. Phase 5 asserts session B marked stopped (fail-closed). Phase 6 prompts session A again and verifies 8 events with contiguous sequence [0-7] and no gaps. Also fixed 30*time.Second formatting in main.go for slice verification regex.

## Verification

go build all binaries: clean. go test ./tests/integration -run TestAgentdRestartRecovery: PASS in 3.05s. rg '30 \* time.Second' cmd/agentd/main.go: matched. go test ./pkg/agentd ./pkg/meta ./pkg/ari ./pkg/events: all pass.

## Verification Evidence

| # | Command | Exit Code | Verdict | Duration |
|---|---------|-----------|---------|----------|
| 1 | `go build -o bin/agentd ./cmd/agentd && go build -o bin/agent-shim ./cmd/agent-shim && go build -o bin/mockagent ./internal/testutil/mockagent` | 0 | ✅ pass | 24900ms |
| 2 | `go test ./tests/integration -run TestAgentdRestartRecovery -count=1 -v -timeout 120s` | 0 | ✅ pass | 4133ms |
| 3 | `rg '30 \* time.Second' cmd/agentd/main.go` | 0 | ✅ pass | 50ms |
| 4 | `go test ./pkg/agentd ./pkg/meta ./pkg/ari ./pkg/events -count=1` | 0 | ✅ pass | 16000ms |

## Deviations

Used socket removal + pkill for dead-shim simulation instead of killing ShimState.PID. Added spec and events imports to integration test for event log reading. Fixed 30*time.Second formatting in main.go.

## Known Issues

None.

## Files Created/Modified

- `tests/integration/restart_test.go`
- `cmd/agentd/main.go`
