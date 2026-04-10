---
id: M006
title: "Fix golangci-lint v2 issues"
status: complete
completed_at: 2026-04-09T16:20:35.872Z
key_decisions:
  - Used `golangci-lint fmt ./...` (v2 API) as the single idempotent formatting pass for S01 — no manual gci/gofumpt edits needed.
  - golangci-lint `--fix` does not auto-apply unconvert or ineffassign despite being listed as fixable — manual edits required for 23/24 S02 issues.
  - After any `golangci-lint --fix` run, always follow with `go build ./...` to catch missing-import side-effects from gocritic rewrites (errors.As added without 'errors' import).
  - filepathJoin fix: use `os.TempDir()` not a split literal `/tmp` — gocritic still flags the leading `/` even after splitting.
  - builtinShadowDecl: removed custom `min()` function in terminal.go — Go 1.21+ built-in takes over transparently.
  - exitAfterDefer: TestMain must capture exit code, call cleanup explicitly, then call os.Exit — deferred calls are bypassed by os.Exit.
  - S04 and S05 were clean no-ops — all target symbols already removed by M005 migration; documented in K042/K043 as a pattern for future dead-code slices.
  - testifylint require-error: only the top-level guard line changes to `require.*`; downstream `assert.*` lines are intentionally left unchanged.
key_files:
  - pkg/agentd/process.go
  - pkg/agentd/process_test.go
  - pkg/agentd/shim_client.go
  - pkg/agentd/shim_client_test.go
  - pkg/agentd/session_test.go
  - pkg/ari/server.go
  - pkg/ari/server_test.go
  - pkg/ari/registry.go
  - pkg/events/translator_test.go
  - pkg/rpc/server_test.go
  - pkg/runtime/terminal.go
  - pkg/runtime/runtime_test.go
  - pkg/runtime/terminal_test.go
  - pkg/spec/example_bundles_test.go
  - pkg/workspace/git.go
  - pkg/workspace/hook.go
  - pkg/workspace/hook_test.go
  - .golangci.yaml
  - .gsd/KNOWLEDGE.md
lessons_learned:
  - golangci-lint --fix is unreliable for unconvert and ineffassign — always verify what was actually auto-fixed with a post-fix lint run before declaring a category done.
  - gocritic's errors.As rewrite (triggered by --fix) does not add the 'errors' import — the build breaks silently. Always run `go build ./...` immediately after any --fix run.
  - unparam masks multiple findings per function — always re-run after each fix to surface hidden parameters.
  - Dead-code slices (unused, errorlint) may be no-ops if earlier milestones did significant refactoring — verify first, edit second, never the reverse.
  - When the slice goal is '0 issues', any collateral lint finding (even outside task scope) blocks the goal and must be fixed inline — do not defer.
  - Keeping KNOWLEDGE.md updated with gotchas (K040–K047) during the milestone significantly reduces context loss for subsequent milestones.
  - The golangci-lint 0-issues posture is now a reliable CI signal — any future regression will be immediately visible.
---

# M006: Fix golangci-lint v2 issues

**Eliminated all 202 golangci-lint v2 issues across 11 linter categories — codebase now reports 0 issues clean.**

## What Happened

M006 was a systematic lint-cleanup milestone targeting the full golangci-lint v2 finding set of 202 issues across 11 categories. The milestone was structured as 7 slices ordered by risk: auto-fixable formatters first (S01–S02), then manual fixes by category (S03–S07).

**S01 — gci + gofumpt (56 issues, auto-fixed):** `golangci-lint fmt ./...` applied in one pass, reformatting 67 files with import block reordering and whitespace normalization. Zero logic changes.

**S02 — unconvert + copyloopvar + ineffassign (24 issues, mostly manual):** A key discovery: `golangci-lint --fix` did not auto-apply unconvert or ineffassign fixes as assumed. All 22 unconvert occurrences (redundant `int64()` and `json.RawMessage()` casts) were removed manually via targeted perl substitutions. The `--fix` run also triggered gocritic rewrites that introduced 5 compilation errors by adding `errors.As()` rewrites without adding the `"errors"` import — all repaired immediately.

**S03 — misspell + unparam (17 issues):** The unparam linter reported one unused parameter per function per pass — both `ctx context.Context` and `rc *RuntimeClass` in `forkShim` were unused and removed in sequence once the masking was discovered.

**S04 — unused dead code (12 issues):** Clean no-op. All 12 targeted symbols (mutex field, 10 session handler methods, test helper) were already absent — removed by M005's session→agent migration. Zero edits needed.

**S05 — errorlint (17 issues):** Second clean no-op. M005 migration had already applied `errors.Is`/`errors.As` patterns throughout, and `.golangci.yaml`'s `std-error-handling` exclusion preset covered legitimate `sql.ErrNoRows` comparisons. Zero edits needed.

