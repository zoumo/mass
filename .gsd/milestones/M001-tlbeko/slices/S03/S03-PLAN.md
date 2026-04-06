# S03: Hook Execution

**Goal:** HookExecutor executes setup/teardown hooks sequentially with abort-on-failure, output capture, and structured HookError diagnostics
**Demo:** After this: Setup hooks execute sequentially; failure aborts prepare and cleans up

## Tasks
- [x] **T01: Implemented HookExecutor with sequential hook execution and HookError structured error diagnostics** — Define HookExecutor type with ExecuteHooks method for sequential hook execution, and HookError structured error type following GitError pattern. ExecuteHooks loops over hooks array, runs each command with exec.CommandContext, sets working directory to workspaceDir, captures combined output, and returns HookError on failure with HookIndex for pinpointing the failed hook.

## Steps

1. Define HookError type in hook.go with fields: Phase (string for setup/teardown), HookIndex (int, 0-based), Command (string), Args ([]string), Description (string), ExitCode (int), Output ([]byte for stdout+stderr), Message (string), Err (error for underlying error)
2. Implement HookError.Error() method joining parts with ': ' separator following GitError.Error pattern from git.go (e.g., "workspace: hook setup failed: hookIndex=0: command=cat: exit=1: ...")
3. Implement HookError.Unwrap() returning Err field for errors.Is/errors.As compatibility
4. Define HookExecutor type as empty struct (like GitHandler pattern)
5. Implement ExecuteHooks(ctx context.Context, hooks []Hook, workspaceDir string, phase string) error method
6. In ExecuteHooks: check if workspaceDir exists with os.Stat, return fmt.Errorf if not (defensive check before execution)
7. Return nil immediately if len(hooks) == 0 or hooks is nil (empty array = no hooks to run = success)
8. Loop over hooks array with index i for HookIndex tracking:
   - Build exec.CommandContext(ctx, hook.Command, hook.Args...) 
   - Set cmd.Dir = workspaceDir (command runs in workspace directory)
   - Call cmd.CombinedOutput() to capture combined stdout+stderr
9. On CombinedOutput error: check ctx.Err() first for cancellation (return ctx.Err() if canceled), then construct HookError with Phase=phase, HookIndex=i, Command/Args/Description from hook, ExitCode from getExitCode(err), Output from combined output bytes, Message describing failure
10. Return nil after all hooks execute successfully

## Must-Haves

- [ ] HookError type defined with all 9 fields: Phase, HookIndex, Command, Args, Description, ExitCode, Output, Message, Err
- [ ] HookError.Error() method produces formatted string matching GitError pattern
- [ ] HookError.Unwrap() method returns Err for error chaining
- [ ] HookExecutor type defined with ExecuteHooks method signature: ExecuteHooks(ctx context.Context, hooks []Hook, workspaceDir string, phase string) error
- [ ] Sequential hook execution: loop iterates hooks in array order, aborts on first failure
- [ ] Output capture: cmd.CombinedOutput() called for each hook, stored in HookError.Output on failure
- [ ] Context cancellation: ctx.Err() checked before constructing HookError, returns ctx.Err() if canceled
- [ ] Defensive workspaceDir existence check: os.Stat before loop, returns error if directory missing
  - Estimate: 45m
  - Files: pkg/workspace/hook.go
  - Verify: go build ./pkg/workspace/... && test -f pkg/workspace/hook.go
- [x] **T02: Created pkg/workspace/hook_test.go with 17 test functions covering HookError structure, Error() method, Unwrap() chaining, and HookExecutor behavior with real command execution** — Write unit tests verifying HookError structure and edge cases (empty hooks, context cancellation), plus integration tests running real commands (echo, cat nonexistent file, sleep) to verify actual execution behavior, output capture, and sequential abort semantics.

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
  - Estimate: 1h
  - Files: pkg/workspace/hook_test.go
  - Verify: go test ./pkg/workspace/... -v -count=1
