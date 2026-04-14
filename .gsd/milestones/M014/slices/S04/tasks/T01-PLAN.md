---
estimated_steps: 30
estimated_files: 2
skills_used: []
---

# T01: Add eventCounts tracking to Translator.broadcast() and expose EventCounts() method with tests

## Why
R055 requires state.json to carry eventCounts covering all event origins. The Translator's broadcast() is the single fan-out entry point for all events (ACP-translated, turn lifecycle, state_change). Adding per-type counting here with fail-closed semantics (skip count on log-append failure) and an exported EventCounts() method establishes the in-memory counting surface that downstream slices (S06, S07) will wire into state.json and runtime/status.

## Steps
1. Read `pkg/shim/server/translator.go` — locate the Translator struct and broadcast() method.
2. Add `eventCounts map[string]int` field to the Translator struct.
3. Initialize `eventCounts: make(map[string]int)` in NewTranslator().
4. In broadcast(), after the `t.nextSeq++` line and before the fan-out loop, add `t.eventCounts[ev.Type]++`. This line must be AFTER the log-append success check (the early return on append failure already skips nextSeq increment — the eventCounts increment goes in the same success path).
5. Add exported method `EventCounts() map[string]int` that acquires `t.mu.Lock()`, copies the map, and returns the copy. Never return the internal map directly — callers must not race with broadcast().
6. Write test `TestEventCounts_PromptTurn` in `pkg/shim/server/translator_test.go`:
   - Create Translator with nil log (no persistence needed for this test)
   - Subscribe, Start
   - NotifyTurnStart → drain
   - NotifyUserPrompt("hello") → drain
   - Send 2 AgentMessageChunk notifications → drain each
   - Send 1 ToolCall notification → drain
   - NotifyTurnEnd → drain
   - NotifyStateChange → drain
   - Call EventCounts() and assert: turn_start:1, user_message:1, text:2, tool_call:1, turn_end:1, state_change:1
7. Write test `TestEventCounts_FailClosedOnAppendFailure` in `pkg/shim/server/translator_test.go`:
   - Create Translator with an EventLog opened on a temp file
   - Subscribe, Start
   - Send 1 successful AgentMessageChunk → drain → assert EventCounts()["text"]==1
   - Close the EventLog file to force Append failures
   - Send another AgentMessageChunk → wait 200ms (should be dropped, per existing TestFailClosed pattern)
   - Assert EventCounts()["text"] is still 1 (not 2)
8. Run `go build ./pkg/shim/...` and `go test ./pkg/shim/server/... -run TestEventCounts` to verify.

## Key constraint (D121)
eventCounts[ev.Type]++ must be in broadcast() after nextSeq++, before fan-out. This is the ONLY counting site — do not add counting in translate(), broadcastSessionEvent(), or any Notify* method.

## Key constraint (D122)
EventCounts is a derived diagnostic field. It must NOT trigger state_change events itself. This slice only adds in-memory counting — no state.json writes, no hook calls.

## Inputs

- ``pkg/shim/server/translator.go` — Translator struct and broadcast() method where counting is added`
- ``pkg/shim/server/translator_test.go` — existing test patterns (drainShimEvent, makeNotif, sendAndDrainShimEvent)`

## Expected Output

- ``pkg/shim/server/translator.go` — eventCounts field added to Translator, initialized in NewTranslator(), incremented in broadcast(), exposed via EventCounts()`
- ``pkg/shim/server/translator_test.go` — TestEventCounts_PromptTurn and TestEventCounts_FailClosedOnAppendFailure added`

## Verification

go build ./pkg/shim/... && go test ./pkg/shim/server/... -run TestEventCounts -v
