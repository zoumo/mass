---
estimated_steps: 25
estimated_files: 4
skills_used: []
---

# T03: Migrate remaining integration tests from session/* to agent/* surface

Three integration test files still call session/* methods (MethodNotFound at runtime): `session_test.go`, `concurrent_test.go`, `e2e_test.go`. They need to be rewritten to use the agent/* surface or deleted. This task rewrites them to preserve the coverage they provide.

**Steps:**

1. **`tests/integration/session_test.go`** — Contains 4 tests (TestSessionLifecycle, TestSessionPromptAndStop, TestSessionAutoStart, TestMultiplePromptsSequential) plus all the shared helpers (setupAgentdTest, prepareTestWorkspace, createTestSession, cleanupTestWorkspace). Rewrite:
   - Remove `createTestSession` helper (uses session/new). Replace with `createAgentAndWait` (defined in restart_test.go — or move it here as a shared helper). To avoid duplication, define `createAgentAndWait` and `waitForAgentState` in session_test.go (as the primary shared helper file) and remove them from restart_test.go.
   - Rewrite TestSessionLifecycle → TestAgentLifecycle: agent/create → waitForAgentState(created) → agent/prompt → agent/stop → waitForAgentState(stopped) → agent/delete. Must also create a room first.
   - Rewrite TestSessionPromptAndStop → TestAgentPromptAndStop: similar lifecycle.
   - Rewrite TestSessionAutoStart → TestAgentPromptFromCreated: agent/create → wait for created → agent/prompt → verify response.
   - Rewrite TestMultiplePromptsSequential → TestMultipleAgentPromptsSequential: multiple sequential prompts to the same agent.
   - Keep `setupAgentdTest`, `prepareTestWorkspace`, `cleanupTestWorkspace`, `waitForSocket` helpers intact.
   - Add `createRoom` helper for creating rooms: `client.Call("room/create", map[string]interface{}{"name": roomName}, &result)`.
   - Replace `testSocketCounter` type: it's `int64` — keep as-is, used by setupAgentdTest.

2. **`tests/integration/concurrent_test.go`** — Contains TestMultipleConcurrentSessions. Rewrite:
   - Replace `createTestSession` with `createAgentAndWait`.
   - Create a shared room, create 3 agents in the room.
   - Prompt concurrently (keep the clientMu pattern).
   - Assert each agent prompt returns without error.
   - Clean up: agent/stop, agent/delete for each.

3. **`tests/integration/e2e_test.go`** — Contains TestEndToEndPipeline. Rewrite:
   - Replace session/* calls with agent/* calls.
   - Core pipeline: workspace/prepare → room/create → agent/create → waitForAgentState(created) → agent/prompt → agent/stop → workspace/cleanup.
   - The `waitForSocket` helper is defined here — keep it in place.
   - Remove any session/* imports.

4. **Verify zero session/* calls remain**: `grep -r 'session/new\|session/prompt\|session/stop\|session/status\|session/remove' tests/integration/` should return empty.

5. **Shared helper consolidation**: The `createAgentAndWait` and `waitForAgentState` helpers used by T02 (restart_test.go) should be defined in `session_test.go` (the main helper file) so they're available to all test files in the package. Remove duplicate definitions from restart_test.go if T02 put them there.

6. **Room lifecycle note**: Some tests create rooms for agents. Add `room/delete` in cleanup. Room create/delete params follow `RoomCreateParams`/`RoomDeleteParams` in pkg/ari/types.go — check the actual field names before writing the tests. Use `map[string]interface{}` calls to avoid import issues.

## Inputs

- ``tests/integration/session_test.go``
- ``tests/integration/concurrent_test.go``
- ``tests/integration/e2e_test.go``
- ``tests/integration/restart_test.go``
- ``pkg/ari/types.go``

## Expected Output

- ``tests/integration/session_test.go``
- ``tests/integration/concurrent_test.go``
- ``tests/integration/e2e_test.go``
- ``tests/integration/restart_test.go``

## Verification

go test ./tests/integration/... -count=1 -timeout 180s && grep -rn 'session/new\|session/prompt\|session/stop\|session/status\|session/remove' tests/integration/ | grep -v '_test.go:.*//\|_test.go:.*waitForSessionState'
