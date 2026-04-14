# S01: Dead code removal

**Goal:** Remove EventTypeFileWrite, EventTypeFileRead, EventTypeCommand constants; FileWriteEvent, FileReadEvent, CommandEvent types; and all decode/test references from pkg/shim — no production or test code references them.
**Demo:** After this: `! rg 'EventTypeFileWrite|EventTypeFileRead|EventTypeCommand|FileWriteEvent|FileReadEvent|CommandEvent' --type go --glob '!docs/plan/*'` returns no output; `go test ./pkg/shim/...` passes.

## Must-Haves

- `! rg 'EventTypeFileWrite|EventTypeFileRead|EventTypeCommand|FileWriteEvent|FileReadEvent|CommandEvent' --type go --glob '!docs/plan/*'` returns no output; `go test ./pkg/shim/...` passes.

## Proof Level

- This slice proves: Not provided.

## Integration Closure

Not provided.

## Verification

- Not provided.

## Tasks

- [x] **T01: Remove file_write, file_read, command dead code from pkg/shim** `est:15m`
  Remove the three dead event types (FileWriteEvent, FileReadEvent, CommandEvent), their constants (EventTypeFileWrite, EventTypeFileRead, EventTypeCommand), their decode cases in shim_event.go, and their test table entries. These types never had an ACP source and are misleading API surface (R058).

Steps:
1. In `pkg/shim/api/event_constants.go`: delete the three constant lines for EventTypeFileWrite, EventTypeFileRead, EventTypeCommand.
2. In `pkg/shim/api/event_types.go`: delete the FileWriteEvent struct (lines ~558-564), FileReadEvent struct (lines ~566-572), and CommandEvent struct (lines ~574-580), including their doc comments and eventType() methods.
3. In `pkg/shim/api/shim_event.go`: delete the three `case FileWriteEvent:` blocks in the type-switch (~lines 219-234) and the three `case EventTypeFileWrite:` / `case EventTypeFileRead:` / `case EventTypeCommand:` blocks in the string-switch (~lines 300-305).
4. In `pkg/shim/server/translator_test.go`: delete the three test table entries referencing FileWriteEvent, FileReadEvent, CommandEvent (~lines 355-357).
5. In `pkg/shim/server/translate_rich_test.go`: delete the three test table entries referencing FileWriteEvent, FileReadEvent, CommandEvent (~lines 586-588).
6. Run `go build ./pkg/shim/...` to confirm compilation.
7. Run `go test ./pkg/shim/...` to confirm tests pass.
8. Run `! rg 'EventTypeFileWrite|EventTypeFileRead|EventTypeCommand|FileWriteEvent|FileReadEvent|CommandEvent' --type go --glob '!docs/plan/*'` to confirm zero references remain.
  - Files: `pkg/shim/api/event_constants.go`, `pkg/shim/api/event_types.go`, `pkg/shim/api/shim_event.go`, `pkg/shim/server/translator_test.go`, `pkg/shim/server/translate_rich_test.go`
  - Verify: go build ./pkg/shim/... && go test ./pkg/shim/... && ! rg 'EventTypeFileWrite|EventTypeFileRead|EventTypeCommand|FileWriteEvent|FileReadEvent|CommandEvent' --type go --glob '!docs/plan/*'

## Files Likely Touched

- pkg/shim/api/event_constants.go
- pkg/shim/api/event_types.go
- pkg/shim/api/shim_event.go
- pkg/shim/server/translator_test.go
- pkg/shim/server/translate_rich_test.go
