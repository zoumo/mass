---
estimated_steps: 4
estimated_files: 4
skills_used: []
---

# T02: Converge runtime bootstrap, identity, and state mapping docs

**Slice:** S01 — Design contract convergence
**Milestone:** M002

## Description

Rewrite the runtime-facing docs so they stop describing overlapping startup paths and ambiguous identity/state semantics. The target contract is that `session/new` establishes configuration, `session/prompt` introduces work later, `agentRoot.path` resolves the canonical working directory, and OAR session identity is clearly distinct from ACP session identity.

## Steps

1. Use `docs/design/contract-convergence.md` as the authority source for the runtime/bootstrap invariants introduced in T01.
2. Update `docs/design/runtime/runtime-spec.md` so lifecycle, `agentRoot.path`, resolved `cwd`, `systemPrompt`, and ACP startup order all describe one bootstrap sequence.
3. Update `docs/design/runtime/config-spec.md` so field meanings and examples match the rewritten runtime spec instead of preserving overlapping `session/new` semantics.
4. Update `docs/design/runtime/design.md` and the authority map with the final runtime/session/process state mapping, OAR-vs-ACP ID split, and the explicit durable-field gaps that S03 must close.

## Must-Haves

- [ ] The runtime docs describe one configuration-first bootstrap path without a hidden bootstrap prompt path inside `session/new`.
- [ ] The design set contains an explicit table or equivalent section mapping runtime state, session state, process status, and ACP `sessionId` authority.
- [ ] The docs name the persistence/config fields that are still missing from durable state instead of implying they already exist.

## Verification

- `rg -n "resolved cwd|ACP sessionId|session/new|systemPrompt|State Mapping" docs/design/runtime/runtime-spec.md docs/design/runtime/config-spec.md docs/design/runtime/design.md docs/design/contract-convergence.md`

## Inputs

- `docs/design/contract-convergence.md` — authority map and invariant sections created in T01
- `docs/design/runtime/runtime-spec.md` — lifecycle and state contract to converge
- `docs/design/runtime/config-spec.md` — config-field meanings that must align with runtime behavior
- `docs/design/runtime/design.md` — rationale and generation-flow narrative that must match the converged contract

## Expected Output

- `docs/design/runtime/runtime-spec.md` — converged lifecycle/bootstrap/state contract
- `docs/design/runtime/config-spec.md` — aligned config semantics for `agentRoot.path`, resolved `cwd`, and ACP session config
- `docs/design/runtime/design.md` — rationale matching the converged runtime contract
- `docs/design/contract-convergence.md` — updated runtime/bootstrap/state authority notes
