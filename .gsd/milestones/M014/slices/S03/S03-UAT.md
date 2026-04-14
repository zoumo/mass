# S03: writeState read-modify-write refactor — UAT

**Milestone:** M014
**Written:** 2026-04-14T15:27:22.154Z

## UAT: writeState read-modify-write refactor

### Preconditions
- Go toolchain available; `make build` succeeds
- Mock agent binary `testdata/mock-agent/agent` exists and is executable (built by test suite)
- No stale state.json files in test temp directories

### Test 1: Kill() preserves Session metadata
1. Run `go test ./pkg/shim/runtime/acp/... -v -run TestRuntimeSuite/TestKill_PreservesSession -count=1`
2. **Expected**: Test creates an agent, injects Session with AgentInfo.Name=="test-agent", calls Kill(), then reads state.json
3. **Assert**: status==stopped AND Session.AgentInfo.Name=="test-agent" AND UpdatedAt is valid RFC3339Nano
4. **Result**: PASS

### Test 2: Process-exit (SIGKILL) preserves Session metadata
1. Run `go test ./pkg/shim/runtime/acp/... -v -run TestRuntimeSuite/TestProcessExit_PreservesSession -count=1`
2. **Expected**: Test creates an agent, injects Session, sends SIGKILL to process, waits for status==stopped
3. **Assert**: Session.AgentInfo.Name=="test-agent" AND UpdatedAt is non-empty
4. **Result**: PASS

### Test 3: UpdatedAt stamped on every write
1. Run `go test ./pkg/shim/runtime/acp/... -v -run TestRuntimeSuite/TestWriteState_SetsUpdatedAt -count=1`
2. **Expected**: Test creates an agent, reads UpdatedAt (non-empty, valid RFC3339Nano), calls Kill(), reads UpdatedAt again
3. **Assert**: Post-Kill UpdatedAt >= post-Create UpdatedAt (monotonic increase)
4. **Result**: PASS

### Test 4: No regression in existing tests
1. Run `go test ./pkg/shim/runtime/acp/... -count=1`
2. **Expected**: All 9 tests pass (6 pre-existing + 3 new)
3. **Result**: PASS

### Test 5: No old-style writeState calls remain
1. Run `grep -c 'writeState(apiruntime.State{' pkg/shim/runtime/acp/runtime.go`
2. **Expected**: 0 matches — all call sites use closure pattern
3. **Result**: 0

### Test 6: Build verification
1. Run `make build`
2. **Expected**: agentd + agentdctl compile without errors
3. **Result**: PASS

### Edge Cases
- **First write (no state.json)**: writeState creates zero State, applies closure, writes — verified by bootstrap-started being the first write in every test
- **Concurrent prompt cycles**: Prompt() no longer has standalone ReadState; writeState's read-modify-write prevents data loss between prompt-started and prompt-completed
- **Closure panic safety**: If the closure panics, the write is not committed (spec.WriteState is never reached)
