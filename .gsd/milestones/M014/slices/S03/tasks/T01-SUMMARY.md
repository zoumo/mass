---
id: T01
parent: S03
milestone: M014
key_files:
  - pkg/shim/runtime/acp/runtime.go
  - .gsd/KNOWLEDGE.md
key_decisions:
  - Used errors.Is(err, os.ErrNotExist) instead of os.IsNotExist for wrapped error detection — spec.ReadState wraps with fmt.Errorf so os.IsNotExist can't see through it
  - UpdatedAt is stamped after the closure runs so callers cannot override it — it's a derived field
duration: 
verification_result: passed
completed_at: 2026-04-14T15:18:57.141Z
blocker_discovered: false
---

# T01: Refactored writeState to closure pattern with read-modify-write semantics and UpdatedAt stamped on every write

**Refactored writeState to closure pattern with read-modify-write semantics and UpdatedAt stamped on every write**

## What Happened

Refactored `Manager.writeState` from accepting a full `apiruntime.State` literal to a closure `func(*apiruntime.State)`. The new implementation reads existing state (or starts from zero on first write), applies the caller's closure, stamps `UpdatedAt` with RFC3339Nano, then writes atomically via `spec.WriteState`.

All 7 call sites were converted:
1. **bootstrap-started** — closure sets OarVersion, ID, Status=creating, Bundle, Annotations
2. **bootstrap-failed** (defer) — closure sets Status=stopped + identity fields
3. **bootstrap-complete** — closure sets Status=idle, PID + identity fields
4. **process-exited** (goroutine) — closure sets Status=stopped + identity fields
5. **runtime-stop** (Kill) — closure sets Status=stopped + identity fields
6. **prompt-started** — closure sets Status=running only (preserves all other fields)
7. **prompt-completed/failed** — closure sets Status=idle only (preserves all other fields)

The `Prompt()` method's standalone `spec.ReadState` calls were removed — `writeState` now handles the read-modify-write internally.

Hit one gotcha during implementation: `os.IsNotExist(err)` doesn't unwrap through `fmt.Errorf("%w", ...)` chains — only through `*PathError`/`*LinkError`/`*SyscallError`. Since `spec.ReadState` wraps with `fmt.Errorf`, had to use `errors.Is(prevErr, os.ErrNotExist)` instead. Recorded as K081.

## Verification

1. `go build ./pkg/shim/runtime/acp/...` — compiles cleanly, no errors
2. `go test ./pkg/shim/runtime/acp/... -count=1 -v` — all 11 tests pass (6 RuntimeSuite + 5 unit tests)
3. `make build` — full project builds (agentd + agentdctl)
4. Verified: zero `writeState(apiruntime.State{` calls remain (`grep -c` returns 0)
5. Verified: no `spec.ReadState` calls in `Prompt()` function
6. Verified: `UpdatedAt` stamped after closure on every write path

## Verification Evidence

| # | Command | Exit Code | Verdict | Duration |
|---|---------|-----------|---------|----------|
| 1 | `go build ./pkg/shim/runtime/acp/...` | 0 | ✅ pass | 1200ms |
| 2 | `go test ./pkg/shim/runtime/acp/... -count=1 -v` | 0 | ✅ pass | 14700ms |
| 3 | `make build` | 0 | ✅ pass | 3000ms |

## Deviations

Used errors.Is(prevErr, os.ErrNotExist) instead of os.IsNotExist(prevErr) as originally planned — os.IsNotExist doesn't unwrap fmt.Errorf chains from spec.ReadState. Added 'errors' import accordingly.

## Known Issues

None.

## Files Created/Modified

- `pkg/shim/runtime/acp/runtime.go`
- `.gsd/KNOWLEDGE.md`
