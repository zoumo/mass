# S05: Manual: errorlint — type assertions on errors (17 issues) — UAT

**Milestone:** M006
**Written:** 2026-04-09T15:25:02.755Z

## UAT: S05 — errorlint clean state

### Preconditions
- golangci-lint v2 is installed and on PATH
- Working directory is `/Users/jim/code/zoumo/open-agent-runtime`
- Go toolchain is installed (go build, go test)

---

### TC-01: No errorlint findings from golangci-lint

**What it tests:** The codebase has zero errorlint violations (type assertions on errors, non-wrapping error comparisons).

**Steps:**
1. Run `golangci-lint run ./... 2>&1 | grep errorlint`
2. Observe exit code with `echo $?`

**Expected outcome:** Command produces no output; grep exits with code 1 (no matches). If any errorlint lines appear, the slice has regressed.

---

### TC-02: Codebase builds cleanly

**What it tests:** No compilation errors were introduced.

**Steps:**
1. Run `go build ./...`
2. Observe exit code.

**Expected outcome:** Exit code 0, no output.

---

### TC-03: All pkg tests pass

**What it tests:** No test regressions were introduced.

**Steps:**
1. Run `go test ./pkg/...`
2. Observe output.

**Expected outcome:** All 8 packages report `ok`: agentd, ari, events, meta, rpc, runtime, spec, workspace.

---

### TC-04: errors.Is / errors.As patterns present in key files

**What it tests:** The M005 migration that made errorlint clean is still in place.

**Steps:**
1. Run `grep -r 'errors\.Is\|errors\.As' pkg/meta/ pkg/ari/ pkg/runtime/ | head -20`
2. Observe output.

**Expected outcome:** Multiple matches across pkg/meta/*.go, pkg/ari/server.go, pkg/runtime/terminal.go — confirming the idiomatic patterns are present and errorlint cleanliness is structural, not accidental.

---

### TC-05: .golangci.yaml std-error-handling preset in place

**What it tests:** The exclusion preset that legitimately suppresses err == sql.ErrNoRows comparisons is still configured.

**Steps:**
1. Run `grep -A2 'std-error-handling\|errorlint' .golangci.yaml`
2. Observe output.

**Expected outcome:** The std-error-handling exclusion preset is referenced in .golangci.yaml, explaining why legitimate SQL sentinel comparisons are not flagged.

