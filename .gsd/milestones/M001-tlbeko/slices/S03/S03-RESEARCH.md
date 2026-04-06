# S03 — Hook Execution

**Date:** 2026-04-03

## Summary

Slice S03 implements hook execution for workspace lifecycle management. Setup hooks run after source preparation (git clone or emptyDir creation), and teardown hooks run before workspace destruction. Hooks execute sequentially in array order with the workspace directory as working directory. Any hook failure aborts execution and returns a structured HookError with captured output (stdout/stderr combined).

This is straightforward Go command execution following the established GitHandler pattern from S01. No external libraries needed — use `exec.CommandContext` with `CombinedOutput()` for output capture.

## Recommendation

Implement HookExecutor following the GitHandler pattern:
1. HookExecutor type with ExecuteHooks method
2. HookError structured error type (similar to GitError from S01)
3. Sequential execution with abort-on-first-failure
4. Output capture via cmd.CombinedOutput()
5. Context cancellation support
6. Working directory set to workspaceDir

Keep it simple — no need for separate stdout/stderr capture, no retry logic, no parallel execution. The requirement specifies sequential execution with failure abort, which maps directly to a loop over hooks with error return on first failure.

## Implementation Landscape

### Key Files

- `pkg/workspace/spec.go` — Hook and Hooks types already defined (Command, Args, Description fields; Setup/Teardown arrays). Use these types directly.
- `pkg/workspace/handler.go` — SourceHandler interface pattern. HookExecutor doesn't need to implement this interface (it's for source handlers), but follows similar naming/style.
- `pkg/workspace/git.go` — GitHandler pattern to follow: exec.CommandContext, context cancellation check, structured error type, working directory handling, exit code extraction.
- `pkg/workspace/hook.go` — **NEW FILE** — HookExecutor implementation + HookError type.
- `pkg/workspace/hook_test.go` — **NEW FILE** — Unit tests (wrong source type rejection, empty command, HookError structure, Unwrap) + integration tests (execute real commands like echo, cat, failing command).

### Build Order

1. **Define HookError type** (hook.go) — Structured error with Phase, HookIndex, Command, Args, Description, ExitCode, Output, Message, Err. Implement Error() and Unwrap() methods following GitError pattern.

2. **Implement HookExecutor.ExecuteHooks** (hook.go) — Loop over hooks, exec.CommandContext for each, set cmd.Dir to workspaceDir, capture CombinedOutput, return HookError on failure, check ctx.Err() for cancellation.

3. **Write unit tests** (hook_test.go) — Test HookError structure, Error() method, Unwrap, empty command rejection, context cancellation.

4. **Write integration tests** (hook_test.go) — Execute real commands (echo "hello", cat nonexistent file for failure, sleep + context cancel for timeout).

5. **Run verification** — `go test ./pkg/workspace/... -v -count=1`

### Verification Approach

```bash
# All workspace tests pass (including new hook tests)
go test ./pkg/workspace/... -v -count=1

# Specific hook test cases:
# - HookError structure and Error() method
# - HookError Unwrap() for errors.Is/errors.As
# - Empty command rejection
# - Sequential execution (first fails → second not executed)
# - Context cancellation aborts execution
# - Integration: real echo command succeeds
# - Integration: failing command returns HookError with output
```

## Constraints

- **No external dependencies** — Use Go standard library exec package only.
- **Follow GitHandler pattern** — Maintain consistency with existing workspace handler code.
- **Output capture to combined stdout+stderr** — Use CombinedOutput() for simplicity. The requirement says "output capture" but doesn't mandate separate stdout/stderr.
- **Sequential execution only** — No parallel hook execution (explicitly stated in design doc).
- **Abort on first failure** — No retry, no continue-on-error.

## Common Pitfalls

- **Working directory must exist** — HookExecutor runs after source preparation, so workspaceDir should already exist. But consider adding a defensive check: if workspaceDir doesn't exist, return error before executing hooks.
- **Empty hooks array is valid** — If hooks.Setup or hooks.Teardown is empty/nil, ExecuteHooks should return nil immediately (no hooks to run = success).
- **Command not found** — Similar to GitHandler's "lookup" phase, HookError should capture when command executable isn't found. Use exec.LookPath before running, or capture the error from cmd.Run.
- **Teardown hook failure should still allow cleanup** — If teardown hooks fail, WorkspaceManager (S04) should still delete the workspace directory. HookExecutor just returns error; cleanup behavior is S04's responsibility.

## Files Created/Modified

- `pkg/workspace/hook.go` — NEW: HookExecutor type, ExecuteHooks method, HookError structured error type
- `pkg/workspace/hook_test.go` — NEW: Unit tests + integration tests for hook execution