---
id: S07
parent: M014
milestone: M014
provides:
  - ["runtime/status returns real-time EventCounts from Translator memory", "Design docs reflect full M014 state schema (session, eventCounts, updatedAt, sessionChanged)"]
requires:
  []
affects:
  []
key_files:
  - ["pkg/shim/server/service.go", "pkg/shim/server/service_test.go", "docs/design/runtime/shim-rpc-spec.md", "docs/design/runtime/runtime-spec.md"]
key_decisions:
  - ["EventCounts overlay uses full replacement (not merge) because Translator holds authoritative cumulative counts", "agent-shim.md intentionally not updated per K029 — descriptive only, defers to spec docs"]
patterns_established:
  - ["Status() overlay pattern: read state from disk, overlay real-time fields from in-memory sources before returning to caller"]
observability_surfaces:
  - none
drill_down_paths:
  []
duration: ""
verification_result: passed
completed_at: 2026-04-14T17:18:17.570Z
blocker_discovered: false
---

# S07: runtime/status overlay + doc updates

**Status() now overlays Translator's real-time in-memory EventCounts onto state.json snapshots, and design docs reflect the full M014-enriched state schema (session, eventCounts, updatedAt, sessionChanged).**

## What Happened

S07 is the final slice of M014, closing the loop on the enriched state.json pipeline with two deliverables:

**T01 — Status() EventCounts overlay:** The `Service.Status()` method in `pkg/shim/server/service.go` previously returned EventCounts exactly as read from state.json via `mgr.GetState()`. Because EventCounts is only flushed to disk piggy-backed on lifecycle transitions and metadata updates (S03/S04 pattern), the file value was stale between writes. Added a single-line overlay: `st.EventCounts = s.trans.EventCounts()` — this replaces the disk-read EventCounts with the authoritative real-time counts held in Translator memory. The overlay uses full replacement, not merge, because Translator is the single authoritative source for cumulative counts.

A test `TestStatus_EventCountsOverlay` in `pkg/shim/server/service_test.go` proves this works: writes a state.json with stale EventCounts (`{"stale_event": 99}`), creates a Translator with different in-memory counts (via 2 NotifyStateChange broadcasts), calls Status(), and asserts the returned EventCounts matches Translator memory — the stale key is absent from the response.

**T02 — Design doc updates:** Both normative design docs were updated to reflect the M014-enriched state schema:
- `shim-rpc-spec.md`: runtime/status response example now includes `updatedAt`, `session` (agentInfo, capabilities), and `eventCounts`; a prose note explains the Translator overlay semantics; a second `state_change` example shows metadata-only changes with `sessionChanged`; prose lists all 6 possible sessionChanged values.
- `runtime-spec.md`: State Example JSON now includes `updatedAt`, `session`, and `eventCounts`; field descriptions added after the "MAY include additional properties" line.
- `agent-shim.md` was intentionally NOT updated per K029 (descriptive only, defers to spec docs).

All M014 slices (S01–S07) are now complete. The enriched state.json pipeline is fully operational: dead placeholders removed (S01), types enriched (S02), read-modify-write safe (S03), EventCounts tracked in memory (S04), bootstrap capabilities captured (S05), session metadata hook chain wired (S06), Status() overlay and docs finalized (S07).

## Verification

All slice-level must-haves verified:

1. `go test ./pkg/shim/server/... -run TestStatus -v -count=1` — TestStatus_EventCountsOverlay PASS (1.226s)
2. `make build` — agentd + agentdctl compile cleanly, exit 0
3. `go test ./...` — all packages pass (105s), exit 0
4. `grep -q 'eventCounts' docs/design/runtime/shim-rpc-spec.md` — PASS
5. `grep -q 'eventCounts' docs/design/runtime/runtime-spec.md` — PASS
6. `grep -q 'sessionChanged' docs/design/runtime/shim-rpc-spec.md` — PASS
7. `grep -q 'updatedAt' docs/design/runtime/runtime-spec.md` — PASS

R055 (eventCounts operability) updated to validated status with evidence spanning S03–S07.

## Requirements Advanced

None.

## Requirements Validated

- R055 — TestStatus_EventCountsOverlay proves Status() returns Translator in-memory counts, not stale state.json; combined with S04 (memory tracking), S03 (disk flush on every write), full pipeline validated end-to-end

## New Requirements Surfaced

None.

## Requirements Invalidated or Re-scoped

None.

## Operational Readiness

None.

## Deviations

None.

## Known Limitations

If Translator is nil, Status() will panic on s.trans.EventCounts() — acceptable since Service construction requires a non-nil Translator.

## Follow-ups

None — this is the final slice of M014.

## Files Created/Modified

None.
