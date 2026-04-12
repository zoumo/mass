---
estimated_steps: 16
estimated_files: 1
skills_used: []
---

# T02: Tests 1-15: translate() and envelope decode

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

## Inputs

- `pkg/events/translator_test.go`
- `pkg/events/types.go`
- `pkg/events/translator.go`

## Expected Output

- `pkg/events/translate_rich_test.go with translate() tests`

## Verification

go test ./pkg/events/... -run TestTranslate_Tool -v 2>&1 | tail -10
