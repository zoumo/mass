# S04: Real CLI integration verification — UAT

**Milestone:** M002
**Written:** 2026-04-07T17:02:06.180Z

## UAT: Real CLI Integration Verification (S04)

### Preconditions
- Go 1.22+ installed
- Repository at `/Users/jim/code/zoumo/open-agent-runtime`
- `bunx` and `gsd` in PATH (for gsd-pi test)
- `ANTHROPIC_API_KEY` set in environment (for both tests)
- Claude-code ACP adapter installed at expected path (for claude-code test)

### Test 1: Build all binaries
**Steps:**
1. Run `go build -o bin/agentd ./cmd/agentd`
2. Run `go build -o bin/agent-shim ./cmd/agent-shim`
3. Run `go build -o bin/mockagent ./internal/testutil/mockagent`

**Expected:** All three builds succeed with exit code 0.

### Test 2: Existing integration tests — no regressions
**Steps:**
1. Run `go test ./tests/integration -run 'TestEndToEnd|TestSession|TestConcurrent|TestAgentdRestart' -count=1 -v -timeout 180s`

**Expected:** All 6 tests pass (TestEndToEndPipeline, TestSessionLifecycle, TestSessionPromptStoppedSession, TestSessionRemoveRunningSession, TestSessionList, TestConcurrentPromptsSameSession). No failures or panics.

### Test 3: Unit tests — no regressions
**Steps:**
1. Run `go test ./pkg/... -count=1 -timeout 120s`

**Expected:** All 8 packages pass (agentd, ari, events, meta, rpc, runtime, spec, workspace).

### Test 4: Real CLI tests skip gracefully without API key
**Steps:**
1. Unset `ANTHROPIC_API_KEY` (or ensure it is not set)
2. Run `go test ./tests/integration -run TestRealCLI -count=1 -v -timeout 180s`

**Expected:**
- TestRealCLI_GsdPi: SKIP with message "skipping: ANTHROPIC_API_KEY not set (gsd-pi needs an LLM key to process prompts)"
- TestRealCLI_ClaudeCode: SKIP with message "skipping: ANTHROPIC_API_KEY not set"
- Overall test result: PASS (skips are not failures)

### Test 5: Real CLI gsd-pi full lifecycle (requires API key)
**Preconditions:** ANTHROPIC_API_KEY set, `bunx` and `gsd` in PATH
**Steps:**
1. Run `go test ./tests/integration -run TestRealCLI_GsdPi -count=1 -v -timeout 180s`

**Expected:**
- workspace/prepare succeeds, workspace ID logged
- session/new creates session with runtimeClass="gsd-pi"
- session/prompt with "respond with only the word hello" returns stopReason="end_turn"
- session/status shows state="running" and shimState is non-nil
- session/stop transitions session to stopped
- session/remove deletes session
- workspace/cleanup succeeds
- Test PASS

### Test 6: Real CLI claude-code full lifecycle (requires API key)
**Preconditions:** ANTHROPIC_API_KEY set, claude-code ACP adapter JS file exists
**Steps:**
1. Run `go test ./tests/integration -run TestRealCLI_ClaudeCode -count=1 -v -timeout 180s`

**Expected:** Same lifecycle as Test 5 but using claude-code runtime class.

### Test 7: Timeout values in source
**Steps:**
1. Run `rg 'startCtx.*30\*time\.Second' pkg/ari/server.go` — auto-start timeout
2. Run `rg 'promptCtx.*120\*time\.Second' pkg/ari/server.go` — prompt timeout
3. Run `rg 'timeout := 20 \* time\.Second' pkg/agentd/process.go` — waitForSocket timeout

**Expected:** Each grep returns exactly one matching line with the expected timeout value.

### Edge Cases

**EC1: Missing `bunx` binary**
- Remove `bunx` from PATH, run TestRealCLI_GsdPi
- Expected: SKIP with message about bunx not found

**EC2: Missing claude-code adapter**
- Rename or remove the adapter JS file, run TestRealCLI_ClaudeCode
- Expected: SKIP with message about adapter file not found

**EC3: Short mode**
- Run `go test ./tests/integration -run TestRealCLI -short -count=1 -v`
- Expected: Both tests SKIP with short-mode message
