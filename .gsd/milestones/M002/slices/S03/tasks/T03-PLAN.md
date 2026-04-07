---
estimated_steps: 9
estimated_files: 1
skills_used: []
---

# T03: Integration test proving restart recovery and event continuity

Extend the existing tests/integration/restart_test.go to prove that agentd restart recovers sessions with event continuity. The current test is aspirational — it only checks session existence after restart. This task makes it prove real recovery: session config survives, shim reconnects, events have no seq gaps, and dead shims result in stopped sessions.

The test must prove R035 (single resume path closes event gap) and R036 (enough config persisted to rebuild truthful state after restart).

Steps:
1. Refactor TestAgentdRestartRecovery to verify bootstrap config persistence: after restart, session/status returns running state with shim reconnected.
2. Add event continuity verification: after Phase 1 prompt, record the event count/last seq. After restart + recovery, send another prompt. Verify the combined event log has no seq gaps.
3. Add a dead-shim recovery case: create a second session, kill its shim PID before restart, verify it's marked stopped after recovery while the live session remains running.
4. Add a helper function to count events via session/status or a new test utility that reads the events.jsonl file.
5. Verify all existing pkg tests still pass alongside the integration test.

Note: This test requires built binaries (agentd, agent-shim, mockagent). The test setup already handles binary discovery. The test uses real Unix sockets and real process fork/kill.

## Inputs

- ``tests/integration/restart_test.go` — existing aspirational test to extend`
- ``pkg/agentd/recovery.go` — RecoverSessions from T02 (tested here end-to-end)`
- ``pkg/meta/session.go` — bootstrap config persistence from T01 (tested here end-to-end)`
- ``pkg/ari/server.go` — session/status endpoint that surfaces recovery state`

## Expected Output

- ``tests/integration/restart_test.go` — comprehensive restart recovery test proving event continuity and dead-shim handling`

## Verification

go build ./cmd/agentd ./cmd/agent-shim ./cmd/mockagent && go test ./tests/integration -run TestAgentdRestartRecovery -count=1 -v -timeout 120s
