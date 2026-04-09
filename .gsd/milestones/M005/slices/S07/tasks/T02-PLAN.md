---
estimated_steps: 41
estimated_files: 1
skills_used: []
---

# T02: Rewrite TestAgentdRestartRecovery to use agent/* surface (R052 integration proof)

The existing `TestAgentdRestartRecovery` in `tests/integration/restart_test.go` proves session-level recovery but uses session/* methods that now return MethodNotFound. R052 requires proving that agent identity (room+name) survives daemon restart. This task rewrites the test to use agent/create, agent/prompt, agent/status, agent/stop, agent/delete.

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

## Inputs

- ``tests/integration/restart_test.go``
- ``tests/integration/session_test.go``
- ``pkg/ari/types.go``
- ``pkg/agentd/recovery.go``

## Expected Output

- ``tests/integration/restart_test.go``

## Verification

make build && go test ./tests/integration/... -count=1 -run TestAgentdRestartRecovery -v -timeout 120s

## Observability Impact

Test emits t.Log for each phase transition. Recovery reconciliation logs from T01 (agent state transitions) will appear in agentd stdout during the test's Phase 3–5.
