# S07: Recovery & Integration Proof — UAT

**Milestone:** M005
**Written:** 2026-04-08T22:10:43.224Z

## UAT: S07 Recovery & Integration Proof

### Preconditions
- Build passes: `make build` exits 0
- `go test ./pkg/agentd/...` exits 0 (includes new RecoverSessions reconciliation tests)
- `go test ./pkg/ari/...` exits 0 (includes server test harnesses with updated NewProcessManager)
- `go test ./tests/integration/...` exits 0 (7 pass, 2 skip for missing ANTHROPIC_API_KEY)
- Zero session/* calls in non-CLI integration test files

---

### Test Case 1: Agent error state on dead shim after restart (unit)
**Location:** `TestRecoverSessions_AgentStateErrorOnDeadShim` in `pkg/agentd/recovery_test.go`
**Steps:**
1. Create a session with an AgentID link and a shim that cannot be reconnected (simulate dead shim)
2. Run `RecoverSessions`
3. Assert the session transitions to `stopped`
4. Assert `agents.Get(ctx, agentID).State == meta.AgentStateError`
**Expected:** Agent is marked error, not stuck in running or creating. ✅ Passes.

---

### Test Case 2: Agent running state on live recovered shim (unit)
**Location:** `TestRecoverSessions_AgentStateRunningOnLiveShim` in `pkg/agentd/recovery_test.go`
**Steps:**
1. Create a session with an AgentID link and a shim that successfully reconnects reporting StatusRunning
2. Run `RecoverSessions`
3. Assert the session is marked recovered
4. Assert `agents.Get(ctx, agentID).State == meta.AgentStateRunning`
**Expected:** Agent reflects the recovered shim's running status. ✅ Passes.

---

### Test Case 3: Creating-phase agent marked error (unit)
**Location:** `TestRecoverSessions_CreatingAgentMarkedError` in `pkg/agentd/recovery_test.go`
**Steps:**
1. Create an agent with `State = meta.AgentStateCreating` directly in the store (no session row)
2. Run `RecoverSessions` (no sessions in DB — tests that the creating-cleanup pass runs even with len(candidates)==0)
3. Assert `agents.Get(ctx, agentID).State == meta.AgentStateError`
**Expected:** Agent bootstrapping at restart time is fail-closed to error. ✅ Passes.

---

### Test Case 4: R052 — Agent identity survives daemon restart (integration)
**Location:** `TestAgentdRestartRecovery` in `tests/integration/restart_test.go`
**Steps:**
1. Start agentd, create workspace, room, agent-A (room="test-room", name="agent-a"), agent-B
2. Prompt both agents, record pre-restart agentIds
3. Stop agentd cleanly, kill all agent-shim and mockagent processes
4. Restart agentd (same config + same meta DB)
5. Wait for recovery pass to complete (2s sleep + polling)
6. Call `agent/status` for agent-A — assert `state="error"` AND `room="test-room"` AND `name="agent-a"` AND `agentId == pre-restart agentId`
7. Call `agent/status` for agent-B — assert `state="error"`
8. Call `agent/list` — assert both agents appear with correct room assignment
9. Call `agent/stop` then `agent/delete` for both agents
**Expected:** agentId, room, and name are identical to pre-restart values even in error state (R052 proven). ✅ Passes in 4.47s.

---

### Test Case 5: Zero session/* calls in non-CLI integration tests
**Location:** All files under `tests/integration/` except `real_cli_test.go`
**Steps:**
1. Run: `grep -rn 'session/new\|session/prompt\|session/stop\|session/status\|session/remove' tests/integration/ | grep -v real_cli_test.go`
**Expected:** Empty output (exit 1 from grep means no matches found). ✅ Clean.

---

### Test Case 6: Full agent lifecycle (integration)
**Location:** `TestAgentLifecycle` in `tests/integration/session_test.go`
**Steps:**
1. Start agentd, create workspace and room
2. Call `agent/create` (room, name params)
3. Poll `agent/status` until state=="created"
4. Call `agent/prompt` with text prompt
5. Poll until state=="running" (end_turn)
6. Call `agent/stop`, poll until state=="stopped"
7. Call `agent/delete`
**Expected:** Clean lifecycle, no errors at any step. ✅ Passes.

---

### Test Case 7: Concurrent agent prompts (integration)
**Location:** `TestMultipleConcurrentSessions` in `tests/integration/concurrent_test.go`
**Steps:**
1. Create 3 agents in the same room
2. Send prompts to all 3 concurrently (using mutex-serialized client writes)
3. Assert all 3 return no error
4. Stop and delete all 3 agents
**Expected:** Concurrent prompts complete without race conditions. ✅ Passes.

---

### Test Case 8: End-to-end pipeline (integration)
**Location:** `TestEndToEndPipeline` in `tests/integration/e2e_test.go`
**Steps:**
1. workspace/prepare → room/create → agent/create → waitForAgentState(created)
2. agent/prompt → waitForAgentState(running)
3. agent/stop → waitForAgentState(stopped)
4. workspace/cleanup
**Expected:** Full 9-step pipeline completes without error. ✅ Passes.

---

### Edge Cases Verified

| Edge Case | How Tested | Result |
|-----------|-----------|--------|
| agent/delete on error-state agent fails without prior agent/stop | Recovery test cleanup — must call stop first | ✅ Handled by stopAndDeleteAgent helper |
| agent/delete while agent is running | TestARIRoomTeardownGuards (pkg/ari) | ✅ Blocked with WARN log |
| creating-cleanup when DB has zero sessions | TestRecoverSessions_CreatingAgentMarkedError | ✅ Creating-cleanup still runs |
| Duplicate agent name in same room | TestARIAgentCreateDuplicateName (pkg/ari) | ✅ Returns error |
| Agent/create with missing room | TestARIAgentCreateMissingRoom (pkg/ari) | ✅ Returns error |

