# S05 Research — Event Ordering: Turn-Aware Envelope Enhancement

## Summary

S05 is a targeted, well-scoped implementation slice. The design is fully specified in `docs/design/runtime/shim-rpc-spec.md` (Turn-Aware Event Ordering section, added in S01). The code is concentrated in `pkg/events/` (3 files) and `pkg/rpc/server.go`. No new dependencies needed — `github.com/google/uuid` is already in `go.mod`.

The work is split cleanly across two independent units: (1) the envelope/translator layer (`pkg/events/`) and (2) the RPC prompt handler (`pkg/rpc/server.go`). The existing test suite is comprehensive and all tests currently pass. S05 adds 3 new fields and hooks up 2 already-written methods.

**Complexity: Light** — well-understood patterns, established codebase, zero new dependencies.

---

## Requirement

**R050** — Event envelopes carry `turnId`, `streamSeq`, and `phase` for turn-aware ordering. Global `seq` retained as log sequence. Chat/replay orders by `(turnId, streamSeq)`.

The R050 validation note reads: "unit test proof (turnId assigned on turn_start, replay ordering) deferred to S05 implementation." S05 must deliver this proof.

---

## Implementation Landscape

### Key Files

| File | Role | S05 Changes |
|------|------|-------------|
| `pkg/events/envelope.go` | `SessionUpdateParams`, `NewSessionUpdateEnvelope`, marshal/unmarshal | Add `TurnId`, `StreamSeq`, `Phase` fields; update constructor |
| `pkg/events/translator.go` | `Translator` fan-out, `NotifyTurnStart`, `NotifyTurnEnd` | Track `currentTurnId` and `currentStreamSeq`; inject fields when building session/update envelopes |
| `pkg/events/translator_test.go` | Unit tests | Add turn-aware field assertions to `TestNotifyTurnStartAndEnd`; add new turn ordering tests |
| `pkg/rpc/server.go` | `handlePrompt` — the place that calls `mgr.Prompt` | Call `trans.NotifyTurnStart()` before and `trans.NotifyTurnEnd()` after `mgr.Prompt` |
| `pkg/rpc/server_test.go` | Integration test with mockagent | Update event count (4→7 per prompt); add turn field assertions |

### What Currently Exists (and Works)

1. **`SessionUpdateParams`** (`pkg/events/envelope.go:63-70`):
   ```go
   type SessionUpdateParams struct {
       SequenceMeta
       Event TypedEvent `json:"event"`
   }
   ```
   Missing: `TurnId`, `StreamSeq`, `Phase` fields.

2. **`NewSessionUpdateEnvelope`** (`pkg/events/envelope.go:92-106`):
   Currently takes `(sessionID, seq, at, ev)` — no turn fields.

3. **`Translator.NotifyTurnStart()`** and **`NotifyTurnEnd()`** (`pkg/events/translator.go:122-130`):
   Already exist and broadcast `TurnStartEvent{}`/`TurnEndEvent{}`. Currently they just call `broadcastSessionEvent`, with no turn ID assigned.

4. **`handlePrompt`** (`pkg/rpc/server.go:158-177`):
   Currently calls `mgr.Prompt(...)` but does NOT call `trans.NotifyTurnStart()` before or `trans.NotifyTurnEnd()` after. This is the main wiring gap.

5. **`Translator.broadcastSessionEvent`** / **`broadcastEnvelope`** (`pkg/events/translator.go:157-185`):
   The internal hot path. `broadcastSessionEvent` wraps `NewSessionUpdateEnvelope` — this is where turn fields must be injected.

### Current Event Count per Prompt (RPC integration test)

