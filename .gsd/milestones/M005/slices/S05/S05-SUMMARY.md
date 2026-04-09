---
id: S05
parent: M005
milestone: M005
provides:
  - ["SessionUpdateParams with TurnId/StreamSeq/*int/Phase fields ready for chat/replay consumption", "Translator with atomic turn tracking (currentTurnId, currentStreamSeq) under mu.Lock", "handlePrompt wired with NotifyTurnStart before and NotifyTurnEnd always after mgr.Prompt", "7 unit tests proving turn-aware ordering semantics", "Updated RPC integration tests proving 6-event per-prompt turn envelope flow"]
requires:
  []
affects:
  - ["S06 — Room & MCP Agent Alignment: if room/send or MCP routing fires through handlePrompt, it will automatically get turn wrapping", "S07 — Recovery & Integration Proof: recovery metadata lastSeq is now 5 per prompt (not 3); recovery tests should account for 6 events per turn"]
key_files:
  - ["pkg/events/envelope.go", "pkg/events/translator.go", "pkg/events/translator_test.go", "pkg/rpc/server.go", "pkg/rpc/server_test.go"]
key_decisions:
  - ["StreamSeq is *int (pointer) not int — omitempty drops int(0), which would make turn_start indistinguishable from a non-turn event; *int(0) survives omitempty, nil is omitted (D077)", "Turn state mutations happen inside broadcastEnvelope callback under mu.Lock — seq allocation and turn-state mutation are atomic (D078)", "NotifyTurnEnd clears currentTurnId AFTER building params so turn_end event carries the turnId before state is cleared", "Test drain-after-send required for mid-turn ACP event tests to prevent race with NotifyTurnEnd clearing turn state", "Actual event count per RPC prompt is 6, not 7 — WriteTextFile does no ACP notification, so no file_write event appears in stream (D079)"]
patterns_established:
  - ["Turn fields (TurnId/StreamSeq) injected atomically inside broadcastEnvelope callback under mu.Lock — use this pattern for any translator state that must be captured atomically with seq", "Drain-after-send test pattern: when testing ordered event streams, send one event → collect one envelope → assert → repeat, rather than bulk-enqueue then collect", "NotifyTurnEnd always fires even on error: capture stopReason before the fallible call, use 'error' default, overwrite on success — guarantees turn state is always cleaned up"]
observability_surfaces:
  - ["session/update envelopes now carry turnId and streamSeq — subscribers can reconstruct causal turn ordering without additional metadata", "turn_start event at seq+1 per prompt, turn_end at seq+4 — operators can measure turn duration from seq timestamps"]
drill_down_paths:
  []
duration: ""
verification_result: passed
completed_at: 2026-04-08T20:34:36.735Z
blocker_discovered: false
---

# S05: Event Ordering — Turn-Aware Envelope Enhancement

**Added turnId, streamSeq (*int), and phase to session/update envelopes with atomic Translator turn tracking, and wired NotifyTurnStart/End into handlePrompt — all 8 packages pass with 7 new ordering tests and updated RPC integration tests.**

## What Happened

## S05 Narrative

### Problem
Event envelopes carried only a global `seq` counter — ordering by receive-time, not causal position within a turn. Chat/replay clients had no way to reconstruct causal ordering because seq only reflects when agentd received an event, not its logical position in an agent's turn. Requirement R050 called for `turnId`, `streamSeq`, and `phase` fields on `session/update` to enable turn-aware ordering.

### T01 — Envelope Fields and Atomic Translator Turn Tracking

**pkg/events/envelope.go:** Added three optional fields to `SessionUpdateParams`:
- `TurnId string` — UUID assigned at turn_start, shared by all events in that turn, cleared after turn_end.
- `StreamSeq *int` — pointer (not int) because omitempty would silently drop `0`, making turn_start (streamSeq=0) indistinguishable from a non-turn event. A non-nil `*int(0)` survives omitempty; nil is omitted.
- `Phase string` — reserved for future turn phase annotation (thinking, tool_call, etc.).

**pkg/events/translator.go:** Added `currentTurnId string` and `currentStreamSeq int` to the Translator struct. Both fields are always accessed under `mu` — no annotation required, all mutation happens inside `broadcastEnvelope` callbacks.

**Atomicity invariant:** `broadcastEnvelope` acquires `mu.Lock` before calling its build callback. Mutations to `currentTurnId`/`currentStreamSeq` inside the callback are atomic with `seq` allocation — seq increment and turn-state mutation happen as a single critical section.

**NotifyTurnStart:** Generates a new UUID before entering the callback (UUID generation is not lock-sensitive). Inside the callback: sets `currentTurnId`, resets `currentStreamSeq = 0`, takes its address for `StreamSeq`, increments `currentStreamSeq` to 1.

**NotifyTurnEnd:** Inside the callback: captures `currentStreamSeq`, builds params with `TurnId = currentTurnId` (so turn_end carries the identifier), then clears `currentTurnId = ""` after building params — ensuring the turn_end event itself carries the turn's ID before the turn is closed.

**broadcastSessionEvent:** Updated to inject `TurnId` and `StreamSeq` from current turn state when `currentTurnId != ""`. Mid-turn ACP events (text, tool_call, file_write, etc.) that flow through `broadcastSessionEvent` are automatically annotated. `runtime/stateChange` events go through a separate path and are intentionally excluded from turn fields.

