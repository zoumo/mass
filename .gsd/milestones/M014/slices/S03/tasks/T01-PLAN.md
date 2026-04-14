---
estimated_steps: 41
estimated_files: 1
skills_used: []
---

# T01: Refactor writeState to closure pattern and stamp UpdatedAt on every write

## Description

Refactor `Manager.writeState` in `pkg/shim/runtime/acp/runtime.go` from accepting a full `apiruntime.State` literal to accepting a closure `func(*apiruntime.State)`. This is the core implementation of D119.

### Why

Currently, 5 of 7 writeState call sites construct fresh State literals that omit Session, EventCounts, and UpdatedAt. When these writes happen (Kill, process-exit, bootstrap transitions), any previously-persisted Session data is silently clobbered. The closure pattern ensures every write starts from the current persisted state and only mutates what the caller intends.

### Current writeState call sites (7 total)

1. **Line ~81** `Create()` bootstrap-started — fresh literal, sets Status=creating
2. **Line ~120** `Create()` bootstrap-failed (defer) — fresh literal, sets Status=stopped
3. **Line ~167** `Create()` bootstrap-complete — fresh literal, sets Status=idle, PID
4. **Line ~181** `Create()` process-exited (goroutine) — fresh literal, sets Status=stopped
5. **Line ~219** `Kill()` runtime-stop — fresh literal, sets Status=stopped
6. **Line ~261** `Prompt()` prompt-started — already read-modify-write, sets Status=running
7. **Line ~275** `Prompt()` prompt-completed/failed — already read-modify-write, sets Status=idle

### Steps

1. Change the `writeState` signature from `writeState(state apiruntime.State, reason string) error` to `writeState(apply func(*apiruntime.State), reason string) error`.

2. Implement the new body:
   - Read existing state via `spec.ReadState(m.stateDir)`. If `errors.Is(err, os.ErrNotExist)`, start with a zero `apiruntime.State{}`. Any other read error: return the error.
   - Note: `spec.ReadState` wraps `os.ReadFile` which returns `*os.PathError` — check with `os.IsNotExist(err)` rather than `errors.Is(err, os.ErrNotExist)` since `os.ErrNotExist` is the inner error. Actually both work via `errors.Is` unwrapping, but verify.
   - Call `apply(&state)` to let the caller mutate what it needs.
   - Set `state.UpdatedAt = time.Now().UTC().Format(time.RFC3339Nano)` after the closure (derived field — closure cannot override it).
   - Call `spec.WriteState(m.stateDir, state)` atomically.
   - If the previous read succeeded and previous.Status != state.Status, call `m.emitStateChange(previous, state, reason)`.

3. Convert all 7 call sites to closure form:
   - **bootstrap-started**: `m.writeState(func(s *apiruntime.State) { s.OarVersion = m.cfg.OarVersion; s.ID = m.cfg.Metadata.Name; s.Status = apiruntime.StatusCreating; s.Bundle = m.bundleDir; s.Annotations = m.cfg.Metadata.Annotations }, "bootstrap-started")`
   - **bootstrap-failed**: same pattern with Status=stopped
   - **bootstrap-complete**: same pattern with Status=idle, s.PID = cmd.Process.Pid
   - **process-exited**: same pattern with Status=stopped
   - **runtime-stop (Kill)**: same pattern with Status=stopped
   - **prompt-started**: `m.writeState(func(s *apiruntime.State) { s.Status = apiruntime.StatusRunning }, "prompt-started")` — no longer needs the outer ReadState
   - **prompt-completed/failed**: `m.writeState(func(s *apiruntime.State) { s.Status = apiruntime.StatusIdle }, reason)` — no longer needs the outer ReadState

4. Remove the now-unnecessary `ReadState` calls in `Prompt()` (lines ~259-261 and ~273-276) since writeState itself handles read-modify-write.

5. Add `"errors"` to imports (needed for errors.Is) and `"time"` (already present).

6. Verify: `go build ./pkg/shim/runtime/acp/...` compiles cleanly, `go test ./pkg/shim/runtime/acp/... -count=1` — all existing tests pass.

### Must-Haves

- writeState signature is `writeState(apply func(*apiruntime.State), reason string) error`
- First-write (no state.json) uses zero State + apply, does not error
- Update path reads existing state.json, applies closure, preserves un-mutated fields
- UpdatedAt set to RFC3339Nano after closure on every write
- All 7 call sites converted — no State literal passed to writeState anywhere
- Prompt() no longer has its own ReadState calls — writeState handles it
- emitStateChange still fires on status transitions
- All existing tests pass without modification

## Inputs

- ``pkg/shim/runtime/acp/runtime.go` — current writeState implementation with 7 call sites`
- ``pkg/runtime-spec/api/state.go` — State struct with UpdatedAt, Session, EventCounts fields (from S02)`
- ``pkg/runtime-spec/state.go` — ReadState/WriteState spec functions`

## Expected Output

- ``pkg/shim/runtime/acp/runtime.go` — writeState refactored to closure pattern, all call sites converted, UpdatedAt stamped on every write`

## Verification

go build ./pkg/shim/runtime/acp/... && go test ./pkg/shim/runtime/acp/... -count=1 -v 2>&1 | tail -20

## Observability Impact

UpdatedAt RFC3339Nano timestamp now set on every state.json write — operators can determine staleness by reading `state.json | jq .updatedAt`
