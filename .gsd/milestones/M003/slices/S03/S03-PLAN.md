# S03: Atomic Event Resume and Damaged-Tail Tolerance

**Goal:** Event log reads tolerate damaged tail lines, and the History→Subscribe gap in recovery is eliminated by an atomic subscribe-from-seq mechanism that returns backfill entries and a live subscription under a single lock hold.
**Demo:** After this: TBD

## Tasks
- [x] **T01: Rewrote ReadEventLog to use bufio.Scanner per-line scanning with damaged-tail tolerance — corrupt trailing lines are skipped while mid-file corruption still errors** — ## Description

Switch `ReadEventLog` from `json.Decoder` (which fails the entire read on any corrupt line) to `bufio.Scanner` + `json.Unmarshal` per line. When a line fails to unmarshal AND no valid lines follow it, treat it as a damaged tail (partial write from a crash) — log the skip and return the successfully decoded entries. If corrupt lines appear in the middle of the file (valid lines follow), return an error as before.

Also verify that `OpenEventLog` + `Append` works correctly after a damaged tail: `countLines` counts the corrupt line as a non-empty line, so `nextSeq` will be one higher than the last valid entry's seq. The next `Append` uses the line-count-based seq, which is correct — the damaged line is a lost slot.

## Steps

1. In `pkg/events/log.go`, rewrite `ReadEventLog` to use `bufio.Scanner` + `json.Unmarshal` per line instead of `json.Decoder`. Use a 1MB buffer (matching `countLines`). For each non-empty line that fails `json.Unmarshal`, peek ahead (continue scanning). If any subsequent line is non-empty AND valid JSON, we have mid-file corruption — return an error. If no valid lines follow, it's tail damage — break and return what we have. Use `log.Printf` (matching existing logging style in the package) to log skipped damaged tail lines.

2. In `pkg/events/log_test.go`, update `TestReadEventLog_CorruptRowFails` — this test writes a single corrupt line. Since that's the only line and no valid lines follow, the new behavior should return an empty slice (not an error). Rename it to `TestReadEventLog_DamagedTailReturnsPartial` and write content with valid entries followed by a corrupt tail.

3. Add `TestReadEventLog_DamagedTailTolerated` — write 3 valid JSONL entries + 1 truncated JSON line at the end. Assert `ReadEventLog` returns the 3 valid entries without error.

4. Add `TestReadEventLog_MidFileCorruptionFails` — write 2 valid entries + 1 corrupt line + 2 more valid entries. Assert `ReadEventLog` returns an error (mid-file corruption is not tolerated).

5. Add `TestEventLog_AppendAfterDamagedTail` — write 3 valid JSONL entries + 1 corrupt tail line to a file. Call `OpenEventLog` (which uses `countLines` — should return 4, setting nextSeq=4). Append a new entry with seq=4. Close and read back all entries. Assert: 3 original + 1 new = 4 valid entries, corrupt line skipped in the read.

## Must-Haves

- [ ] `ReadEventLog` uses line-by-line scanning, not `json.Decoder`
- [ ] Damaged tail (corrupt line(s) at end of file with no valid lines after) returns partial results, not error
- [ ] Mid-file corruption (valid lines after corrupt lines) still returns an error
- [ ] `OpenEventLog` + `Append` works correctly after damaged tail — seq numbering is consistent
- [ ] All existing `log_test.go` tests pass (updated as needed for new behavior)

## Verification

- `go test ./pkg/events/... -count=1 -v` — all tests pass including new damaged-tail tests
- `go vet ./pkg/events/...` — no issues

## Inputs

- `pkg/events/log.go` — current ReadEventLog implementation to modify
- `pkg/events/log_test.go` — existing tests to update and extend

## Expected Output

- `pkg/events/log.go` — ReadEventLog rewritten with line-by-line scanning and damaged-tail tolerance
- `pkg/events/log_test.go` — 3+ new tests for damaged-tail scenarios, updated existing corrupt-row test
  - Estimate: 30m
  - Files: pkg/events/log.go, pkg/events/log_test.go
  - Verify: go test ./pkg/events/... -count=1 -v && go vet ./pkg/events/...
- [x] **T02: Added Translator.SubscribeFromSeq for gap-free log read + subscription under a single mutex hold, and extended RPC session/subscribe with fromSeq parameter returning backfill entries** — ## Description

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
  - Estimate: 45m
  - Files: pkg/events/translator.go, pkg/events/translator_test.go, pkg/rpc/server.go, pkg/rpc/server_test.go
  - Verify: go test ./pkg/events/... -count=1 -v && go test ./pkg/rpc/... -count=1 -v && go vet ./pkg/events/... ./pkg/rpc/...
- [x] **T03: Extended ShimClient.Subscribe with fromSeq parameter and replaced History+Subscribe recovery flow with atomic Subscribe(fromSeq=0)** — ## Description

Update the daemon-side `ShimClient` to support the extended `session/subscribe` protocol (with `fromSeq` parameter and backfill entries in the response), update the mock shim server to handle the new protocol, and replace the three-step `Status → History → Subscribe` recovery flow in `recoverSession` with a two-step `Status → Subscribe(fromSeq=0)` that uses the atomic path. This eliminates the event gap between History and Subscribe that was identified in the M003 research.

The `runtime/history` RPC method and `ShimClient.History` method are kept for backward compatibility but are no longer used by recovery.

## Steps

1. In `pkg/agentd/shim_client.go`, extend the client-side types:
   - Add `FromSeq *int \`json:"fromSeq,omitempty"\`` to `SessionSubscribeParams`
   - Add `Entries []events.Envelope \`json:"entries,omitempty"\`` to `SessionSubscribeResult`

