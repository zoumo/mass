# S06: Manual: gocritic (45 issues) — UAT

**Milestone:** M006
**Written:** 2026-04-09T15:49:09.314Z

# UAT: S06 — Manual: gocritic (45 issues)

## Preconditions
- Go toolchain installed, `go build ./...` passes
- `golangci-lint` v2 installed and on PATH
- Working directory: `/Users/jim/code/zoumo/open-agent-runtime`

---

## Test Cases

### TC-01: Zero gocritic findings after all fixes
**What it verifies:** The primary slice goal — no gocritic issues remain.

**Steps:**
1. Run: `golangci-lint run ./... 2>&1 | grep gocritic`
2. Observe: No output (grep finds nothing and exits 1)
3. Run: `golangci-lint run ./... 2>&1 | grep gocritic; [ $? -eq 1 ] && echo PASS || echo FAIL`

**Expected:** Output is `PASS`

---

### TC-02: Build succeeds after all changes
**What it verifies:** None of the 11 edited files introduced compilation errors (especially the removal of `min()` from terminal.go).

**Steps:**
1. Run: `go build ./...`
2. Observe exit code 0 and no output

**Expected:** Silent success (exit 0)

---

### TC-03: filepathJoin — os.TempDir() used in agentd tests
**What it verifies:** Both agentd test files use `os.TempDir()` instead of literal string paths in `filepath.Join`.

**Steps:**
1. Run: `grep -n 'filepath.Join.*"/tmp"' pkg/agentd/process_test.go pkg/agentd/shim_client_test.go`

**Expected:** No matches (exit 1 / no output)

---

### TC-04: importShadow — no local vars named `meta` or `workspace` shadowing imports
**What it verifies:** importShadow renames applied in registry.go, server.go, server_test.go.

**Steps:**
1. Run: `grep -n '^\s*meta :=' pkg/ari/registry.go pkg/ari/server.go pkg/ari/server_test.go`
2. Run: `grep -n '^\s*workspace :=' pkg/ari/server.go pkg/ari/server_test.go`

**Expected:** Both commands produce no output (variables renamed to `wsMeta` and `ws`)

---

### TC-05: appendAssign fix — pre-allocated slice in translator_test.go
**What it verifies:** The pattern that avoids mutating `turn1`'s backing array is in place.

**Steps:**
1. Run: `grep -n 'make.*Envelope' pkg/events/translator_test.go`

**Expected:** A line matching `make([]Envelope, 0, len(turn1)+len(turn2))` is present

---

### TC-06: exitAfterDefer fix — deferred cleanup removed, explicit call added
**What it verifies:** Both TestMain functions clean up tmpDir explicitly before os.Exit.

**Steps:**
1. Run: `grep -n 'defer os.RemoveAll' pkg/rpc/server_test.go pkg/runtime/runtime_test.go`
2. Run: `grep -n 'os.RemoveAll(tmpDir)' pkg/rpc/server_test.go pkg/runtime/runtime_test.go`

**Expected:** Step 1 produces no output; Step 2 shows one match per file (without `defer`)

---

### TC-07: builtinShadowDecl — custom min() removed from terminal.go
**What it verifies:** The user-defined `min` function shadowing the Go 1.21+ built-in is gone.

**Steps:**
1. Run: `grep -n 'func min' pkg/runtime/terminal.go`

**Expected:** No output (function deleted)

---

### TC-08: appendCombine fix — single append in hook.go
**What it verifies:** Two consecutive appends merged into one.

**Steps:**
1. Run: `grep -A1 'hook %s failed' pkg/workspace/hook.go`

**Expected:** The second `fmt.Sprintf("hookIndex=%d"...)` appears as the second argument of the same `append(...)` call, not as a separate `append` statement

---

### TC-09: elseif fix — flattened else-if in hook_test.go
**What it verifies:** The `else { if ... }` anti-pattern is gone.

**Steps:**
1. Run: `grep -n 'else {' pkg/workspace/hook_test.go | head -5`
2. Run: `grep -n 'else if err' pkg/workspace/hook_test.go`

**Expected:** Step 2 shows a match; the `else {` for that error check is gone

---

### TC-10: Remaining lint issues are only S07-scoped (testifylint) + pre-existing gci
**What it verifies:** S06 changes introduced no new lint regressions beyond known pending work.

**Steps:**
1. Run: `golangci-lint run ./... 2>&1 | grep -v '^$'`

**Expected:** Only `testifylint` and `gci` findings appear; no gocritic, no errorlint, no unused, no misspell, no unparam lines

