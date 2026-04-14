# S06: Phase 5: Cleanup — UAT

**Milestone:** M012
**Written:** 2026-04-14T03:49:30.401Z

## UAT: S06 — Phase 5: Cleanup

### Preconditions
- Clean checkout with all M012 slices S01–S05 already applied
- `make build` available
- `rg` (ripgrep) available

---

### TC-01: make build exits 0 (both binaries produced)

**Steps:**
1. Run `make build`
2. Verify exit code is 0
3. Verify `bin/agentd` exists and is executable
4. Verify `bin/agentdctl` exists and is executable

**Expected:** Exit 0, both binaries present.

---

### TC-02: go test ./... -count=1 exits 0 (all packages)

**Steps:**
1. Run `go test ./... -count=1`
2. Verify every package line is `ok` (no `FAIL` lines)

**Expected:** All 17 test packages show `ok`. Any `FAIL` is a blocker.

**Known pre-existing issue:** `pkg/agentd` has an intermittent `send on closed channel` panic under high-parallelism runs (`-count=3`). A single-count run must pass. If pkg/agentd fails on the first try, re-run once; persistent failure is a blocker.

---

### TC-03: pkg/rpc directory is fully deleted

**Steps:**
1. Run `ls pkg/rpc`
2. Run `rg '"github.com/zoumo/oar/pkg/rpc"' --type go`

**Expected:** `ls pkg/rpc` exits 2 (no such file or directory). `rg` exits 1 (zero matches).

---

### TC-04: pkg/agentd/shim_client.go and shim_client_test.go are deleted

**Steps:**
1. Run `ls pkg/agentd/shim_client.go`
2. Run `ls pkg/agentd/shim_client_test.go`

**Expected:** Both exits 2 (files do not exist).

---

### TC-05: pkg/ari/server.go monolith is deleted

**Steps:**
1. Run `ls pkg/ari/server.go`

**Expected:** Exits 2 (file does not exist).

---

### TC-06: No ari.New references remain in Go source

**Steps:**
1. Run `rg 'ari\.New\b' --type go`

**Expected:** Exit 1 (zero matches). Any match indicates the old monolith API is still referenced.

---

### TC-07: pkg/ari/server_test.go compiles and all ARI tests pass

**Steps:**
1. Run `go test ./pkg/ari/... -count=1 -v 2>&1 | grep -E '(PASS|FAIL)'`

**Expected:** All tests pass (show `--- PASS`). No `--- FAIL` lines.

---

### TC-08: pkg/agentd recovery tests still pass (mock_shim_server_test.go extracted correctly)

**Steps:**
1. Run `go test ./pkg/agentd/... -count=1 -run TestRecover -v`

**Expected:** All `TestRecover*` tests pass. These depend on the `newMockShimServer` infrastructure extracted into `mock_shim_server_test.go`.

---

### TC-09: No legacy pkg/rpc sourcegraph/jsonrpc2 import in go.mod usage

**Steps:**
1. Run `rg 'sourcegraph' go.mod go.sum | grep -v '^go.sum'`

**Expected:** No direct `require` line for `github.com/sourcegraph/jsonrpc2` in go.mod (it may remain in go.sum as an indirect dependency). The `rg '"github.com/sourcegraph/jsonrpc2"'` in Go source files should return zero matches.

---

### TC-10: Integration tests pass end-to-end (smoke)

**Steps:**
1. Run `go test ./tests/integration/... -count=1` (timeout ~3 minutes)

**Expected:** Exit 0. All integration tests pass against the cleaned-up codebase.
