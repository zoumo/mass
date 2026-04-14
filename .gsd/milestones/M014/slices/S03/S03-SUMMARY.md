---
id: S03
parent: M014
milestone: M014
provides:
  - ["writeState closure pattern guaranteeing Session/EventCounts never clobbered by lifecycle writes", "UpdatedAt RFC3339Nano timestamp on every state.json write", "newManagerWithStateDir test helper for downstream test slices"]
requires:
  []
affects:
  - ["S05", "S06"]
key_files:
  - ["pkg/shim/runtime/acp/runtime.go", "pkg/shim/runtime/acp/runtime_test.go"]
key_decisions:
  - ["D119: writeState closure pattern — writeState accepts func(*apiruntime.State) instead of a full State literal, ensuring read-modify-write semantics", "Used errors.Is(err, os.ErrNotExist) instead of os.IsNotExist for wrapped error detection from spec.ReadState", "UpdatedAt is stamped after the closure runs — it's a derived field callers cannot override", "Refactored newManager into newManagerWithStateDir to expose stateDir for test injection without breaking existing callers"]
patterns_established:
  - ["writeState closure pattern: all state mutations go through func(*apiruntime.State) closures — never construct State literals directly", "UpdatedAt as unconditional derived field stamped after every closure in writeState", "newManagerWithStateDir test helper for exposing stateDir to tests that need direct state.json injection"]
observability_surfaces:
  - none
drill_down_paths:
  []
duration: ""
verification_result: passed
completed_at: 2026-04-14T15:27:22.154Z
blocker_discovered: false
---

# S03: writeState read-modify-write refactor

**Refactored all 7 writeState call sites to a read-modify-write closure pattern so Kill, process-exit, and prompt cycles never clobber Session metadata, and UpdatedAt is stamped unconditionally on every write.**

## What Happened

## What This Slice Delivered

This slice converted `Manager.writeState` from accepting a full `apiruntime.State` literal to a closure `func(*apiruntime.State)`, implementing the core data-integrity guarantee for all downstream session-metadata work in M014.

### T01: writeState closure refactor + UpdatedAt stamping

The writeState signature changed to `writeState(apply func(*apiruntime.State), reason string) error`. The new body:
1. Reads existing state via `spec.ReadState` (or starts from zero `apiruntime.State{}` if the file doesn't exist yet).
2. Calls the caller's closure, which mutates only the fields it cares about.
3. Stamps `state.UpdatedAt = time.Now().UTC().Format(time.RFC3339Nano)` unconditionally — a derived field callers cannot override.
4. Writes atomically via `spec.WriteState`.
5. Fires `emitStateChange` if status changed.

All 7 call sites were converted:
- **bootstrap-started/failed/complete, process-exited, Kill** — closures set identity + status fields, preserving Session/EventCounts
- **prompt-started, prompt-completed/failed** — closures set only Status, preserving everything else; the standalone `ReadState` calls in `Prompt()` were removed since writeState now handles read-modify-write internally

Key gotcha: `os.IsNotExist(err)` doesn't unwrap through `fmt.Errorf("%w", ...)` chains from `spec.ReadState`. Used `errors.Is(err, os.ErrNotExist)` instead. Recorded as K081.

### T02: Integration tests proving Session preservation

Three integration tests were added:
- **TestKill_PreservesSession**: Create → inject Session via `writeSessionToStateDir` → Kill() → assert status==stopped AND Session.AgentInfo.Name=="test-agent" AND UpdatedAt valid
- **TestProcessExit_PreservesSession**: Create → inject Session → SIGKILL externally → Eventually status==stopped → assert Session preserved
- **TestWriteState_SetsUpdatedAt**: Create → assert UpdatedAt non-empty + valid RFC3339Nano → Kill → assert UpdatedAt >= previous value

A `newManagerWithStateDir` helper was added to expose stateDir for test injection without breaking existing callers (original `newManager` delegates to it).

## Why This Matters for Downstream Slices

S05 (bootstrap capture) will write Session metadata at bootstrap-complete. S06 (metadata hook chain) will update Session fields at runtime. Both depend on the guarantee proven here: lifecycle writes (Kill, process-exit, prompt cycles) will never clobber their data. Without this slice, every Session write would be silently erased by the next status transition.

## Verification

### Slice-Level Verification

1. **Targeted tests pass**: `go test ./pkg/shim/runtime/acp/... -count=1 -v -run 'TestRuntimeSuite/(TestKill_PreservesSession|TestProcessExit_PreservesSession|TestWriteState_SetsUpdatedAt)'` — all 3 PASS
2. **Full test suite**: `go test ./pkg/shim/runtime/acp/... -count=1` — PASS (zero regressions, all 9 tests pass)
3. **Build**: `make build` — agentd + agentdctl compile cleanly
4. **No old-style writeState calls**: `grep -c 'writeState(apiruntime.State{' runtime.go` → 0
5. **writeState signature is closure-based**: `func (m *Manager) writeState(apply func(*apiruntime.State), reason string) error`
6. **UpdatedAt stamped unconditionally**: line 337 in runtime.go, after closure and before write
7. **No standalone ReadState in Prompt()**: verified via awk scan — writeState handles read-modify-write internally

## Requirements Advanced

None.

## Requirements Validated

- R057 — TestKill_PreservesSession + TestProcessExit_PreservesSession prove Session never clobbered; all 7 writeState call sites use closure pattern; zero old-style State literal calls remain
- R059 — TestWriteState_SetsUpdatedAt proves UpdatedAt non-empty and valid RFC3339Nano after every write; monotonic increase across Create→Kill; stamped unconditionally in writeState line 337

## New Requirements Surfaced

None.

## Requirements Invalidated or Re-scoped

None.

## Deviations

None.

## Known Limitations

None.

## Follow-ups

None.

## Files Created/Modified

None.
