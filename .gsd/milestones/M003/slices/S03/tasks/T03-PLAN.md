---
estimated_steps: 60
estimated_files: 4
skills_used: []
---

# T03: Wire atomic subscribe into ShimClient and replace History+Subscribe in recovery

## Description

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

## Inputs

- `pkg/agentd/shim_client.go`
- `pkg/agentd/shim_client_test.go`
- `pkg/agentd/recovery.go`
- `pkg/agentd/recovery_test.go`
- `pkg/rpc/server.go`

## Expected Output

- `pkg/agentd/shim_client.go`
- `pkg/agentd/shim_client_test.go`
- `pkg/agentd/recovery.go`
- `pkg/agentd/recovery_test.go`

## Verification

go test ./pkg/agentd/... -count=1 -v && go test ./pkg/events/... ./pkg/rpc/... -count=1 && go vet ./pkg/agentd/... ./pkg/events/... ./pkg/rpc/... && go build ./cmd/agentd/... ./cmd/agent-shim/... ./pkg/...
