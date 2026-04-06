---
estimated_steps: 24
estimated_files: 1
skills_used: []
---

# T01: Implement HookExecutor and HookError types

Define HookExecutor type with ExecuteHooks method for sequential hook execution, and HookError structured error type following GitError pattern. ExecuteHooks loops over hooks array, runs each command with exec.CommandContext, sets working directory to workspaceDir, captures combined output, and returns HookError on failure with HookIndex for pinpointing the failed hook.

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

## Inputs

- `pkg/workspace/spec.go`
- `pkg/workspace/git.go`

## Expected Output

- `pkg/workspace/hook.go`

## Verification

go build ./pkg/workspace/... && test -f pkg/workspace/hook.go

## Observability Impact

HookError adds structured failure diagnostics: Phase field distinguishes setup/teardown context, HookIndex identifies which hook failed, Output field captures command stdout+stderr for debugging. Future agents inspect HookError to diagnose hook failures without parsing log files.
