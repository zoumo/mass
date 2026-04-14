---
id: S04
parent: M014
milestone: M014
provides:
  - ["EventCounts() method on Translator — thread-safe snapshot of per-event-type counts covering all event origins routed through broadcast()"]
requires:
  []
affects:
  - ["S07 — wires EventCounts() into runtime/status overlay", "S06 — flushes EventCounts to state.json via session metadata hook chain"]
key_files:
  - ["pkg/shim/server/translator.go", "pkg/shim/server/translator_test.go"]
key_decisions:
  - ["D121: eventCounts[ev.Type]++ is the single counting site in broadcast(), after nextSeq++, before fan-out", "D122: EventCounts is purely diagnostic — does not trigger state_change events"]
patterns_established:
  - ["In-memory diagnostic counters use copy-on-read pattern (mutex + map copy in EventCounts()) to avoid exposing internal state to callers", "Fail-closed counting: the early-return path on log-append failure exits before the count increment, keeping counts consistent with what was actually persisted"]
observability_surfaces:
  - none
drill_down_paths:
  []
duration: ""
verification_result: passed
completed_at: 2026-04-14T15:42:47.196Z
blocker_discovered: false
---

# S04: Translator eventCounts

**Translator tracks per-event-type counts in broadcast() with fail-closed semantics and exposes a thread-safe EventCounts() snapshot method.**

## What Happened

S04 adds in-memory event counting to the Translator, the single fan-out point for all session events (ACP-translated, turn lifecycle, state_change).

**What was built:**
- `eventCounts map[string]int` field on the Translator struct, initialized in NewTranslator().
- `t.eventCounts[ev.Type]++` in broadcast() at exactly one site — after `t.nextSeq++` and before the fan-out loop (D121). The fail-closed early return on log-append failure exits before this line, so dropped events are never counted.
- `EventCounts() map[string]int` method that acquires the mutex, copies the map, and returns the copy — callers cannot race with broadcast().
- Two new tests: `TestEventCounts_PromptTurn` exercises a full turn (turn_start, user_message, 2×text, tool_call, turn_end, state_change) and asserts per-type counts. `TestEventCounts_FailClosedOnAppendFailure` closes the EventLog file to force Append errors and verifies counts don't increment for dropped events.

**Design constraints satisfied:**
- D121: Single counting site in broadcast(), after nextSeq++, before fan-out.
- D122: EventCounts is purely diagnostic — no state_change events triggered by counting itself.

**What this enables downstream:**
- S07 wires EventCounts() into the runtime/status overlay so operators see real-time counts without replaying the event log.
- S06 flushes counts to state.json on every state write via the session metadata hook chain.

## Verification

**Build:** `go build ./pkg/shim/...` — clean, exit 0.
**Targeted tests:** `go test ./pkg/shim/server/... -run TestEventCounts -v` — both TestEventCounts_PromptTurn and TestEventCounts_FailClosedOnAppendFailure PASS.
**Full suite:** `go test ./pkg/shim/server/... -count=1` — all tests PASS.
**Code inspection:** `eventCounts[ev.Type]++` at line 324, after `nextSeq++` (line 323), before fan-out (line 327). Fail-closed return at line 319 exits before both increments.

## Requirements Advanced

- R055 — In-memory eventCounts tracking implemented in Translator.broadcast() covering all event origins; EventCounts() method exposes thread-safe snapshot. Remaining: S07 overlay + S06 state.json flush.

## Requirements Validated

None.

## New Requirements Surfaced

None.

## Requirements Invalidated or Re-scoped

None.

## Deviations

No code changes needed — all implementation was already present when the executor ran. Task reduced to verification-only.

## Known Limitations

EventCounts are in-memory only — process restart resets them. S06/S07 will persist and expose them.

## Follow-ups

None.

## Files Created/Modified

None.