The existing `pkg/rpc/server_test.go` `TestRPCServer_CleanBreakSurface` expects exactly **4 events** per `session/prompt` call against the mockagent:
- seq 0: `runtime/stateChange` (created→running) — from `SetStateChangeHook`
- seq 1: `session/update` file_write (mockagent writes `/tmp/mock-agent-test.txt`)
- seq 2: `session/update` text "write:ok"
- seq 3: `session/update` text "mock response"
- (seq 3): `runtime/stateChange` (running→created) — wait, let me re-check: test asserts `live[3].Method == MethodRuntimeStateChange`

After S05 adds `NotifyTurnStart` before `Prompt` and `NotifyTurnEnd` after, the event sequence becomes:
- seq 0: `runtime/stateChange` (created→running)
- seq 1: `session/update` **turn_start** (with `turnId`, `streamSeq=0`)
- seq 2: `session/update` file_write (with `turnId`, `streamSeq=1`)
- seq 3: `session/update` text "write:ok" (with `turnId`, `streamSeq=2`)
- seq 4: `session/update` text "mock response" (with `turnId`, `streamSeq=3`)
- seq 5: `session/update` **turn_end** (with `turnId`, `streamSeq=4`)
- seq 6: `runtime/stateChange` (running→created)

**Total: 7 events** (up from 4). The RPC integration tests must be updated from `collect(4, ...)` to `collect(7, ...)` and assertions updated accordingly. Other tests in the file that expect 4 events per prompt similarly need updating.

---

## Design Spec (from shim-rpc-spec.md)

### New fields on `SessionUpdateParams`:

| Field | Type | Semantics |
|-------|------|-----------|
| `turnId` | `string` (omitempty) | Assigned at `turn_start`, cleared after `turn_end`. Same value on all events within the turn. |
| `streamSeq` | `int` (pointer, omitempty) | Resets to 0 at `turn_start`, increments per event within turn. Absent on non-turn events and `runtime/stateChange`. |
| `phase` | `string` (omitempty) | Optional: `"thinking"`, `"acting"`, `"tool_call"`. Not required for initial implementation. |

### Ordering rules:
1. `seq` = global unique sequence (all notifications, including `runtime/stateChange`)
2. `turnId` assigned at `NotifyTurnStart`, cleared at `NotifyTurnEnd` (or after)
3. `streamSeq` resets to 0 at `NotifyTurnStart`, increments per `session/update` within turn
4. `runtime/stateChange` excludes turn fields (seq only)

### Replay semantics:
- Within a `turnId`: order by `(turnId, streamSeq)`
- Across turns or absent `turnId`: order by `seq`

---

## Implementation Plan

### Task 1 — Envelope fields + Translator turn tracking (`pkg/events/`)

**Files:** `pkg/events/envelope.go`, `pkg/events/translator.go`

1. Add optional fields to `SessionUpdateParams`:
   ```go
   type SessionUpdateParams struct {
       SequenceMeta
       TurnId    string `json:"turnId,omitempty"`
       StreamSeq *int   `json:"streamSeq,omitempty"`
       Phase     string `json:"phase,omitempty"`
       Event     TypedEvent `json:"event"`
   }
   ```

2. Add turn state to `Translator`:
   ```go
   type Translator struct {
       ...
       currentTurnId  string  // empty = not in a turn
       currentStreamSeq int   // resets to 0 per turn
   }
   ```

3. Update `broadcastSessionEvent` to capture/inject current turn fields under the same `mu.Lock()` that increments `nextSeq`. This ensures atomic assignment:
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

