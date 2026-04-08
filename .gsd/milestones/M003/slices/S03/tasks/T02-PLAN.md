---
estimated_steps: 56
estimated_files: 4
skills_used: []
---

# T02: Add atomic SubscribeFromSeq to Translator and extend RPC subscribe handler

## Description

Add a `SubscribeFromSeq(logPath string, fromSeq int)` method to `events.Translator` that atomically reads the JSONL log from `fromSeq` and registers a live subscription, all under `t.mu`. This eliminates the event gap between separate History and Subscribe calls. Then extend the shim RPC server's `session/subscribe` handler to support an optional `fromSeq` parameter that triggers the atomic path, returning backfill entries in the response.

The key invariant: because `broadcastEnvelope` holds `t.mu` while assigning seq numbers and collecting the subscriber list, and `SubscribeFromSeq` holds `t.mu` while reading the log and registering the subscription, no events can be assigned between the history read and the subscription start.

Note: `SubscribeFromSeq` performs file I/O under the Translator's mutex. This is acceptable for recovery/startup (sub-millisecond for typical log sizes) but must NOT be used in hot paths. Document this in the method's godoc.

## Steps

1. In `pkg/events/translator.go`, add method:
   ```go
   // SubscribeFromSeq atomically reads history from logPath starting at fromSeq
   // and registers a live subscription, all under the Translator's mutex.
   // This eliminates the event gap between separate History and Subscribe calls.
   //
   // Intended for recovery/startup only — holds the mutex during file I/O.
   // Do not use in hot paths where event broadcasting latency matters.
   func (t *Translator) SubscribeFromSeq(logPath string, fromSeq int) ([]Envelope, <-chan Envelope, int, int, error)
   ```
   Implementation: acquire `t.mu`, call `ReadEventLog(logPath, fromSeq)`, register subscription channel (same as `Subscribe()`), capture `t.nextSeq`, release `t.mu`, return `(entries, ch, subID, nextSeq, nil)`.

2. In `pkg/events/translator_test.go`, add `TestSubscribeFromSeq_BackfillAndLive`:
   - Create a temp JSONL file, open EventLog, write 5 entries (seq 0-4), close.
   - Create a Translator with a new EventLog opened on the same path.
   - Call `SubscribeFromSeq(logPath, 2)` — assert 3 backfill entries (seq 2,3,4).
   - Broadcast a new event via the Translator.
   - Assert the subscription channel receives the new event (seq 5).
   - Assert no gap: backfill ends at seq 4, live starts at seq 5.

3. In `pkg/events/translator_test.go`, add `TestSubscribeFromSeq_EmptyLog`:
   - Create an empty log path (non-existent file).
   - Call `SubscribeFromSeq` — assert empty backfill, valid subscription.

4. In `pkg/rpc/server.go`, extend the RPC types:
   - Add `FromSeq *int \`json:"fromSeq,omitempty"\`` to `SessionSubscribeParams`
   - Add `Entries []events.Envelope \`json:"entries,omitempty"\`` to `SessionSubscribeResult`

5. In `pkg/rpc/server.go`, update `handleSubscribe`: when `p.FromSeq` is not nil, validate `*p.FromSeq >= 0`, then call `h.srv.trans.SubscribeFromSeq(h.srv.logPath, *p.FromSeq)` instead of `h.srv.trans.Subscribe()`. Return entries in the result. The notification goroutine uses the subscription channel exactly as before. When `FromSeq` is present, ignore `AfterSeq` — the atomic path handles filtering.

6. In `pkg/rpc/server_test.go`, add a subtest `subscribe with fromSeq returns backfill` inside `TestRPCServer_CleanBreakSurface` (or as a standalone test). After the first prompt that generates 4 events, open a second client, call `session/subscribe` with `fromSeq=0`. Assert the response contains 4 backfill entries with contiguous seq [0-3]. Then trigger a second prompt and assert the new events arrive as live notifications on the same connection.

## Must-Haves

- [ ] `Translator.SubscribeFromSeq` reads log + registers subscription under a single `t.mu` lock hold
- [ ] Backfill entries from `SubscribeFromSeq` have correct seq numbers matching the log
- [ ] Live subscription after `SubscribeFromSeq` receives events starting from the next seq after backfill
- [ ] RPC `session/subscribe` with `fromSeq` returns entries in the response
- [ ] RPC `session/subscribe` without `fromSeq` continues to work exactly as before (backward compatible)
- [ ] Negative `fromSeq` is rejected with InvalidParams error

## Negative Tests

- **Malformed inputs**: `fromSeq < 0` → InvalidParams error at RPC level
- **Boundary conditions**: `fromSeq=0` on empty log → empty entries, valid subscription; `fromSeq` beyond log end → empty entries (ReadEventLog returns empty for fromSeq > max)

## Verification

- `go test ./pkg/events/... -count=1 -v` — translator tests pass including new SubscribeFromSeq tests
- `go test ./pkg/rpc/... -count=1 -v` — RPC server tests pass including new fromSeq integration test
- `go vet ./pkg/events/... ./pkg/rpc/...` — no issues

## Inputs

- `pkg/events/translator.go` — existing Subscribe() and broadcastEnvelope() to understand lock discipline
- `pkg/events/translator_test.go` — existing test patterns for Translator
- `pkg/events/log.go` — ReadEventLog function called by SubscribeFromSeq (damaged-tail tolerant after T01)
- `pkg/rpc/server.go` — existing handleSubscribe to extend
- `pkg/rpc/server_test.go` — existing server test harness

## Expected Output

- `pkg/events/translator.go` — new SubscribeFromSeq method
- `pkg/events/translator_test.go` — 2+ new tests for SubscribeFromSeq
- `pkg/rpc/server.go` — extended types and handleSubscribe handler
- `pkg/rpc/server_test.go` — new integration test for subscribe with fromSeq

## Inputs

- `pkg/events/translator.go`
- `pkg/events/translator_test.go`
- `pkg/events/log.go`
- `pkg/rpc/server.go`
- `pkg/rpc/server_test.go`

## Expected Output

- `pkg/events/translator.go`
- `pkg/events/translator_test.go`
- `pkg/rpc/server.go`
- `pkg/rpc/server_test.go`

## Verification

go test ./pkg/events/... -count=1 -v && go test ./pkg/rpc/... -count=1 -v && go vet ./pkg/events/... ./pkg/rpc/...
