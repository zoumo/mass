---
id: M011
title: "Reduce Shim Event Translation Overhead"
status: complete
completed_at: 2026-04-12T14:43:24.951Z
key_decisions:
  - MASS preserves _meta in all ContentBlock variants even though ACP SDK ContentBlock.MarshalJSON strips it in union serialization — data fidelity over perfect wire shape alignment
  - ConfigSelectOptions uses bare array with grouped-first heuristic (check Group field) for ungrouped/grouped discrimination during unmarshal
  - Shape alignment tests use Meta-free inputs for structural key tests due to ACP SDK _meta strip behavior
key_files:
  - api/events.go
  - pkg/events/types.go
  - pkg/events/translator.go
  - pkg/events/envelope.go
  - pkg/events/translate_rich_test.go
  - pkg/events/wire_shape_test.go
  - docs/design/runtime/runtime-spec.md
  - docs/design/runtime/run-rpc-spec.md
lessons_learned:
  - ACP SDK codegen for ContentBlock union deliberately creates a selective nm map that strips _meta and annotations — inspect MarshalJSON implementations before assuming struct tags drive wire shape
  - Test matrix items in plan docs translate directly to isolated test functions; naming them T01-style makes coverage auditing simple
---

# M011: Reduce Shim Event Translation Overhead

**ACP event translation now preserves full data fidelity — all 11 SessionUpdate branches translated, 5 new event types added, union types mirror ACP flat wire shape.**

## What Happened

M011 eliminated the over-aggressive ACP event translation in pkg/events/translator.go. The translate() function previously silently discarded 5 ACP SessionUpdate branches (AvailableCommandsUpdate, CurrentModeUpdate, ConfigOptionUpdate, SessionInfoUpdate, UsageUpdate) and stripped most fields from ToolCall and ToolCallUpdate events. 

S01 delivered the complete structural implementation: api/events.go gained 5 new constants; types.go was rewritten with 15+ new support types implementing flat ACP wire shape (ContentBlock, ToolCallContent, EmbeddedResource, ConfigOption, AvailableCommandInput unions all with json:"-" variant pointers + custom MarshalJSON/UnmarshalJSON); existing event types enriched with full ACP fields; 5 new event types added; translate() now covers all 11 branches; decodeEventPayload handles 17 types; design docs updated. S02 fixed 6 broken tests and added 31 new tests covering all 22 plan matrix items, including shape alignment tests that discovered ACP SDK's ContentBlock.MarshalJSON selectively strips _meta from union wire shape (documented in code).

Key design note: MASS intentionally preserves _meta in all support types even though ACP SDK's ContentBlock union MarshalJSON omits it — this is the "保留 _meta 扩展点" principle taking precedence over perfect wire shape alignment for extension metadata.

## Success Criteria Results

All 5 success criteria met: make build passes, go test ./pkg/events/... passes (62 tests), translate() has no nil returns, wire shape tests pass, 5 new constants in api/events.go.

## Definition of Done Results

- make build passes ✅\n- go test ./pkg/events/... passes with 22 test matrix items covered ✅\n- All 5 new event types decode correctly through decodeEventPayload ✅ (test 14 covers all 17 types)\n- docs/design updated ✅

## Requirement Outcomes



## Deviations

None.

## Follow-ups

None.
