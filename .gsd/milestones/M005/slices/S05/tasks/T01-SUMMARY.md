---
id: T01
parent: S05
milestone: M005
key_files:
  - pkg/events/envelope.go
  - pkg/events/translator.go
  - pkg/events/translator_test.go
key_decisions:
  - StreamSeq is *int (pointer) so omitempty preserves 0 (turn_start's seq) while nil means no active turn
  - Turn state mutations happen inside broadcastEnvelope callback under mu.Lock — seq allocation and turn-state mutation are atomic
  - NotifyTurnEnd clears currentTurnId AFTER building the params so the turn_end event carries the identifier
  - Test drain-after-send pattern required to ensure ACP goroutine processes mid-turn events before NotifyTurnEnd fires
duration: 
verification_result: passed
completed_at: 2026-04-08T20:23:04.615Z
blocker_discovered: false
---

# T01: Added TurnId/StreamSeq/Phase to SessionUpdateParams and rewrote Translator turn tracking for atomic ordering; all 7 turn-aware tests pass with zero regressions

**Added TurnId/StreamSeq/Phase to SessionUpdateParams and rewrote Translator turn tracking for atomic ordering; all 7 turn-aware tests pass with zero regressions**

## What Happened

Added three optional fields (TurnId, StreamSeq *int, Phase) to SessionUpdateParams in envelope.go. Added currentTurnId/currentStreamSeq to Translator struct. Rewrote NotifyTurnStart and NotifyTurnEnd to mutate turn state atomically inside broadcastEnvelope callbacks (under mu.Lock). Updated broadcastSessionEvent to inject turn fields for mid-turn ACP events. Updated TestNotifyTurnStartAndEnd with turn-field assertions and added 6 new TestTurnAwareEnvelope_* tests proving TurnId assignment, streamSeq monotonicity, multi-turn isolation, stateChange exclusion, JSON round-trip, and replay ordering invariants.

## Verification

go test ./pkg/events/... -count=1 -v filtered for PASS/FAIL/TestTurnAware/TestNotifyTurn: all 7 new tests PASS. go test ./pkg/... -count=1: all 8 packages OK, zero failures.

## Verification Evidence

| # | Command | Exit Code | Verdict | Duration |
|---|---------|-----------|---------|----------|
| 1 | `go test ./pkg/events/... -count=1 -v 2>&1 | grep -E 'PASS|FAIL|TestTurnAware|TestNotifyTurn'` | 0 | ✅ pass | 6100ms |
| 2 | `go test ./pkg/... -count=1` | 0 | ✅ pass | 19300ms |

## Deviations

TestTurnAwareEnvelope_ReplayOrdering was rewritten from bulk-enqueue+collect to drain-after-each-send to eliminate a race where ACP events were processed after NotifyTurnEnd cleared currentTurnId.

## Known Issues

None.

## Files Created/Modified

- `pkg/events/envelope.go`
- `pkg/events/translator.go`
- `pkg/events/translator_test.go`
