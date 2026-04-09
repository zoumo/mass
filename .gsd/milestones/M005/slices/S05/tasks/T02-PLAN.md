---
estimated_steps: 69
estimated_files: 2
skills_used: []
---

# T02: Wire NotifyTurnStart/End in handlePrompt and update RPC integration tests

Add NotifyTurnStart before mgr.Prompt and NotifyTurnEnd after mgr.Prompt in handlePrompt in pkg/rpc/server.go. NotifyTurnEnd must always fire — even on error — to clear turn state in the Translator. Update pkg/rpc/server_test.go to expect 7 events per prompt (up from 4) and add turn field assertions.

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

## Inputs

- ``pkg/rpc/server.go` — existing handlePrompt (output of slice baseline; no T01 dependency needed)`
- ``pkg/rpc/server_test.go` — existing TestRPCServer_CleanBreakSurface with 3 collect(4,...) calls to update`
- ``pkg/events/translator.go` — NotifyTurnStart/NotifyTurnEnd signatures (output of T01)`
- ``pkg/events/envelope.go` — SessionUpdateParams with TurnId/StreamSeq fields (output of T01)`

## Expected Output

- ``pkg/rpc/server.go` — handlePrompt with NotifyTurnStart before mgr.Prompt and NotifyTurnEnd always-called after`
- ``pkg/rpc/server_test.go` — all collect(4,...) changed to collect(7,...); turn field assertions added; lastSeq assertions updated`

## Verification

go test ./pkg/rpc/... -count=1 -v 2>&1 | grep -E 'PASS|FAIL|---'
go test ./pkg/... -count=1
# Both must exit 0 with PASS.
# Also confirm: grep -c 'collect(4' pkg/rpc/server_test.go should return 0 (all updated).
