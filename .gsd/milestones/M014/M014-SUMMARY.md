---
id: M014
title: "Enrich state.json + Session Metadata Pipeline"
status: complete
completed_at: 2026-04-14T17:42:21.757Z
key_decisions:
  - D119: writeState closure pattern — func(*apiruntime.State) for read-modify-write, preventing Session/EventCounts clobber
  - D120: Session metadata hook chain — Translator.sessionMetadataHook after broadcast, not Manager as second ACP consumer (preserves single-consumer invariant)
  - D121: Single eventCounts counting site in broadcast() — covers all event origins, fail-closed on append failure
  - D122: updatedAt and eventCounts are derived fields — never trigger independent state_change (prevents infinite recursion)
  - D123: runtime-spec/api types self-contained — no import of pkg/agentrun/api; MarshalJSON copied with 'state:' error prefix
  - D124: Bootstrap capabilities signaled via synthetic idle→idle state_change after Translator.Start() — subscribers discover via history backfill
key_files:
  - pkg/runtime-spec/api/session.go — All session metadata types (SessionState, AgentInfo, AgentCapabilities, union types)
  - pkg/runtime-spec/api/state.go — State struct extended with UpdatedAt, Session, EventCounts
  - pkg/agentrun/runtime/acp/runtime.go — writeState closure pattern, convertInitializeToSession, UpdateSessionMetadata, SetEventCountsFn
  - pkg/agentrun/server/translator.go — eventCounts tracking, EventCounts(), SetSessionMetadataHook, maybeNotifyMetadata
  - cmd/agentd/subcommands/shim/session_update.go — buildSessionUpdate, sort helpers, type-switch conversion for 4 metadata types
  - cmd/agentd/subcommands/shim/command.go — Wiring: SetSessionMetadataHook, SetEventCountsFn, synthetic bootstrap-metadata event
  - pkg/agentrun/api/event_types.go — StateChangeEvent.SessionChanged field; dead types removed
  - pkg/agentrun/server/service.go — Status() EventCounts overlay
  - pkg/agentrun/runtime/acp/runtime_test.go — 9 integration tests including Session preservation, metadata hook chain
  - pkg/agentrun/server/translator_test.go — EventCounts, SessionMetadataHook, metadata event routing tests
lessons_learned:
  - errors.Is(err, os.ErrNotExist) is required when ReadState wraps errors with fmt.Errorf — os.IsNotExist doesn't unwrap (K081)
  - Closure-based state mutation (writeState pattern) is the cleanest way to protect existing fields from lifecycle writes that only care about status
  - Synthetic idle→idle state_change events are a useful pattern for metadata-only signals — they don't cause status transitions but flow through the normal event pipeline
  - Single counting site in broadcast() covers all event origins (ACP-translated, manual, state_change) — counting at translate() would miss non-ACP events
  - Copy-on-read pattern for diagnostic counters (mutex + map copy in EventCounts()) prevents exposing internal state to callers
  - Sort helpers for deterministic JSON output (sortCommandsByName, sortConfigOptionsByID) prevent flaky test assertions and noisy state.json diffs
  - Status() overlay pattern (read disk, overlay real-time fields) solves the staleness gap between disk writes without adding extra write frequency
---

# M014: Enrich state.json + Session Metadata Pipeline

**state.json became a reliable session capability snapshot with enriched types, safe read-modify-write, real-time EventCounts, ACP bootstrap capture, end-to-end session metadata hook chain, and Status() overlay — all backed by comprehensive integration tests.**

## What Happened

M014 delivered the session metadata pipeline across 7 slices (S01–S07), transforming state.json from a bare lifecycle status file into a rich, reliable session snapshot.

**S01 — Dead placeholder removal.** Removed 6 dead symbols (EventTypeFileWrite/FileRead/Command constants and their wire types) that had no ACP source and were misleading API surface. Pure deletion — no logic changes. Verification: `rg` returns zero matches across the entire Go codebase.

**S02 — State type enrichment.** Created `pkg/runtime-spec/api/session.go` with all session metadata types: SessionState (6 sub-fields), AgentInfo, AgentCapabilities, McpCapabilities, PromptCapabilities, SessionCapabilities, SessionForkCapabilities, SessionInfo, plus 3 discriminated-union type pairs (AvailableCommand/AvailableCommandInput, ConfigOption/ConfigOptionSelect, ConfigSelectOptions/ConfigSelectOption/ConfigSelectGroup) — each with custom MarshalJSON/UnmarshalJSON. Extended State struct with UpdatedAt, Session, EventCounts fields (all omitempty for backward compat). Round-trip tests prove full fidelity including nil edge cases.

**S03 — writeState read-modify-write refactor.** Converted `Manager.writeState` from accepting full `State` literals to `func(*apiruntime.State)` closures. All 7 call sites now mutate only the fields they care about; Session and EventCounts are preserved through Kill, process-exit, and prompt cycles. UpdatedAt is stamped unconditionally as a derived field callers cannot override. Integration tests prove Session survives Kill() and external SIGKILL. Key gotcha: `errors.Is(err, os.ErrNotExist)` required for wrapped errors from spec.ReadState (K081).

**S04 — Translator eventCounts.** Added `eventCounts map[string]int` to Translator with a single counting site in `broadcast()` — after `nextSeq++`, before fan-out. Fail-closed semantics: log-append failures skip the count increment. Thread-safe `EventCounts()` method returns a copy. Tests verify per-type counts through a full prompt turn and fail-closed behavior on append failure.

**S05 — ACP bootstrap capabilities capture.** `Manager.Create()` now captures `InitializeResponse` (previously discarded) and `convertInitializeToSession()` maps ACP types to runtime-spec types. Session written at bootstrap-complete. Synthetic `bootstrap-metadata` state_change event emitted after `Translator.Start()` (idle→idle, metadata-only) so subscribers discover capabilities via history backfill. `StateChangeEvent.SessionChanged` field added for metadata change events.

