---
estimated_steps: 89
estimated_files: 3
skills_used: []
---

# T01: Add turn fields to SessionUpdateParams and implement Translator turn tracking + unit tests

Add turnId, streamSeq (pointer), and phase (omitempty) to SessionUpdateParams. Add currentTurnId and currentStreamSeq fields to Translator. Rewrite NotifyTurnStart and NotifyTurnEnd to operate inside broadcastEnvelope callbacks (which run under mu.Lock) so turn state assignment and seq increment are atomic. Update broadcastSessionEvent to inject current turn fields when currentTurnId is non-empty. Add 6 new unit tests to translator_test.go proving turn-aware semantics, and update the existing TestNotifyTurnStartAndEnd to assert turn fields.

**Thread-safety invariant to preserve:** broadcastEnvelope acquires mu.Lock before calling the build callback and releases it after. Any mutations to t.currentTurnId or t.currentStreamSeq inside the callback are therefore safe — they run under the same lock that assigns t.nextSeq.

**Steps:**

1. In `pkg/events/envelope.go`, add three optional fields to SessionUpdateParams between SequenceMeta and Event:
   ```go
   TurnId    string `json:"turnId,omitempty"`
   StreamSeq *int   `json:"streamSeq,omitempty"`
   Phase     string `json:"phase,omitempty"`
   ```
   Note: StreamSeq must be *int (pointer), not int. With int, omitempty would omit the value 0, making turn_start (streamSeq=0) indistinguishable from a non-turn event. A non-nil pointer to 0 is included by omitempty; a nil pointer is omitted.

2. In `pkg/events/translator.go`, add two fields to the Translator struct (alongside the existing fields, before the closing brace):
   ```go
   currentTurnId    string
   currentStreamSeq int
   ```
   No lock annotation needed — these are always accessed under mu.

3. Rewrite `NotifyTurnStart` to build the envelope atomically inside broadcastEnvelope's callback:
   ```go
   func (t *Translator) NotifyTurnStart() {
       newTurnId := uuid.New().String()
       t.broadcastEnvelope(func(seq int, at time.Time) Envelope {
           // Runs under mu.Lock — safe to mutate turn state here.
           t.currentTurnId = newTurnId
           t.currentStreamSeq = 0
           ss := t.currentStreamSeq  // = 0
           t.currentStreamSeq++
           params := SessionUpdateParams{
               SequenceMeta: SequenceMeta{
                   SessionID: t.sessionID, Seq: seq,
                   Timestamp: at.UTC().Format(time.RFC3339Nano),
               },
               TurnId: t.currentTurnId, StreamSeq: &ss,
               Event:  newTypedEvent(TurnStartEvent{}),
           }
           return Envelope{Method: MethodSessionUpdate, Params: params}
       })
   }
   ```
   Add import `github.com/google/uuid` (already in go.mod).

4. Rewrite `NotifyTurnEnd` to build the envelope atomically inside broadcastEnvelope's callback, using current turn fields BEFORE clearing them:
   ```go
   func (t *Translator) NotifyTurnEnd(reason acp.StopReason) {
       t.broadcastEnvelope(func(seq int, at time.Time) Envelope {
           // Runs under mu.Lock.
           ss := t.currentStreamSeq
           t.currentStreamSeq++
           params := SessionUpdateParams{
               SequenceMeta: SequenceMeta{
                   SessionID: t.sessionID, Seq: seq,
                   Timestamp: at.UTC().Format(time.RFC3339Nano),
               },
               TurnId: t.currentTurnId, StreamSeq: &ss,
               Event:  newTypedEvent(TurnEndEvent{StopReason: string(reason)}),
           }
           t.currentTurnId = ""  // Clear AFTER using — turn_end event carries the turnId
           return Envelope{Method: MethodSessionUpdate, Params: params}
       })
   }
   ```

