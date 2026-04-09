# S05: Event Ordering — Turn-Aware Envelope Enhancement — UAT

**Milestone:** M005
**Written:** 2026-04-08T20:34:36.736Z

## S05 UAT: Event Ordering — Turn-Aware Envelope Enhancement

### Preconditions
- Go toolchain available; `go test` works in the repository root.
- No external services required; all tests use in-process mocks.
- Working directory: `/Users/jim/code/zoumo/open-agent-runtime`.

---

### Test Group A: Unit Tests — Envelope Fields and Translator Turn Tracking

**A1. Turn fields present on session/update events inside a turn**

Command:
```
go test ./pkg/events/... -count=1 -v -run TestTurnAwareEnvelope_TurnIdAssigned
```
Steps:
1. Run command.
2. Observe test output.

Expected: `--- PASS: TestTurnAwareEnvelope_TurnIdAssigned`. The test confirms:
- turn_start, two text events, and turn_end all carry a non-empty TurnId.
- A state-change event after turn_end carries no TurnId.

---

**A2. StreamSeq is monotonically increasing within a turn**

Command:
```
go test ./pkg/events/... -count=1 -v -run TestTurnAwareEnvelope_StreamSeqMonotonic
```
Expected: `--- PASS: TestTurnAwareEnvelope_StreamSeqMonotonic`. The test confirms:
- turn_start has StreamSeq=0.
- First text event has StreamSeq=1.
- Second text event has StreamSeq=2.
- turn_end has StreamSeq=3.

---

**A3. StreamSeq=0 is not dropped by omitempty (pointer semantics)**

Command:
```
go test ./pkg/events/... -count=1 -v -run TestTurnAwareEnvelope_RoundTrip
```
Expected: `--- PASS: TestTurnAwareEnvelope_RoundTrip`. The test confirms:
- A `SessionUpdateParams` with `StreamSeq = ptr(0)` marshals to JSON with `"streamSeq":0` present.
- Unmarshaling back restores `*StreamSeq == 0` (not nil).
- TurnId and Phase round-trip correctly.

Edge case covered: if `*int` were replaced with `int`, the value `0` would be omitted. This test would catch the regression.

---

**A4. Multiple turns: TurnId resets, streamSeq resets to 0**

Command:
```
go test ./pkg/events/... -count=1 -v -run TestTurnAwareEnvelope_MultipleTurns
```
Expected: `--- PASS: TestTurnAwareEnvelope_MultipleTurns`. The test confirms:
- Turn 1 events all share TurnId-A.
- Turn 2 events all share TurnId-B.
- TurnId-A != TurnId-B.
- Turn 2 turn_start has StreamSeq=0 (reset).

---

**A5. runtime/stateChange events excluded from turn fields**

Command:
```
go test ./pkg/events/... -count=1 -v -run TestTurnAwareEnvelope_StateChangeExcludesTurnFields
```
Expected: `--- PASS: TestTurnAwareEnvelope_StateChangeExcludesTurnFields`. The test confirms:
- A `runtime/stateChange` envelope emitted while a turn is active does NOT carry TurnId or StreamSeq.
- Its seq increments correctly (global seq is unaffected by turn state).
- The envelope's Params is `RuntimeStateChangeParams`, not `SessionUpdateParams`.

---

**A6. Replay ordering invariants across two complete turns**

Command:
```
go test ./pkg/events/... -count=1 -v -run TestTurnAwareEnvelope_ReplayOrdering
```
Expected: `--- PASS: TestTurnAwareEnvelope_ReplayOrdering`. The test confirms:
1. All events in turn 1 share a common TurnId.
2. All events in turn 2 share a different common TurnId.
3. Within each turn, streamSeq is 0, 1, 2, ... (monotonic, no gaps).
4. Global seq is monotonically increasing across both turns (no resets, no gaps).

---

**A7. NotifyTurnStartAndEnd — baseline turn field assertions**

