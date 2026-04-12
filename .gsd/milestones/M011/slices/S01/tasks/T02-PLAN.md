---
estimated_steps: 9
estimated_files: 1
skills_used: []
---

# T02: pkg/events/types.go — support types + enriched events

Rewrite pkg/events/types.go:
1. Add Annotations, ContentBlock union (5 variants + flat MarshalJSON/UnmarshalJSON), EmbeddedResource union, ToolCallContent union, ToolCallLocation
2. Add AvailableCommand/Input, ConfigOption union, ConfigSelectOptions union, Cost
3. Enrich TextEvent/ThinkingEvent/UserMessageEvent with Content *ContentBlock
4. Enrich ToolCallEvent with Meta/Status/Content/Locations/RawInput/RawOutput
5. Enrich ToolResultEvent with Meta/Kind/Title/Content/Locations/RawInput/RawOutput
6. Add PlanEvent.Meta
7. Add 5 new event types: AvailableCommandsEvent, CurrentModeEvent, ConfigOptionEvent, SessionInfoEvent, UsageEvent

All union types: json:"-" variant pointers + custom MarshalJSON (error on empty) + UnmarshalJSON (error on unknown type).

## Inputs

- `docs/plan/reduce-event-translation-20260412.md`
- `pkg/events/types.go`
- `ACP SDK types_gen.go`

## Expected Output

- `pkg/events/types.go — all support types + enriched + new event types`

## Verification

go build ./pkg/events/...