4. Update `NotifyTurnStart` to assign a new `turnId` (using `github.com/google/uuid`) and reset `streamSeq`:
   ```go
   func (t *Translator) NotifyTurnStart() {
       t.mu.Lock()
       t.currentTurnId = uuid.New().String()
       t.currentStreamSeq = 0
       t.mu.Unlock()
       t.broadcastSessionEvent(TurnStartEvent{})
   }
   ```
   Wait — the turn state must be set BEFORE the turn_start event is broadcast (so the event carries turnId/streamSeq=0). The `mu.Lock()` in `broadcastEnvelope` handles atomicity — but `broadcastSessionEvent` calls `broadcastEnvelope`, which internally acquires the lock. If we set the turn state outside the lock and then call broadcastEnvelope, there's a race. 
   
   **Correct approach**: set turn state inside the `broadcastEnvelope` callback under the lock, or set it directly before calling `broadcastSessionEvent` by acquiring the translator's mutex once for the whole operation. The cleanest solution is to have `NotifyTurnStart` set the turn fields directly within the callback passed to `broadcastEnvelope` (since the callback runs under `mu.Lock()`):

   ```go
   func (t *Translator) NotifyTurnStart() {
       newTurnId := uuid.New().String()
       t.broadcastEnvelope(func(seq int, at time.Time) Envelope {
           // Under mu.Lock()
           t.currentTurnId = newTurnId
           t.currentStreamSeq = 0
           ss := t.currentStreamSeq
           t.currentStreamSeq++
           params := SessionUpdateParams{
               SequenceMeta: SequenceMeta{SessionID: t.sessionID, Seq: seq, Timestamp: at.UTC().Format(time.RFC3339Nano)},
               TurnId:    t.currentTurnId,
               StreamSeq: &ss,
               Event:     newTypedEvent(TurnStartEvent{}),
           }
           return Envelope{Method: MethodSessionUpdate, Params: params}
       })
   }
   ```
   This executes atomically under the `mu.Lock()` in `broadcastEnvelope`.

5. Update `NotifyTurnEnd` to clear `currentTurnId` AFTER the turn_end event (so the event itself still carries the turnId):
   ```go
   func (t *Translator) NotifyTurnEnd(reason acp.StopReason) {
       t.broadcastEnvelope(func(seq int, at time.Time) Envelope {
           // Under mu.Lock() — still in turn, so populate fields
           ss := t.currentStreamSeq
           t.currentStreamSeq++
           params := SessionUpdateParams{
               SequenceMeta: SequenceMeta{SessionID: t.sessionID, Seq: seq, Timestamp: at.UTC().Format(time.RFC3339Nano)},
               TurnId:    t.currentTurnId,
               StreamSeq: &ss,
               Event:     newTypedEvent(TurnEndEvent{StopReason: string(reason)}),
           }
           t.currentTurnId = ""  // clear AFTER using it
           return Envelope{Method: MethodSessionUpdate, Params: params}
       })
   }
   ```

6. Update `broadcastSessionEvent` (for mid-turn ACP events) to inject turn fields if `currentTurnId != ""`.

7. Update `NewSessionUpdateEnvelope` if needed for the constructor path (used in tests directly). Since the turn fields are injected by the Translator and not by the constructor, the constructor signature can remain unchanged — tests that build envelopes directly won't need turn fields.

**Note on `broadcastEnvelope` mutation:** `broadcastEnvelope` currently calls `build(t.nextSeq, time.Now().UTC())` under `mu.Lock()`. The callback mutates translator state — this is safe as long as the callback only runs under the same lock. The callback currently modifies nothing; adding turn state mutations inside is a natural extension.

### Task 2 — RPC server wiring (`pkg/rpc/server.go`)

**File:** `pkg/rpc/server.go`

