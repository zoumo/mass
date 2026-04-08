---
id: T02
parent: S03
milestone: M003
key_files:
  - pkg/events/translator.go
  - pkg/events/translator_test.go
  - pkg/rpc/server.go
  - pkg/rpc/server_test.go
key_decisions:
  - Made afterSeq subtest dynamic (queries runtime/status for current lastSeq) to avoid brittle hardcoded seq expectations when earlier subtests generate events
duration: 
verification_result: passed
completed_at: 2026-04-08T02:53:26.025Z
blocker_discovered: false
---

# T02: Added Translator.SubscribeFromSeq for gap-free log read + subscription under a single mutex hold, and extended RPC session/subscribe with fromSeq parameter returning backfill entries

**Added Translator.SubscribeFromSeq for gap-free log read + subscription under a single mutex hold, and extended RPC session/subscribe with fromSeq parameter returning backfill entries**

## What Happened

Added SubscribeFromSeq(logPath, fromSeq) to events.Translator that atomically reads history and registers a live subscription under a single t.mu lock hold, eliminating the History→Subscribe event gap. Extended RPC session/subscribe with optional fromSeq parameter that triggers the atomic path and returns backfill entries in the response. Added 2 unit tests for SubscribeFromSeq (backfill+live continuity, empty log), 1 RPC integration test (backfill + live events), and 1 negative test (fromSeq < 0 → InvalidParams). Fixed pre-existing afterSeq subtest to use dynamic seq floor.

## Verification

go test ./pkg/events/... -count=1 -v — all 27 tests pass including 2 new SubscribeFromSeq tests. go test ./pkg/rpc/... -count=1 -v — all tests pass including new fromSeq integration and negative tests. go vet ./pkg/events/... ./pkg/rpc/... — no issues.

## Verification Evidence

| # | Command | Exit Code | Verdict | Duration |
|---|---------|-----------|---------|----------|
| 1 | `go test ./pkg/events/... -count=1 -v` | 0 | ✅ pass | 1274ms |
| 2 | `go test ./pkg/rpc/... -count=1 -v` | 0 | ✅ pass | 12661ms |
| 3 | `go vet ./pkg/events/... ./pkg/rpc/...` | 0 | ✅ pass | 500ms |

## Deviations

Adapted pre-existing afterSeq subtest to dynamically query current lastSeq via runtime/status instead of hardcoding nextSeq==4, since the new fromSeq subtest generates additional events before it runs.

## Known Issues

None.

## Files Created/Modified

- `pkg/events/translator.go`
- `pkg/events/translator_test.go`
- `pkg/rpc/server.go`
- `pkg/rpc/server_test.go`
