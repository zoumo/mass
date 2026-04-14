# S01: Runtime-spec consumer migration — UAT

**Milestone:** M013
**Written:** 2026-04-14T09:21:13.337Z

# S01 UAT: Runtime-spec consumer migration

## Preconditions

- Repository at the state produced by S01 (all three tasks complete)
- Go toolchain available (`go version`)
- Working directory: repo root

---

## TC-01: make build produces both binaries with no errors

**Steps:**
1. Run `make build`

**Expected outcome:**
- Exit code 0
- `bin/agentd` produced
- `bin/agentdctl` produced
- No compilation errors in output

---

## TC-02: Full test suite passes

**Steps:**
1. Run `go test ./...`

**Expected outcome:**
- All packages print `ok` or `[no test files]`
- No `FAIL` lines in output
- Exit code 0

---

## TC-03: No remaining api/runtime import paths

**Steps:**
1. Run `rg '"github.com/zoumo/oar/api/runtime"' --type go`

**Expected outcome:**
- ripgrep prints nothing and exits with code 1 (no matches found)
- Exit code 0 from `! rg '"github.com/zoumo/oar/api/runtime"' --type go`

---

## TC-04: No remaining pkg/spec import paths

**Steps:**
1. Run `rg '"github.com/zoumo/oar/pkg/spec"' --type go`

**Expected outcome:**
- ripgrep prints nothing and exits with code 1 (no matches found)

---

## TC-05: api/runtime/ directory does not exist

**Steps:**
1. Run `test ! -d api/runtime && echo PASS || echo FAIL`

**Expected outcome:**
- Prints `PASS`
- `ls api/runtime` returns "No such file or directory"

---

## TC-06: api/types.go does not exist

**Steps:**
1. Run `test ! -f api/types.go && echo PASS || echo FAIL`

**Expected outcome:**
- Prints `PASS`

---

## TC-07: Empty runtimeclass stubs deleted

**Steps:**
1. Run `test ! -f pkg/agentd/runtimeclass.go && test ! -f pkg/agentd/runtimeclass_test.go && echo PASS || echo FAIL`

**Expected outcome:**
- Prints `PASS`

---

## TC-08: pkg/runtime-spec/api provides Status and EnvVar types used across the codebase

**Steps:**
1. Run `rg 'pkg/runtime-spec/api' --type go | head -20`

**Expected outcome:**
- Multiple `.go` files import `pkg/runtime-spec/api` (typically with alias `apiruntime`)
- All are in consumer packages (pkg/agentd, pkg/runtime, api/ari, pkg/store, etc.)

---

## TC-09: Pattern B files (process.go, shim_boundary_test.go) retain api import for method/event constants

**Steps:**
1. Run `grep '"github.com/zoumo/oar/api"' pkg/agentd/process.go pkg/agentd/shim_boundary_test.go`

**Expected outcome:**
- Both files still import `"github.com/zoumo/oar/api"` (for `api.MethodShimEvent`, `api.CategoryRuntime`, `api.EventTypeStateChange` constants)
- Neither file references `api.Status` or `api.EnvVar` — those now use `apiruntime.*`

---

## TC-10: Integration test pipeline still works end-to-end

**Steps:**
1. Run `go test ./tests/integration/... -run TestEndToEndPipeline -v -count=1`

**Expected outcome:**
- Test passes (exit 0)
- Confirms that the migration did not break the agentd → shim → mockagent pipeline

