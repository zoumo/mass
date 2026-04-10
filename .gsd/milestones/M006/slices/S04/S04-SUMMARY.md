---
id: S04
parent: M006
milestone: M006
provides:
  - ["Zero unused linter findings — golangci-lint run ./... shows no unused issues. S05 (errorlint) can proceed on a codebase with no unused findings."]
requires:
  []
affects:
  - ["S05 — depends on S04; can now start"]
key_files:
  - ["pkg/agentd/shim_client.go (verified: mu field absent)", "pkg/ari/server.go (verified: 10 session handler methods absent; deliverPromptAsync intact)", "pkg/events/translator_test.go (verified: ptrInt absent)"]
key_decisions:
  - ["All 12 unused symbols were already absent before T01 ran — confirmed with grep, git diff, and golangci-lint; no code edits were made (clean no-op execution).", "K042 added to KNOWLEDGE.md: always verify symbol presence before editing in dead-code removal tasks; earlier milestone refactoring (M005 session→agent migration) had already eliminated these paths."]
patterns_established:
  - ["Dead-code removal slices should run lint check first, before any edits, to avoid no-op or corrupting already-clean files."]
observability_surfaces:
  - []
drill_down_paths:
  - [".gsd/milestones/M006/slices/S04/tasks/T01-SUMMARY.md"]
duration: ""
verification_result: passed
completed_at: 2026-04-09T15:12:07.279Z
blocker_discovered: false
---

# S04: Manual: unused dead code (12 issues)

**Zero unused linter findings confirmed — all 12 target symbols (mu mutex field, 10 dead session handler methods, ptrInt test helper) were already absent from the codebase before this slice ran.**

## What Happened

S04 targeted 12 symbols flagged by the `unused` linter across three files: the `mu sync.Mutex` field in `pkg/agentd/shim_client.go`, 10 unreachable session handler methods in `pkg/ari/server.go` (`handleSessionNew`, `deliverPrompt`, `handleSessionPrompt` through `handleSessionDetach`), and the `ptrInt` helper in `pkg/events/translator_test.go`.

T01 ran authoritative checks before making any edits. All three files were grepped and all 12 symbols were already absent. `git diff` showed no tracked changes. `golangci-lint run ./... 2>&1 | grep unused` returned no matches (exit code 1 = grep found nothing = PASS). The slice goal was satisfied at the start — no code changes were required.

Root cause: earlier milestones (particularly the M005 session→agent migration) and sibling slices (S02/S03) had already removed or superseded the dead code paths during prior refactoring. This is the expected pattern when a codebase undergoes significant restructuring — dead-code linter findings become stale before targeted cleanup slices execute.

The KNOWLEDGE base was updated with K042 documenting this pattern for future executors: always verify symbol presence before editing; a clean zero-findings lint run is the authoritative confirmation.

## Verification

1. `go build ./...` — exit 0, clean build.
2. `golangci-lint run ./... 2>&1 | grep unused` — no matches (grep exits 1 = PASS: no unused findings).
3. `go test ./pkg/...` — all 8 packages pass (agentd, ari, events, meta, rpc, runtime, spec, workspace).
Remaining golangci-lint output is gocritic (S06 target) and testifylint (S07 target) — not in scope for S04.

## Requirements Advanced

None.

## Requirements Validated

None.

## New Requirements Surfaced

None.

## Requirements Invalidated or Re-scoped

None.

## Deviations

["T01 plan described editing three files; all 12 symbols were already absent so no file edits were required. Clean no-op execution confirmed by golangci-lint and grep."]

## Known Limitations

["Pre-existing integration test failures in tests/integration (5 tests related to prompt acceptance) are unrelated to dead-code removal and were present before this slice."]

## Follow-ups

["None — S05 (errorlint) is unblocked and ready to start."]

## Files Created/Modified

- `pkg/agentd/shim_client.go` — No changes — mu field already absent
- `pkg/ari/server.go` — No changes — 10 session handler methods already absent
- `pkg/events/translator_test.go` — No changes — ptrInt helper already absent
