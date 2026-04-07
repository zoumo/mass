---
estimated_steps: 4
estimated_files: 5
skills_used: []
---

# T03: Align Room ownership and security-boundary docs

**Slice:** S01 — Design contract convergence
**Milestone:** M002

## Description

Resolve the contradictions around Room ownership, session creation, shared workspace semantics, and host-impact boundaries. This task should make the orchestrator, agentd, ARI, and workspace docs tell one story about who owns desired state, who owns realized runtime state, and where local path, hook, env, and shared-workspace trust boundaries sit.

## Steps

1. Rewrite `docs/design/orchestrator/room-spec.md` so Room remains orchestrator-owned desired state and projects into runtime sessions without claiming agentd owns orchestration intent.
2. Update `docs/design/agentd/agentd.md` and `docs/design/agentd/ari-spec.md` so realized runtime room state, `session/new`, `session/prompt`, and shared workspace membership line up with the desired-vs-realized split.
3. Update `docs/design/workspace/workspace-spec.md` so local workspace attachment, hook execution, env precedence, and shared-workspace reuse/access are stated as explicit host-impact boundary rules.
4. Reflect the final Room/workspace/security rules back into `docs/design/contract-convergence.md`, including any explicit follow-on gaps reserved for S03 under R036/R044.

## Must-Haves

- [ ] The docs use one desired-vs-realized Room model instead of mixing “agentd only sees sessions” with runtime-managed `room/*` semantics.
- [ ] `session/new` is described as configuration-only while `session/prompt` remains the work-entry path.
- [ ] The design set explicitly names local path canonicalization, hook execution, env precedence, shared workspace implications, and the intended ACP capability/security posture.

## Verification

- `rg -n "Desired vs Realized|session/new|session/prompt|local workspace|hook|env|shared workspace|capability" docs/design/orchestrator/room-spec.md docs/design/agentd/agentd.md docs/design/agentd/ari-spec.md docs/design/workspace/workspace-spec.md docs/design/contract-convergence.md`

## Inputs

- `docs/design/contract-convergence.md` — runtime authority notes and invariant structure from T01/T02
- `docs/design/orchestrator/room-spec.md` — desired-state Room contract to reconcile
- `docs/design/agentd/agentd.md` — realized room/session contract to reconcile
- `docs/design/agentd/ari-spec.md` — ARI surface whose session/room semantics must converge
- `docs/design/workspace/workspace-spec.md` — workspace and host-boundary rules that must be made explicit

## Expected Output

- `docs/design/orchestrator/room-spec.md` — desired-state Room contract aligned with the new ownership model
- `docs/design/agentd/agentd.md` — realized room/session contract aligned with the desired-vs-realized split
- `docs/design/agentd/ari-spec.md` — converged ARI semantics for `session/new`, `session/prompt`, and `room/*`
- `docs/design/workspace/workspace-spec.md` — explicit local/hook/env/shared-workspace boundary rules
- `docs/design/contract-convergence.md` — updated Room/workspace/security authority notes