Command:
```
go test ./pkg/events/... -count=1 -v -run TestNotifyTurnStartAndEnd
```
Expected: `--- PASS: TestNotifyTurnStartAndEnd`. The test confirms:
- turn_start carries non-empty TurnId and StreamSeq=0.
- turn_end carries the same TurnId and StreamSeq=1.

---

### Test Group B: RPC Integration Tests — End-to-End Turn Envelope Flow

**B1. Full 6-event sequence per prompt with turn field assertions**

Command:
```
go test ./pkg/rpc/... -count=1 -v -run TestRPCServer_CleanBreakSurface/prompt_history_and_recovery_metadata
```
Expected: `--- PASS: TestRPCServer_CleanBreakSurface/prompt_history_and_recovery_metadata`. The test confirms:
- 6 events collected per prompt (not 4 as before, not 7 as planned).
- live[0]: `runtime/stateChange` (created→running) — no TurnId.
- live[1]: `session/update` turn_start — TurnId non-empty, StreamSeq=0.
- live[2]: `session/update` text 'write:ok' — same TurnId, StreamSeq=1.
- live[3]: `session/update` text 'mock response' — same TurnId, StreamSeq=2.
- live[4]: `session/update` turn_end — same TurnId, StreamSeq=3.
- live[5]: `runtime/stateChange` (running→created) — no TurnId.
- Recovery lastSeq = 5.

---

**B2. No stale collect(4) calls remain**

Command:
```
grep -c 'collect(4' pkg/rpc/server_test.go
```
Expected: `0` — all prompt collection calls use collect(6,...).

---

**B3. All RPC subtests pass**

Command:
```
go test ./pkg/rpc/... -count=1 -v
```
Expected: all 20 test cases PASS. Key subtests:
- `subscribe_and_status` — PASS.
- `prompt_history_and_recovery_metadata` — PASS.
- `subscribe_with_fromSeq_returns_backfill` — PASS (backfill Entries len=6, not 4).
- `subscribe_afterSeq_filters_prior_history` — PASS.
- All `RejectsLegacyAndInvalidParams` subtests — PASS.
- `StopRepliesBeforeDisconnect` — PASS.

---

### Test Group C: Regression — All Packages

**C1. Full package regression**

Command:
```
go test ./pkg/... -count=1
```
Expected: all 8 packages report `ok`:
```
ok  github.com/open-agent-d/open-agent-d/pkg/agentd
ok  github.com/open-agent-d/open-agent-d/pkg/ari
ok  github.com/open-agent-d/open-agent-d/pkg/events
ok  github.com/open-agent-d/open-agent-d/pkg/meta
ok  github.com/open-agent-d/open-agent-d/pkg/rpc
ok  github.com/open-agent-d/open-agent-d/pkg/runtime
ok  github.com/open-agent-d/open-agent-d/pkg/spec
ok  github.com/open-agent-d/open-agent-d/pkg/workspace
```
No FAIL lines. Exit code 0.

---

### Edge Cases

**EC1. turn_end always fires even when Prompt returns error**
- `handlePrompt` uses `stopReason := "error"` default, overwrites with actual stop reason on success.
- `NotifyTurnEnd` called before the error reply branch.
- If the Translator is inspected after a failed prompt, `currentTurnId` must be empty (turn cleared).

**EC2. StreamSeq=0 on turn_start is not confused with "no streamSeq"**
- Covered by TestTurnAwareEnvelope_RoundTrip (A3 above).
- A subscriber seeing `"streamSeq":0` in JSON knows this is a turn event at position 0, not a non-turn event missing the field.

**EC3. Mid-turn ACP events injected after NotifyTurnEnd has cleared turn state have no TurnId**
- Covered by TestTurnAwareEnvelope_TurnIdAssigned (A1 above): event emitted after turn_end has no TurnId.
- In production, this can occur if an ACP notification is delayed. The event is still globally sequenced; it just isn't attributable to a turn.