**7 new unit tests** in `translator_test.go`:
1. `TestNotifyTurnStartAndEnd` (updated) — asserts TurnId/StreamSeq on turn_start and turn_end.
2. `TestTurnAwareEnvelope_TurnIdAssigned` — all events in a turn share same non-empty TurnId; event after turn_end has no TurnId.
3. `TestTurnAwareEnvelope_StreamSeqMonotonic` — streamSeq is 0,1,2,3 across turn_start, 2 text events, turn_end.
4. `TestTurnAwareEnvelope_MultipleTurns` — two turns have different TurnIds; streamSeq resets to 0 at second turn_start.
5. `TestTurnAwareEnvelope_StateChangeExcludesTurnFields` — runtime/stateChange envelopes have no TurnId/StreamSeq; seq still increments.
6. `TestTurnAwareEnvelope_RoundTrip` — JSON marshal/unmarshal roundtrip for TurnId/StreamSeq/Phase fields.
7. `TestTurnAwareEnvelope_ReplayOrdering` — two full turns, asserts (a) common TurnId within each turn, (b) different TurnIds across turns, (c) streamSeq 0,1,2,... within each turn, (d) global seq monotonic across both turns.

**Key deviation (T01):** `TestTurnAwareEnvelope_ReplayOrdering` was initially written to bulk-enqueue all ACP events then call `NotifyTurnEnd`. This caused a race: ACP goroutine processed events after `NotifyTurnEnd` cleared `currentTurnId`, producing events with no TurnId. Fixed by drain-after-each-send: send one ACP notification → collect one envelope → repeat → then call `NotifyTurnEnd`.

### T02 — RPC Integration Wiring

**pkg/rpc/server.go `handlePrompt`:** Added `NotifyTurnStart()` before `mgr.Prompt(...)` and `NotifyTurnEnd(stopReason)` always after, even on error. A `stopReason` variable captures `"error"` as default, overwritten with `string(resp.StopReason)` on success. This ensures turn state in the Translator is always cleared regardless of prompt outcome.

**pkg/rpc/server_test.go `TestRPCServer_CleanBreakSurface`:** Updated all three subtests from `collect(4,...)` to `collect(6,...)`. The task plan assumed 7 events per prompt but actual is 6 — `acpClient.WriteTextFile` performs a direct OS write with no ACP SessionNotification emitted, so no `file_write` event appears in the subscriber stream. The actual 6-event sequence per prompt:
- `seq+0`: runtime/stateChange (created→running)
- `seq+1`: session/update turn_start (TurnId non-empty, StreamSeq=0)
- `seq+2`: session/update text 'write:ok' (same TurnId, StreamSeq=1)
- `seq+3`: session/update text 'mock response' (same TurnId, StreamSeq=2)
- `seq+4`: session/update turn_end (same TurnId, StreamSeq=3)
- `seq+5`: runtime/stateChange (running→created)

Turn field assertions added to the `prompt history and recovery metadata` subtest: turn_start at `live[1]` has StreamSeq=0, all `session/update` events `live[1..4]` share the same TurnId, turn_end at `live[4]` has StreamSeq=3. `lastSeq` and `GreaterOrEqual` assertions updated from 3→5.

`grep -c 'collect(4' pkg/rpc/server_test.go` returns 0 — no stale occurrences.

### Verification Results
- `go test ./pkg/events/... -count=1 -v`: all 7 turn-aware tests PASS, all 27 events tests PASS.
- `go test ./pkg/rpc/... -count=1 -v`: all 20 RPC test cases PASS (including all 4 CleanBreakSurface subtests).
- `go test ./pkg/... -count=1`: all 8 packages pass — zero failures, zero regressions.


## Verification

All slice-level verification checks pass:

1. `go test ./pkg/events/... -count=1 -v | grep -E 'PASS|FAIL|TestTurnAware|TestNotifyTurn'` — 7 turn-aware tests (TestNotifyTurnStartAndEnd + 6 TestTurnAwareEnvelope_*) all PASS. Exit 0.

2. `go test ./pkg/rpc/... -count=1 -v | grep -E 'PASS|FAIL|---'` — all 20 RPC test cases PASS, including all 4 CleanBreakSurface subtests. Exit 0.

3. `go test ./pkg/... -count=1` — all 8 packages pass: pkg/agentd, pkg/ari, pkg/events, pkg/meta, pkg/rpc, pkg/runtime, pkg/spec, pkg/workspace. Zero failures, zero regressions.

4. `grep -c 'collect(4' pkg/rpc/server_test.go` → 0 (no stale collect(4) calls).

5. StreamSeq=0 is preserved in turn_start JSON (TestTurnAwareEnvelope_RoundTrip confirms *int omitempty semantics work correctly).

## Requirements Advanced

None.

## Requirements Validated

- R050 — 7 unit tests prove turnId assigned on turn_start, propagated to all mid-turn events, reset on new turn; streamSeq monotonic within turn, reset to 0 on turn_start; runtime/stateChange excluded from turn fields. RPC integration tests confirm turn fields flow end-to-end through handlePrompt. go test ./pkg/... -count=1 all pass.

## New Requirements Surfaced

None.

## Requirements Invalidated or Re-scoped

None.

## Deviations

T01: TestTurnAwareEnvelope_ReplayOrdering required drain-after-send instead of bulk-enqueue due to race between ACP goroutine and NotifyTurnEnd. T02: event count per prompt is 6 not 7 (WriteTextFile emits no ACP notification); lastSeq updated from 6→5, turn_end StreamSeq from 4→3, collect(7) changed to collect(6) throughout.

## Known Limitations

Phase field is added to the struct and JSON schema but not populated by any current code path. It is available for future use (e.g. annotating thinking/tool_call phases within a turn).

## Follow-ups

Phase field population: when agent-shim or Translator gains phase awareness, wire the Phase field on relevant events. If acpClient.WriteTextFile is updated to emit ACP notifications, the RPC integration tests will need to be updated from collect(6) back to collect(7).

## Files Created/Modified

None.
