---
estimated_steps: 21
estimated_files: 1
skills_used: []
---

# T02: Write comprehensive unit and integration tests

Write unit tests verifying HookError structure and edge cases (empty hooks, context cancellation), plus integration tests running real commands (echo, cat nonexistent file, sleep) to verify actual execution behavior, output capture, and sequential abort semantics.

## Steps

1. Write TestHookErrorStructure unit test: create HookError with all fields populated, verify each field accessible and correctly typed (Phase string, HookIndex int, Output []byte, etc.)
2. Write TestHookErrorErrorMethod unit test: verify Error() output format matches expected pattern with phase, hookIndex, command, exit code, message joined by ': '
3. Write TestHookErrorUnwrap unit test: create HookError with underlying os.ErrClosed or similar, verify errors.Is(hookErr, os.ErrClosed) returns true, verify errors.As can extract underlying error
4. Write TestExecuteHooksEmptyHooks unit test: call ExecuteHooks with empty []Hook{} and nil hooks, verify both return nil immediately without error
5. Write TestExecuteHooksNonexistentWorkspace unit test: call ExecuteHooks with workspaceDir that doesn't exist, verify returns error containing "workspace directory" or similar message
6. Write integration tests using real commands in temp directory:
   - TestExecuteHooksSuccess: single hook {Command: "echo", Args: ["hello world"]}, verify returns nil, no error
   - TestExecuteHooksFailureWithOutput: single hook {Command: "cat", Args: ["nonexistent-file"]}, verify returns HookError with non-zero ExitCode and Output containing stderr message like "No such file or directory"
   - TestExecuteHooksSequentialAbort: two hooks, first {Command: "false"} (exits 1), second {Command: "touch", Args: ["marker-file"]}, verify first fails, returns HookError with HookIndex=0, verify marker-file NOT created (second hook not executed)
   - TestExecuteHooksContextCancel: hook {Command: "sleep", Args: ["5"]}, context canceled before ExecuteHooks call, verify returns context.Canceled immediately without 5s delay
7. Run go test ./pkg/workspace/... -v -count=1 and verify all tests pass with PASS status

## Must-Haves

- [ ] Unit tests: TestHookErrorStructure, TestHookErrorErrorMethod, TestHookErrorUnwrap, TestExecuteHooksEmptyHooks
- [ ] Integration tests: TestExecuteHooksSuccess, TestExecuteHooksFailureWithOutput, TestExecuteHooksSequentialAbort, TestExecuteHooksContextCancel
- [ ] All tests pass: go test ./pkg/workspace/... -v -count=1 returns 0 exit code and shows PASS for all test functions

## Negative Tests

**Malformed inputs:** Empty hooks array []Hook{} (valid edge case, returns nil). Hook with empty Command field (defensive error even though spec validation prevents this — executor should still check).

**Error paths:** Command not found in PATH (returns HookError with ExitCode 127 or 1, descriptive Message). Command exits with non-zero code (returns HookError with ExitCode and Output). Context canceled during execution (returns ctx.Err(), not HookError). Workspace directory doesn't exist (returns error before attempting hooks).

**Boundary conditions:** First hook fails → subsequent hooks not executed (sequential abort proven by marker file test). Last hook fails → earlier hooks completed successfully (reverse of first-fail case). Single hook in array (no sequential interaction to test).

## Inputs

- `pkg/workspace/hook.go`
- `pkg/workspace/spec.go`

## Expected Output

- `pkg/workspace/hook_test.go`

## Verification

go test ./pkg/workspace/... -v -count=1

## Observability Impact

Tests verify HookError structure exposes all diagnostic fields. TestExecuteHooksFailureWithOutput proves Output captures stderr for debugging. TestExecuteHooksSequentialAbort proves HookIndex correctly identifies which hook failed.
