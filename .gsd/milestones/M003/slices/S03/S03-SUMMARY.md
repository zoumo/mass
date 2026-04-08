---
id: S03
parent: M003
milestone: M003
provides:
  - ["SubscribeFromSeq atomic method on events.Translator", "fromSeq parameter on session/subscribe RPC and ShimClient.Subscribe", "Gap-free recovery path: Status → Subscribe(fromSeq=0)", "Damaged-tail tolerant ReadEventLog"]
requires:
  []
affects:
  - ["S04"]
key_files:
  - ["pkg/events/log.go", "pkg/events/log_test.go", "pkg/events/translator.go", "pkg/events/translator_test.go", "pkg/rpc/server.go", "pkg/rpc/server_test.go", "pkg/agentd/shim_client.go", "pkg/agentd/shim_client_test.go", "pkg/agentd/recovery.go", "pkg/agentd/process.go"]
key_decisions:
  - ["D046: ReadEventLog damaged-tail detection uses two-pass classification — corrupt lines only at tail are skipped, mid-file corruption still errors", "D047: Translator.SubscribeFromSeq holds t.mu during file I/O + subscription registration, eliminating History→Subscribe gap; recovery uses Subscribe(fromSeq=0) instead of separate History+Subscribe"]
patterns_established:
  - ["Damaged-tail tolerance: JSONL readers should classify corrupt lines by position (tail vs mid-file) rather than failing the entire read", "Atomic subscribe-from-seq: when gap-free event continuity is required, hold the broadcast mutex during both history-read and subscription-registration", "Recovery-only mutex+IO: acceptable to hold locks during file I/O in startup/recovery paths, but document the constraint in godoc to prevent hot-path misuse"]
observability_surfaces:
  - ["ReadEventLog logs skipped damaged tail lines via log.Printf", "Recovery logs backfill entry count from atomic subscribe"]
drill_down_paths:
  - [".gsd/milestones/M003/slices/S03/tasks/T01-SUMMARY.md", ".gsd/milestones/M003/slices/S03/tasks/T02-SUMMARY.md", ".gsd/milestones/M003/slices/S03/tasks/T03-SUMMARY.md"]
duration: ""
verification_result: passed
completed_at: 2026-04-08T03:04:06.148Z
blocker_discovered: false
---

# S03: Atomic Event Resume and Damaged-Tail Tolerance

**Event log reads tolerate damaged tails, and recovery uses a single atomic Subscribe(fromSeq=0) that eliminates the History→Subscribe event gap structurally.**

## What Happened

This slice delivered two complementary hardening improvements to the event recovery path.

**T01 — Damaged-Tail Tolerance in ReadEventLog.** Replaced the `json.Decoder`-based `ReadEventLog` with `bufio.Scanner` + per-line `json.Unmarshal`. The new implementation classifies each non-empty line as valid or corrupt, then walks forward: if a corrupt line is followed by any valid line, it's mid-file corruption (error); if corrupt lines only appear at the end, they're damaged-tail (skip and return partial results with a log message). This makes event replay resilient to partial writes from daemon crashes. Five new/updated tests cover the full classification matrix: tail-only-corrupt, tail-returns-partial, mid-file-corruption-fails, and append-after-damaged-tail.

Key deviation from plan: appending a valid entry past a corrupt line correctly reclassifies the corruption as mid-file (not tail), so ReadEventLog returns an error — the test was adapted accordingly.

**T02 — Atomic SubscribeFromSeq on Translator and RPC.** Added `Translator.SubscribeFromSeq(logPath, fromSeq)` that reads the JSONL log and registers a live subscription under a single `t.mu` lock hold. This eliminates the event gap that existed between separate History and Subscribe calls — because `broadcastEnvelope` also holds `t.mu` while assigning seq numbers, no events can slip between the history read and subscription start. Extended the RPC `session/subscribe` handler with an optional `fromSeq` parameter that triggers the atomic path and returns backfill entries in the response. Backward compatible — existing `afterSeq` behavior is unchanged. Negative `fromSeq` is rejected with InvalidParams.

