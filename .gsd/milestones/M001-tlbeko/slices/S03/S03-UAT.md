# S03: Hook Execution — UAT

**Milestone:** M001-tlbeko
**Written:** 2026-04-02T18:41:51.851Z

# S03 UAT: Hook Execution

## Preconditions
1. Go toolchain installed and working
2. Repository cloned at `/Users/jim/code/zoumo/open-agent-runtime`
3. No pending changes to `pkg/workspace/hook.go` or `pkg/workspace/hook_test.go`

## Test Cases

### TC01: HookExecutor handles empty hooks array
**Purpose:** Verify empty hooks array (nil or len==0) returns nil immediately (no hooks = success)

**Steps:**
1. Run: `go test ./pkg/workspace/... -v -run TestExecuteHooksEmptyHooks -count=1`
2. Observe: Test passes with subtests for nil hooks, empty array, empty with capacity

**Expected:** All 3 subtests PASS, ExecuteHooks returns nil for empty hooks

---

### TC02: HookExecutor returns error for nonexistent workspace directory
**Purpose:** Verify defensive check prevents execution in nonexistent directory

**Steps:**
1. Run: `go test ./pkg/workspace/... -v -run TestExecuteHooksNonexistentWorkspace -count=1`
2. Observe: Test passes with error containing "workspaceDir" and "does not exist"

**Expected:** Test PASS, error message mentions workspaceDir doesn't exist

---

### TC03: HookExecutor executes successful hook
**Purpose:** Verify successful hook execution returns nil

**Steps:**
1. Run: `go test ./pkg/workspace/... -v -run TestExecuteHooksSuccess -count=1`
2. Observe: Test passes with single echo command returning nil

**Expected:** Test PASS, ExecuteHooks returns nil for successful command

---

### TC04: HookExecutor captures output on failure
**Purpose:** Verify stdout+stderr captured in HookError.Output

**Steps:**
1. Run: `go test ./pkg/workspace/... -v -run TestExecuteHooksFailureWithOutput -count=1`
2. Observe: Test passes with HookError.Output containing "No such file or directory"

**Expected:** Test PASS, HookError.Output contains stderr from cat nonexistent file

---

### TC05: HookExecutor aborts on first failure (sequential abort)
**Purpose:** Verify first failure stops execution, subsequent hooks not run

**Steps:**
1. Run: `go test ./pkg/workspace/... -v -run TestExecuteHooksSequentialAbort -count=1`
2. Observe: Test passes with marker file NOT created (proves second hook didn't run)

**Expected:** Test PASS, HookIndex=0 in error, marker file doesn't exist

**Critical verification:** This proves abort-on-failure behavior. If marker file exists, sequential abort is broken.

---

### TC06: HookExecutor respects context cancellation
**Purpose:** Verify canceled context returns context.Canceled immediately

**Steps:**
1. Run: `go test ./pkg/workspace/... -v -run TestExecuteHooksContextCancel -count=1`
2. Observe: Test passes with duration < 100ms (not 5s sleep delay)

**Expected:** Test PASS, returns context.Canceled without delay

---

### TC07: HookExecutor handles command not found
**Purpose:** Verify error for nonexistent command in PATH

**Steps:**
1. Run: `go test ./pkg/workspace/... -v -run TestExecuteHooksCommandNotFound -count=1`
2. Observe: Test passes with HookError containing non-zero ExitCode

**Expected:** Test PASS, HookError.ExitCode non-zero (127 or 1), error message mentions command

---

### TC08: HookError Error() method format
**Purpose:** Verify Error() string follows GitError pattern with ': ' separator

**Steps:**
1. Run: `go test ./pkg/workspace/... -v -run TestHookErrorErrorMethod -count=1`
2. Observe: All 5 subtests pass checking format variations

**Expected:** All 5 subtests PASS, Error() contains expected substrings joined by ': '

---

### TC09: HookError Unwrap() for error chaining
**Purpose:** Verify errors.Is works through Unwrap()

**Steps:**
1. Run: `go test ./pkg/workspace/... -v -run TestHookErrorUnwrap -count=1`
2. Observe: Test passes with errors.Is(hookErr, underlyingErr) returning true

**Expected:** Test PASS, errors.Is and errors.As work correctly

---

### TC10: HookExecutor runs hooks in workspace directory
**Purpose:** Verify cmd.Dir = workspaceDir for each hook

**Steps:**
1. Run: `go test ./pkg/workspace/... -v -run TestExecuteHooksWorkingDirectory -count=1`
2. Observe: Test passes with pwd output matching tmpDir

**Expected:** Test PASS, hook's pwd output equals workspaceDir

---

### TC11: All hook tests pass
**Purpose:** Comprehensive verification of all hook functionality

**Steps:**
1. Run: `go test ./pkg/workspace/... -v -run "Hook" -count=1`
2. Observe: All 17 test functions pass

**Expected:** 17 tests PASS, 0 FAIL, exit code 0

---

### TC12: Full workspace package builds and tests pass
**Purpose:** Ensure hook implementation doesn't break existing code

**Steps:**
1. Run: `go build ./pkg/workspace/...`
2. Run: `go test ./pkg/workspace/... -count=1`
3. Observe: Build succeeds, all tests pass

**Expected:** Build exit code 0, test exit code 0, 78+ tests PASS

## Edge Cases Covered

| Case | Test | Behavior |
|------|------|----------|
| Empty hooks array | TestExecuteHooksEmptyHooks | Returns nil (success) |
| Nil hooks | TestExecuteHooksEmptyHooks/nil_hooks | Returns nil (success) |
| Nonexistent workspace | TestExecuteHooksNonexistentWorkspace | Returns error before execution |
| Empty Command field | TestExecuteHooksEmptyCommand | Returns HookError with HookIndex=0 |
| First hook fails | TestExecuteHooksSequentialAbort | Abort, subsequent hooks not run |
| Last hook fails | TestExecuteHooksLastHookFails | Earlier hooks completed |
| Context canceled before | TestExecuteHooksContextCancel | Returns context.Canceled immediately |
| Context timeout during | TestExecuteHooksContextCancelDuringExecution | Returns context error, aborts |
| Command not found | TestExecuteHooksCommandNotFound | HookError with ExitCode 127/1 |
| Single hook | TestExecuteHooksSingleHook | Works without sequential interaction |

## Pass Criteria

- All 17 hook tests pass
- Build succeeds without compilation errors
- Full workspace package tests pass (78+ tests)
- TestExecuteHooksSequentialAbort proves abort-on-failure (marker file NOT created)
