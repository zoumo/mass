---
id: S05
parent: M014
milestone: M014
provides:
  - ["state.json.session populated at bootstrap-complete with AgentInfo and Capabilities from ACP InitializeResponse", "StateChangeEvent.SessionChanged []string field for metadata change events", "NotifyStateChange accepts sessionChanged parameter", "Synthetic bootstrap-metadata event pattern for post-Start() metadata signals"]
requires:
  []
affects:
  - ["S06"]
key_files:
  - ["pkg/shim/runtime/acp/runtime.go", "pkg/shim/runtime/acp/runtime_test.go", "pkg/shim/api/event_types.go", "pkg/shim/server/translator.go", "pkg/shim/server/translator_test.go", "cmd/agentd/subcommands/shim/command.go", "internal/testutil/mockagent/main.go"]
key_decisions:
  - ["D124: Bootstrap capabilities signaled via synthetic state_change after Translator.Start() — emitted idle→idle with bootstrap-metadata reason because Manager.Create() finishes before Translator exists"]
patterns_established:
  - ["convertInitializeToSession maps ACP SDK types to runtime-spec/api types — reuse pattern for future ACP→state.json field mappings", "Synthetic idle→idle state_change with reason + sessionChanged for metadata-only events (no status transition)", "Variable declared before defer block to keep initResp in scope for both error handling and bootstrap closure"]
observability_surfaces:
  - none
drill_down_paths:
  []
duration: ""
verification_result: passed
completed_at: 2026-04-14T16:24:47.975Z
blocker_discovered: false
---

# S05: ACP bootstrap capabilities capture

**ACP InitializeResponse is captured at bootstrap-complete and written to state.Session; a synthetic bootstrap-metadata state_change event is emitted after Translator.Start() so subscribers discover agent identity and capabilities via history backfill.**

## What Happened

This slice wired ACP bootstrap capabilities into the state.json session pipeline and the event log.

**T01 — Capture InitializeResponse into state.Session:** The mockagent was updated to return a populated InitializeResponse (AgentInfo name="mockagent" version="0.1.0", LoadSession=true, Sse=true, Image=true). A new `convertInitializeToSession()` function in runtime.go maps all ACP types (Implementation → AgentInfo, AgentCapabilities → AgentCapabilities with Mcp/Prompt/Session sub-capabilities including nil Fork handling). Manager.Create() now captures `initResp` (previously discarded as `_`) and the bootstrap-complete writeState closure writes `s.Session = convertInitializeToSession(initResp)`. The `initResp` variable is declared before the defer block so it stays in scope for both error handling and the bootstrap closure. TestCreate_PopulatesSession verifies all fields and confirms Session survives Kill() (leveraging S03's closure pattern).

**T02 — SessionChanged field + synthetic bootstrap-metadata event:** StateChangeEvent (api layer) and StateChange (runtime layer) gained a `SessionChanged []string` field with omitempty JSON semantics — nil renders as absent, preserving backward compat for lifecycle events. NotifyStateChange was extended to accept `sessionChanged []string` as a 5th parameter. In command.go, two changes: (1) the stateChangeHook closure relays `change.SessionChanged`; (2) immediately after `trans.Start()`, a synthetic `NotifyStateChange("idle","idle",pid,"bootstrap-metadata",["agentInfo","capabilities"])` is emitted — idle→idle because it's metadata-only, no status transition. All 6 existing callers in translator_test.go were updated to pass nil. TestNotifyStateChange_WithSessionChanged verifies the event roundtrips correctly through the event log.

Both tasks had zero deviations from plan and zero known issues.

## Verification

All slice-level verification checks pass:

1. **TestCreate_PopulatesSession** — `go test ./pkg/shim/runtime/acp/... -count=1 -v -run TestRuntimeSuite/TestCreate_PopulatesSession` → PASS (3.1s). Confirms state.json.session.agentInfo.name=="mockagent", version=="0.1.0", capabilities.loadSession==true, mcpCapabilities.sse==true, promptCapabilities.image==true. Session survives Kill().

2. **TestNotifyStateChange_WithSessionChanged** — `go test ./pkg/shim/server/... -count=1 -v -run TestNotifyStateChange_WithSessionChanged` → PASS (0.5s). Confirms bootstrap-metadata event with sessionChanged:["agentInfo","capabilities"], type=="state_change", category=="runtime", idle→idle status.

3. **Full ACP runtime suite** — `go test ./pkg/shim/runtime/acp/... -count=1` → ok (3.0s). Zero regressions across all runtime tests.

4. **Full server/translator suite** — `go test ./pkg/shim/server/... -count=1` → ok (1.6s). Zero regressions.

5. **Build** — `make build` → builds agentd and agentdctl successfully.

## Requirements Advanced

None.

## Requirements Validated

- R056 — TestCreate_PopulatesSession proves session fields match mock InitializeResponse; TestNotifyStateChange_WithSessionChanged proves bootstrap-metadata event with sessionChanged appears in event log

## New Requirements Surfaced

None.

## Requirements Invalidated or Re-scoped

None.

## Operational Readiness

None.

## Deviations

None.

## Known Limitations

None.

## Follow-ups

S06 (session metadata hook chain) will wire runtime ACP notifications (e.g. ConfigOptionUpdate) into state.json updates with state_change events, building on the SessionChanged field and NotifyStateChange pattern established here.

## Files Created/Modified

None.
