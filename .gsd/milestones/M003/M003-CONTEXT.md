---
depends_on: [M002]
---

# M003: Recovery and Safety Hardening

**Gathered:** 2026-04-07
**Status:** Ready for planning

## Project Description

This milestone takes the semantics stabilized in M002 and hardens the runtime against failure, stale state, and unsafe cleanup. It is not a general polish pass. It is the milestone where OAR has to start telling the truth after restart and uncertainty, especially when `agentd` restarts while shims may still be alive.

The main recovery story for this milestone is **live shim reconnect**. The system should rediscover still-running shims, reconnect to them, rebuild truthful session state, and refuse operational actions when recovery certainty is incomplete.

## Why This Milestone

M002 is optimized for contract convergence, shim-rpc clean break, and real validation with `gsd-pi` plus `claude-code`. That still leaves a second layer of work: making the runtime trustworthy in failure paths.

Right now the repo already shows the recovery surface area, but it is not yet hard enough to rely on:

- `events.jsonl` replay is brittle when tail data is damaged
- workspace refcount truth is split between in-memory tracking and persisted metadata
- socket cleanup has a race window
- restart/reconnect exists as an intended story, but not yet as a milestone-level truth boundary
- if recovery is uncertain, the current design is too close to silent guessing

This milestone exists so later work — especially Codex strengthening and eventual Room runtime work — does not build on ambiguous recovery behavior.

## User-Visible Outcome

### When this milestone is complete, the user can:

- restart `agentd`, have it rediscover and reconnect to live shims, and inspect session state with confidence about whether the system is healthy, degraded, or blocked
- trust that when recovery truth is uncertain, OAR allows read-only status inspection but blocks operational actions instead of guessing

### Entry point / environment

- Entry point: `agentd`, `agent-shim`, ARI session/status and attach surfaces, restart/reconnect flow, and real ACP clients including Codex
- Environment: local development and production-like local runtime verification
- Live dependencies involved: SQLite metadata store, shim Unix sockets, `state.json`, `events.jsonl`, real ACP CLIs (`gsd-pi`, `claude-code`, `codex`)

## Completion Class

- Contract complete means: the recovery posture, degraded/blocked state semantics, and fail-closed behavior are written clearly enough that later slices can implement them without guessing
- Integration complete means: `agentd` restart plus live shim reconnect works as the primary recovery path, and Codex completes one real prompt round-trip on the hardened runtime path
- Operational complete means: when recovery truth is incomplete, the runtime exposes explicit degraded/blocked state, allows status inspection, and refuses operational actions such as cleanup, resume, or continued prompt flow

## Final Integrated Acceptance

To call this milestone complete, we must prove:

- `agentd` can restart, rediscover live shim sockets, reconnect to still-running shims, and surface truthful session state instead of silently degrading into guesswork
- a session in uncertain recovery state remains inspectable through status but blocks operational actions until truth is re-established
- Codex can complete one real prompt round-trip on the hardened runtime path; this milestone is not only about `gsd-pi` and `claude-code`, and the Codex path cannot be simulated away if this milestone is to count as complete

## Risks and Unknowns

- Live shim reconnect may expose gaps between persisted metadata, live shim state, and operator-visible session state — this matters because the milestone is explicitly about runtime truthfulness after restart
- `events.jsonl` tail damage or partial writes may still break replay ordering or confidence — this matters because reconnect without trustworthy history is only a partial recovery story
- Workspace reference truth is currently split between in-memory manager state and persisted `workspace_refs/ref_count` — this matters because unsafe cleanup after restart is one of the exact failure modes this milestone is meant to stop
- Socket startup and cleanup races may make restart behavior nondeterministic — this matters because restart proof is the primary operational bar for the milestone
- The explicit degraded/blocked state surface must be strong enough to guide operators without becoming a vague warning bucket — this matters because the user wants read-only inspection to remain available while operations are blocked

## Existing Codebase / Prior Art

