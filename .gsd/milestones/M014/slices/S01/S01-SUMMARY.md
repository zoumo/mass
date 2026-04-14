---
id: S01
parent: M014
milestone: M014
provides:
  - ["Clean event type surface — only ACP-sourced event types remain in pkg/shim/api"]
requires:
  []
affects:
  - ["S04"]
key_files:
  - ["pkg/shim/api/event_constants.go", "pkg/shim/api/event_types.go", "pkg/shim/api/shim_event.go", "pkg/shim/server/translator_test.go", "pkg/shim/server/translate_rich_test.go"]
key_decisions:
  - ["Removed dead event types without replacement — they had no ACP source and no production consumers"]
patterns_established:
  - ["Pure deletion slices: remove symbols, decode cases, and test entries in lockstep to keep the test surface accurate"]
observability_surfaces:
  - none
drill_down_paths:
  []
duration: ""
verification_result: passed
completed_at: 2026-04-14T14:50:22.061Z
blocker_discovered: false
---

# S01: Dead code removal

**Removed EventTypeFileWrite, EventTypeFileRead, EventTypeCommand constants and FileWriteEvent, FileReadEvent, CommandEvent structs plus all decode/test references from pkg/shim.**

## What Happened

Slice S01 removed six dead symbols from pkg/shim that never had an ACP source and were misleading API surface:

**Constants removed** from `event_constants.go`: `EventTypeFileWrite`, `EventTypeFileRead`, `EventTypeCommand`.

**Structs removed** from `event_types.go`: `FileWriteEvent`, `FileReadEvent`, `CommandEvent` — each with doc comment and `eventType()` method.

**Decode paths removed** from `shim_event.go`: 3 type-switch cases in the unmarshal closure, 3 string-switch cases in `decodeEventPayload`.

**Test entries removed**: 3 entries from `TestEventTypes` table in `translator_test.go`, 3 entries from `TestTranslateRich_ShimEventDecode_AllEventTypes` in `translate_rich_test.go`.

All changes were pure deletions — no logic modifications to surrounding code. The remaining event type surface accurately reflects what ACP actually produces.

## Verification

**All 3 verification checks passed:**

1. `go build ./pkg/shim/...` — exit 0, compiles cleanly.
2. `go test ./pkg/shim/...` — exit 0, all tests pass (server 1.5s, acp cached).
3. `rg 'EventTypeFileWrite|EventTypeFileRead|EventTypeCommand|FileWriteEvent|FileReadEvent|CommandEvent' --type go --glob '!docs/plan/*'` — exit 1 (zero matches), confirming complete removal across the entire Go codebase.

## Requirements Advanced

None.

## Requirements Validated

- R058 — rg confirms zero references; go build and go test clean

## New Requirements Surfaced

None.

## Requirements Invalidated or Re-scoped

None.

## Deviations

None.

## Known Limitations

None.

## Follow-ups

None.

## Files Created/Modified

None.
