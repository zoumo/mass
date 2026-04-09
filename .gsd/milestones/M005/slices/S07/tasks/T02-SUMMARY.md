---
id: T02
parent: S07
milestone: M005
key_files:
  - tests/integration/restart_test.go
key_decisions:
  - Adopted kill-all-shims strategy (both agents error) rather than selective kill — simplifies test while fully proving R052 identity persistence
  - agent/prompt field is 'prompt' not 'text' (plan had this wrong)
  - After agent/prompt end_turn, agent state stays 'running' — updated waitForAgentState accordingly
  - Agents in error state require agent/stop before agent/delete
duration: 
verification_result: passed
completed_at: 2026-04-08T21:53:53.289Z
blocker_discovered: false
---

# T02: Rewrote TestAgentdRestartRecovery to use agent/* ARI surface, proving R052: agent identity (room+name+agentId) survives daemon restart and dead-shim agents are fail-closed to error state

**Rewrote TestAgentdRestartRecovery to use agent/* ARI surface, proving R052: agent identity (room+name+agentId) survives daemon restart and dead-shim agents are fail-closed to error state**

## What Happened

Replaced the stale session/* restart test with a 7-phase agent/* integration test. Phase 1 creates workspace, room, and two agents (agent-A, agent-B) and prompts both. Phase 2 stops agentd and kills all shim/mockagent processes. Phase 3 restarts agentd, which runs the T01 RecoverSessions reconciliation and marks both agents error. Phase 4 asserts R052: agent-A's UUID, room, and name are identical to pre-restart values. Phase 5 asserts agent-B state=error. Phase 6 verifies agent/list returns both agents with intact room identity. Phase 7 cleans up via stop+delete. Two key local adaptations: (1) AgentPromptParams.Prompt field is 'prompt' not 'text'; (2) after end_turn, agent stays in 'running' state not 'created'. Added waitForAgentState and createAgentAndWait helpers. Preserved startAgentd/stopAgentd since they belong in this file.

## Verification

make build (exit 0); go test ./tests/integration/... -count=1 -run TestAgentdRestartRecovery -v -timeout 120s (exit 0, PASS in 4.47s). All 7 phases executed correctly including R052 identity assertions and complete cleanup.

## Verification Evidence

| # | Command | Exit Code | Verdict | Duration |
|---|---------|-----------|---------|----------|
| 1 | `make build` | 0 | ✅ pass | 6300ms |
| 2 | `go test ./tests/integration/... -count=1 -run TestAgentdRestartRecovery -v -timeout 120s` | 0 | ✅ pass | 4468ms |

## Deviations

agent/prompt JSON field is 'prompt' not 'text' (plan said 'text'); post-prompt agent state is 'running' not 'created'; cleanup requires agent/stop before agent/delete for error-state agents; used kill-all-shims fallback strategy from step 7 of the plan.

## Known Issues

None.

## Files Created/Modified

- `tests/integration/restart_test.go`
