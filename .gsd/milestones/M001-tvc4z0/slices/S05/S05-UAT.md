# S05: Process Manager — UAT

**Milestone:** M001-tvc4z0
**Written:** 2026-04-06T14:55:06.506Z

# S05: Process Manager - User Acceptance Test

## Prerequisites
- Built binaries: `bin/agent-shim`, `bin/mockagent`
- Go test environment configured
- Working directory: project root

## Test Cases

### TC1: ProcessManager Start - Full Lifecycle
**Purpose:** Verify ProcessManager starts shim process, connects socket, and manages full lifecycle.

**Steps:**
1. Run: `go test ./pkg/agentd/... -run TestProcessManagerStart -v -timeout 30s`

**Expected Results:**
- Test passes
- Logs show: "session created" → "session started" → "session state transition ... running"
- ShimClient connects successfully
- GetState RPC returns shim state with Status=created, PID>0
- Prompt RPC returns stopReason=end_turn
- Events received (2 TextEvents from mockagent)
- Graceful shutdown completes
- Session transitions to stopped
- Bundle directory cleaned up

**Evidence:** Test output shows complete lifecycle in ~5 seconds.

### TC2: ShimClient Unit Tests
**Purpose:** Verify ShimClient RPC methods work correctly.

**Steps:**
1. Run: `go test ./pkg/agentd/... -run ShimClient -v`

**Expected Results:**
- 11 tests pass:
  - TestShimClientDial, TestShimClientDialFail
  - TestShimClientPrompt, TestShimClientCancel
  - TestShimClientSubscribe
  - TestShimClientGetState, TestShimClientShutdown, TestShimClientClose
  - TestShimClientDisconnectNotify
  - TestShimClientMultipleMethods, TestShimClientConcurrentCalls

### TC3: Event Parsing
**Purpose:** Verify all event types parse correctly.

**Steps:**
1. Run: `go test ./pkg/agentd/... -run TestParseEvent -v`

**Expected Results:**
- 7 sub-tests pass for event types: text, thinking, user_message, tool_call, tool_result, file_write, error

### TC4: All Agentd Tests
**Purpose:** Verify no regressions.

**Steps:**
1. Run: `go test ./pkg/agentd/... -v`

**Expected Results:**
- All tests pass (SessionManager, ShimClient, ProcessManager)

## Edge Cases Verified

1. **Socket readiness:** Uses net.Dial (not os.OpenFile) to verify Unix socket - handles macOS socket behavior correctly.

2. **Shutdown RPC:** Shim main cancels context when RPC server exits, enabling graceful shutdown without SIGTERM.

3. **Bundle cleanup:** Done channel closed after all cleanup, preventing race conditions.

4. **ACP handshake:** cmd.Wait() called after handshake to avoid pipe read interference.

## Manual Verification (Optional)

### Manual Shim Test
```bash
# Create test bundle
mkdir -p /tmp/test-bundle
cat > /tmp/test-bundle/config.json << 'EOF'
{
  "oarVersion": "0.1.0",
  "metadata": {"name": "test-session"},
  "agentRoot": {"path": "workspace"},
  "acpAgent": {
    "process": {
      "command": "/Users/jim/code/zoumo/open-agent-runtime/bin/mockagent",
      "args": []
    }
  }
}
EOF
mkdir -p /tmp/test-bundle/workspace

# Start shim
./bin/agent-shim --bundle /tmp/test-bundle --id test-session --state-dir /tmp/test-shim &

# Verify socket appears
ls -la /tmp/test-shim/test-session/agent-shim.sock

# Connect with ShimClient (would need Go code or netcat)
# ...

# Cleanup
kill %1
```

## Test Results Summary

| Test | Status | Duration |
|------|--------|----------|
| TestProcessManagerStart | ✅ PASS | 5.08s |
| ShimClient tests (11) | ✅ PASS | <1s |
| SessionManager tests | ✅ PASS | <1s |

**Overall:** All tests pass. Slice goal achieved.
