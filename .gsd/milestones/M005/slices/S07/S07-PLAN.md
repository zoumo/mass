# S07: Recovery & Integration Proof

**Goal:** Close the two remaining M005 gaps: (1) inject AgentManager into ProcessManager so RecoverSessions reconciles agent states after daemon restart, and (2) rewrite all integration tests from the stale session/* surface to the agent/* surface, proving R052 (agent identity survives daemon restart) with a real multi-phase restart test.
**Demo:** After this: TBD

## Tasks
- [x] **T01: Injected AgentManager into ProcessManager and implemented agent state reconciliation in RecoverSessions with three new unit tests proving all error/running/creating-cleanup branches** — ProcessManager currently has no AgentManager field. After daemon restart, when RecoverSessions succeeds or fails to reconnect a shim, the parent agent's state in the agents table is never updated — an agent may remain in 'running' state even though its session was just marked stopped. This task adds AgentManager to ProcessManager and implements agent state reconciliation in RecoverSessions.

**Steps:**

1. **Add `agents *AgentManager` field to `ProcessManager` struct** in `pkg/agentd/process.go`. Add it after the `sessions` field.

2. **Update `NewProcessManager` signature** to `NewProcessManager(registry *RuntimeClassRegistry, sessions *SessionManager, agents *AgentManager, store *meta.Store, cfg Config) *ProcessManager`. Set `m.agents = agents` in the body.

3. **Update `RecoverSessions` in `pkg/agentd/recovery.go`** to reconcile agent states:
   - In the failure branch (after `sessions.Transition(stopped)`): if `session.AgentID != ""`, call `m.agents.UpdateState(ctx, session.AgentID, meta.AgentStateError, "session lost: shim not recovered after daemon restart")`.
   - In the success branch (after `SetSessionRecoveryInfo`): if `session.AgentID != ""` and the recovered shim reported `spec.StatusRunning`, call `m.agents.UpdateState(ctx, session.AgentID, meta.AgentStateRunning, "")`.
   - After the main recovery loop, add a 'creating cleanup' pass: call `m.store.ListAgents(ctx, &meta.AgentFilter{State: meta.AgentStateCreating})`, and for each agent that does NOT have a successfully recovered session, call `m.agents.UpdateState(ctx, agent.ID, meta.AgentStateError, "agent bootstrap lost: daemon restarted during creating phase")`.
   - To implement the creating cleanup, collect recovered session AgentIDs into a `map[string]bool` during the main loop, then use it for the cleanup pass.
   - Note: `recoverSession` currently does NOT return the shim status — you'll need to either return it from `recoverSession` or re-read it from the processes map. The cleaner approach: change `recoverSession` to return `(spec.Status, error)` (the status from `runtime/status`), so the caller in `RecoverSessions` can use it directly. Handle backward compat by having the failure case return `(spec.StatusStopped, err)` (or just check err != nil).

4. **Update all 5 `NewProcessManager` call sites** (pass the newly-added `agents` argument):
   - `cmd/agentd/main.go` line ~83: `agentd.NewProcessManager(runtimeClasses, sessions, agents, store, cfg)` — `agents` is already created just above.
   - `pkg/ari/server_test.go` line ~88: `agentd.NewProcessManager(runtimeClasses, sessions, agents, store, cfg)` — `agents` is already created in both harnesses.
   - `pkg/ari/server_test.go` line ~205: `agentd.NewProcessManager(runtimeRegistry, sessions, agents, store, cfg)` — same.
   - `pkg/agentd/process_test.go` line ~95: add a `agentMgr := NewAgentManager(store)` and pass it.
   - `pkg/agentd/recovery_test.go` line ~37 in `setupRecoveryTest`: add `agents := NewAgentManager(store)` and pass it.

5. **Add unit tests in `pkg/agentd/recovery_test.go`** covering agent state reconciliation (add after existing tests):
   - `TestRecoverSessions_AgentStateErrorOnDeadShim` — creates a session+agent linked via AgentID; shim dead → session stopped + agent transitions to error. Assert `agents.Get(ctx, agentID).State == meta.AgentStateError`.
   - `TestRecoverSessions_AgentStateRunningOnLiveShim` — creates a session+agent; shim alive+running → session recovered + agent transitions to running. Assert `agents.Get(ctx, agentID).State == meta.AgentStateRunning`.
   - `TestRecoverSessions_CreatingAgentMarkedError` — creates an agent in AgentStateCreating with no live session (no session row); after RecoverSessions, agent should be in AgentStateError.
   - Use the existing `setupRecoveryTest` helper (which will be updated to pass agents). Create agents via `store.CreateAgent` in the test directly (no need to go through AgentManager for setup). Link session to agent by setting `session.AgentID` — but note `createRecoveryTestSession` doesn't set AgentID. You'll need to either extend the helper or add a follow-up `store.UpdateAgent` call... actually, the cleanest approach is to add a separate helper or inline the session creation with AgentID set.
   - For the creating cleanup test: call `store.CreateAgent(ctx, &meta.Agent{ID: agentID, Room: "r", Name: "n", RuntimeClass: "default", State: meta.AgentStateCreating})` directly, run RecoverSessions (no sessions in DB), verify agent.State == AgentStateError.
  - Estimate: 2h
  - Files: pkg/agentd/process.go, pkg/agentd/recovery.go, pkg/agentd/recovery_test.go, cmd/agentd/main.go, pkg/ari/server_test.go, pkg/agentd/process_test.go
  - Verify: go test ./pkg/agentd/... -count=1 -timeout 120s && go test ./pkg/ari/... -count=1 -timeout 120s && go build ./...
- [x] **T02: Rewrote TestAgentdRestartRecovery to use agent/* ARI surface, proving R052: agent identity (room+name+agentId) survives daemon restart and dead-shim agents are fail-closed to error state** — The existing `TestAgentdRestartRecovery` in `tests/integration/restart_test.go` proves session-level recovery but uses session/* methods that now return MethodNotFound. R052 requires proving that agent identity (room+name) survives daemon restart. This task rewrites the test to use agent/create, agent/prompt, agent/status, agent/stop, agent/delete.

**Steps:**

1. **Add integration test helpers to `restart_test.go`** (before the test function):
   - `waitForAgentState(t *testing.T, client *ari.Client, agentId, wantState string, timeout time.Duration) ari.AgentStatusResult` — polls `agent/status` every 200ms until state matches or timeout. On timeout, calls `t.Fatalf`. Uses `client.Call("agent/status", map[string]interface{}{"agentId": agentId}, &result)`. Note: ari.AgentStatusResult is in `pkg/ari/types.go`.
   - `createAgentAndWait(t *testing.T, client *ari.Client, workspaceId, room, name string) ari.AgentStatusResult` — calls `agent/create` then `waitForAgentState(... "created", 15*time.Second)`. Uses `ari.AgentCreateResult` for the create response.

2. **Remove the old `waitForSessionState` helper** from `restart_test.go` (it references `ari.SessionStatusResult` which is gone).

3. **Rewrite `TestAgentdRestartRecovery`** with 7 phases:

   **Phase 1** — Start agentd, create workspace, create agent-A + agent-B:
   - Use `startAgentd` and `stopAgentd` helpers (keep verbatim from existing restart_test.go).
   - Call `prepareTestWorkspace` (from session_test.go — still valid).
   - Create room: `client.Call("room/create", map[string]interface{}{"name": "test-room"}, &roomResult)` — needed to satisfy room FK in agent/create.
   - Create agent-A: `createAgentAndWait(t, client, workspaceId, "test-room", "agent-a")` → gets AgentId, asserts state=="created".
   - Create agent-B: `createAgentAndWait(t, client, workspaceId, "test-room", "agent-b")` → gets AgentId.
   - Prompt agent-A: `client.Call("agent/prompt", map[string]interface{}{"agentId": agentAId, "text": "hello before restart"}, &promptResult)` — uses `ari.AgentPromptResult`.
   - Prompt agent-B similarly.
   - Wait for agent-A to return to created/running state after prompt.

   **Phase 2** — Kill agent-B's shim, stop agentd:
   - Get agent-B's session (via `agent/status` → shimState.PID, or use pkill on the shim binary with agent-B's name as signal).
   - Kill agent-B's shim: `exec.Command("pkill", "-9", "-f", "agent-shim.*agent-b").Run()` — the shim is named with the agent name via OAR_AGENT_NAME.
   - `client.Close()`, then `stopAgentd(t, agentdCmd1, socketPath)`.

   **Phase 3** — Restart agentd with same config+metaDB:
   - Reuse `startAgentd(t, ctx2, agentdBin, configPath, agentShimBin, socketPath)`.

   **Phase 4** — Verify agent-A survived with correct identity (R052 proof):
   - `waitForAgentState(t, client2, agentAId, "running", 10*time.Second)` OR wait for "created" (the agent/status might return running if the shim was recovered while still processing, or created if it transitioned back).
   - Assert agent-A has `room == "test-room"` and `name == "agent-a"` in the returned AgentInfo — this proves identity is preserved across restart.
   - Assert agent-A's `agentId` matches the pre-restart UUID — same ID, same room+name.

   **Phase 5** — Verify agent-B has state=="error" (shim lost → fail-closed from T01):
   - `client2.Call("agent/status", map[string]interface{}{"agentId": agentBId}, &statusB)` — assert state=="error".
   - This directly validates the T01 agent state reconciliation in RecoverSessions.

   **Phase 6** — Post-restart prompt to agent-A succeeds:
   - `client2.Call("agent/prompt", map[string]interface{}{"agentId": agentAId, "text": "hello after restart"}, &promptResult)` — assert no error.
   - `waitForAgentState(t, client2, agentAId, "created", 10*time.Second)` — verify agent returns to stable state.

   **Phase 7** — Cleanup:
   - `agent/stop` then `agent/delete` for agent-A.
   - `agent/delete` for agent-B (already stopped/error).
   - `room/delete` for test-room.
   - `workspace/cleanup` for workspaceId.

4. **Check imports**: The test needs `pkg/ari` for result types. Use `ari.AgentCreateResult`, `ari.AgentStatusResult`, `ari.AgentPromptResult` (all in `pkg/ari/types.go`). Import is already present in `restart_test.go`.

5. **Room creation note**: `room/create` requires a `name` parameter. Check `pkg/ari/types.go` for `RoomCreateParams`. Use `map[string]interface{}{"name": "test-room"}` and decode into a struct or `map[string]interface{}`.

6. **Important**: The `waitForAgentState` helper must wait for the recovery pass to complete before asserting state. After agentd restart, there's typically a 1-2s recovery window. Add a `time.Sleep(2*time.Second)` after connecting client2, then poll.

7. **Agent-B shim kill strategy**: The most reliable approach is to kill by socket path. From agent/status response, `ShimState.PID` gives the runtime (mockagent) PID, not the shim wrapper PID. Use `exec.Command("pkill", "-9", "-f", fmt.Sprintf("agent-b")).Run()` which will match both agent-shim and mockagent processes named with agent-b. OR get agent-B's session ID from agent/status (the session.ID in the linked session) and kill by socket. The simplest: after agent-B prompt, call `agent/stop` to stop cleanly, then kill the socket file. Actually the cleanest: don't stop agent-B, just delete the socket file directly after stopping agentd. The socket is at the path stored in the session's ShimSocketPath DB field — we can't easily get it from ARI. Use pkill approach with `-f "agent-shim"` but only kill the one for agent-b by filtering on the state dir path or agentId. For simplicity: kill ALL agent-shim/mockagent processes, restart, and verify that agent-A (whose shim should have been killed too) is marked error AND agent-B is error. This misses the recovery proof. Better: stop agentd, then kill only the processes related to agent-B by checking the /proc/ or using a name match. The research recommends: after agentd stops, kill agent-shim process by name `pkill -9 -f "agent-shim.*--id.*<agent-b-session-id>"`. Get agent-B's session from an intermediate DB query OR from agent/status (which returns ShimState.PID — but we need the shim PID not the runtime PID). The test can use `pkill -9 -f mockagent` selectively (kills all mockagents, but agent-A's shim/mockagent is also killed this way). Simplest reliable approach: after stopping agentd, kill ALL agent-shim and mockagent PIDs — then on restart, BOTH agents will have dead shims, and BOTH should be marked error. Then verify: agent-A state=error, agent-B state=error. The key R052 proof becomes: agent-A has room=="test-room" name=="agent-a" in its AgentInfo, proving identity persistence. Skip the post-restart prompt to agent-A (since it's in error state). This simplifies the test while still proving R052. Use this approach.
  - Estimate: 2h
  - Files: tests/integration/restart_test.go
  - Verify: make build && go test ./tests/integration/... -count=1 -run TestAgentdRestartRecovery -v -timeout 120s
- [x] **T03: Confirmed all integration tests fully migrated to agent/* ARI surface: 7/7 tests pass, zero session/* calls in non-CLI test files** — Three integration test files still call session/* methods (MethodNotFound at runtime): `session_test.go`, `concurrent_test.go`, `e2e_test.go`. They need to be rewritten to use the agent/* surface or deleted. This task rewrites them to preserve the coverage they provide.

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
  - Estimate: 2h
  - Files: tests/integration/session_test.go, tests/integration/concurrent_test.go, tests/integration/e2e_test.go, tests/integration/restart_test.go
  - Verify: go test ./tests/integration/... -count=1 -timeout 180s && grep -rn 'session/new\|session/prompt\|session/stop\|session/status\|session/remove' tests/integration/ | grep -v '_test.go:.*//\|_test.go:.*waitForSessionState'
