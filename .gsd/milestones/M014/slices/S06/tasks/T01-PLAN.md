---
estimated_steps: 48
estimated_files: 2
skills_used: []
---

# T01: Manager.UpdateSessionMetadata + SetEventCountsFn + writeState EventCounts flush

Add the Manager-side infrastructure for session metadata updates and EventCounts flushing.

**Context:** Manager.writeState (from S03) is a read-modify-write closure that preserves Session across lifecycle writes. This task adds:
1. `eventCountsFn func() map[string]int` field on Manager + `SetEventCountsFn` setter — allows the Translator's EventCounts() to be injected.
2. Modify `writeState` to flush EventCounts on every write: after the closure runs and before `spec.WriteState`, call `m.eventCountsFn()` and set `state.EventCounts`.
3. `UpdateSessionMetadata(changed []string, reason string, apply func(*apiruntime.State)) error` — exported method that:
   - Acquires m.mu
   - Reads current state via spec.ReadState (error if not found — agent must exist)
   - Ensures state.Session is non-nil (create `&apiruntime.SessionState{}` if nil)
   - Calls `apply(&state)` to update specific session fields
   - Sets `state.UpdatedAt = time.Now().UTC().Format(time.RFC3339Nano)`
   - Sets `state.EventCounts` from eventCountsFn if available
   - Writes via spec.WriteState
   - Copies stateChangeHook reference + builds StateChange with SessionChanged
   - Releases m.mu
   - Calls hook outside lock (D120 lock order: Manager.mu released before Translator.mu acquired)

**Lock semantics:** UpdateSessionMetadata acquires m.mu for the read-modify-write-emit cycle. The stateChangeHook is called AFTER releasing m.mu, matching the existing emitStateChange pattern. The StateChange emitted has previousStatus==status (metadata-only) and SessionChanged populated.

**Differences from lifecycle writeState:** writeState only emits state_change on status transitions. UpdateSessionMetadata ALWAYS emits state_change (metadata-only).

## Steps

1. Add `eventCountsFn func() map[string]int` field to Manager struct and `SetEventCountsFn(fn func() map[string]int)` method.
2. In `writeState`, after `apply(&state)` and `state.UpdatedAt = ...`, add: `if m.eventCountsFn != nil { state.EventCounts = m.eventCountsFn() }`.
3. Add `UpdateSessionMetadata(changed []string, reason string, apply func(*apiruntime.State)) error` method:
   - Lock m.mu
   - Read state via spec.ReadState; return error if fails (no ErrNotExist guard — state must exist)
   - If state.Session == nil, set state.Session = &apiruntime.SessionState{}
   - apply(&state)
   - state.UpdatedAt = time.Now().UTC().Format(time.RFC3339Nano)
   - if m.eventCountsFn != nil { state.EventCounts = m.eventCountsFn() }
   - spec.WriteState(m.stateDir, state); return on error (do NOT emit state_change)
   - Copy hook := m.stateChangeHook
   - Build change := StateChange{SessionID: state.ID, PreviousStatus: state.Status, Status: state.Status, PID: state.PID, Reason: reason, SessionChanged: changed}
   - Unlock m.mu
   - If hook != nil, call hook(change)
4. Add tests in runtime_test.go:
   - **TestUpdateSessionMetadata_UpdatesStateJSON**: Create manager → Create() → call UpdateSessionMetadata with configOptions apply → ReadState → verify session.configOptions populated + UpdatedAt set
   - **TestUpdateSessionMetadata_EmitsStateChange**: Create → register stateChangeHook → UpdateSessionMetadata → verify hook called with correct PreviousStatus==Status, Reason, SessionChanged
   - **TestUpdateSessionMetadata_PreservedByKill**: Create → UpdateSessionMetadata → Kill → ReadState → verify configOptions still present
   - **TestWriteState_FlushesEventCounts**: Create → SetEventCountsFn(mock returning counts) → trigger writeState via Kill → ReadState → verify EventCounts populated

## Must-Haves

- [ ] eventCountsFn field + SetEventCountsFn on Manager
- [ ] writeState flushes EventCounts on every write
- [ ] UpdateSessionMetadata exported method with correct lock/emit semantics
- [ ] UpdateSessionMetadata emits state_change with previousStatus==status and sessionChanged populated
- [ ] Hook called OUTSIDE m.mu (no nested lock)
- [ ] All new tests pass; existing tests pass (zero regressions)

## Verification

- `go test ./pkg/shim/runtime/acp/... -count=1 -v -run 'TestRuntimeSuite/(TestUpdateSessionMetadata|TestWriteState_FlushesEventCounts)'` — all PASS
- `go test ./pkg/shim/runtime/acp/... -count=1` — full suite PASS
- `make build` — clean

## Inputs

- ``pkg/shim/runtime/acp/runtime.go` — Manager with writeState closure pattern (S03), emitStateChange, StateChange struct with SessionChanged`
- ``pkg/shim/runtime/acp/runtime_test.go` — existing test helpers (newManagerWithStateDir, writeSessionToStateDir)`
- ``pkg/runtime-spec/api/state.go` — State struct with Session *SessionState, EventCounts map[string]int`
- ``pkg/runtime-spec/api/session.go` — SessionState with ConfigOptions, AvailableCommands, etc.`

## Expected Output

- ``pkg/shim/runtime/acp/runtime.go` — Manager gains eventCountsFn, SetEventCountsFn, UpdateSessionMetadata; writeState flushes EventCounts`
- ``pkg/shim/runtime/acp/runtime_test.go` — TestUpdateSessionMetadata_UpdatesStateJSON, TestUpdateSessionMetadata_EmitsStateChange, TestUpdateSessionMetadata_PreservedByKill, TestWriteState_FlushesEventCounts`

## Verification

go test ./pkg/shim/runtime/acp/... -count=1 -v -run 'TestRuntimeSuite/(TestUpdateSessionMetadata|TestWriteState_FlushesEventCounts)' && go test ./pkg/shim/runtime/acp/... -count=1 && make build
