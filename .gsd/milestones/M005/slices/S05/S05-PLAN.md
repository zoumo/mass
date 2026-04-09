# S05: Event Ordering — Turn-Aware Envelope Enhancement

**Goal:** Add turnId, streamSeq, and phase fields to the session/update envelope; track turn state in Translator; wire NotifyTurnStart/End in handlePrompt; prove ordering semantics with unit tests and updated RPC integration tests.
**Demo:** After this: TBD

## Tasks
- [x] **T01: Added TurnId/StreamSeq/Phase to SessionUpdateParams and rewrote Translator turn tracking for atomic ordering; all 7 turn-aware tests pass with zero regressions** — Add turnId, streamSeq (pointer), and phase (omitempty) to SessionUpdateParams. Add currentTurnId and currentStreamSeq fields to Translator. Rewrite NotifyTurnStart and NotifyTurnEnd to operate inside broadcastEnvelope callbacks (which run under mu.Lock) so turn state assignment and seq increment are atomic. Update broadcastSessionEvent to inject current turn fields when currentTurnId is non-empty. Add 6 new unit tests to translator_test.go proving turn-aware semantics, and update the existing TestNotifyTurnStartAndEnd to assert turn fields.

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
  - Estimate: 90 minutes
  - Files: pkg/events/envelope.go, pkg/events/translator.go, pkg/events/translator_test.go
  - Verify: go test ./pkg/events/... -count=1 -v 2>&1 | grep -E 'PASS|FAIL|TestTurnAware|TestNotifyTurn'
# All existing tests must pass; all 7 turn-aware tests must show PASS.
# Also verify no omitempty issue with streamSeq=0:
# grep for 'streamSeq' in test output to confirm turn_start carries the field.
- [x] **T02: Wired NotifyTurnStart/NotifyTurnEnd into handlePrompt and updated RPC integration tests from 4-event to 6-event model with turn field assertions** — Add NotifyTurnStart before mgr.Prompt and NotifyTurnEnd after mgr.Prompt in handlePrompt in pkg/rpc/server.go. NotifyTurnEnd must always fire — even on error — to clear turn state in the Translator. Update pkg/rpc/server_test.go to expect 7 events per prompt (up from 4) and add turn field assertions.

**Steps:**

1. In `pkg/rpc/server.go`, rewrite `handlePrompt` to call NotifyTurnStart before Prompt and NotifyTurnEnd after (always, even on error):
   ```go
   func (h *connHandler) handlePrompt(ctx context.Context, conn *jsonrpc2.Conn, req *jsonrpc2.Request) {
       var p SessionPromptParams
       if err := unmarshalParams(req, &p); err != nil {
           replyError(ctx, conn, req.ID, jsonrpc2.CodeInvalidParams, err.Error())
           return
       }
       if p.Prompt == "" {
           replyError(ctx, conn, req.ID, jsonrpc2.CodeInvalidParams, "missing prompt")
           return
       }

       h.srv.trans.NotifyTurnStart()
       resp, err := h.srv.mgr.Prompt(ctx, []acp.ContentBlock{acp.TextBlock(p.Prompt)})
       stopReason := "error"
       if err == nil {
           stopReason = string(resp.StopReason)
       }
       h.srv.trans.NotifyTurnEnd(acp.StopReason(stopReason))

       if err != nil {
           replyError(ctx, conn, req.ID, jsonrpc2.CodeInternalError, err.Error())
           return
       }
       _ = conn.Reply(ctx, req.ID, SessionPromptResult{StopReason: string(resp.StopReason)})
   }
   ```

2. In `pkg/rpc/server_test.go`, the test `TestRPCServer_CleanBreakSurface` has three subtests that each collect events after a prompt. Each must be updated from collect(4,...) to collect(7,...). The expected event sequence per prompt is:
   - seq+0: runtime/stateChange (created→running)
   - seq+1: session/update turn_start (TurnId non-empty, StreamSeq=0)
   - seq+2: session/update file_write (TurnId same as seq+1, StreamSeq=1)
   - seq+3: session/update text 'write:ok' (TurnId same, StreamSeq=2)
   - seq+4: session/update text 'mock response' (TurnId same, StreamSeq=3)
   - seq+5: session/update turn_end (TurnId same, StreamSeq=4)
   - seq+6: runtime/stateChange (running→created)

   Search for all occurrences of `collect(4,` in server_test.go and change them to `collect(7,`. There are 3 occurrences (first prompt, second prompt, third prompt subtests).

3. In the 'prompt history and recovery metadata' subtest, update the existing assertions:
   - Change `require.Equal(t, events.MethodRuntimeStateChange, live[3].Method)` → `live[6].Method`
   - The `status.Recovery.LastSeq` assertion `require.Equal(t, 3, ...)` must change to `require.Equal(t, 6, ...)` (7 events means lastSeq = 6)
   - Add turn field assertions after `sortEnvelopesBySeq(live)`:
     ```go
     // Assert turn_start (live[1]) has TurnId and StreamSeq=0
     ts := live[1].Params.(events.SessionUpdateParams)
     require.Equal(t, events.MethodSessionUpdate, live[1].Method)
     require.NotEmpty(t, ts.TurnId)
     require.NotNil(t, ts.StreamSeq)
     require.Equal(t, 0, *ts.StreamSeq)
     // Assert all session/update events in turn share the same TurnId
     for i := 1; i <= 5; i++ {
         p := live[i].Params.(events.SessionUpdateParams)
         require.Equal(t, ts.TurnId, p.TurnId, "live[%d] TurnId mismatch", i)
     }
     // Assert turn_end (live[5]) has StreamSeq=4
     te := live[5].Params.(events.SessionUpdateParams)
     require.NotNil(t, te.StreamSeq)
     require.Equal(t, 4, *te.StreamSeq)
     // Assert stateChange events have no TurnId
     sc0 := live[0].Params.(events.RuntimeStateChangeParams)
     require.Equal(t, "", sc0.SessionID) // sanity — wrong type would panic
     ```
     Actually, RuntimeStateChangeParams does not have TurnId — the type assertion itself proves they're different params types. Just assert Method for stateChange events.

4. In the 'subscribe with fromSeq returns backfill' subtest:
   - Change `require.Len(t, subResult.Entries, 4, ...)` → 7
   - Change `collect(4, ...)` → `collect(7, ...)` for the second prompt
   - Update the `require.GreaterOrEqual(t, seq, 4, ...)` comment and value: live events from second prompt have seq >= 7

5. In the 'subscribe afterSeq filters prior history' subtest:
   - Change `collect(4, ...)` → `collect(7, ...)`

6. Run `go test ./pkg/rpc/... -count=1 -v` to confirm all tests pass. Then run `go test ./pkg/... -count=1` for full regression check.
  - Estimate: 45 minutes
  - Files: pkg/rpc/server.go, pkg/rpc/server_test.go
  - Verify: go test ./pkg/rpc/... -count=1 -v 2>&1 | grep -E 'PASS|FAIL|---'
go test ./pkg/... -count=1
# Both must exit 0 with PASS.
# Also confirm: grep -c 'collect(4' pkg/rpc/server_test.go should return 0 (all updated).
