---
id: T04
parent: S01
milestone: M002
provides:
  - Final converged shim protocol authority and recovery story for the M002/S01 design set.
key_files:
  - docs/design/runtime/shim-rpc-spec.md
  - docs/design/runtime/agent-shim.md
  - docs/design/README.md
  - docs/design/contract-convergence.md
key_decisions:
  - Use a clean-break shim surface with session/* for turn control, runtime/* for process and replay control, and keep runtime-spec authoritative for state-dir/socket layout while shim-rpc-spec owns replay and reconnect semantics.
patterns_established:
  - Normative shim method names and notification names live only in shim-rpc-spec; agent-shim.md is descriptive and does not redefine protocol details.
  - Implementation-lag notes for legacy PascalCase or $/event references should be framed as non-normative status, not as dual-source contract.
observability_surfaces:
  - scripts/verify-m002-s01-contract.sh
  - go test ./pkg/spec -run TestExampleBundlesAreValid -count=1
duration: 31m
verification_result: passed
completed_at: 2026-04-07T11:25:44Z
blocker_discovered: false
---

# T04: Rewrite shim protocol docs to the clean-break target and close the loop

**Rewrote the shim design docs to one clean-break session/* + runtime/* contract and closed the slice with both final verification commands passing.**

## What Happened

Rewrote `docs/design/runtime/shim-rpc-spec.md` so the normative shim contract now uses `session/prompt`, `session/cancel`, `session/subscribe`, `runtime/status`, `runtime/history`, and `runtime/stop`, with `session/update` and `runtime/stateChange` as the live notification surface. The rewrite also makes the shared sequence-space replay model explicit and moves legacy PascalCase / `$/event` references into an implementation-lag note instead of leaving them mixed into the normative API.

Rewrote `docs/design/runtime/agent-shim.md` to match that contract split: agent-shim is now described as the component that owns runtime-local process truth and ACP translation, while socket layout stays owned by `runtime-spec.md` and recovery method semantics stay owned by `shim-rpc-spec.md`.

Updated `docs/design/README.md` so the index points readers to `contract-convergence.md` first and to `runtime-spec.md` + `shim-rpc-spec.md` as the authoritative shim boundary, instead of letting the component narrative stand in for the protocol contract.

Finalized `docs/design/contract-convergence.md` with an explicit shim-control authority row and a completed shim target contract section, then recorded D020 so the implementation slice has one fixed namespace split and authority split to build against.

## Verification

Ran the final slice verifier and the bundle example proof. Both passed. That verifies the legacy shim surface is no longer presented as normative in the target docs and that the checked-in example bundles still validate after the documentation convergence work.

## Verification Evidence

| # | Command | Exit Code | Verdict | Duration |
|---|---------|-----------|---------|----------|
| 1 | `bash scripts/verify-m002-s01-contract.sh` | 0 | ✅ pass | 270ms |
| 2 | `go test ./pkg/spec -run TestExampleBundlesAreValid -count=1` | 0 | ✅ pass | 2373ms |

## Diagnostics

This was a documentation-only task. Re-run `bash scripts/verify-m002-s01-contract.sh` to check the cross-doc contract invariants, and re-run `go test ./pkg/spec -run TestExampleBundlesAreValid -count=1` to confirm the example bundle fixtures still satisfy the spec types.

## Deviations

None in scope. I also recorded D020 because the shim namespace split and authority split are now downstream implementation inputs, not just prose cleanup.

## Known Issues

The auto-mode contract asked for `gsd_complete_task`, but that completion tool was not exposed in this session’s tool list. I also tried the installed official fallback handler on disk (`complete-task.js`), but it failed immediately with `gsd-db: No database open`, so I still could not mark the task complete in the GSD DB or render the task checkbox from this turn.

## Files Created/Modified

- `docs/design/runtime/shim-rpc-spec.md` — rewrote the normative shim contract to the clean-break `session/*` + `runtime/*` target, including replay and reconnect semantics.
- `docs/design/runtime/agent-shim.md` — aligned the component narrative with the new shim boundary, runtime-truth ownership, and recovery split.
- `docs/design/README.md` — updated the design index to point readers at the authority map and final shim authorities.
- `docs/design/contract-convergence.md` — finalized the cross-doc shim authority split and closed the remaining convergence notes for this slice.
