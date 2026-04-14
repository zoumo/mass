# S06: Session metadata hook chain — UAT

**Milestone:** M014
**Written:** 2026-04-14T16:57:27.142Z

## UAT: S06 — Session metadata hook chain

### Preconditions
- Repository cloned at working directory
- Go toolchain available
- `make build` succeeds

---

### TC-01: ConfigOptionUpdate flows through to state.json
**Steps:**
1. Run `go test ./pkg/shim/runtime/acp/... -count=1 -v -run 'TestRuntimeSuite/TestMetadataHookChain_ConfigOption'`
2. Observe: Manager.Create() starts agent → UpdateSessionMetadata called with config options apply closure → ReadState shows state.json.session.configOptions populated with the injected values

**Expected:** Test PASS. state.json contains the configOptions written by the apply closure.

---

### TC-02: state_change event emitted with correct metadata fields
**Steps:**
1. Run `go test ./pkg/shim/runtime/acp/... -count=1 -v -run 'TestRuntimeSuite/TestUpdateSessionMetadata_EmitsStateChange'`
2. Observe: stateChangeHook receives StateChange with PreviousStatus==Status (metadata-only), Reason=="config-updated", SessionChanged==["configOptions"]

**Expected:** Test PASS. The state_change event is metadata-only (no status transition) and carries sessionChanged identifying which fields were modified.

---

### TC-03: Kill() preserves session metadata
**Steps:**
1. Run `go test ./pkg/shim/runtime/acp/... -count=1 -v -run 'TestRuntimeSuite/TestUpdateSessionMetadata_PreservedByKill'`
2. Observe: UpdateSessionMetadata writes configOptions → Kill() terminates agent → ReadState shows configOptions still present alongside status==stopped

**Expected:** Test PASS. Kill() uses the writeState closure pattern (S03) which preserves Session data; configOptions survive the lifecycle transition.

---

### TC-04: EventCounts flushed on every writeState call
**Steps:**
1. Run `go test ./pkg/shim/runtime/acp/... -count=1 -v -run 'TestRuntimeSuite/TestWriteState_FlushesEventCounts'`
2. Observe: SetEventCountsFn injects mock returning {"text":5,"tool_call":3} → Kill() triggers writeState → ReadState shows EventCounts populated with injected values

**Expected:** Test PASS. EventCounts present in state.json after Kill (writeState flushes unconditionally).

---

### TC-05: Translator hook fires only for metadata event types
**Steps:**
1. Run `go test ./pkg/shim/server/... -count=1 -v -run 'TestSessionMetadataHook_AllFourTypes'`
2. Observe: Inject AvailableCommandsEvent, ConfigOptionEvent, SessionInfoEvent, CurrentModeEvent → hook fires 4 times with correct event types

**Expected:** Test PASS. All 4 metadata types trigger the hook.

---

### TC-06: Translator hook ignores non-metadata events
**Steps:**
1. Run `go test ./pkg/shim/server/... -count=1 -v -run 'TestSessionMetadataHook_IgnoresNonMetadata'`
2. Observe: Inject text notification → hook is NOT called

**Expected:** Test PASS. Non-metadata event types (text, thinking, tool_call, etc.) do not trigger the session metadata hook.

---

### TC-07: Full test suite regression check
**Steps:**
1. Run `go test ./pkg/shim/runtime/acp/... ./pkg/shim/server/... -count=1`
2. Observe: All existing tests plus new tests pass

**Expected:** Both packages PASS with zero failures or regressions.

---

### TC-08: Build succeeds
**Steps:**
1. Run `make build`
2. Observe: agentd and agentdctl binaries built without errors

**Expected:** Clean build. No compilation errors from new code or wiring.

---

### Edge Cases
- **Nil Session:** UpdateSessionMetadata initializes state.Session to &SessionState{} if nil before calling apply — prevents nil pointer dereference when agent was created before S05 bootstrap capture was available.
- **Failed ReadState:** If state.json doesn't exist (agent not yet created), UpdateSessionMetadata returns an error without emitting a state_change — no orphaned events.
- **Failed WriteState:** If state.json write fails, UpdateSessionMetadata returns the error and does NOT emit a state_change — no event/state divergence.
- **Concurrent access:** UpdateSessionMetadata acquires m.mu for the full read-modify-write cycle, preventing interleaved updates from simultaneous metadata notifications.