**T03 — Recovery Flow Simplified to Atomic Subscribe.** Extended `ShimClient.Subscribe` to accept a `fromSeq *int` parameter, updated the mock shim server to return backfill entries when `fromSeq` is present, and replaced the three-step `Status → History → Subscribe` recovery flow in `recoverSession` with a two-step `Status → Subscribe(fromSeq=0)`. The separate `History` call is no longer used by recovery (kept for backward compatibility). Also updated `process.go` fresh-start Subscribe call site for the new signature (not in plan but required for compilation).

The net result: daemon recovery now has a structurally gap-free event resume path with crash-tolerant log reads. The `runtime/history` RPC and `ShimClient.History` remain available but are no longer critical to recovery correctness.

## Verification

All slice-level verification commands passed:

1. `go test ./pkg/events/... -count=1 -v` — 27/27 tests pass, including 5 damaged-tail tests + 2 SubscribeFromSeq tests
2. `go test ./pkg/rpc/... -count=1 -v` — all pass, including fromSeq backfill integration test + negative fromSeq test
3. `go test ./pkg/agentd/... -count=1 -v` — all pass, including TestShimClientSubscribeFromSeq + all recovery tests with atomic subscribe
4. `go vet ./pkg/agentd/... ./pkg/events/... ./pkg/rpc/...` — clean, no issues
5. `go build ./cmd/agentd/... ./cmd/agent-shim/... ./pkg/...` — full build passes

## Requirements Advanced

None.

## Requirements Validated

- R035 — M003/S03 upgraded the resume path: SubscribeFromSeq reads log + registers subscription under single mutex hold. RecoverSession uses atomic Subscribe(fromSeq=0). Proven by TestSubscribeFromSeq_BackfillAndLive, TestShimClientSubscribeFromSeq, and full recovery suite.

## New Requirements Surfaced

None.

## Requirements Invalidated or Re-scoped

None.

## Deviations

T01: Plan step 5 expected ReadEventLog to return 4 valid entries after append past a corrupt line. The algorithm correctly classifies this as mid-file corruption — test was adapted. T03: Also updated pkg/agentd/process.go (not in plan) for the new Subscribe signature.

## Known Limitations

SubscribeFromSeq performs file I/O under the Translator mutex — acceptable for recovery but must not be used in hot paths. This is documented in godoc but not enforced structurally.

## Follow-ups

The runtime/history RPC and ShimClient.History method are no longer used by recovery but remain for backward compatibility. Consider deprecating or removing them if no other consumers emerge.

## Files Created/Modified

- `pkg/events/log.go` — Rewrote ReadEventLog to use bufio.Scanner per-line scanning with damaged-tail tolerance
- `pkg/events/log_test.go` — Added 5 new/updated tests for damaged-tail scenarios
- `pkg/events/translator.go` — Added SubscribeFromSeq method for atomic log-read + subscription under single mutex
- `pkg/events/translator_test.go` — Added TestSubscribeFromSeq_BackfillAndLive and TestSubscribeFromSeq_EmptyLog
- `pkg/rpc/server.go` — Extended session/subscribe with fromSeq parameter and backfill entries in response
- `pkg/rpc/server_test.go` — Added fromSeq backfill integration test and negative fromSeq validation test
- `pkg/agentd/shim_client.go` — Extended Subscribe signature with fromSeq parameter and updated types
- `pkg/agentd/shim_client_test.go` — Updated mock server for fromSeq, added TestShimClientSubscribeFromSeq, updated all call sites
- `pkg/agentd/recovery.go` — Replaced History+Subscribe with atomic Subscribe(fromSeq=0) in recoverSession
- `pkg/agentd/process.go` — Updated fresh-start Subscribe call for new signature (fromSeq=nil)
