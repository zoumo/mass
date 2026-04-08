# S03: Atomic Event Resume and Damaged-Tail Tolerance — UAT

**Milestone:** M003
**Written:** 2026-04-08T03:04:06.148Z

## UAT: Atomic Event Resume and Damaged-Tail Tolerance

### Preconditions
- Go toolchain available (`go test`, `go vet`, `go build` all work)
- Repository checked out at the commit containing S03 changes
- No external services required — all tests use in-process mocks

---

### Test Case 1: Damaged-Tail Tolerance in ReadEventLog

**Objective:** Verify that ReadEventLog skips corrupt trailing lines and returns valid entries.

1. Run `go test ./pkg/events/... -run TestReadEventLog_DamagedTailTolerated -v`
   - **Expected:** Test passes. ReadEventLog returns 3 valid entries from a file with 3 valid JSONL lines + 1 truncated JSON line at the end. No error returned.

2. Run `go test ./pkg/events/... -run TestReadEventLog_DamagedTailReturnsPartial -v`
   - **Expected:** Test passes. Valid entries before the corrupt tail are returned. Log output includes "skipping 1 damaged tail line(s)".

3. Run `go test ./pkg/events/... -run TestReadEventLog_DamagedTailOnlyCorrupt -v`
   - **Expected:** Test passes. A file containing only a corrupt line returns empty slice, no error.

### Test Case 2: Mid-File Corruption Still Errors

**Objective:** Verify that corrupt lines in the middle of the file (valid lines follow) still produce an error.

1. Run `go test ./pkg/events/... -run TestReadEventLog_MidFileCorruptionFails -v`
   - **Expected:** Test passes. ReadEventLog returns an error when 2 valid entries + 1 corrupt line + 2 more valid entries are present.

### Test Case 3: Append After Damaged Tail

**Objective:** Verify that OpenEventLog + Append works after a damaged tail, and re-reading correctly detects mid-file corruption.

1. Run `go test ./pkg/events/... -run TestEventLog_AppendAfterDamagedTail -v`
   - **Expected:** Test passes. After appending past a corrupt tail line, ReadEventLog correctly identifies the formerly-tail corruption as mid-file corruption (because valid data now follows it).

### Test Case 4: Atomic SubscribeFromSeq — Backfill + Live Continuity

**Objective:** Verify that SubscribeFromSeq returns backfill entries and a live subscription with contiguous seq numbers (no gap).

1. Run `go test ./pkg/events/... -run TestSubscribeFromSeq_BackfillAndLive -v`
   - **Expected:** Test passes. SubscribeFromSeq(logPath, 2) returns 3 backfill entries (seq 2,3,4). A subsequently broadcast event arrives on the subscription channel as seq 5. No gap between backfill and live.

2. Run `go test ./pkg/events/... -run TestSubscribeFromSeq_EmptyLog -v`
   - **Expected:** Test passes. SubscribeFromSeq on a non-existent file returns empty backfill and a valid subscription channel.

### Test Case 5: RPC session/subscribe with fromSeq

**Objective:** Verify the RPC layer correctly passes through the atomic subscribe path.

1. Run `go test ./pkg/rpc/... -run TestRPCServer_CleanBreakSurface/subscribe_with_fromSeq_returns_backfill -v`
   - **Expected:** Test passes. After generating events via a prompt, a second client subscribes with `fromSeq=0` and receives all prior events as backfill entries in the response. Subsequent events arrive as live notifications.

2. Run `go test ./pkg/rpc/... -run TestRPCServer_RejectsLegacyAndInvalidParams/subscribe_negative_fromSeq -v`
   - **Expected:** Test passes. `fromSeq=-1` is rejected with InvalidParams error.

### Test Case 6: ShimClient Subscribe with fromSeq

**Objective:** Verify the daemon-side client passes fromSeq through and receives backfill entries.

1. Run `go test ./pkg/agentd/... -run TestShimClientSubscribeFromSeq -v`
   - **Expected:** Test passes. Subscribe with fromSeq=0 returns 3 pre-built entries from the mock server. Subscription is registered.

### Test Case 7: Recovery Uses Atomic Subscribe

**Objective:** Verify the recovery flow uses the atomic path (no separate History call).

1. Run `go test ./pkg/agentd/... -run TestRecoverSessions -v`
   - **Expected:** All recovery subtests pass (LiveShim, DeadShim, NoSessions, SkipsStoppedSessions, MixedLiveAndDead, NoSocketPath, ShimReportsStopped, ReconcileCreatedToRunning, ShimMismatchLogsWarning). The LiveShim subtest completes recovery using the atomic Subscribe path.

### Test Case 8: Backward Compatibility — Subscribe Without fromSeq

**Objective:** Verify existing subscribe behavior (afterSeq, no fromSeq) is unchanged.

1. Run `go test ./pkg/agentd/... -run TestShimClientSubscribeNoAfterSeq -v`
   - **Expected:** Test passes. Subscribe(ctx, nil, nil) works as before.

2. Run `go test ./pkg/agentd/... -run TestShimClientSubscribeWithAfterSeq -v`
   - **Expected:** Test passes. Subscribe with afterSeq still filters correctly.

3. Run `go test ./pkg/rpc/... -run TestRPCServer_CleanBreakSurface/subscribe_afterSeq_filters_prior_history -v`
   - **Expected:** Test passes. afterSeq filtering is unaffected by the new fromSeq parameter.

### Test Case 9: Full Build and Vet

**Objective:** Verify no compilation or static analysis issues across all affected packages.

1. Run `go build ./cmd/agentd/... ./cmd/agent-shim/... ./pkg/...`
   - **Expected:** Clean build, no errors.

2. Run `go vet ./pkg/agentd/... ./pkg/events/... ./pkg/rpc/...`
   - **Expected:** No issues reported.

### Edge Cases

- **Empty file with fromSeq=0:** Returns empty backfill, valid subscription (covered by TestSubscribeFromSeq_EmptyLog)
- **fromSeq beyond log end:** Returns empty backfill entries, valid subscription (ReadEventLog returns empty for fromSeq > max seq)
- **Negative fromSeq at RPC level:** Rejected with InvalidParams (covered by negative_fromSeq test)
- **Damaged tail with only corrupt lines:** Returns empty slice, no error (covered by TestReadEventLog_DamagedTailOnlyCorrupt)
- **Fresh-start Subscribe (no recovery):** process.go passes fromSeq=nil, behaves as original Subscribe (covered by existing tests)