2. In `pkg/agentd/shim_client.go`, update the `Subscribe` method signature to accept a `fromSeq *int` parameter:
   ```go
   func (c *ShimClient) Subscribe(ctx context.Context, afterSeq *int, fromSeq *int) (SessionSubscribeResult, error)
   ```
   Build `SessionSubscribeParams{AfterSeq: afterSeq, FromSeq: fromSeq}`. The rest of the method is unchanged.

3. In `pkg/agentd/shim_client_test.go`, update the mock shim server's `handleSubscribe` to support `fromSeq`:
   - Parse `SessionSubscribeParams` from request params
   - When `FromSeq` is present, return the mock's `historyEntries` filtered by fromSeq in the response as `Entries`
   - Return `NextSeq` based on the entries (count of history entries, or last seq + 1)
   - Set `srv.subscribed = true` as before

4. In `pkg/agentd/shim_client_test.go`, update ALL existing `Subscribe` call sites to pass the new `fromSeq` parameter as `nil` (backward compatible). Affected tests: `TestShimClientSubscribeNoAfterSeq`, `TestShimClientSubscribeWithAfterSeq`, `TestShimClientSubscribeReceivesSessionUpdate`, `TestShimClientSubscribeDropsUnknownMethods`, `TestShimClientMultipleMethods`, `TestShimClientRepeatedSubscribe`.

5. Add `TestShimClientSubscribeFromSeq` test:
   - Set mock server's `historyEntries` to 3 pre-built envelopes (seq 0, 1, 2)
   - Call `Subscribe(ctx, nil, &fromSeq)` with `fromSeq=0`
   - Assert result contains 3 entries with correct seq numbers
   - Assert `srv.subscribed` is true

6. In `pkg/agentd/recovery.go`, update `recoverSession` to replace the separate History + Subscribe calls:
   - Remove the `client.History(ctx, &fromSeq)` call (step 6 in the current code)
   - Remove the `client.Subscribe(ctx, &lastSeq)` call (step 7)
   - Replace with a single `client.Subscribe(ctx, nil, &fromSeq)` where `fromSeq = 0`
   - Log the number of backfill entries from the subscribe response
   - The rest of the recovery flow (register in processes map, start watchProcess) is unchanged

7. In `pkg/agentd/recovery_test.go`, update `createRecoveryTestSession` and all recovery tests:
   - The mock shim server's `handleSubscribe` now returns entries, so set `historyEntries` on the mock server for tests that care about history
   - Update the `TestRecoverSessions_LiveShim` test: set `historyEntries` on the mock, verify recovery completes (subscribed=true)
   - All other recovery tests (`DeadShim`, `NoSessions`, `SkipsStoppedSessions`, `MixedLiveAndDead`, `NoSocketPath`, `ShimReportsStopped`, `ReconcileCreatedToRunning`, `ShimMismatchLogsWarning`) should continue to pass — the subscribe call shape changed but the mock handles both old and new parameters

## Must-Haves

- [ ] `ShimClient.Subscribe` accepts `fromSeq *int` parameter alongside existing `afterSeq`
- [ ] Mock shim server returns backfill entries when `fromSeq` is present
- [ ] `recoverSession` uses atomic `Subscribe(fromSeq=0)` instead of separate History + Subscribe
- [ ] The separate `History` call is removed from the recovery path
- [ ] All existing recovery tests pass with no regressions
- [ ] All existing shim_client tests pass (updated for new Subscribe signature)

## Failure Modes

| Dependency | On error | On timeout | On malformed response |
|------------|----------|-----------|----------------------|
| ShimClient.Subscribe (atomic) | recoverSession returns error, session marked stopped (fail-closed) | Context timeout, same fail-closed path | JSON unmarshal error from Subscribe, same fail-closed path |

## Verification

- `go test ./pkg/agentd/... -count=1 -v` — all recovery + shim_client tests pass
- `go test ./pkg/events/... ./pkg/rpc/... -count=1` — regression check
- `go vet ./pkg/agentd/... ./pkg/events/... ./pkg/rpc/...` — no issues
- `go build ./cmd/agentd/... ./cmd/agent-shim/... ./pkg/...` — full build passes

## Inputs

- `pkg/agentd/shim_client.go` — existing Subscribe method to extend
- `pkg/agentd/shim_client_test.go` — mock shim server to update
- `pkg/agentd/recovery.go` — existing recoverSession with History+Subscribe to replace
- `pkg/agentd/recovery_test.go` — existing recovery tests to update
- `pkg/rpc/server.go` — SessionSubscribeParams/Result types for reference (T02 output)

## Expected Output

- `pkg/agentd/shim_client.go` — extended Subscribe signature + updated types
- `pkg/agentd/shim_client_test.go` — updated mock server + all tests passing with new signature + new SubscribeFromSeq test
- `pkg/agentd/recovery.go` — simplified recovery using atomic subscribe
- `pkg/agentd/recovery_test.go` — updated tests for new subscribe behavior
  - Estimate: 45m
  - Files: pkg/agentd/shim_client.go, pkg/agentd/shim_client_test.go, pkg/agentd/recovery.go, pkg/agentd/recovery_test.go
  - Verify: go test ./pkg/agentd/... -count=1 -v && go test ./pkg/events/... ./pkg/rpc/... -count=1 && go vet ./pkg/agentd/... ./pkg/events/... ./pkg/rpc/... && go build ./cmd/agentd/... ./cmd/agent-shim/... ./pkg/...
