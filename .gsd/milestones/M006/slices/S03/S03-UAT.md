# S03: Manual: misspell + unparam (17 issues) — UAT

**Milestone:** M006
**Written:** 2026-04-09T14:37:32.779Z

## UAT: S03 — misspell + unparam clean

### Preconditions
- Go toolchain available (`go build` works)
- golangci-lint v2 installed and on PATH
- Working directory: `/Users/jim/code/zoumo/open-agent-runtime`

### Test Cases

#### TC-01: Build passes
**Steps:**
1. Run `go build ./...`

**Expected:** Exit code 0, no output.

#### TC-02: No misspell or unparam findings
**Steps:**
1. Run `golangci-lint run ./... 2>&1 | grep -E '(misspell|unparam)'`

**Expected:** No output. The `grep` command exits with code 1 (no matches). Any output is a failure.

#### TC-03: agentd package tests pass
**Steps:**
1. Run `go test ./pkg/agentd/...`

**Expected:** `ok  github.com/open-agent-d/open-agent-d/pkg/agentd` with zero failures.

#### TC-04: forkShim signature no longer includes ctx or rc
**Steps:**
1. Run `grep -n 'func.*forkShim' pkg/agentd/process.go`

**Expected:** Signature contains only `session *meta.Session`, `bundlePath`, and `stateDir` parameters — no `ctx context.Context` and no `rc *RuntimeClass`.

#### TC-05: Call site updated to match new signature
**Steps:**
1. Run `grep -n 'forkShim' pkg/agentd/process.go`

**Expected:** The single call site passes three arguments (session, bundlePath, stateDir) — not five.

### Edge Cases
- **Masked findings:** unparam reports one unused parameter per function per pass. If a future parameter is added and goes unused, TC-02 will catch it.
- **Upstream rc usage:** RuntimeClass is still passed to `generateConfig`/`createBundle` upstream — removing it from forkShim does not affect bundle generation.

