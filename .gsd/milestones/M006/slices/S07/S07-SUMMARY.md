---
id: S07
parent: M006
milestone: M006
provides:
  - ["golangci-lint 0 issues across entire codebase — M006 fully complete"]
requires:
  []
affects:
  - ["M006 milestone completion — all 7 slices done, milestone can now be closed"]
key_files:
  - (none)
key_decisions:
  - ["Fixed collateral gci import formatting in pkg/runtime/terminal.go (not in task plan) because a first lint run after the 5 require edits still showed a gci finding there — fixing it was required to reach the 0-issues goal", "Subsequent assert.Nil/assert.Contains lines after require-fixed positions were intentionally left as assert — only the standalone top-level error guard needed require"]
patterns_established:
  - ["testifylint require-error: standalone assert.Error/assert.NoError that fully controls test flow should be require — only the specific error guard line changes, not the downstream assert.Contains/assert.Nil lines", "When a lint slice goal is '0 issues', any unrelated lint finding that surfaces during the run must also be fixed as collateral — the overall goal takes precedence over the task plan scope"]
observability_surfaces:
  - []
drill_down_paths:
  []
duration: ""
verification_result: passed
completed_at: 2026-04-09T16:14:05.704Z
blocker_discovered: false
---

# S07: Manual: testifylint (31 issues)

**Fixed 5 testifylint require-error findings and a collateral gci formatting issue, bringing golangci-lint to 0 issues across the entire codebase.**

## What Happened

S07 was the final slice of M006 — eliminating the last 5 lint findings to achieve a fully clean golangci-lint run.

**What was done:**
The 5 `require-error` findings flagged by testifylint were scattered across three test files in `pkg/agentd/`:

- `agent_test.go:270` — `assert.NoError` → `require.NoError` (standalone error check after a Get call)
- `session_test.go:236` — `assert.NoError` → `require.NoError` (standalone error check in a table-driven transitions test)
- `shim_client_test.go:233` — `assert.Error` → `require.Error` in `TestShimClientDialFail`
- `shim_client_test.go:606` — `assert.Error` → `require.Error` in `TestParseSessionUpdateMalformed`
- `shim_client_test.go:633` — `assert.Error` → `require.Error` in `TestParseRuntimeStateChangeMalformed`

All three files already imported `github.com/stretchr/testify/require`, so no import additions were needed. The subsequent `assert.Nil` / `assert.Contains` calls were intentionally left as `assert` — only the standalone top-level error checks needed to be `require`.

**Collateral fix:** A first-run after the five edits still showed one lint issue — a pre-existing `gci` import formatting problem in `pkg/runtime/terminal.go` (trailing blank lines and a collapsed blank separator between the default and localmodule import sections). This file was not in the task plan but required fixing to reach the 0-issues goal. `gci write` was used to normalise it.

**Outcome:** `golangci-lint run ./...` exits 0 with `0 issues.` — the milestone goal is fully achieved. All 11 linter categories are clean across the entire codebase. `go test ./pkg/agentd/...` passes with no regressions.

## Verification

golangci-lint run ./... exits 0 with "0 issues." — confirmed. go test ./pkg/agentd/... exits 0 with all tests passing (cached ok).

## Requirements Advanced

None.

## Requirements Validated

None.

## New Requirements Surfaced

- []

## Requirements Invalidated or Re-scoped

None.

## Deviations

None.

## Known Limitations

None.

## Follow-ups

None. golangci-lint is fully clean. M006 milestone can be completed.

## Files Created/Modified

- `pkg/agentd/agent_test.go` — assert.NoError → require.NoError at line 270 (standalone error check after Get)
- `pkg/agentd/session_test.go` — assert.NoError → require.NoError at line 236 (table-driven state transition error check)
- `pkg/agentd/shim_client_test.go` — assert.Error → require.Error at lines 233, 606, 633 in TestShimClientDialFail, TestParseSessionUpdateMalformed, TestParseRuntimeStateChangeMalformed
- `pkg/runtime/terminal.go` — Collateral gci fix: removed trailing blank lines and restored missing blank separator between default and localmodule import sections
