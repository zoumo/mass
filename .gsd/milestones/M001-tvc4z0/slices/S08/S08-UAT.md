# S08: Integration Tests — UAT

**Milestone:** M001-tvc4z0
**Written:** 2026-04-06T18:32:06.524Z

# S08 UAT: Integration Tests Verification

## Overview

This UAT verifies the integration tests that prove the full agentd → agent-shim → mockagent pipeline works end-to-end. The tests cover session lifecycle, error handling, concurrent sessions, and restart behavior.

## Preconditions

1. Go toolchain installed (go 1.21+)
2. Project compiled: `go build ./cmd/agentd && go build ./cmd/agent-shim`
3. mockagent runtime class configured in test config
4. Test environment has access to /tmp for socket files
5. No existing agentd or shim processes running

## Test Case 1: End-to-End Pipeline

**Purpose:** Verify full lifecycle works from daemon start to cleanup

**Steps:**
1. Run: `go test ./tests/integration/... -run TestEndToEndPipeline -v`
2. Verify test output shows:
   - "agentd started with PID"
   - "socket ready at /tmp/..."
   - "workspace prepared: id=UUID"
   - "session created: id=UUID state=created"
   - "prompt completed: stopReason=end_turn"
   - "session stopped"
   - "session removed"
   - "workspace cleaned up"
   - "agentd shutdown complete"
3. Verify test result: "--- PASS: TestEndToEndPipeline"

**Expected Outcome:** Test passes, demonstrating full pipeline works

## Test Case 2: Session Lifecycle

**Purpose:** Verify session state machine transitions correctly

**Steps:**
1. Run: `go test ./tests/integration/... -run TestSessionLifecycle -v`
2. Verify test output shows state transitions:
   - "Step: session/new → state=created"
   - "Step: session/prompt → auto-start → state=running"
   - "INFO session state transition from_state=created to_state=running"
   - "Step: session/status → returns shim state"
   - "Step: session/stop → state=stopped"
   - "INFO session state transition from_state=running to_state=stopped"
   - "Step: session/remove → session deleted"
3. Verify test result: "--- PASS: TestSessionLifecycle"

**Expected Outcome:** Test passes, all state transitions work correctly

## Test Case 3: Error Handling - Prompt Stopped Session

**Purpose:** Verify error returned when prompting stopped session

**Steps:**
1. Run: `go test ./tests/integration/... -run TestSessionPromptStoppedSession -v`
2. Verify test output shows:
   - "Attempting to prompt stopped session (expecting error)"
   - "got expected error: rpc error -32602: session ... not running"
3. Verify test result: "--- PASS: TestSessionPromptStoppedSession"

**Expected Outcome:** Test passes, error handling works correctly

## Test Case 4: Protected Deletion - Remove Running Session

**Purpose:** Verify running sessions cannot be deleted

**Steps:**
1. Run: `go test ./tests/integration/... -run TestSessionRemoveRunningSession -v`
2. Verify test output shows:
   - "Attempting to remove running session (expecting error)"
   - "got expected error: ... cannot delete session ... in state running"
3. Verify test result: "--- PASS: TestSessionRemoveRunningSession"

**Expected Outcome:** Test passes, deletion protection works

## Test Case 5: Session List

**Purpose:** Verify session listing with count verification

**Steps:**
1. Run: `go test ./tests/integration/... -run TestSessionList -v`
2. Verify test output shows:
   - "created 3 sessions"
   - "list shows count=3"
   - "removed all sessions"
   - "list shows count=0"
3. Verify test result: "--- PASS: TestSessionList"

**Expected Outcome:** Test passes, listing functionality works

## Test Case 6: Agentd Restart Recovery

**Purpose:** Verify restart behavior (documents current limitation)

**Steps:**
1. Run: `go test ./tests/integration/... -run TestAgentdRestartRecovery -v`
2. Verify test output shows:
   - "Phase 1: Start agentd and create running session"
   - "shim running with PID"
   - "Phase 2: Kill agentd (keeping shim running)"
   - "shim process (PID ...) is still running after agentd exit"
   - "Phase 3: Restart agentd with same config"
   - "Phase 4: Verify reconnect to existing shim"
   - "prompt after restart failed: ... session ... not running (may be expected if shim disconnected)"
   - "Restart recovery test completed!"
3. Verify test result: "--- PASS: TestAgentdRestartRecovery"

**Expected Outcome:** Test passes, documenting that restart recovery is future work

## Test Case 7: Multiple Concurrent Sessions

**Purpose:** Verify 3 sessions can run concurrently without interference

**Steps:**
1. Run: `go test ./tests/integration/... -run TestMultipleConcurrentSessions -v`
2. Verify test output shows:
   - "prepared workspace 1: UUID"
   - "prepared workspace 2: UUID"
   - "prepared workspace 3: UUID"
   - "created session 1: UUID"
   - "created session 2: UUID"
   - "created session 3: UUID"
   - "Prompting sessions concurrently..."
   - "session 1 prompt completed: stopReason=end_turn"
   - "session 2 prompt completed: stopReason=end_turn"
   - "session 3 prompt completed: stopReason=end_turn"
   - "All 3 sessions responded successfully!"
3. Verify test result: "--- PASS: TestMultipleConcurrentSessions"

**Expected Outcome:** Test passes, concurrent sessions work independently

## Test Case 8: Concurrent Prompts Same Session

**Purpose:** Verify same session handles concurrent prompts

**Steps:**
1. Run: `go test ./tests/integration/... -run TestConcurrentPromptsSameSession -v`
2. Verify test output shows session handles concurrent requests
3. Verify test result: "--- PASS: TestConcurrentPromptsSameSession"

**Expected Outcome:** Test passes, same session handles concurrent prompts

## Full Test Suite Verification

**Steps:**
1. Run: `go test ./tests/integration/... -v -count=1`
2. Verify all 8 tests pass:
   - TestMultipleConcurrentSessions
   - TestConcurrentPromptsSameSession
   - TestEndToEndPipeline
   - TestAgentdRestartRecovery
   - TestSessionLifecycle
   - TestSessionPromptStoppedSession
   - TestSessionRemoveRunningSession
   - TestSessionList

**Expected Outcome:** All tests pass with PASS status

## Edge Cases Covered

1. **Long socket paths:** Tests use `/tmp/oar-{pid}-{counter}.sock` to avoid macOS 104-char limit
2. **Orphaned processes:** Cleanup uses pkill to terminate leftover shim/mockagent
3. **Concurrent client access:** Mutex serialization prevents interleaved JSON-RPC calls
4. **Invalid operations:** Prompting stopped session, removing running session return errors
5. **Restart behavior:** Shim survives agentd exit, but reconnection not yet implemented

## Notes

- Restart recovery (shim socket reconnection after agentd restart) is documented as future enhancement
- TestAgentdRestartRecovery passes but reveals current limitation
- Integration tests use real Unix sockets and mockagent, not mocks
