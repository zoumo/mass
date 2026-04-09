---
id: T01
parent: S04
milestone: M005
key_files:
  - pkg/ari/server.go
  - pkg/ari/server_test.go
  - pkg/ari/types.go
key_decisions:
  - handleRoomDelete now blocks on non-stopped agents (not just sessions) to cover async creating window
  - error agent state blocks room/delete — users must call agent/stop first
  - AgentInfo.ErrorMessage added to types.go to surface bootstrap errors via agent/status
  - 4 tests migrated from newTestHarness to newSessionTestHarness
duration: 
verification_result: passed
completed_at: 2026-04-08T19:48:23.247Z
blocker_discovered: false
---

# T01: Made handleAgentCreate return state:"creating" immediately with background goroutine bootstrap, added creating-state guard to handleAgentPrompt, updated 20+ tests with pollAgentUntilReady helper, added TestARIAgentCreateAsync and TestARIAgentCreateAsyncErrorState — all pass

**Made handleAgentCreate return state:"creating" immediately with background goroutine bootstrap, added creating-state guard to handleAgentPrompt, updated 20+ tests with pollAgentUntilReady helper, added TestARIAgentCreateAsync and TestARIAgentCreateAsyncErrorState — all pass**

## What Happened

Changed handleAgentCreate to create the agent record in AgentStateCreating, reply immediately with state:"creating", then launch a 90s background goroutine that creates the session, acquires workspace/registry refs, calls processes.Start, and transitions agent to "created" (success) or "error" (failure with cleanup). Added creating-state guard to handleAgentPrompt. Enhanced handleRoomDelete to block on non-stopped agents (not just sessions) to cover the async window before session is created. Added ErrorMessage to AgentInfo wire type. Updated 20+ tests to use new pollAgentUntilReady helper. Added TestARIAgentCreateAsync and TestARIAgentCreateAsyncErrorState. Migrated 4 tests from newTestHarness to newSessionTestHarness.

## Verification

go test ./pkg/ari/... -count=1 -timeout 120s → PASS; go test ./pkg/ari/... -run TestARIAgentCreateAsync -v -timeout 60s → PASS; go test ./pkg/ari/... -run TestARIAgentCreateAsyncErrorState -v -timeout 60s → PASS; go build ./... → clean

## Verification Evidence

| # | Command | Exit Code | Verdict | Duration |
|---|---------|-----------|---------|----------|
| 1 | `go test ./pkg/ari/... -count=1 -timeout 120s` | 0 | ✅ pass | 13700ms |
| 2 | `go test ./pkg/ari/... -run TestARIAgentCreateAsync -v -timeout 60s` | 0 | ✅ pass | 1000ms |
| 3 | `go test ./pkg/ari/... -run TestARIAgentCreateAsyncErrorState -v -timeout 60s` | 0 | ✅ pass | 500ms |
| 4 | `go build ./...` | 0 | ✅ pass | 800ms |

## Deviations

handleRoomDelete enhanced with agent-state guard (not in task plan). 'error' state blocks room/delete by design. AgentInfo.ErrorMessage added to types.go. 4 tests migrated to newSessionTestHarness.

## Known Issues

None.

## Files Created/Modified

- `pkg/ari/server.go`
- `pkg/ari/server_test.go`
- `pkg/ari/types.go`