Add `NotifyTurnStart` before `Prompt` and `NotifyTurnEnd` after in `handlePrompt`:

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
    h.srv.trans.NotifyTurnEnd(acp.StopReason(func() string {
        if err != nil { return "error" }
        return string(resp.StopReason)
    }()))

    if err != nil {
        replyError(ctx, conn, req.ID, jsonrpc2.CodeInternalError, err.Error())
        return
    }
    _ = conn.Reply(ctx, req.ID, SessionPromptResult{StopReason: string(resp.StopReason)})
}
```

The `NotifyTurnEnd` must always fire (even on error) to ensure turn state is cleared in the Translator.

### Task 3 — Unit tests (`pkg/events/translator_test.go`)

New tests required to prove R050:

1. **`TestTurnAwareEnvelope_TurnIdAssignedOnTurnStart`**: Verify turnId is non-empty on turn_start event, same turnId on all events within turn, turnId absent (empty) after turn_end.

2. **`TestTurnAwareEnvelope_StreamSeqMonotonic`**: Verify streamSeq=0 on turn_start, increments 1,2,3... for subsequent events within the turn.

3. **`TestTurnAwareEnvelope_MultipleTurns`**: Verify different turnIds on different turns, streamSeq resets to 0 at each new turn_start.

4. **`TestTurnAwareEnvelope_StateChangeExcludesTurnFields`**: Verify `runtime/stateChange` (via `NotifyStateChange`) does NOT carry `turnId`/`streamSeq`.

5. **`TestTurnAwareEnvelope_RoundTrip`**: Verify JSON marshal/unmarshal preserves turn fields.

6. **`TestTurnAwareEnvelope_ReplayOrdering`**: Given a sequence of events from two turns, verify that sorting by `(turnId, streamSeq)` within each turn and by `seq` across turns produces deterministic causal order.

### Task 4 — RPC integration test update (`pkg/rpc/server_test.go`)

Update `TestRPCServer_CleanBreakSurface` and related tests:

- Change `collect(4, ...)` → `collect(7, ...)` for first prompt (stateChange + turn_start + file_write + text + text + turn_end + stateChange)
- Change `collect(4, ...)` → `collect(7, ...)` for subsequent prompts
- Assert turn_start has turnId, streamSeq=0
- Assert turn_end has same turnId, non-zero streamSeq
- Assert stateChange events lack turnId/streamSeq
- Assert all session/update events within a turn share same turnId

---

## Constraints and Risks

### Thread safety
`Translator.broadcastEnvelope` already uses `mu` for the `nextSeq` increment and the subscriber snapshot. Turn state (`currentTurnId`, `currentStreamSeq`) must also be mutated under this same lock. The key insight: the `build` callback passed to `broadcastEnvelope` runs inside `mu.Lock()`, so any mutations to turn state inside the callback are safe. This is the correct pattern.

### Interleaved concurrent prompts
The shim is single-session-per-shim (D060 decision). Only one `session/prompt` can be active at a time (it blocks until turn completes). So there's no risk of two concurrent turns racing on `currentTurnId`. However, `handleCancel` could interleave. Since `session/cancel` doesn't emit a session/update (it returns nil), it doesn't interact with turn ordering.

### Phase field
The spec defines `phase` as optional (`"thinking"`, `"acting"`, `"tool_call"`). For S05, `phase` can be left unset (omitempty, empty string) — the field is declared but not populated in the initial implementation. Phase inference from event types would require additional logic and is not required for R050.

### Existing tests that need updating
`pkg/rpc/server_test.go` has multiple places that check for exactly 4 events per prompt. These all need updating after the NotifyTurnStart/NotifyTurnEnd calls are wired. The failing test count would be a clear indicator of missed updates.

### JSON marshal/unmarshal for `StreamSeq *int`
Using a pointer (`*int`) for `StreamSeq` allows JSON `omitempty` to omit the field when nil (non-turn events) vs. including `0` when the pointer is non-nil pointing to zero (turn_start event with streamSeq=0). If an `int` is used instead, `0` would be omitted by `omitempty`, making turn_start indistinguishable from a non-turn event. A pointer is required here.

---

## Verification Commands

```bash
# Run events package unit tests
go test ./pkg/events/... -count=1 -v

# Run RPC integration tests  
go test ./pkg/rpc/... -count=1 -v

# Run all package tests
go test ./pkg/... -count=1

# Contract script (confirms S01 docs unchanged)
bash scripts/verify-m005-s01-contract.sh
```

---

## Skills Discovered

None applicable — pure Go, established patterns in this codebase.
