# S08: Integration Tests

**Goal:** Integration tests: full pipeline, session lifecycle, agentd restart recovery
**Demo:** After this: Full pipeline works: agentd → agent-shim → mockagent end-to-end; restart recovery verified

## Tasks

### T01: End-to-End Pipeline Test
**One-liner:** End-to-end test proving agentd → agent-shim → mockagent full lifecycle works
**Description:** Create integration test that starts agentd daemon, creates workspace, creates session, prompts mockagent, verifies response, stops session, cleans up. Proves all components work together.

**Steps:**
1. Create test directory structure for integration tests
2. Write TestEndToEndPipeline function that:
   - Starts agentd daemon with test config (mockagent runtime class)
   - Calls workspace/prepare via ARI client
   - Calls session/new via ARI client
   - Calls session/prompt via ARI client
   - Verifies mockagent response (stopReason=end_turn)
   - Calls session/stop
   - Calls session/remove
   - Calls workspace/cleanup
   - Stops agentd daemon
3. Use existing pkg/ari/client.go for ARI calls
4. Use t.TempDir() for workspace root to avoid conflicts
5. Run test: go test -run TestEndToEndPipeline

**Must-Haves:**
- [ ] TestEndToEndPipeline passes with full lifecycle
- [ ] agentd starts and listens on socket
- [ ] workspace/prepare creates workspace
- [ ] session/new creates session with state=created
- [ ] session/prompt auto-starts shim, returns stopReason=end_turn
- [ ] session/stop stops shim gracefully
- [ ] session/remove deletes session
- [ ] workspace/cleanup removes workspace
- [ ] agentd shuts down cleanly

**Estimate:** 1h
**Files:** tests/integration/e2e_test.go (new)
**Verify:** go test ./tests/integration/... -run TestEndToEndPipeline -v

---

### T02: Session Lifecycle Tests
**One-liner:** Test all session state transitions and error handling
**Description:** Create tests covering session state machine: created → running → stopped, error cases like prompt on stopped session, remove on running session.

**Steps:**
1. Create TestSessionLifecycle function covering:
   - session/new → state=created
   - session/prompt → auto-start → state=running
   - session/status → returns shim state
   - session/stop → state=stopped
   - session/remove → session deleted
2. Create TestSessionPromptStoppedSession for error case
3. Create TestSessionRemoveRunningSession for error case (blocked)
4. Create TestSessionList showing multiple sessions
5. Run tests: go test ./tests/integration/... -run TestSession -v

**Must-Haves:**
- [ ] TestSessionLifecycle passes with all transitions
- [ ] TestSessionPromptStoppedSession returns InvalidParams error
- [ ] TestSessionRemoveRunningSession returns InvalidParams error (protected)
- [ ] TestSessionList shows correct session count

**Estimate:** 45m
**Files:** tests/integration/session_test.go (new)
**Verify:** go test ./tests/integration/... -run TestSession -v

---

### T03: agentd Restart Recovery Test
**One-liner:** Test agentd restart reconnects to existing shim sockets
**Description:** Create test that starts session, kills agentd (keeps shim running), restarts agentd, verifies reconnect to existing shim.

**Steps:**
1. Create TestAgentdRestartRecovery function:
   - Start agentd
   - Create workspace and session
   - Prompt session (shim running)
   - Kill agentd process (SIGTERM, keeps shim running)
   - Restart agentd with same socket/config
   - Call session/status → verify shim reconnected
   - Call session/prompt → verify shim responds
   - Stop session cleanly
2. Handle shim socket discovery at startup
3. Run test: go test ./tests/integration/... -run TestAgentdRestart -v

**Must-Haves:**
- [ ] TestAgentdRestartRecovery passes
- [ ] agentd reconnects to existing shim on startup
- [ ] session/status shows running after restart
- [ ] session/prompt works after restart

**Estimate:** 45m
**Files:** tests/integration/restart_test.go (new)
**Verify:** go test ./tests/integration/... -run TestAgentdRestart -v

---

### T04: Multiple Concurrent Sessions Test
**One-liner:** Test multiple sessions running concurrently
**Description:** Create test with multiple sessions (2-3) running simultaneously, verify each responds independently, no interference.

**Steps:**
1. Create TestMultipleConcurrentSessions function:
   - Start agentd
   - Prepare 2 workspaces
   - Create 2 sessions with different workspaces
   - Prompt both sessions concurrently (goroutines)
   - Verify each session responds correctly
   - Stop both sessions
   - Cleanup both workspaces
2. Use sync.WaitGroup for concurrent prompts
3. Run test: go test ./tests/integration/... -run TestMultipleConcurrent -v

**Must-Haves:**
- [ ] TestMultipleConcurrentSessions passes
- [ ] Both sessions created successfully
- [ ] Both prompts return correct responses
- [ ] No interference between sessions
- [ ] Both sessions stopped cleanly

**Estimate:** 30m
**Files:** tests/integration/concurrent_test.go (new)
**Verify:** go test ./tests/integration/... -run TestMultipleConcurrent -v

---

## Verification Summary

After all tasks complete:
```bash
go test ./tests/integration/... -v
go test ./... -v  # full project test suite
```

**Expected:** All integration tests pass, proving end-to-end pipeline works.