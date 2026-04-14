---
estimated_steps: 10
estimated_files: 5
skills_used: []
---

# T01: Remove file_write, file_read, command dead code from pkg/shim

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

## Inputs

- ``pkg/shim/api/event_constants.go` — contains the three dead constants to remove`
- ``pkg/shim/api/event_types.go` — contains the three dead types to remove`
- ``pkg/shim/api/shim_event.go` — contains the six decode cases to remove`
- ``pkg/shim/server/translator_test.go` — contains three dead test entries`
- ``pkg/shim/server/translate_rich_test.go` — contains three dead test entries`

## Expected Output

- ``pkg/shim/api/event_constants.go` — three constants removed`
- ``pkg/shim/api/event_types.go` — three types and eventType() methods removed`
- ``pkg/shim/api/shim_event.go` — six decode cases removed`
- ``pkg/shim/server/translator_test.go` — three test entries removed`
- ``pkg/shim/server/translate_rich_test.go` — three test entries removed`

## Verification

go build ./pkg/shim/... && go test ./pkg/shim/... && ! rg 'EventTypeFileWrite|EventTypeFileRead|EventTypeCommand|FileWriteEvent|FileReadEvent|CommandEvent' --type go --glob '!docs/plan/*'
