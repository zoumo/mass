# S07: Manual: testifylint (31 issues) — UAT

**Milestone:** M006
**Written:** 2026-04-09T16:14:05.704Z

# S07 UAT — testifylint require-error fixes

## Preconditions
- Go toolchain and golangci-lint v2 installed
- Working directory: `/Users/jim/code/zoumo/open-agent-runtime`
- All earlier M006 slices (S01–S06) complete

---

## Test Case 1: Zero golangci-lint issues (primary goal)

**Purpose:** Confirm the entire codebase is clean across all 11 linter categories.

**Steps:**
1. Run `golangci-lint run ./...`
2. Observe exit code and output

**Expected outcome:**
- Exit code 0
- Output: `0 issues.`
- No testifylint, gci, gofumpt, unconvert, copyloopvar, ineffassign, misspell, unparam, unused, errorlint, or gocritic findings

---

## Test Case 2: require-error edits compile and tests pass

**Purpose:** Confirm the 5 assert→require substitutions are syntactically correct and don't break test logic.

**Steps:**
1. Run `go test ./pkg/agentd/...`
2. Observe exit code

**Expected outcome:**
- Exit code 0
- `ok  github.com/open-agent-d/open-agent-d/pkg/agentd` (or cached ok)

---

## Test Case 3: require.Error/NoError substitution locations

**Purpose:** Confirm the exact substitutions were made at the planned locations.

**Steps:**
1. Run `grep -n "require\.NoError" pkg/agentd/agent_test.go | grep -E "270"`
2. Run `grep -n "require\.NoError" pkg/agentd/session_test.go | grep -E "236"`
3. Run `grep -n "require\.Error" pkg/agentd/shim_client_test.go | grep -E "233|606|633"`

**Expected outcome:**
- Each grep returns a matching line with `require.Error` or `require.NoError` at the expected line numbers (±1 for minor reformatting)
- No `assert.Error` / `assert.NoError` at those positions

---

## Test Case 4: Subsequent assert lines preserved

**Purpose:** Confirm the `assert.Nil` and `assert.Contains` lines immediately after each require-fixed line were NOT changed.

**Steps:**
1. Run `grep -A2 "require\.Error" pkg/agentd/shim_client_test.go | grep "assert\."` 

**Expected outcome:**
- `assert.Nil` and/or `assert.Contains` lines appear after each `require.Error` — they were intentionally left as `assert` since they don't independently gate the test path.

---

## Test Case 5: Full test suite passes

**Purpose:** Confirm no regressions across all packages.

**Steps:**
1. Run `go build ./...`
2. Run `go test ./...` (or a targeted subset across pkg/agentd, pkg/rpc, pkg/runtime)

**Expected outcome:**
- `go build ./...` exits 0
- All test packages pass

---

## Edge Cases

- **Import verification:** All three edited test files already import `testify/require` — no new imports needed. Confirm with `grep -n '"github.com/stretchr/testify/require"' pkg/agentd/agent_test.go pkg/agentd/session_test.go pkg/agentd/shim_client_test.go`.
- **Collateral gci fix in terminal.go:** `golangci-lint run ./pkg/runtime/...` should produce 0 issues — the trailing-blank-line and import-section-separator fix was applied.