**S06 — Session metadata hook chain.** Wired the end-to-end pipeline: Translator.maybeNotifyMetadata (type-switch on 4 ACP notification types) → Manager.UpdateSessionMetadata (read-modify-write + state_change) → state.json updated with sessionChanged field. Lock order: Translator.mu → release → Manager.mu → release (no nesting). buildSessionUpdate converts apishim→apiruntime types with sort helpers for deterministic output. EventCounts flushed on every writeState call via SetEventCountsFn. command.go wires everything together.

**S07 — Status() overlay + doc updates.** `Service.Status()` overlays Translator's real-time in-memory EventCounts onto the state.json snapshot before returning — callers always get authoritative counts. Design docs (run-rpc-spec.md, runtime-spec.md) updated with enriched state schema examples.

Total: 19 non-GSD source files changed, +1613/-163 lines. 12 tasks across 7 slices, zero replans, zero blockers.

## Success Criteria Results

The roadmap defined success criteria per-slice via "After this" acceptance tests. All verified:

- **S01** ✅ `rg 'EventTypeFileWrite|EventTypeFileRead|EventTypeCommand|FileWriteEvent|FileReadEvent|CommandEvent' --type go --glob '!docs/plan/*'` returns exit 1 (zero matches). `go test ./pkg/agentrun/...` passes.
- **S02** ✅ `TestFullStateRoundTrip` proves WriteState→ReadState round-trip with full SessionState including ConfigOption Select variant + AvailableCommandInput Unstructured. EventCounts and UpdatedAt survive round-trip. `TestStateRoundTripNilSession` and `TestStateRoundTripEmptyEventCounts` pass.
- **S03** ✅ `TestKill_PreservesSession` proves Kill() → status==stopped AND Session still present. `TestProcessExit_PreservesSession` proves external SIGKILL preserves Session. `TestWriteState_SetsUpdatedAt` proves non-empty valid RFC3339Nano on every write with monotonic increase.
- **S04** ✅ `TestEventCounts_PromptTurn` exercises a full turn and verifies per-type counts (text, tool_call, turn_start, turn_end, user_message, state_change). `TestEventCounts_FailClosedOnAppendFailure` proves counts stay at 0 on failed append.
- **S05** ✅ `TestCreate_PopulatesSession` proves state.json.session.agentInfo.name=="mockagent", capabilities.loadSession==true from mock InitializeResponse. `TestNotifyStateChange_WithSessionChanged` proves bootstrap-metadata event with sessionChanged:["agentInfo","capabilities"] appears in event log.
- **S06** ✅ `TestMetadataHookChain_ConfigOption` proves full chain: ConfigOptionUpdate → state.json.session.configOptions updated → state_change with reason:config-updated and sessionChanged:["configOptions"]. `TestUpdateSessionMetadata_PreservedByKill` proves Kill() preserves configOptions.
- **S07** ✅ `TestStatus_EventCountsOverlay` proves Status() returns Translator's real-time EventCounts, not stale state.json values. `make build` + `go test ./pkg/agentrun/... ./pkg/runtime-spec/...` all pass.

## Definition of Done Results

- ✅ All 7 slices complete with status `complete` in DB
- ✅ All 12 tasks complete (S01:1, S02:2, S03:2, S04:1, S05:2, S06:2, S07:2)
- ✅ All slice summaries exist on disk (S01–S07 all have S##-SUMMARY.md)
- ✅ `make build` succeeds — agentd + agentdctl compile cleanly
- ✅ `go test ./pkg/agentrun/... ./pkg/runtime-spec/...` — all tests pass (0 failures)
- ✅ Cross-slice integration verified: S03's closure pattern proven to preserve S05/S06 session writes through Kill/exit; S04's EventCounts flushed via S06's SetEventCountsFn; S07's overlay reads S04's in-memory counts correctly
- ✅ Design docs updated (runtime-spec.md, run-rpc-spec.md) to reflect enriched schema

## Requirement Outcomes

| Requirement | Previous Status | New Status | Evidence |
|-------------|----------------|------------|----------|
| R053 | active | **validated** | S02 types + S05 bootstrap capture + S06 hook chain prove progressive session metadata population; TestCreate_PopulatesSession + TestMetadataHookChain_ConfigOption + TestUpdateSessionMetadata_PreservedByKill |
| R054 | validated | validated | TestMetadataHookChain_ConfigOption + TestSessionMetadataHook_AllFourTypes prove state_change with sessionChanged for all 4 metadata types |
| R055 | validated | validated | TestEventCounts_PromptTurn + TestStatus_EventCountsOverlay prove in-memory tracking and real-time overlay; EventCounts flushed on every state write |
| R056 | validated | validated | TestCreate_PopulatesSession + TestNotifyStateChange_WithSessionChanged prove bootstrap capture and synthetic event |
| R057 | validated | validated | TestKill_PreservesSession + TestProcessExit_PreservesSession prove closure pattern integrity |
| R058 | validated | validated | rg exit 1 confirms zero references; go build + go test clean |
| R059 | validated | validated | TestWriteState_SetsUpdatedAt proves RFC3339Nano stamping on every write |

## Deviations

None. All 7 slices delivered without replans or blockers. S04 had no code changes needed (implementation was already present from earlier work) — task reduced to verification-only.

## Follow-ups

Usage tracking (excluded from R053 per design — high-frequency, event stream only) could be considered for a future milestone if operators need usage metrics in state.json. The convertInitializeToSession pattern is reusable for any future ACP→state.json field mappings.
