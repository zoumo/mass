---
estimated_steps: 36
estimated_files: 1
skills_used: []
---

# T02: Add tests proving Session preservation across Kill and process-exit

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

## Inputs

- ``pkg/shim/runtime/acp/runtime.go` — refactored writeState from T01`
- ``pkg/shim/runtime/acp/runtime_test.go` — existing test suite with newManager helper`
- ``pkg/runtime-spec/state.go` — WriteState function for injecting Session into state.json`
- ``pkg/runtime-spec/api/state.go` — State, SessionState types`
- ``pkg/runtime-spec/api/session.go` — AgentInfo, AvailableCommand types`

## Expected Output

- ``pkg/shim/runtime/acp/runtime_test.go` — three new tests proving Session preservation and UpdatedAt stamping`

## Verification

go test ./pkg/shim/runtime/acp/... -count=1 -v -run 'TestRuntimeSuite/(TestKill_PreservesSession|TestProcessExit_PreservesSession|TestWriteState_SetsUpdatedAt)' 2>&1 | tail -20 && go test ./pkg/shim/runtime/acp/... -count=1 2>&1 | tail -5
