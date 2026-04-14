# S03: writeState read-modify-write refactor

**Goal:** All state write paths in Manager use a read-modify-write closure pattern so Session, EventCounts, and UpdatedAt are never clobbered by lifecycle writes.
**Demo:** After this: test proves Kill() → state.json.status==stopped AND state.json.session (previously written by bootstrap-complete closure) still present; process-exit similarly; UpdatedAt present on every write; EventCounts flushed on every write.

## Must-Haves

- writeState accepts a closure `func(*apiruntime.State)` instead of a full State literal
- First-write path (state.json doesn't exist yet) creates a zero State, applies the closure, writes
- Update path reads the existing state.json, applies the closure, writes — preserving all fields the closure doesn't touch
- UpdatedAt is set to time.Now().UTC().Format(time.RFC3339Nano) on every write, unconditionally
- All 7 call sites in runtime.go converted to closure form
- Test proves: Create (writes Session to bootstrap-complete state) → Kill → state.json.status==stopped AND state.json.session still present
- Test proves: Create (writes Session to bootstrap-complete state) → process-exit → state.json.status==stopped AND state.json.session still present
- Test proves: UpdatedAt is non-empty after every write

## Proof Level

- This slice proves: integration — tests exercise the real Manager against a mock agent binary, verifying state.json persistence through Kill and process-exit lifecycles

## Integration Closure

- Upstream surfaces consumed: `pkg/runtime-spec/api/state.go` (State, SessionState types from S02), `pkg/runtime-spec/state.go` (ReadState/WriteState)
- New wiring introduced: writeState closure pattern in `pkg/shim/runtime/acp/runtime.go`; UpdatedAt stamping on every write
- What remains: S04 (EventCounts flushing), S05 (bootstrap capture populating Session), S06 (metadata hook chain populating Session at runtime)

## Verification

- Runtime signals: UpdatedAt timestamp on every state.json write — operators can determine staleness without filesystem mtime
- Inspection surfaces: `cat state.json | jq .updatedAt` — always present after first write
- Failure visibility: If writeState closure panics, the write is not committed (read-modify-write is atomic at the spec.WriteState level)
- Redaction constraints: none

## Tasks

- [x] **T01: Refactor writeState to closure pattern and stamp UpdatedAt on every write** `est:45m`
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
  - Files: `pkg/shim/runtime/acp/runtime.go`
  - Verify: go build ./pkg/shim/runtime/acp/... && go test ./pkg/shim/runtime/acp/... -count=1 -v 2>&1 | tail -20

- [x] **T02: Add tests proving Session preservation across Kill and process-exit** `est:30m`
  ## Description

Add integration tests to `pkg/shim/runtime/acp/runtime_test.go` that prove the core slice demo: Kill() and process-exit do not clobber Session metadata previously written to state.json.

### Why

The writeState refactor (T01) is the implementation; these tests are the proof that R057 (Session never clobbered) and R059 (UpdatedAt on every write) are satisfied. The demo sentence is: "test proves Kill() → state.json.status==stopped AND state.json.session still present; process-exit similarly; UpdatedAt present on every write."

### Steps

1. Add a helper function `writeSessionToStateDir(t *testing.T, stateDir string)` that directly writes a state.json with a non-nil Session field using `spec.WriteState`. This simulates what S05's bootstrap-capture will do — it writes Session metadata into state.json before Kill/process-exit can clobber it. The Session should contain a recognizable AgentInfo (e.g. Name: "test-agent", Version: "1.0.0") and at least one AvailableCommand.

2. Add `TestKill_PreservesSession`:
   - Create manager, call Create(ctx)
   - Read current state with GetState(), confirm status == idle
   - Call `writeSessionToStateDir(t, stateDir)` to inject Session into state.json
   - Read state again, confirm Session is present
   - Call Kill(ctx)
   - Read state, assert:
     - status == stopped
     - Session != nil
     - Session.AgentInfo.Name == "test-agent"
     - UpdatedAt is non-empty and parses as valid RFC3339Nano

3. Add `TestProcessExit_PreservesSession`:
   - Create manager, call Create(ctx)
   - Call `writeSessionToStateDir(t, stateDir)` to inject Session into state.json
   - Kill the process externally with SIGKILL (same pattern as TestCreate_ReachesCreatedState)
   - Wait via Eventually for status == stopped
   - Assert Session != nil, Session.AgentInfo.Name == "test-agent", UpdatedAt non-empty

4. Add `TestWriteState_SetsUpdatedAt`:
   - Create manager, call Create(ctx)
   - Read state, assert UpdatedAt is non-empty and parses as valid time
   - Call Kill(ctx)
   - Read state, assert UpdatedAt is non-empty and is >= the previous UpdatedAt

5. **Important:** The test helper needs access to the stateDir. Currently `newManager` creates stateDir internally. Refactor `newManager` to return `(mgr, stateDir)` or add a new helper `newManagerWithStateDir` that exposes the stateDir path. The existing tests don't need stateDir so you can either update newManager's return signature (and fix all callers) or add a parallel helper.

6. Add import for `pkg/runtime-spec` (as `runtimespec`) and `pkg/runtime-spec/api` (as `apiruntime` — already imported) to the test file.

7. Run full test suite: `go test ./pkg/shim/runtime/acp/... -count=1 -v`

### Must-Haves

- TestKill_PreservesSession passes: status==stopped AND Session.AgentInfo.Name=="test-agent" after Kill
- TestProcessExit_PreservesSession passes: status==stopped AND Session.AgentInfo.Name=="test-agent" after external SIGKILL
- TestWriteState_SetsUpdatedAt passes: UpdatedAt non-empty and valid RFC3339Nano after Create and after Kill
- All pre-existing tests still pass
  - Files: `pkg/shim/runtime/acp/runtime_test.go`
  - Verify: go test ./pkg/shim/runtime/acp/... -count=1 -v -run 'TestRuntimeSuite/(TestKill_PreservesSession|TestProcessExit_PreservesSession|TestWriteState_SetsUpdatedAt)' 2>&1 | tail -20 && go test ./pkg/shim/runtime/acp/... -count=1 2>&1 | tail -5

## Files Likely Touched

- pkg/shim/runtime/acp/runtime.go
- pkg/shim/runtime/acp/runtime_test.go
