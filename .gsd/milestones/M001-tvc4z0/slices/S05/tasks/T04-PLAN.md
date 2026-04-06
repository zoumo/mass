---
estimated_steps: 12
estimated_files: 2
skills_used: []
---

# T04: Fix ACP NewSession hang in test environment

### Goal
Fix the ACP NewSession hang that occurs when running ProcessManager tests.

### Steps
1. Add debug logging to mockagent to trace NewSession request reception
2. Add timeout to NewSession call in runtime.Create() to understand if it's blocking or failing silently
3. Compare manual shim execution vs test subprocess execution to find the difference
4. Fix the root cause (likely context handling, pipe buffering, or ACP library behavior in test environment)

### Context
- Debug logging in runtime.Create() shows Initialize succeeds but NewSession hangs
- Manual shim execution works (socket created, status=created)
- Test fails with "socket not ready after 5s"
- exitCode=-1 in state indicates process was killed after timeout

## Inputs

- `S05-BLOCKER.md analysis`

## Expected Output

- `pkg/runtime/runtime.go with potential fix`
- `mockagent debug logging`

## Verification

go test ./pkg/agentd/... -run TestProcessManagerStart -v passes
