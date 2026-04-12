# S01: Core types, translator, and envelope

**Goal:** Implement all code changes from the plan: api/events.go constants, pkg/events/types.go support types + enriched event types, translator.go translate() + convert helpers, envelope.go decodeEventPayload(), docs updates.
**Demo:** go build ./... passes; translate() covers all SessionUpdate branches with no nil returns

## Must-Haves

- go build ./... passes; api/events.go has 5 new constants; translate() has no nil returns for any known ACP branch; decodeEventPayload handles 17 types; docs updated.

## Proof Level

- This slice proves: Not provided.

## Integration Closure

Not provided.

## Verification

- Not provided.

## Tasks

- [x] **T01: api/events.go — new event type constants** `est:5m`
  Add 5 new event type constants to api/events.go:
- EventTypeAvailableCommands = "available_commands"
- EventTypeCurrentMode = "current_mode"
- EventTypeConfigOption = "config_option"
- EventTypeSessionInfo = "session_info"
- EventTypeUsage = "usage"
  - Files: `api/events.go`
  - Verify: grep -c EventTypeAvailableCommands api/events.go && grep -c EventTypeUsage api/events.go

- [x] **T02: pkg/events/types.go — support types + enriched events** `est:30m`
  Rewrite pkg/events/types.go:
1. Add Annotations, ContentBlock union (5 variants + flat MarshalJSON/UnmarshalJSON), EmbeddedResource union, ToolCallContent union, ToolCallLocation
2. Add AvailableCommand/Input, ConfigOption union, ConfigSelectOptions union, Cost
3. Enrich TextEvent/ThinkingEvent/UserMessageEvent with Content *ContentBlock
4. Enrich ToolCallEvent with Meta/Status/Content/Locations/RawInput/RawOutput
5. Enrich ToolResultEvent with Meta/Kind/Title/Content/Locations/RawInput/RawOutput
6. Add PlanEvent.Meta
7. Add 5 new event types: AvailableCommandsEvent, CurrentModeEvent, ConfigOptionEvent, SessionInfoEvent, UsageEvent

All union types: json:"-" variant pointers + custom MarshalJSON (error on empty) + UnmarshalJSON (error on unknown type).
  - Files: `pkg/events/types.go`
  - Verify: go build ./pkg/events/...

- [x] **T03: pkg/events/translator.go — translate() + convert helpers** `est:30m`
  Update translator.go:
1. Rewrite translate() to cover all 11 SessionUpdate branches (no nil returns)
2. Add convertContentBlock, convertAnnotations, convertEmbeddedResource
3. Add convertToolCallContents, convertLocations
4. Add convertCommands, convertAvailableCommandInput
5. Add convertConfigOptions, convertConfigSelectOptions, convertConfigCategory
6. Add convertCost
7. Add safeToolKind, safeStringPtr helpers
  - Files: `pkg/events/translator.go`
  - Verify: go build ./pkg/events/...

- [x] **T04: pkg/events/envelope.go — decodeEventPayload 5 new cases** `est:10m`
  Update envelope.go decodeEventPayload():
1. Add 5 new cases to both the outer switch and the unmarshal closure type switch:
   - available_commands -> AvailableCommandsEvent
   - current_mode -> CurrentModeEvent
   - config_option -> ConfigOptionEvent
   - session_info -> SessionInfoEvent
   - usage -> UsageEvent
  - Files: `pkg/events/envelope.go`
  - Verify: go build ./pkg/events/...

- [x] **T05: docs — runtime-spec + shim-rpc-spec updates** `est:10m`
  Update design docs:
1. runtime-spec.md: add 5 rows to event type table + payload preservation policy note
2. shim-rpc-spec.md: add 5 rows to Typed Event table + update tool_call and tool_result payload field descriptions
  - Files: `docs/design/runtime/runtime-spec.md`, `docs/design/runtime/shim-rpc-spec.md`
  - Verify: grep available_commands docs/design/runtime/runtime-spec.md && grep available_commands docs/design/runtime/shim-rpc-spec.md

## Files Likely Touched

- api/events.go
- pkg/events/types.go
- pkg/events/translator.go
- pkg/events/envelope.go
- docs/design/runtime/runtime-spec.md
- docs/design/runtime/shim-rpc-spec.md
