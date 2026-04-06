# S05: Process Manager — BLOCKER

## Summary
This slice has a **blocking issue** that prevents verification from passing. A new task T04 has been added to fix this.

## What Was Built
1. **ShimClient** (T01): RPC client for agent-shim with 11 passing unit tests ✅
2. **ProcessManager.Start()** (T02): Workflow to start shim process ✅
3. **ProcessManager Stop/State/Connect** (T03): Lifecycle methods ✅
4. **Fix ACP NewSession hang** (T04): NEW - needs to be done ❌

## Blocker: ACP NewSession Hangs During Handshake

### Symptoms
- `TestProcessManagerStart` fails with "socket not ready after 5s"
- State shows `status="stopped"` with `exitCode=-1` (process killed)
- Socket file never created

### Root Cause Found
Added debug logging to `pkg/runtime/runtime.go` and traced the issue:

```
DEBUG: Create() starting
DEBUG: Resolved workDir=/private/var/folders/.../workspaces/test-workspace
DEBUG: Starting agent process: command=.../bin/mockagent
DEBUG: Agent process started with PID=41357
DEBUG: Starting ACP Initialize handshake
DEBUG: ACP Initialize succeeded     <-- Initialize works!
DEBUG: Starting ACP NewSession      <-- NewSession HANGS HERE
(no output - test times out)
```

### Analysis
1. **ACP Initialize succeeds** - connection between shim and mockagent works
2. **ACP NewSession blocks forever** - never returns, eventually times out
3. **Shim killed by timeout** - ProcessManager kills shim after 5s, writes "stopped" state

### Why Manual Tests Worked
When running shim manually from command line, everything works:
- Socket created successfully
- State shows `status="created"` with valid PID
- Mockagent responds to prompts

The issue is **specific to the test environment** - something about how `go test` runs the process differs from running manually.

## Instructions for T04 Agent

### What to Try
1. **Add logging to mockagent** - Check if NewSession request reaches mockagent:
   ```go
   func (a *mockAgent) NewSession(...) {
       log.Printf("DEBUG: mockagent received NewSession")
       ...
   }
   ```

2. **Add timeout to NewSession call** - See if it eventually returns or errors:
   ```go
   ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
   defer cancel()
   sessionResp, err := conn.NewSession(ctx, ...)
   ```

3. **Check ACP library behavior** - Initialize works, NewSession hangs. Look at acp-go-sdk for differences.

4. **Check context handling** - The test uses `context.WithTimeout(30s)` but shim uses `signal.NotifyContext`. Could be interaction.

### Files to Modify
- `pkg/runtime/runtime.go` - NewSession call is here
- `internal/testutil/mockagent/main.go` - Add logging here
- `pkg/agentd/process_test.go` - May need test adjustments

### Debug Command
```bash
# Run test with verbose output
go test ./pkg/agentd/... -run TestProcessManagerStart -v -count=1 2>&1

# Manual test that works
bin/agent-shim --bundle /tmp/test-bundle --id test --state-dir /tmp/shim --permissions approve-all
```

## Files Modified
- `pkg/agentd/shim_client.go` - ShimClient implementation
- `pkg/agentd/shim_client_test.go` - 11 passing tests
- `pkg/agentd/process.go` - ProcessManager implementation
- `pkg/agentd/process_test.go` - Integration tests (FAILING)

## Verification Status
- ✅ ShimClient tests: 11 pass
- ❌ ProcessManager tests: TestProcessManagerStart FAILS
- ❌ Slice verification: NOT passing

## Next Action
**Complete T04** to fix the ACP NewSession hang, then verify the slice.