# S01: Dead code removal — UAT

**Milestone:** M014
**Written:** 2026-04-14T14:50:22.062Z

## UAT: S01 Dead Code Removal

### Preconditions
- Repository checked out at post-S01 state
- Go toolchain available

### Test 1: No dead symbol references in Go source
**Steps:**
1. Run: `rg 'EventTypeFileWrite|EventTypeFileRead|EventTypeCommand|FileWriteEvent|FileReadEvent|CommandEvent' --type go --glob '!docs/plan/*'`
2. Verify exit code is 1 (no matches found)
3. Verify stdout is empty

**Expected:** Zero references to any of the six removed symbols anywhere in Go source files (excluding docs/plan/).

### Test 2: pkg/shim compiles
**Steps:**
1. Run: `go build ./pkg/shim/...`

**Expected:** Exit code 0, no compilation errors.

### Test 3: pkg/shim tests pass
**Steps:**
1. Run: `go test ./pkg/shim/...`

**Expected:** Exit code 0. `pkg/shim/server` and `pkg/shim/runtime/acp` tests all pass.

### Test 4: No regression in event type decode surface
**Steps:**
1. Open `pkg/shim/api/shim_event.go`
2. Verify there are no case branches for `EventTypeFileWrite`, `EventTypeFileRead`, or `EventTypeCommand` in either the type-switch or string-switch decode functions.
3. Open `pkg/shim/api/event_constants.go`
4. Verify the three constants are absent.

**Expected:** None of the removed symbols appear in decode logic or constant declarations.

### Edge Cases
- The remaining event types (text, tool_call, turn_start, etc.) must still decode correctly — covered by `go test ./pkg/shim/...` passing.
