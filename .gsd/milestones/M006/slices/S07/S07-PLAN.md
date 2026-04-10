# S07: Manual: testifylint (31 issues)

**Goal:** Fix all 5 remaining testifylint `require-error` findings so that `golangci-lint run ./...` reports 0 issues across the entire codebase.
**Demo:** After this: golangci-lint run ./... reports 0 issues.

## Tasks
- [x] **T01: Replaced 5 assert.Error/NoError with require.Error/NoError in pkg/agentd tests and fixed gci import formatting in pkg/runtime/terminal.go, bringing golangci-lint to 0 issues** — The `require-error` checker in testifylint flags 5 locations where `assert.Error` or `assert.NoError` is used for a standalone error assertion. testifylint requires `require` (not `assert`) for these because a failing error check should stop the test immediately — continuing after an unexpected error or non-error is misleading and can panic on subsequent lines.

The 5 locations to fix:

1. `pkg/agentd/agent_test.go:270` — `assert.NoError(t, err, "Get on missing agent should return nil error")` → `require.NoError(t, err, "Get on missing agent should return nil error")`

2. `pkg/agentd/session_test.go:236` — `assert.NoError(t, err, "Valid transition %s -> %s should succeed", tc.from, tc.to)` → `require.NoError(t, err, "Valid transition %s -> %s should succeed", tc.from, tc.to)`

3. `pkg/agentd/shim_client_test.go:233` — in `TestShimClientDialFail`: `assert.Error(t, err)` → `require.Error(t, err)` (the two lines after it — `assert.Nil(t, c)` and `assert.Contains(t, err.Error(), "dial")` — stay as `assert`)

4. `pkg/agentd/shim_client_test.go:606` — in `TestParseSessionUpdateMalformed`: `assert.Error(t, err)` → `require.Error(t, err)` (the `assert.Contains` line after stays as `assert`)

5. `pkg/agentd/shim_client_test.go:633` — in `TestParseRuntimeStateChangeMalformed`: `assert.Error(t, err)` → `require.Error(t, err)` (the `assert.Contains` line after stays as `assert`)

All three files already import `github.com/stretchr/testify/require` — verify with grep before editing. After edits, run `golangci-lint run ./...` and confirm zero output, then run `go test ./pkg/agentd/...` to confirm no test regressions.
  - Estimate: 20m
  - Files: pkg/agentd/agent_test.go, pkg/agentd/session_test.go, pkg/agentd/shim_client_test.go
  - Verify: golangci-lint run ./... 2>&1; [ $? -eq 0 ] && echo PASS || echo FAIL
