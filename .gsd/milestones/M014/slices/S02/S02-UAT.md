# S02: state.json type definitions — UAT

**Milestone:** M014
**Written:** 2026-04-14T15:07:29.387Z

## UAT: S02 — state.json type definitions

### Preconditions
- Go toolchain available (`go version` returns 1.24+)
- Working directory is repository root

### Test 1: Full State Round-Trip (Demo Criterion)
1. Run `go test ./pkg/runtime-spec/... -v -run TestFullStateRoundTrip`
2. **Expected:** Test PASS — WriteState with full SessionState (AgentInfo, Capabilities with Fork, AvailableCommands with Unstructured input variant, ConfigOptions with Select variant containing both Ungrouped and Grouped ConfigSelectOptions, SessionInfo, CurrentMode) plus EventCounts and UpdatedAt → ReadState reproduces identical values via deep-equal.

### Test 2: ConfigOption Select Variant Discrimination
1. In TestFullStateRoundTrip, the ConfigOption with Type="select" round-trips through JSON.
2. **Expected:** MarshalJSON writes `"type":"select"` discriminator; UnmarshalJSON restores ConfigOptionSelect struct with all fields including ConfigSelectOptions union.

### Test 3: AvailableCommandInput Unstructured Variant Discrimination  
1. In TestFullStateRoundTrip, one AvailableCommand has Input with Unstructured variant (Hint field present).
2. **Expected:** MarshalJSON writes field-presence discriminator; UnmarshalJSON restores UnstructuredCommandInput with Hint value.

### Test 4: Nil Session Edge Case
1. Run `go test ./pkg/runtime-spec/... -v -run TestStateRoundTripNilSession`
2. **Expected:** Test PASS — State with nil Session written; read back confirms Session is nil (no spurious `"session": {}` in JSON).

### Test 5: Nil EventCounts Edge Case
1. Run `go test ./pkg/runtime-spec/... -v -run TestStateRoundTripEmptyEventCounts`
2. **Expected:** Test PASS — State with nil EventCounts written; read back confirms EventCounts is nil.

### Test 6: No Cross-Package Import
1. Run `grep 'shim/api' pkg/runtime-spec/api/session.go`
2. **Expected:** No output (exit 1) — session.go does not import pkg/shim/api.

### Test 7: Build Integrity
1. Run `go build ./pkg/runtime-spec/...`
2. **Expected:** Exit 0, no output — all types compile cleanly.

### Test 8: Existing Tests Unbroken
1. Run `go test ./pkg/runtime-spec/... -v -run TestStateSuite`
2. **Expected:** All 10 tests PASS (7 existing + 3 new).
