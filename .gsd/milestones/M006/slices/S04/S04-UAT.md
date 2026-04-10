# S04: Manual: unused dead code (12 issues) — UAT

**Milestone:** M006
**Written:** 2026-04-09T15:12:07.280Z

## UAT: S04 — unused dead code removal

### Preconditions
- Go toolchain available (`go build ./...` succeeds)
- golangci-lint v2 available

### Test Cases

#### TC-01: No unused linter findings
**Steps:**
1. Run `golangci-lint run ./... 2>&1 | grep unused`
2. Check exit code

**Expected:** grep exits with code 1 (no matches). Output is empty. This confirms zero unused findings across the entire codebase.

#### TC-02: Build remains clean
**Steps:**
1. Run `go build ./...`

**Expected:** Exit code 0, no compiler errors. Removing dead code must not leave any dangling references.

#### TC-03: All pkg tests pass
**Steps:**
1. Run `go test ./pkg/...`

**Expected:** All 8 packages report `ok`. No new test failures introduced by dead-code removal.

#### TC-04: Specific symbol absence — mu mutex field
**Steps:**
1. Run `grep -n 'mu sync.Mutex' pkg/agentd/shim_client.go`

**Expected:** No output (grep exits non-zero). The `mu` field is absent from the ShimClient struct.

#### TC-05: Specific symbol absence — session handler methods
**Steps:**
1. Run `grep -n 'func.*handleSession\|func.*deliverPrompt' pkg/ari/server.go`

**Expected:** No output. `handleSessionNew`, `deliverPrompt`, `handleSessionPrompt`, `handleSessionSend`, `handleSessionStatus`, `handleSessionList`, `handleSessionStop`, `handleSessionPause`, `handleSessionResume`, `handleSessionDetach` are all absent.

#### TC-06: deliverPromptAsync is NOT removed (must remain)
**Steps:**
1. Run `grep -n 'func.*deliverPromptAsync' pkg/ari/server.go`

**Expected:** One match. `deliverPromptAsync` is a live function called by `handleAgentPrompt` and `handleRoomSend`; it must not be deleted.

#### TC-07: Specific symbol absence — ptrInt test helper
**Steps:**
1. Run `grep -n 'ptrInt' pkg/events/translator_test.go`

**Expected:** No output. The `ptrInt` helper function and its comment are absent.

### Edge Cases
- **Scope boundary:** `golangci-lint run ./... 2>&1 | grep -E '\(gocritic\)|\(testifylint\)'` may still produce output — these are S06/S07 targets and are expected to be non-zero until those slices complete.
- **Integration tests:** `tests/integration/...` has pre-existing failures unrelated to dead-code removal. These failures are not regressions from S04.

