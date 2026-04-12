# S02: Fix and extend tests

**Goal:** Fix broken existing tests (Content field now non-nil), then add all 22 test matrix items from the plan covering translate(), JSON shape alignment, envelope decode, union error cases, and backward compatibility.
**Demo:** go test ./pkg/events/... passes with full coverage of all new types and JSON shape alignment

## Must-Haves

- go test ./pkg/events/... passes; 22 test matrix items covered; make build passes.

## Proof Level

- This slice proves: Not provided.

## Integration Closure

Not provided.

## Verification

- Not provided.

## Tasks

- [x] **T01: Fix broken existing tests** `est:15m`
  Fix broken existing tests:
1. TestTranslate_AgentMessageChunk, TestTranslate_AgentThoughtChunk, TestTranslate_UserMessageChunk: update assertions to include non-nil Content field
2. TestEventLog_TranslatorWritesCanonicalEnvelope: same
3. Any other existing tests that check TextEvent/ThinkingEvent/UserMessageEvent equality without Content field

Use assert.Equal with full struct or check specific fields instead of full struct equality.
  - Files: `pkg/events/translator_test.go`, `pkg/events/log_test.go`
  - Verify: go test ./pkg/events/... -run 'TestTranslate_Agent|TestTranslate_User|TestEventLog_Translator' 2>&1 | grep -E 'PASS|FAIL|ok'

- [x] **T02: Tests 1-15: translate() and envelope decode** `est:40m`
  New test file pkg/events/translate_rich_test.go covering test matrix items 1-15:

1. ToolCall full translation: Meta, Content (diff/terminal/content all 3 variants), Locations, RawInput, RawOutput, Status all preserved
2. ToolCallUpdate full translation: Meta, nullable Status/Kind/Title, Content, Locations, RawInput, RawOutput preserved
3. ContentBlock Text variant: Text + Annotations + Meta preserved; convenience text field filled
4. ContentBlock Image variant: Data, MimeType, URI, Annotations, Meta preserved
5. ContentBlock Audio variant: Data, MimeType, Annotations, Meta preserved
6. ContentBlock ResourceLink variant: URI, Name, Description, MimeType, Title, Size, Annotations, Meta preserved
7. ContentBlock Resource variant: EmbeddedResource (text/blob both) + Annotations + Meta preserved
8. AvailableCommandsUpdate: Commands list with Name/Description/Input(Unstructured.Hint)/Meta
9. CurrentModeUpdate: ModeID, Meta
10. ConfigOptionUpdate: ConfigOptions with ID/Name/CurrentValue/Description/Category/Options (ungrouped+grouped)/Meta
11. SessionInfoUpdate: Title, UpdatedAt, Meta
12. UsageUpdate: Cost(Amount/Currency), Size, Used, Meta
13. RawInput/RawOutput JSON round-trip: arbitrary JSON struct through translate+marshal+unmarshal preserved
14. Envelope decode all 17 event types: decodeEventPayload correctly recovers all types
15. Backward compat: old JSON with only original fields still deserializes correctly
  - Files: `pkg/events/translate_rich_test.go`
  - Verify: go test ./pkg/events/... -run TestTranslate_Tool -v 2>&1 | tail -10

- [x] **T03: Tests 16-22: JSON shape alignment + union error cases** `est:30m`
  New test file pkg/events/wire_shape_test.go covering test matrix items 16-22:

16. ContentBlock JSON shape: 5 variants - construct ACP SDK object, marshal, compare JSON key layout to OAR mirror marshal
17. ToolCallContent JSON shape: content/diff/terminal 3 variants ACP vs OAR marshal alignment
18. AvailableCommandInput JSON shape: unstructured variant flat shape alignment
19. ConfigOption JSON shape: select variant + options (ungrouped/grouped) alignment
20. EmbeddedResource JSON shape: text/blob variants (no type field, field-presence discriminated)
21. Union empty variant marshal error: ContentBlock zero variant -> error, ToolCallContent zero -> error
22. Union unknown type unmarshal error: ContentBlock unknown type -> error with type name, ToolCallContent unknown -> error with type name

For shape tests: json.Marshal ACP SDK object -> map[string]any, json.Marshal OAR object -> map[string]any, compare keys present (except sessionUpdate).
  - Files: `pkg/events/wire_shape_test.go`
  - Verify: go test ./pkg/events/... -run TestWireShape -v 2>&1 | tail -20

- [x] **T04: Final verification: go test + make build** `est:10m`
  Final verification:
1. go test ./pkg/events/... (all tests pass)
2. make build (full build passes)
3. Fix any remaining test failures
  - Verify: go test ./pkg/events/... && make build

## Files Likely Touched

- pkg/events/translator_test.go
- pkg/events/log_test.go
- pkg/events/translate_rich_test.go
- pkg/events/wire_shape_test.go
