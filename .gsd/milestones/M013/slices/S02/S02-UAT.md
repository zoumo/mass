# S02: ARI package restructure — UAT

**Milestone:** M013
**Written:** 2026-04-14T10:17:35.318Z

## UAT: S02 — ARI Package Restructure

### Preconditions
- Working directory: `/Users/jim/code/zoumo/open-agent-runtime`
- Go toolchain available (`go`, `make`)

---

### TC-01: No legacy api/ari imports remain
**Steps:**
1. Run `rg '"github.com/zoumo/oar/api/ari"' --type go`
**Expected:** Command exits with code 1 (no matches found). Zero lines of output.

---

### TC-02: No bare pkg/ari root imports remain
**Steps:**
1. Run `rg '"github.com/zoumo/oar/pkg/ari"[^/]' --type go`
**Expected:** Command exits with code 1 (no matches found). Zero lines of output.

---

### TC-03: api/ari directory is deleted
**Steps:**
1. Run `test ! -d api/ari && echo PASS || echo FAIL`
**Expected:** Prints `PASS`.

---

### TC-04: pkg/ari root files are deleted
**Steps:**
1. Run `test ! -f pkg/ari/registry.go && test ! -f pkg/ari/client.go && echo PASS || echo FAIL`
**Expected:** Prints `PASS`.
2. Run `ls pkg/ari/`
**Expected:** Output shows only three entries: `api`, `client`, `server` directories.

---

### TC-05: pkg/ari/api/ has all three required files
**Steps:**
1. Run `ls pkg/ari/api/`
**Expected:** Exactly `domain.go`, `methods.go`, `types.go` listed.
2. Run `grep -l "MethodWorkspaceCreate\|MethodAgentRunCreate\|MethodAgentSet" pkg/ari/api/methods.go`
**Expected:** `pkg/ari/api/methods.go` returned (confirms ARI method constants are present).

---

### TC-06: pkg/ari/server/ has all expected files
**Steps:**
1. Run `ls pkg/ari/server/`
**Expected:** Contains `service.go`, `registry.go`, `server.go`, `server_test.go`, `registry_test.go`.

---

### TC-07: pkg/ari/client/ has all expected files
**Steps:**
1. Run `ls pkg/ari/client/`
**Expected:** Contains `typed.go`, `simple.go`, `client.go`, `simple_test.go`.

---

### TC-08: make build produces both binaries
**Steps:**
1. Run `make build`
**Expected:** Exit code 0. `bin/agentd` and `bin/agentdctl` both produced (no compile errors).

---

### TC-09: pkg/ari sub-packages compile and tests pass
**Steps:**
1. Run `go test ./pkg/ari/...`
**Expected:** `pkg/ari/api` shows `[no test files]`; `pkg/ari/client` and `pkg/ari/server` both show `ok` with passing tests.

---

### TC-10: Full test suite passes
**Steps:**
1. Run `go test ./... -timeout=180s`
**Expected:** All packages pass. In particular: `pkg/store`, `pkg/agentd`, `pkg/workspace`, `tests/integration`, `cmd/agentdctl/subcommands/up` all show `ok`.

---

### TC-11: ARI method constants are accessible from new path
**Steps:**
1. Create a temp file and verify:
   ```
   grep "MethodWorkspaceCreate" pkg/ari/api/methods.go
   grep "MethodAgentRunCreate" pkg/ari/api/methods.go
   grep "MethodAgentSet" pkg/ari/api/methods.go
   ```
**Expected:** Each grep returns a line with the constant definition (value like `"workspace/create"`, `"agentrun/create"`, `"agent/set"`).

---

### TC-12: Shim method constants NOT present in pkg/ari/api/methods.go
**Steps:**
1. Run `grep -c "MethodSession\|MethodRuntime\|MethodShimEvent" pkg/ari/api/methods.go`
**Expected:** Prints `0` (shim constants are S03 concern, not included in ARI api package).

---

### TC-13: Integration tests pass end-to-end
**Steps:**
1. Run `go test ./tests/integration/... -v -run TestEndToEndPipeline -timeout=60s`
**Expected:** `TestEndToEndPipeline` passes (agentd starts, agentrun/create → agentrun/prompt → agentrun/stop → agentrun/delete cycle completes).

