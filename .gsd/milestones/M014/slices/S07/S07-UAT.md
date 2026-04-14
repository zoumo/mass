# S07: runtime/status overlay + doc updates — UAT

**Milestone:** M014
**Written:** 2026-04-14T17:18:17.572Z

## UAT: S07 — runtime/status overlay + doc updates

### Preconditions
- M014 S01–S06 complete (state types, writeState closure, Translator EventCounts, bootstrap capture, session metadata hook)
- `make build` exits 0
- `go test ./...` exits 0

### Test Case 1: Status() returns Translator memory counts, not stale file counts
1. Run `go test ./pkg/shim/server/... -run TestStatus_EventCountsOverlay -v -count=1`
2. **Expected:** Test PASS — the returned `EventCounts` matches Translator in-memory counts (2 state_change events), NOT the stale `{"stale_event": 99}` written to state.json

### Test Case 2: No regressions in shim server package
1. Run `go test ./pkg/shim/server/... -count=1`
2. **Expected:** All tests pass, exit 0

### Test Case 3: Full test suite passes
1. Run `go test ./...`
2. **Expected:** All packages pass, exit 0

### Test Case 4: Build succeeds
1. Run `make build`
2. **Expected:** agentd and agentdctl binaries produced, exit 0

### Test Case 5: shim-rpc-spec.md documents eventCounts in runtime/status response
1. Open `docs/design/runtime/shim-rpc-spec.md`
2. Find the `runtime/status` response JSON example
3. **Expected:** Example includes `eventCounts`, `session` (with `agentInfo` and `capabilities`), and `updatedAt` fields
4. **Expected:** Prose note explains Translator memory overlay semantics

### Test Case 6: shim-rpc-spec.md documents sessionChanged in state_change
1. Open `docs/design/runtime/shim-rpc-spec.md`
2. Find the `state_change` content examples
3. **Expected:** A metadata-only state_change example includes `"sessionChanged": ["configOptions"]`
4. **Expected:** Prose note lists all 6 possible sessionChanged values (agentInfo, capabilities, availableCommands, configOptions, sessionInfo, currentMode)

### Test Case 7: runtime-spec.md documents enriched state schema
1. Open `docs/design/runtime/runtime-spec.md`
2. Find the State Example JSON
3. **Expected:** Example includes `updatedAt`, `session` (with `agentInfo`), and `eventCounts` fields
4. **Expected:** Field descriptions appear after the "MAY include additional properties" line

### Edge Cases
- If Translator is nil, Status() will panic on `s.trans.EventCounts()` — this is existing behavior for nil-guard-free fields and is acceptable since a Service should never be constructed with a nil Translator
- EventCounts overlay is full replacement, not merge — if state.json has counts not in Translator memory, they are discarded (this is correct because Translator is the single authoritative source)
