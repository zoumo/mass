---
id: S01
parent: M011
milestone: M011
provides:
  - ["All ACP event data available for S02 tests", "17-type decodeEventPayload for envelope round-trip tests"]
requires:
  []
affects:
  []
key_files:
  - ["api/events.go", "pkg/events/types.go", "pkg/events/translator.go", "pkg/events/envelope.go", "docs/design/runtime/runtime-spec.md", "docs/design/runtime/shim-rpc-spec.md"]
key_decisions:
  - (none)
patterns_established:
  - (none)
observability_surfaces:
  - none
drill_down_paths:
  []
duration: ""
verification_result: passed
completed_at: 2026-04-12T14:29:54.983Z
blocker_discovered: false
---

# S01: Core types, translator, and envelope

**All ACP SessionUpdate branches now translated with full field preservation; 5 new event types added throughout the stack**

## What Happened

S01 implemented the complete structural changes: api/events.go gained 5 new constants; types.go was rewritten with 15+ new support types (ContentBlock, ToolCallContent, EmbeddedResource, ConfigOption, AvailableCommandInput unions all with flat JSON wire shape matching ACP SDK); existing event types enriched with full ACP fields; 5 new event types added. translator.go translate() now covers all 11 SessionUpdate branches with no nil returns and a full suite of convert helpers. envelope.go handles 17 event types in decodeEventPayload(). Both design docs updated.

## Verification

go build ./... passes; go vet ./pkg/events/... clean.

## Requirements Advanced

None.

## Requirements Validated

None.

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
