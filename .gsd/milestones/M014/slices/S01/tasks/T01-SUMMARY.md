---
id: T01
parent: S01
milestone: M014
key_files:
  - pkg/shim/api/event_constants.go
  - pkg/shim/api/event_types.go
  - pkg/shim/api/shim_event.go
  - pkg/shim/server/translator_test.go
  - pkg/shim/server/translate_rich_test.go
key_decisions:
  - Removed dead event types without replacement — they had no ACP source and no production consumers
duration: 
verification_result: passed
completed_at: 2026-04-14T14:47:44.704Z
blocker_discovered: false
---

# T01: Remove dead FileWriteEvent, FileReadEvent, CommandEvent types, constants, decode cases, and test entries from pkg/shim

**Remove dead FileWriteEvent, FileReadEvent, CommandEvent types, constants, decode cases, and test entries from pkg/shim**

## What Happened

Removed three dead event types that never had an ACP source and were misleading API surface:

1. **event_constants.go** — deleted `EventTypeFileWrite`, `EventTypeFileRead`, `EventTypeCommand` constant declarations.
2. **event_types.go** — deleted `FileWriteEvent`, `FileReadEvent`, `CommandEvent` structs and their `eventType()` methods (18 lines total including doc comments).
3. **shim_event.go** — deleted 3 type-switch cases in the `unmarshal` closure and 3 string-switch cases in `decodeEventPayload` (18 lines total).
4. **translator_test.go** — deleted 3 entries from the `TestEventTypes` table.
5. **translate_rich_test.go** — deleted 3 entries from the `TestTranslateRich_ShimEventDecode_AllEventTypes` table.

All edits were straightforward deletions with no logic changes to surrounding code.

## Verification

Ran `go build ./pkg/shim/...` — compiled cleanly with no errors. Ran `go test ./pkg/shim/...` — all tests pass (server package 1.469s, acp package cached). Ran `rg 'EventTypeFileWrite|EventTypeFileRead|EventTypeCommand|FileWriteEvent|FileReadEvent|CommandEvent' --type go --glob '!docs/plan/*'` — zero matches (exit code 1), confirming complete removal.

## Verification Evidence

| # | Command | Exit Code | Verdict | Duration |
|---|---------|-----------|---------|----------|
| 1 | `go build ./pkg/shim/...` | 0 | ✅ pass | 7200ms |
| 2 | `go test ./pkg/shim/...` | 0 | ✅ pass | 7800ms |
| 3 | `rg 'EventTypeFileWrite|EventTypeFileRead|EventTypeCommand|FileWriteEvent|FileReadEvent|CommandEvent' --type go --glob '!docs/plan/*'` | 1 | ✅ pass (no matches) | 200ms |

## Deviations

None.

## Known Issues

None.

## Files Created/Modified

- `pkg/shim/api/event_constants.go`
- `pkg/shim/api/event_types.go`
- `pkg/shim/api/shim_event.go`
- `pkg/shim/server/translator_test.go`
- `pkg/shim/server/translate_rich_test.go`