- `pkg/events/log.go` — current event log implementation; replay fails on decode error and does not yet tolerate damaged tail data
- `pkg/workspace/manager.go` — current in-memory workspace refcount tracking that becomes suspect after daemon restart
- `pkg/meta/schema.sql` and `pkg/meta/workspace.go` — persisted `workspace_refs` and `ref_count` machinery already exist in SQLite and should remain the storage direction for this milestone
- `cmd/agentd/main.go` — current shutdown timeout bug and socket cleanup race are both directly relevant to this milestone
- `tests/integration/restart_test.go` — existing restart-recovery test path proves there is already a concrete surface to harden rather than inventing one from scratch
- `bin/bundles/README.md` — documents the current assumption that `agentd` discovers running shims by scanning the socket pattern after restart
- `docs/plan/unified-modification-plan.md` — source of the unified recovery/safety hardening backlog after contract convergence
- `docs/design/agentd/agentd.md` and `docs/design/runtime/*` — prior-art design surfaces that mention restart recovery, `session/load`, and runtime state responsibilities

> See `.gsd/DECISIONS.md` for all architectural and pattern decisions — it is an append-only register; read it during planning, append to it during execution.

## Relevant Requirements

- R035 — this milestone strengthens the event recovery path beyond M002’s baseline proof level
- R036 — this milestone hardens what session configuration and runtime identity are durable enough for truthful restart/state rebuild
- R037 — this milestone turns workspace identity, reuse, cleanup, and shared-access semantics into safety-enforced behavior rather than just design intent
- R038 — this milestone applies the front-loaded security boundary to actual fail-closed runtime behavior
- R040 — this milestone advances Codex from compatibility target-in-contract to one real prompt round-trip proof
- R044 — this is the primary milestone intended by the “recovery and safety hardening beyond M002 proof level” deferred requirement

## Scope

### In Scope

- live shim reconnect as the primary recovery path after `agentd` restart
- event-log damage tolerance and replay behavior hardening
- workspace refcount truth after restart, including persisted truth vs in-memory state
- socket bind / cleanup race handling
- durable session configuration and truthful state rebuild paths after restart
- explicit degraded / blocked recovery state surface
- fail-closed safety behavior: when recovery truth is uncertain, status is visible but operational actions are blocked
- one real Codex prompt round-trip on the hardened runtime path

### Out of Scope / Non-Goals

- making `session/load` cold resume the main proof path for this milestone
- reopening the metadata backend choice or replacing SQLite with BoltDB
- carrying backward compatibility for old recovery semantics, old RPC names, or old state/event formats
- realized Room runtime and routing work
- broad terminal capability work

## Technical Constraints

- M003 depends on M002 having already converged the core contract and shim-rpc surface
- SQLite remains the metadata backend for this milestone; backend replacement is intentionally out of scope
- the safety posture is explicit: **when recovery truth is uncertain, the system should only allow viewing state and should not allow operation**
- uncertainty must be represented as an explicit operator-visible state, not only as incidental error text
- live shim reconnect is the primary recovery bar; `session/load` may remain a later capability rather than the anchor of this milestone

## Integration Points

- `agentd` restart lifecycle — this milestone hardens restart discovery, reconnect, and state rebuild behavior
- `agent-shim` socket/state directory — the reconnect surface being treated as first-class runtime truth
- SQLite metadata store — retained source for persisted workspace/session bookkeeping
- `events.jsonl` and `state.json` — persistent runtime artifacts whose trustworthiness directly affects restart behavior
- Codex ACP path — must complete one real prompt round-trip in this milestone
- `gsd-pi` and `claude-code` — remain surrounding proof surfaces even though Codex is the client being strengthened here

## Open Questions

- the exact field names and state shape for the explicit degraded / blocked recovery surface — current thinking: the semantics matter more than the final names, but the surface must make it obvious that status is readable while operations are blocked
- how much recovery state can be re-established from live shim state versus persisted metadata alone — current thinking: prefer live shim truth where available, but never guess when the two disagree
- how far event-log damage tolerance should go beyond damaged-tail handling in this milestone — current thinking: enough to keep reconnect truthful and non-destructive, without turning this milestone into a general storage redesign
