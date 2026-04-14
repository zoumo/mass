---
id: T02
parent: S03
milestone: M014
key_files:
  - pkg/shim/runtime/acp/runtime_test.go
key_decisions:
  - Refactored newManager into newManagerWithStateDir returning (mgr, stateDir) to expose stateDir for test injection — original newManager delegates to it so all existing callers unchanged
duration: 
verification_result: passed
completed_at: 2026-04-14T15:23:34.709Z
blocker_discovered: false
---

# T02: Add tests proving Session preservation across Kill() and process-exit, plus UpdatedAt stamping on every write

**Add tests proving Session preservation across Kill() and process-exit, plus UpdatedAt stamping on every write**

## What Happened

Added three integration tests to `pkg/shim/runtime/acp/runtime_test.go` that prove the core S03 demo: the read-modify-write closure pattern in `writeState` preserves Session metadata through Kill() and external SIGKILL, and stamps UpdatedAt on every write.

**Implementation details:**

1. **`newManagerWithStateDir` helper** — Refactored the existing `newManager` into `newManagerWithStateDir` which returns `(*Manager, string)` so tests can access the stateDir for direct state.json injection. The original `newManager` delegates to it, so all existing callers are unaffected.

2. **`writeSessionToStateDir` helper** — Reads existing state.json via `spec.ReadState`, injects a recognizable `SessionState` with `AgentInfo{Name: "test-agent", Version: "1.0.0"}` and one `AvailableCommand`, then writes back via `spec.WriteState`. This simulates what S05's bootstrap-capture will do.

3. **`TestKill_PreservesSession`** — Creates agent → injects Session → calls Kill() → asserts status==stopped AND Session.AgentInfo.Name=="test-agent" AND UpdatedAt is valid RFC3339Nano.

4. **`TestProcessExit_PreservesSession`** — Creates agent → injects Session → sends SIGKILL externally → waits via Eventually for status==stopped → asserts Session.AgentInfo.Name=="test-agent" AND UpdatedAt non-empty.

5. **`TestWriteState_SetsUpdatedAt`** — Creates agent → reads UpdatedAt (must be non-empty, valid RFC3339Nano) → calls Kill() → reads UpdatedAt again → asserts it is >= the post-Create value.

Added `spec "github.com/zoumo/oar/pkg/runtime-spec"` import to the test file for direct state.json manipulation.

## Verification

All three new tests pass, plus all 6 pre-existing tests pass (9 total, zero regressions). Slice-level verification command passes both targeted and full suite runs.

## Verification Evidence

| # | Command | Exit Code | Verdict | Duration |
|---|---------|-----------|---------|----------|
| 1 | `go test ./pkg/shim/runtime/acp/... -count=1 -v -run 'TestRuntimeSuite/(TestKill_PreservesSession|TestProcessExit_PreservesSession|TestWriteState_SetsUpdatedAt)'` | 0 | ✅ pass | 5500ms |
| 2 | `go test ./pkg/shim/runtime/acp/... -count=1` | 0 | ✅ pass | 5900ms |
| 3 | `go build ./pkg/shim/runtime/acp/...` | 0 | ✅ pass | 6300ms |

## Deviations

None.

## Known Issues

None.

## Files Created/Modified

- `pkg/shim/runtime/acp/runtime_test.go`
