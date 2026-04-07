---
id: T01
parent: S01
milestone: M002
key_files:
  - docs/design/contract-convergence.md
  - scripts/verify-m002-s01-contract.sh
  - pkg/spec/example_bundles_test.go
  - bin/bundles/claude-code/config.json
key_decisions:
  - The slice verifier now treats seed-prompt systemPrompt wording, acpAgent.session.cwd wording, and PascalCase shim RPC methods as contract drift to retire in T02-T04.
patterns_established:
  - Checked-in bundle examples are validated by walking every bundle config.json through spec.ParseConfig and spec.ValidateConfig.
observability_surfaces:
  - scripts/verify-m002-s01-contract.sh
  - go test ./pkg/spec -run TestExampleBundlesAreValid -count=1
verification_result: passed
completed_at: 2026-04-07T11:01:31Z
blocker_discovered: false
---

# T01: Add the contract verifier and bundle example proof

**Added the contract convergence gate and proved the checked-in bundle examples validate.**

## What Happened

Created `docs/design/contract-convergence.md` as the slice authority map with the required sections for authority ownership, bootstrap contract, state mapping, security boundaries, and shim target contract. Added `scripts/verify-m002-s01-contract.sh` as the durable drift gate: it enforces the new section headings and fails on the legacy wording this slice is meant to remove from the runtime, ARI, and shim docs. Added `pkg/spec/example_bundles_test.go`, which walks the checked-in bundle examples, parses each `config.json` with `spec.ParseConfig`, and validates each config with `spec.ValidateConfig`. Fixed `bin/bundles/claude-code/config.json` to use `oarVersion`, which unblocked the new bundle proof.

## Verification

Confirmed the verifier script is executable and shell-valid with `test -x` and `bash -n`. Ran `go test ./pkg/spec -run TestExampleBundlesAreValid -count=1` and it passed, proving the checked-in bundle examples are parseable and spec-valid. Ran the slice-level `bash scripts/verify-m002-s01-contract.sh` gate as well; it failed as expected on the still-stale normative wording in `docs/design/runtime/config-spec.md`, `docs/design/runtime/runtime-spec.md`, `docs/design/agentd/ari-spec.md`, and `docs/design/runtime/shim-rpc-spec.md`. Those failures are the intended follow-up targets for T02-T04, not a plan-invalidating blocker.

## Verification Evidence

| # | Command | Exit Code | Verdict | Duration |
|---|---------|-----------|---------|----------|
| 1 | `test -x scripts/verify-m002-s01-contract.sh` | 0 | ✅ pass | 0.08s |
| 2 | `bash -n scripts/verify-m002-s01-contract.sh` | 0 | ✅ pass | 0.06s |
| 3 | `go test ./pkg/spec -run TestExampleBundlesAreValid -count=1` | 0 | ✅ pass | 2.37s |
| 4 | `bash scripts/verify-m002-s01-contract.sh` | 1 | ❌ fail | 0.10s |

## Diagnostics

Future agents can inspect this task by reading `docs/design/contract-convergence.md`, running `bash scripts/verify-m002-s01-contract.sh`, and running `go test ./pkg/spec -run TestExampleBundlesAreValid -count=1`. The verifier prints the exact file and matching stale phrase when contract drift remains. The bundle proof fails with the specific bundle path or validation error if a checked-in example breaks.

## Deviations

None. Implementation matched the task plan.

## Known Issues

- The slice-level contract verifier is intentionally still failing on unresolved legacy wording in `docs/design/runtime/config-spec.md`, `docs/design/runtime/runtime-spec.md`, `docs/design/agentd/ari-spec.md`, and `docs/design/runtime/shim-rpc-spec.md`. Later tasks in this slice are meant to converge those docs until the gate passes.
- The current harness session does not expose a `gsd_complete_task` tool, so task completion could not be marked in the planner database from this session.

## Files Created/Modified

- `docs/design/contract-convergence.md` — added the slice authority map and invariant headings for later doc convergence work.
- `scripts/verify-m002-s01-contract.sh` — added the contract drift verifier and stale-phrase gate.
- `pkg/spec/example_bundles_test.go` — added bundle example parsing and validation coverage.
- `bin/bundles/claude-code/config.json` — fixed the bundle schema key from `oaiVersion` to `oarVersion`.
