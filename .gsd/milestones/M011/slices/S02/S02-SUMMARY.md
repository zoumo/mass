---
id: S02
parent: M011
milestone: M011
provides:
  - (none)
requires:
  []
affects:
  []
key_files:
  - ["pkg/events/translate_rich_test.go", "pkg/events/wire_shape_test.go", "pkg/events/translator_test.go", "pkg/events/log_test.go"]
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
completed_at: 2026-04-12T14:42:27.701Z
blocker_discovered: false
---

# S02: Fix and extend tests

**Fixed 6 broken tests; added 31 new tests covering all 22 plan matrix items; 62 total pass**

## What Happened

T01 fixed 6 tests broken by the new Content *ContentBlock field: 3 chunk tests, FanOut, log canonical envelope, and replaced IgnoredVariants with PreviouslyIgnoredVariants. T02 added translate_rich_test.go with 16 tests covering full ToolCall/ToolCallUpdate fields, all 5 ContentBlock variants with meta+annotations, all 5 new event types, RawInput/RawOutput round-trip, 17-type envelope decode, and backward compat. T03 added wire_shape_test.go with 15 tests covering JSON structural key layout against ACP SDK marshal output, including discovery that ACP SDK deliberately strips _meta from ContentBlock union MarshalJSON (documented with comment). T04 confirmed make build passes.

## Verification

go test ./pkg/events/... → 62 pass. make build → bin/agentd + bin/agentdctl produced.

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