5. Update `broadcastSessionEvent` to inject turn fields when currentTurnId is set. This handles all mid-turn ACP events (text, tool_call, file_write, etc.) that flow through the normal ACP notification path:
   ```go
   func (t *Translator) broadcastSessionEvent(ev Event) {
       t.broadcastEnvelope(func(seq int, at time.Time) Envelope {
           env := NewSessionUpdateEnvelope(t.sessionID, seq, at, ev)
           if t.currentTurnId != "" {
               params := env.Params.(SessionUpdateParams)
               params.TurnId = t.currentTurnId
               ss := t.currentStreamSeq
               params.StreamSeq = &ss
               t.currentStreamSeq++
               env.Params = params
           }
           return env
       })
   }
   ```

6. In `pkg/events/translator_test.go`, update `TestNotifyTurnStartAndEnd` to assert:
   - first.TurnId is non-empty UUID
   - second.TurnId == first.TurnId
   - first.StreamSeq is non-nil and *first.StreamSeq == 0
   - second.StreamSeq is non-nil and *second.StreamSeq == 1
   Then add 6 new test functions:

   **TestTurnAwareEnvelope_TurnIdAssigned**: Call NotifyTurnStart, send two ACP text events, call NotifyTurnEnd. Assert all 4 events carry same non-empty TurnId. Assert event after NotifyTurnEnd (via another NotifyStateChange) has no TurnId.

   **TestTurnAwareEnvelope_StreamSeqMonotonic**: Same 4-event sequence. Assert streamSeq values are 0, 1, 2, 3.

   **TestTurnAwareEnvelope_MultipleTurns**: Two NotifyTurnStart/End pairs with text events in each. Assert first turn's TurnId != second turn's TurnId. Assert streamSeq resets to 0 at second turn_start.

   **TestTurnAwareEnvelope_StateChangeExcludesTurnFields**: Call NotifyTurnStart, then NotifyStateChange. Assert the stateChange envelope (method=runtime/stateChange) is not a SessionUpdateParams and has no TurnId/StreamSeq fields. Assert the stateChange seq incremented correctly.

   **TestTurnAwareEnvelope_RoundTrip**: Build a SessionUpdateParams with TurnId='test-turn', StreamSeq=ptr(2), Phase='thinking', marshal to JSON, unmarshal back, assert fields match.

   **TestTurnAwareEnvelope_ReplayOrdering**: Build a sequence of envelopes from two turns by calling NotifyTurnStart, sending events, calling NotifyTurnEnd, then repeating. Assert (1) all events in turn 1 have a common turnId, (2) all events in turn 2 have a different common turnId, (3) within each turn, streamSeq is 0,1,2,..., (4) seq is globally monotonic across both turns.

   For tests that need mid-turn ACP events, feed notifications into the `in` channel while the translator is running (using the existing makeNotif/drainEnvelope helpers).

## Inputs

- ``pkg/events/envelope.go` — existing SessionUpdateParams struct and NewSessionUpdateEnvelope constructor`
- ``pkg/events/translator.go` — Translator struct, NotifyTurnStart, NotifyTurnEnd, broadcastSessionEvent, broadcastEnvelope`
- ``pkg/events/translator_test.go` — existing TestNotifyTurnStartAndEnd to update, helper functions to reuse`

## Expected Output

- ``pkg/events/envelope.go` — SessionUpdateParams with TurnId/StreamSeq/Phase fields added`
- ``pkg/events/translator.go` — Translator with currentTurnId/currentStreamSeq; rewritten NotifyTurnStart/NotifyTurnEnd; updated broadcastSessionEvent`
- ``pkg/events/translator_test.go` — updated TestNotifyTurnStartAndEnd + 6 new TestTurnAwareEnvelope_* test functions`

## Verification

go test ./pkg/events/... -count=1 -v 2>&1 | grep -E 'PASS|FAIL|TestTurnAware|TestNotifyTurn'
# All existing tests must pass; all 7 turn-aware tests must show PASS.
# Also verify no omitempty issue with streamSeq=0:
# grep for 'streamSeq' in test output to confirm turn_start carries the field.
