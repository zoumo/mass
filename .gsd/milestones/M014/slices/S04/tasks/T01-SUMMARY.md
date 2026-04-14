---
id: T01
parent: S04
milestone: M014
key_files:
  - pkg/shim/server/translator.go
  - pkg/shim/server/translator_test.go
key_decisions:
  - eventCounts tracking was already implemented in broadcast() at the correct location (after nextSeq++, before fan-out), satisfying D121 and D122 constraints
duration: 
verification_result: passed
completed_at: 2026-04-14T15:41:07.298Z
blocker_discovered: false
---

# T01: Verified eventCounts tracking in Translator.broadcast() with EventCounts() method and fail-closed tests — all already implemented

**Verified eventCounts tracking in Translator.broadcast() with EventCounts() method and fail-closed tests — all already implemented**

## What Happened

The eventCounts tracking described in the task plan was already fully implemented in the codebase. Verification confirmed all six implementation points:

1. **`eventCounts map[string]int` field** — present on Translator struct (line 32)
2. **Initialization** — `eventCounts: make(map[string]int)` in NewTranslator() (line 50)
3. **Counting in broadcast()** — `t.eventCounts[ev.Type]++` at line 324, after `t.nextSeq++` and before the fan-out loop, satisfying constraint D121 (single counting site)
4. **EventCounts() method** — acquires `t.mu.Lock()`, copies the map, returns the copy (lines 131–140), ensuring thread safety
5. **TestEventCounts_PromptTurn** — exercises a full prompt turn (turn_start, user_message, 2×text, tool_call, turn_end, state_change) and asserts correct per-type counts
6. **TestEventCounts_FailClosedOnAppendFailure** — verifies that when EventLog.Append fails, eventCounts are NOT incremented (count stays at 1 after a dropped event)

Constraint D122 is satisfied: EventCounts() is purely diagnostic and does not trigger state_change events.

Build and full test suite verified: `go build ./pkg/shim/...` succeeds, `go test ./pkg/shim/server/...` passes all 74 tests including both new EventCounts tests.

## Verification

Ran `go build ./pkg/shim/...` — clean build, exit 0.
Ran `go test ./pkg/shim/server/... -run TestEventCounts -v` — both TestEventCounts_PromptTurn and TestEventCounts_FailClosedOnAppendFailure PASS.
Ran `go test ./pkg/shim/server/... -v` — all 74 tests PASS, no failures.

## Verification Evidence

| # | Command | Exit Code | Verdict | Duration |
|---|---------|-----------|---------|----------|
| 1 | `go build ./pkg/shim/...` | 0 | ✅ pass | 800ms |
| 2 | `go test ./pkg/shim/server/... -run TestEventCounts -v` | 0 | ✅ pass | 500ms |
| 3 | `go test ./pkg/shim/server/... -v` | 0 | ✅ pass | 1577ms |

## Deviations

No code changes needed — all implementation was already present in the codebase. Task reduced to verification-only.

## Known Issues

None.

## Files Created/Modified

- `pkg/shim/server/translator.go`
- `pkg/shim/server/translator_test.go`
