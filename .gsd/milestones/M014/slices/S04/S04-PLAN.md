# S04: Translator eventCounts

**Goal:** Translator tracks in-memory eventCounts by event type in broadcast(), exposes EventCounts() method returning a thread-safe copy, and preserves fail-closed semantics (failed log appends don't increment counts).
**Demo:** After this: test runs a prompt turn through mockagent; Translator.EventCounts() returns {text: N, tool_call: M, turn_start: 1, turn_end: 1, user_message: 1, state_change: K}; injecting a failing log proves counts stay at 0 on failed append.

## Must-Haves

- `go test ./pkg/shim/server/... -run TestEventCounts` passes
- EventCounts() returns correct per-type counts after a prompt turn (turn_start, user_message, text, tool_call, turn_end, state_change)
- Failed log append → counts stay at 0 for the dropped event
- `go build ./pkg/shim/...` clean

## Proof Level

- This slice proves: contract — unit tests exercise the counting invariant in isolation

## Integration Closure

- Upstream surfaces consumed: `pkg/shim/server/translator.go` broadcast() fan-out path, `pkg/shim/api/event_constants.go` event type strings
- New wiring introduced in this slice: `Translator.eventCounts` field + `EventCounts()` method — no external wiring yet
- What remains: S07 wires EventCounts() into runtime/status overlay; S06 uses the hook to flush counts to state.json

## Verification

- Runtime signals: eventCounts map in Translator memory, queryable via EventCounts() method
- Inspection surfaces: EventCounts() method — downstream slices (S07) expose this via runtime/status RPC
- Failure visibility: log-append failures already logged via slog.Error in broadcast(); counting skip is implicit (no separate signal)

## Tasks

- [x] **T01: Add eventCounts tracking to Translator.broadcast() and expose EventCounts() method with tests** `est:30m`
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
  - Files: `pkg/shim/server/translator.go`, `pkg/shim/server/translator_test.go`
  - Verify: go build ./pkg/shim/... && go test ./pkg/shim/server/... -run TestEventCounts -v

## Files Likely Touched

- pkg/shim/server/translator.go
- pkg/shim/server/translator_test.go
