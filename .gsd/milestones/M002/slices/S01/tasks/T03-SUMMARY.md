---
id: T03
parent: S01
milestone: M002
key_files:
  - docs/design/orchestrator/room-spec.md
  - docs/design/agentd/agentd.md
  - docs/design/agentd/ari-spec.md
  - docs/design/workspace/workspace-spec.md
  - docs/design/contract-convergence.md
key_decisions:
  - Treat the Room Spec as orchestrator-owned desired state and treat ARI room/* as the realized runtime projection maintained by agentd.
  - Keep session/new configuration-only and require work to enter through session/prompt, even when bootstrap side effects happen during create.
duration: 
verification_result: mixed
completed_at: 2026-04-07T11:18:41.362Z
blocker_discovered: false
---

# T03: Aligned Room ownership docs around a desired-vs-realized split and made workspace host-impact boundaries explicit.

**Aligned Room ownership docs around a desired-vs-realized split and made workspace host-impact boundaries explicit.**

## What Happened

Rewrote docs/design/orchestrator/room-spec.md so Room is clearly the orchestrator-owned desired-state object, with agentd only owning the realized runtime projection used for membership tracking and future routing metadata. Rewrote docs/design/agentd/agentd.md and docs/design/agentd/ari-spec.md to match that split: room/* is now described as runtime projection state, session/new is configuration-only bootstrap, and session/prompt is the work-entry path for both external callers and future room-delivered work. Rewrote docs/design/workspace/workspace-spec.md to state the host-impact rules directly: local workspace attachment requires canonicalization and is never agentd-deleted, hook execution is host command execution, env precedence is inherited host env → runtime-class env → session/new overrides, and shared workspace reuse implies shared visibility and shared write risk rather than hidden isolation. Updated docs/design/contract-convergence.md to reflect the final ownership model, restore the verifier’s expected authority headings, and name the remaining S03/R036/R044 follow-on gaps as durable bootstrap, replay, cleanup, and cross-client hardening rather than unresolved contract ambiguity. Recorded D019 so later runtime work keeps the same Room ownership split instead of reintroducing the earlier mixed model.

## Verification

Ran the task-plan grep verification across the five target docs; it passed and showed the required desired-vs-realized, bootstrap, local workspace, hook, env, shared workspace, and capability language in the updated surfaces. Ran the broader slice verifier as a regression check; it now fails only on docs/design/runtime/shim-rpc-spec.md still carrying the legacy PascalCase / $/event surface, which is the planned T04 rewrite target.

## Verification Evidence

| # | Command | Exit Code | Verdict | Duration |
|---|---------|-----------|---------|----------|
| 1 | `rg -n "Desired vs Realized|session/new|session/prompt|local workspace|hook|env|shared workspace|capability" docs/design/orchestrator/room-spec.md docs/design/agentd/agentd.md docs/design/agentd/ari-spec.md docs/design/workspace/workspace-spec.md docs/design/contract-convergence.md` | 0 | ✅ pass | 60ms |
| 2 | `bash scripts/verify-m002-s01-contract.sh` | 1 | ❌ fail | 70ms |

## Deviations

Minor: restored the exact ## Security Boundaries and ## Shim Target Contract headings expected by the existing contract verifier after the first rewrite used different section titles.

## Known Issues

bash scripts/verify-m002-s01-contract.sh still fails because docs/design/runtime/shim-rpc-spec.md presents the legacy PascalCase / $/event surface as normative. That is the planned T04 follow-on, not a blocker for this task.

## Files Created/Modified

- `docs/design/orchestrator/room-spec.md`
- `docs/design/agentd/agentd.md`
- `docs/design/agentd/ari-spec.md`
- `docs/design/workspace/workspace-spec.md`
- `docs/design/contract-convergence.md`
