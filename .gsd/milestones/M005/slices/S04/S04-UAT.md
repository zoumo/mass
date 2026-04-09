# S04: Agent Lifecycle — Async Create, Stop/Delete Separation, Restart — UAT

**Milestone:** M005
**Written:** 2026-04-08T20:05:11.604Z

## S04 UAT: Agent Lifecycle — Async Create, Stop/Delete Separation, Restart

### Preconditions
- agentd built from source with `go build ./...` (exit 0)
- mockagent runtime class configured (used by all integration tests)
- `agent-shim` binary present at `bin/agent-shim`
- Test harness: `newSessionTestHarness(t)` (used by all integration tests below)

---

### TC-01: agent/create returns creating state immediately

**Test:** `TestARIAgentCreateAsync` (pkg/ari/server_test.go)

**Steps:**
1. Call `agent/create` with room=async-create-room, name=async-agent, runtimeClass=mockagent
2. Assert result.State == "creating" (immediate return)
3. Poll `agent/status` every 200ms, up to 30s
4. Assert final state == "created"
5. Assert shimState is present in status response (session is running)
6. Call `agent/stop`, assert state transitions to stopped
7. Call `agent/delete`, assert agent is removed

**Expected:**
- Step 2: state is "creating" — goroutine not yet complete
- Step 4: state becomes "created" within 30s
- Step 6-7: clean stop and delete

**Pass criteria:** All assertions pass. `go test ./pkg/ari/... -run TestARIAgentCreateAsync -v -timeout 60s` exits 0.

---

### TC-02: agent/create with invalid runtimeClass transitions to error

**Test:** `TestARIAgentCreateAsyncErrorState` (pkg/ari/server_test.go)

**Steps:**
1. Call `agent/create` with runtimeClass=nonexistent-class
2. Assert result.State == "creating"
3. Poll `agent/status` every 200ms, up to 30s
4. Assert final state == "error"
5. Assert agent.ErrorMessage is non-empty (contains "nonexistent-class")

**Expected:**
- Goroutine fails at processes.Start with "runtime class not found"
- Agent transitions to error state with ErrorMessage populated
- No session row remains (cleanup on failure)

**Pass criteria:** `go test ./pkg/ari/... -run TestARIAgentCreateAsyncErrorState -v -timeout 60s` exits 0.

---

### TC-03: agent/prompt during creating state returns CodeInvalidParams

**Precondition:** Agent exists in creating state (between create reply and bootstrap completion).

**Steps:**
1. Create agent — result.State == "creating"
2. Immediately call `agent/prompt` before state transitions to created
3. Assert error code == CodeInvalidParams
4. Assert error message contains "still being provisioned" or "poll agent/status"

**Expected:** Prompt is rejected with a clear actionable error message. No partial delivery to unready agent.

**Pass criteria:** Guard fires correctly; no panic or data race. Covered indirectly by TestARIAgentCreateAsync (prompt only sent after polling to created).

---

### TC-04: agent/delete blocked when agent is in creating state

**Precondition:** Agent exists in creating state.

**Steps:**
1. Create agent — result.State == "creating"
2. Immediately call `agent/delete`
3. Assert error — delete must be rejected (agent is not stopped)

**Expected:** ErrDeleteNotStopped fires because creating != stopped.

**Pass criteria:** Delete returns an error. Covered by TestARIAgentDeleteRequiresStopped (updated in T01 to handle creating state).

---

### TC-05: agent/restart performs full async restart lifecycle

**Test:** `TestARIAgentRestartAsync` (pkg/ari/server_test.go)

**Steps:**
1. Create agent → poll until state == "created"
2. Send prompt to verify agent is functional → assert response received
3. Call `agent/stop` → assert state == "stopped"
4. Call `agent/restart` → assert result.State == "creating" (immediate return)
5. Poll `agent/status` every 200ms, up to 30s
6. Assert final state == "created"
7. Assert new session has different sessionId than original
8. Send second prompt → assert response received (new session functional)
9. Call `agent/stop` + `agent/delete` for cleanup

**Expected:**
- Restart is non-blocking: returns creating immediately
- Old session is deleted inside goroutine
- New session with fresh UUID is started
- Agent returns to created state with functional session

**Pass criteria:** `go test ./pkg/ari/... -run TestARIAgentRestartAsync -v -timeout 90s` exits 0.

---

### TC-06: agent/restart rejected when agent is in created (running) state

**Precondition:** Agent exists in created state (not stopped).

**Steps:**
1. Create agent → poll until state == "created"
2. Call `agent/restart` (without stopping first)
3. Assert error code == CodeInvalidParams
4. Assert error message indicates agent must be stopped first

**Expected:** Restart guard fires; only stopped and error agents can be restarted.

**Pass criteria:** Covered by TestARIAgentRestartAsync precondition assertions.

---

### TC-07: OAR_AGENT_ID and OAR_AGENT_NAME injected in MCP server env

**Test:** Verified via ACP session/new request log output in TestARIAgentCreateAsync and TestARIAgentRestartAsync.

**Steps:**
1. Run TestARIAgentCreateAsync with -v flag
2. Inspect the `runtime: acp session/new request` log output
3. Assert env array contains `{"name": "OAR_AGENT_ID", "value": "<agentId>"}`
4. Assert env array contains `{"name": "OAR_AGENT_NAME", "value": "async-agent"}`
5. Assert env array still contains `OAR_SESSION_ID` (deprecated alias)
6. Assert env array still contains `OAR_ROOM_AGENT` (deprecated alias)

**Expected:** Both new canonical names and deprecated aliases present. S06 will remove deprecated aliases.

**Pass criteria:** Log output from TestARIAgentCreateAsync shows all 4 env entries. Confirmed in test run output.

---

### TC-08: agentdctl restart subcommand registered with correct usage

**Steps:**
1. Build agentdctl: `go build -o /tmp/agentdctl ./cmd/agentdctl`
2. Run: `/tmp/agentdctl agent restart --help`
3. Assert output contains "Restart a stopped (or errored) agent"
4. Assert output contains usage: `agentdctl agent restart <agent-id>`
5. Assert Long description advises polling `agentdctl agent status <agent-id>`

**Expected:** Command is registered, Args validation is ExactArgs(1), help text contains async polling guidance.

**Pass criteria:** `agentdctl agent restart --help` exits 0 with correct output. Verified in slice verification run.

---

### TC-09: Full suite regression — no regressions from async refactor

**Steps:**
1. Run `go test ./pkg/ari/... -count=1 -timeout 120s`
2. Assert all tests pass

**Expected:** No regressions. Tests that previously asserted state=="created" after create now use pollAgentUntilReady to handle the async transition.

**Pass criteria:** `go test ./pkg/ari/... -count=1` exits 0. Verified in slice closure run (13.049s).

