# S01: Auto-fix: gci + gofumpt formatting (56 issues) — UAT

**Milestone:** M006
**Written:** 2026-04-09T13:57:32.769Z

## UAT: S01 — Auto-fix: gci + gofumpt formatting

### Preconditions
- golangci-lint v2.x is installed and on PATH
- Working directory is the repo root (`/Users/jim/code/zoumo/open-agent-runtime`)
- `.golangci.yml` is present with gci and gofumpt linter configuration

---

### Test Case 1: No gci findings in linter output

**Goal:** Confirm all gci import-ordering violations are resolved.

**Steps:**
1. Run: `golangci-lint run ./... 2>&1 | grep '(gci)'`
2. Observe output and exit code.

**Expected outcome:** No output lines. `grep` exits with code 1 (no matches). If any `(gci)` lines appear, S01 is incomplete.

---

### Test Case 2: No gofumpt findings in linter output

**Goal:** Confirm all gofumpt whitespace violations are resolved.

**Steps:**
1. Run: `golangci-lint run ./... 2>&1 | grep '(gofumpt)'`
2. Observe output and exit code.

**Expected outcome:** No output lines. `grep` exits with code 1 (no matches). If any `(gofumpt)` lines appear, S01 is incomplete.

---

### Test Case 3: Combined one-liner verification

**Goal:** Single command confirms both linters report zero findings.

**Steps:**
1. Run: `golangci-lint run ./... 2>&1 | grep -E '\(gci\)|\(gofumpt\)'; test $? -ne 0 && echo 'PASS: zero gci/gofumpt findings' || echo 'FAIL: findings remain'`

**Expected outcome:** Output is `PASS: zero gci/gofumpt findings`.

---

### Test Case 4: Idempotency — re-running fmt produces no diff

**Goal:** Confirm `golangci-lint fmt ./...` is idempotent (running it again changes nothing).

**Steps:**
1. Run: `golangci-lint fmt ./...`
2. Run: `git diff --name-only`

**Expected outcome:** No files appear in `git diff --name-only` after the second fmt run. The formatter output matches the already-committed state.

---

### Edge Case: Only gci/gofumpt findings eliminated (other linters unaffected)

**Goal:** Confirm S01 did not accidentally suppress or introduce findings in other linter categories.

**Steps:**
1. Run: `golangci-lint run ./... 2>&1 | grep -v '(gci)' | grep -v '(gofumpt)' | grep -c '\.'`

**Expected outcome:** Non-zero count is acceptable (other linters like unconvert, misspell, etc. are addressed in S02–S07). The key assertion is that gci and gofumpt specifically show zero findings.
