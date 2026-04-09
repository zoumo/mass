---
id: S07
parent: M005
milestone: M005
provides:
  - ["R052 validated: agent identity (room+name+agentId) survives daemon restart — proven by TestAgentdRestartRecovery", "Fail-safe agent state reconciliation: daemon restart marks dead-shim agents as error (not stuck in running/creating)", "Full integration test suite migrated to agent/* ARI surface: 7/7 tests pass, zero session/* calls in non-CLI test files", "Three new unit tests for RecoverSessions reconciliation branches (error/running/creating-cleanup)"]
requires:
  - slice: S01
    provides: Agent model contract and ARI surface definition
  - slice: S02
    provides: AgentManager and agent/* state machine
  - slice: S03
    provides: ARI agent/* method handlers
  - slice: S04
    provides: Async create, stop/delete separation, restart
  - slice: S05
    provides: Turn-aware event envelope
  - slice: S06
    provides: Room/MCP agent alignment, OAR_AGENT_ID/OAR_AGENT_NAME env vars
affects:
  []
key_files:
  - ["pkg/agentd/process.go", "pkg/agentd/recovery.go", "pkg/agentd/recovery_test.go", "cmd/agentd/main.go", "pkg/ari/server_test.go", "pkg/agentd/process_test.go", "tests/integration/restart_test.go", "tests/integration/session_test.go", "tests/integration/concurrent_test.go", "tests/integration/e2e_test.go"]
key_decisions:
  - ["Changed recoverSession to return (spec.Status, error) so RecoverSessions can reconcile agent state from shim status without re-querying the processes map", "Removed len(candidates)==0 early-return guard from RecoverSessions so creating-cleanup always runs even when there are no candidate sessions", "Kill-all-shims strategy for TestAgentdRestartRecovery — simplifies test while still proving R052 identity persistence (agentId+room+name survive restart regardless of error state)", "agent/prompt field is 'prompt' not 'text', and post-prompt agent state is 'running' not 'created' — integration test helpers updated accordingly", "Shared helpers (waitForAgentState, createAgentAndWait, createRoom, deleteRoom, stopAndDeleteAgent) consolidated in session_test.go for use across all integration test files"]
patterns_established:
  - ["AgentManager injected into ProcessManager via constructor — same pattern as SessionManager injection", "RecoverSessions creating-cleanup pass: always collect recovered AgentIDs into a map, then ListAgents(creating) and mark any not in the map as error — this pattern handles all restart-during-bootstrap races", "Integration test helper consolidation: shared helpers live in session_test.go (the primary helper file), all other test files in the package consume them without duplication", "stopAndDeleteAgent helper pattern: always call agent/stop before agent/delete regardless of current state — safe for stopped, error, and running agents"]
observability_surfaces:
  - ["RecoverSessions logs agent state transitions at INFO level: 'updating agent state component=agentd.agent' for each reconciled agent", "RecoverSessions creating-cleanup logs at INFO level with message 'agent bootstrap lost: daemon restarted during creating phase'"]
drill_down_paths:
  []
duration: ""
verification_result: passed
completed_at: 2026-04-08T22:10:43.222Z
blocker_discovered: false
---

# S07: Recovery & Integration Proof

**Closed M005's two remaining gaps: injected AgentManager into RecoverSessions for fail-safe agent state reconciliation on daemon restart, and rewrote all integration tests to the agent/* ARI surface, proving R052 (agent identity survives restart).**

## What Happened

S07 closed the two final gaps that stood between M005's design contract and a fully proven, production-safe implementation.

**T01 — Agent state reconciliation in RecoverSessions**

Before S07, ProcessManager had no reference to AgentManager. When the daemon restarted and RecoverSessions ran its session recovery loop, agent rows in the agents table were never updated — a running agent whose shim failed to reconnect would remain in `running` state forever, invisible to operators and potentially accepting prompts it could not service.

T01 injected `*AgentManager` into ProcessManager (new field and updated NewProcessManager signature across 5 call sites: cmd/agentd/main.go, two server_test.go harnesses, process_test.go, recovery_test.go). The core implementation change was in RecoverSessions:
- Changed `recoverSession` to return `(spec.Status, error)` so the caller can use the shim's reported status directly for agent reconciliation without re-querying the processes map.
- Failure branch: if shim reconnect fails and `session.AgentID != ""`, call `agents.UpdateState(ctx, session.AgentID, meta.AgentStateError, "session lost: shim not recovered after daemon restart")`.
- Success branch: if shim reconnected and reports StatusRunning, call `agents.UpdateState(ctx, session.AgentID, meta.AgentStateRunning, "")`.
- Post-loop creating-cleanup pass: `store.ListAgents` for all `creating` agents; any agent that does NOT have a successfully-recovered session is marked error with message "agent bootstrap lost: daemon restarted during creating phase". This handles the race window where a daemon restart occurs between agent row creation and shim startup.
- A subtle early-return bug was fixed: the original code had `if len(candidates) == 0 { return nil }` which would short-circuit the creating-cleanup pass whenever the DB had zero sessions. Removed so the cleanup always runs.

Three new unit tests cover all reconciliation paths: dead-shim→agent-error, live-shim-running→agent-running, creating-agent-with-no-session→agent-error. All 15 TestRecoverSessions_* tests pass.

**T02 — Rewrite TestAgentdRestartRecovery to agent/* ARI surface**

The existing `TestAgentdRestartRecovery` used session/* methods that now return MethodNotFound. T02 replaced it with a 7-phase agent/* integration test proving R052:
- Phase 1: start agentd, create workspace, room, agent-A, agent-B; prompt both.
- Phase 2: stop agentd, kill all agent-shim and mockagent processes (kill-all-shims strategy for test simplicity and reliability).
- Phase 3: restart agentd — RecoverSessions runs, detects all shims are dead, marks both agents error.
- Phase 4: assert R052 — agent-A's UUID, room, and name are identical to pre-restart values. Identity persists even when the agent is in error state.
- Phase 5: assert agent-B state=error.
- Phase 6: verify agent/list returns both agents with intact room identity.
- Phase 7: stop + delete all agents and clean up workspace.

Key implementation discoveries: `AgentPromptParams.Prompt` is the JSON field name (not "text"), and after a successful prompt round-trip (end_turn), agent state is `running` not `created`. Added `waitForAgentState` and `createAgentAndWait` helpers.

**T03 — Confirm full agent/* migration across all integration tests**

The three other integration test files (session_test.go, concurrent_test.go, e2e_test.go) were already fully rewritten to agent/* before T03 executed. T03 confirmed correctness by running the full suite (7/7 tests pass, 2 skip for missing ANTHROPIC_API_KEY) and verifying zero session/* calls in non-CLI test files. `real_cli_test.go` legitimately retains session/* calls because it tests the real CLI runtime.

Shared helpers (waitForAgentState, createAgentAndWait, createRoom, deleteRoom, stopAndDeleteAgent) are defined in session_test.go and used across all test files in the package without duplication.

**Verification summary:**
- `go test ./pkg/agentd/... -count=1 -timeout 120s` → ok (1.572s)
- `go test ./pkg/ari/... -count=1 -timeout 120s` → ok (11.914s)
- `go test ./tests/integration/... -count=1 -timeout 180s` → ok (6.559s), 7 pass, 2 skip
- `go build ./...` → ok
- `grep session/* in tests/integration/ excluding real_cli_test.go` → empty (clean)

## Verification

All slice-level verification checks passed:
1. `go test ./pkg/agentd/... -count=1 -timeout 120s` — exit 0, ok (1.572s). All 15 TestRecoverSessions_* tests pass including three new agent reconciliation tests.
2. `go test ./pkg/ari/... -count=1 -timeout 120s` — exit 0, ok (11.914s). All ARI server tests pass.
3. `go test ./tests/integration/... -count=1 -timeout 180s` — exit 0, ok (6.559s). 7 tests pass, 2 skip (ANTHROPIC_API_KEY not set).
4. `go build ./...` — exit 0, BUILD OK.
5. `grep -rn 'session/new|session/prompt|session/stop|session/status|session/remove' tests/integration/ | grep -v real_cli_test.go` — empty (no session/* calls in non-CLI test files).
6. TestAgentdRestartRecovery isolated run: exit 0, PASS in 4.47s. All 7 phases executed including R052 identity assertions.

## Requirements Advanced

None.

## Requirements Validated

- R052 — TestAgentdRestartRecovery (7-phase integration test, 4.47s): agent-A and agent-B created pre-restart. After daemon restart with all shims killed, both agents are in error state but agentId, room, and name are identical to pre-restart values. agent/list returns both agents with intact room identity.

## New Requirements Surfaced

None.

## Requirements Invalidated or Re-scoped

None.

## Deviations

T01: Removed the `if len(candidates) == 0 { return nil }` early-return guard (undocumented in plan but required for creating-cleanup correctness). T02: agent/prompt field is 'prompt' not 'text'; post-prompt state is 'running' not 'created'; kill-all-shims strategy used (plan step 7 fallback); agent/stop required before agent/delete for error-state agents. T03: All three target files were already fully migrated — task confirmed correctness by running the suite rather than performing migrations.

## Known Limitations

RecoverSessions agent reconciliation uses best-effort UpdateState calls — if the agents table is unavailable during recovery, the session recovery still proceeds and the agent state update error is logged but not fatal. This is intentional (session recovery is more critical than agent state tracking during disaster recovery).

## Follow-ups

None. All M005 gaps are closed.

## Files Created/Modified

- `pkg/agentd/process.go` — Added agents *AgentManager field; updated NewProcessManager signature to accept agents parameter
- `pkg/agentd/recovery.go` — Changed recoverSession to return (spec.Status, error); added agent state reconciliation in RecoverSessions (failure, success, and creating-cleanup branches); removed early-return guard
- `pkg/agentd/recovery_test.go` — Updated setupRecoveryTest to pass AgentManager; added createTestAgentForRecovery helper; added 3 new reconciliation tests
- `cmd/agentd/main.go` — Updated NewProcessManager call site to pass agents parameter
- `pkg/ari/server_test.go` — Updated both NewProcessManager call sites to pass agents parameter
- `pkg/agentd/process_test.go` — Updated NewProcessManager call site to pass new AgentManager
- `tests/integration/restart_test.go` — Rewrote TestAgentdRestartRecovery with 7-phase agent/* test proving R052
- `tests/integration/session_test.go` — Rewrote all 4 tests to agent/* surface; added shared helpers: waitForAgentState, createAgentAndWait, createRoom, deleteRoom, stopAndDeleteAgent
- `tests/integration/concurrent_test.go` — Rewrote TestMultipleConcurrentSessions to use 3 agents in a shared room
- `tests/integration/e2e_test.go` — Rewrote TestEndToEndPipeline to use full 9-step agent/* lifecycle
