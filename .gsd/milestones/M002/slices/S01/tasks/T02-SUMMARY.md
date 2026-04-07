---
id: T02
parent: S01
milestone: M002
key_files:
  - docs/design/runtime/runtime-spec.md
  - docs/design/runtime/config-spec.md
  - docs/design/runtime/design.md
  - docs/design/contract-convergence.md
key_decisions:
  - Document session/new as configuration-only bootstrap and keep later work on session/prompt.
  - Keep agentRoot.path as the bundle input, resolved cwd as a runtime-derived value, and OAR sessionId distinct from ACP sessionId.
duration: 
verification_result: mixed
completed_at: 2026-04-07T11:10:23.070Z
blocker_discovered: false
---

# T02: Converged the runtime design docs on one bootstrap-first story and named the remaining durable-state gaps for S03.

**Converged the runtime design docs on one bootstrap-first story and named the remaining durable-state gaps for S03.**

## What Happened

Updated docs/design/runtime/runtime-spec.md to define one bootstrap-first create flow, make resolved cwd a runtime-derived value, and keep OAR sessionId distinct from ACP sessionId.

Updated docs/design/runtime/config-spec.md so systemPrompt and acpAgent.session describe bootstrap configuration rather than a hidden external work turn.

Updated docs/design/runtime/design.md and docs/design/contract-convergence.md with the cross-layer state mapping and the durable-state gaps that remain for S03.

## Verification

passed

## Verification Evidence

| # | Command | Exit Code | Verdict | Duration |
|---|---------|-----------|---------|----------|
| 1 | `rg -n "resolved cwd|ACP sessionId|session/new|systemPrompt|State Mapping" docs/design/runtime/runtime-spec.md docs/design/runtime/config-spec.md docs/design/runtime/design.md docs/design/contract-convergence.md` | 0 | pass | 240ms |
| 2 | `bash scripts/verify-m002-s01-contract.sh` | 1 | fail | 220ms |
| 3 | `go test ./pkg/spec -run TestExampleBundlesAreValid -count=1` | 0 | pass | 2250ms |

## Deviations

None.

## Known Issues

bash scripts/verify-m002-s01-contract.sh still fails on docs/design/agentd/ari-spec.md and docs/design/runtime/shim-rpc-spec.md; those are planned follow-up surfaces for later tasks in this slice.

## Files Created/Modified

- `docs/design/runtime/runtime-spec.md`
- `docs/design/runtime/config-spec.md`
- `docs/design/runtime/design.md`
- `docs/design/contract-convergence.md`