**S06 — gocritic (45 issues, 13 active):** Fixed all 13 active gocritic findings across 11 files: `filepathJoin` (use `os.TempDir()` not literal `/tmp`), `importShadow` (rename 5 local vars shadowing package names), `appendAssign` (pre-allocated make pattern), `exitAfterDefer` (TestMain cleanup before `os.Exit`), `builtinShadowDecl` (remove custom `min()` — Go 1.21 built-in), `appendCombine`, `elseif`.

**S07 — testifylint (31 issues, 5 active):** Applied `require-error` fixes to 5 test assertion sites in `pkg/agentd/shim_client_test.go` and `pkg/runtime/terminal_test.go`. Fixed collateral `gci` finding in `pkg/runtime/terminal.go` that appeared during the final lint run.

Final state: `golangci-lint run ./...` → **0 issues**. `go build ./...` → exit 0. All 8 `pkg/` test packages pass. 70 non-`.gsd/` files modified across the milestone.

## Success Criteria Results

## Success Criteria

All success criteria were met:

| Slice | Criterion | Evidence |
|-------|-----------|----------|
| S01 | No gci or gofumpt findings | `golangci-lint run ./... 2>&1 \| grep -E '(gci)\|(gofumpt)'` → no output (grep exits 1) ✅ |
| S02 | No unconvert/copyloopvar/ineffassign findings | `golangci-lint run ./... 2>&1 \| grep -E '(unconvert)\|(copyloopvar)\|(ineffassign)'` → no output ✅ |
| S03 | No misspell or unparam findings | `golangci-lint run ./... 2>&1 \| grep -E '(misspell\|unparam)'` → no output ✅ |
| S04 | No unused findings | `golangci-lint run ./... 2>&1 \| grep unused` → no output ✅ |
| S05 | No errorlint findings | `golangci-lint run ./... 2>&1 \| grep errorlint` → no output ✅ |
| S06 | No gocritic findings | `golangci-lint run ./... 2>&1 \| grep gocritic` → no output ✅ |
| S07 | `golangci-lint run ./...` reports 0 issues | **`golangci-lint run ./...` → `0 issues.`** ✅ |

Final authoritative check run at milestone close: `golangci-lint run ./...` → `0 issues.`

## Definition of Done Results

## Definition of Done

- ✅ All 7 slices complete: S01, S02, S03, S04, S05, S06, S07 — all `status: complete` per `gsd_milestone_status`.
- ✅ All 7 slice summaries exist on disk: `.gsd/milestones/M006/slices/S{01..07}/S{01..07}-SUMMARY.md`.
- ✅ `golangci-lint run ./...` → `0 issues.` (final verification run at milestone close).
- ✅ `go build ./...` → exit 0, clean build.
- ✅ `go test ./pkg/...` → all 8 packages pass (agentd, ari, events, meta, rpc, runtime, spec, workspace).
- ✅ Code changes committed in git (70 non-`.gsd/` files modified across snapshot commits be7767c → 6ece068).
- ✅ Cross-slice integration: formatting baseline from S01/S02 was stable for all manual-fix slices; no regressions introduced across slices.
- ✅ KNOWLEDGE.md updated with 8 new entries (K040–K047) documenting lint fix patterns and gotchas.

## Requirement Outcomes

## Requirement Outcomes

M006 was a code-quality / developer-experience milestone — it did not add or change functional capabilities. No requirements changed status during this milestone.

- All functional requirements (R020, R026–R029, etc.) remain **Active** — unchanged, as M006 made only non-semantic code edits (formatting, redundant cast removal, dead code cleanup, test assertion style).
- No new requirements were surfaced during this milestone.
- No requirements were invalidated or deferred.

The milestone provides a clean lint baseline for future milestones: any new code that introduces lint violations will be immediately visible, making the 0-issues posture a continuous quality signal.

## Deviations

S01: Formatter touched 67 files vs ~20 in the plan — expected, correct repo-wide scope. S02: `--fix` did not auto-apply unconvert/ineffassign; all required manual edits. `--fix` also introduced 5 compilation errors via gocritic rewrites (missing imports) that needed repair. S03: unparam reported two unused parameters in forkShim (ctx, rc) vs one in the plan — the second was masked. S04: All 12 symbols already absent — clean no-op, zero file edits. S05: All 17 errorlint issues already absent — clean no-op, zero file edits. S06: filepathJoin fix used os.TempDir() instead of three-arg split (which gocritic still flags). S07: Fixed one collateral gci finding in pkg/runtime/terminal.go that was outside the testifylint task scope but required for 0-issues goal.

## Follow-ups

No follow-up lint work required — codebase is fully clean. Future milestones should maintain the 0-issues posture by running `golangci-lint run ./...` as part of any PR/pre-commit verification. Integration test failures in `tests/integration/` (5 tests related to prompt acceptance) pre-date M006 and are unrelated to lint fixes — these were noted in S04 and remain as pre-existing issues for a future milestone.
