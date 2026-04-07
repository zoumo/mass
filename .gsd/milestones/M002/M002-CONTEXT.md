# M002: Contract Convergence and ACP Runtime Truthfulness

**Gathered:** 2026-04-07
**Status:** Ready for planning

## Project Description

This milestone replaces the abandoned `M001-terminal` direction with a contract-first wave: 收口设计契约, perform a shim-rpc clean break, and make runtime/recovery behavior truthful enough to validate against real ACP CLIs. The point is not to add one more surface feature. The point is to stop the design, protocol, and implementation layers from continuing to drift apart.

## Why This Milestone

OAR already has substantial implemented surface area in `agent-shim`, `agentd`, workspace management, ARI, and integration tests. The risk now is not lack of code — it is that `docs/design/*`, shim-rpc, and runtime state semantics no longer line up cleanly. If that drift continues, later work on recovery, Codex compatibility, or Room runtime will rest on unstable assumptions. This milestone exists to fix that now, while the user is still the sole operator and has explicitly said there is no need to preserve backward compatibility.

## User-Visible Outcome

### When this milestone is complete, the user can:

- inspect the design and runtime contract and see one consistent story for Room, Session, Runtime, Workspace, bootstrap, and recovery semantics
- run the converged runtime against real `gsd-pi` and `claude-code` bundle configurations and get proof that the new contract is not only theoretical

### Entry point / environment

- Entry point: `agentd`, `agent-shim`, ARI, shim RPC, and bundle configs under `bin/bundles/*`
- Environment: local development and production-like local runtime verification
- Live dependencies involved: SQLite metadata store, Unix sockets, real ACP CLIs (`gsd-pi`, `claude-code`), and existing bundle configuration

## Completion Class

- Contract complete means: design documents, state shapes, and protocol surfaces no longer contradict each other on the core lifecycle and recovery path
- Integration complete means: the converged contract is exercised with real ACP clients using `bin/bundles/gsd-pi` and `bin/bundles/claude-code`
- Operational complete means: restart/reconnect/history semantics and cleanup boundaries are specified tightly enough that failure behavior is truthful rather than aspirational

## Final Integrated Acceptance

To call this milestone complete, we must prove:

- `gsd-pi` can be launched through the converged runtime path and the resulting session/bootstrap/recovery semantics match the rewritten contract
- `claude-code` can be launched through the converged runtime path and the same contract still holds without a client-specific exception story
- the milestone does not merely rewrite docs; at least one real reconnect/recovery-oriented path and the post-clean-break protocol surface must be exercised, because this milestone is about truthfulness, not just wording

## Risks and Unknowns

- Room ownership and lifecycle meaning are currently split across multiple design docs — if left unresolved, later Room runtime work will be built on contradictory assumptions
- shim-rpc still exposes the older PascalCase and `$/event` surface in code — a clean break is straightforward now, but any hesitation will multiply translation debt
- event recovery still has a visible gap between history replay and live subscription — if the new contract does not close that, restart/reconnect will remain suspect
- workspace identity, cleanup, and shared access semantics are underspecified — this is a direct safety risk for later multi-agent work
- Codex is a required compatibility target, but this milestone is only expected to keep it in the contract and sequence, not fully prove it end-to-end yet

## Existing Codebase / Prior Art

- `docs/plan/unified-modification-plan.md` — the primary planning seed for this milestone; it merges design收口, shim-rpc redesign, and implementation hardening into one route
- `docs/plan/shim-rpc-redesign.md` — the clean-break protocol direction that moves the shim surface toward `session/*` + `runtime/*`
- `docs/plan/code-improvement-plan.md` — source of the concrete bug/hardening backlog that still matters after contract convergence
- `docs/design/agentd/agentd.md` — contains the current Session/Room/bootstrap drift that must be reconciled
- `docs/design/agentd/ari-spec.md` — contains the ARI-facing lifecycle and room API assumptions that currently conflict with other docs
- `docs/design/orchestrator/room-spec.md` — contains the desired-state Room story that must be reconciled with agentd’s realized-state story
- `docs/design/runtime/runtime-spec.md` — holds runtime lifecycle and `agentRoot.path` / `cwd` assumptions that need to become single-source
- `docs/design/runtime/shim-rpc-spec.md` — the old shim surface definition to be cleanly replaced
- `cmd/agentd/main.go` — contains the real shutdown-timeout bug and current socket cleanup behavior
- `pkg/agentd/shim_client.go` — shows the old shim naming/event model still alive in code
- `pkg/events/log.go` — current event replay shape and damage-handling limitations
- `pkg/workspace/manager.go` — current refcount/canonical cleanup direction that needs a clearer truth model
- `bin/bundles/gsd-pi/config.json` — real bundle surface for one required ACP validation target
- `bin/bundles/claude-code/config.json` — real bundle surface for the other required ACP validation target

> See `.gsd/DECISIONS.md` for all architectural and pattern decisions — it is an append-only register; read it during planning, append to it during execution.

## Relevant Requirements

- R032 — converge the design contract before more runtime surface is built
- R033 — unify ACP bootstrap semantics (`agentRoot.path`, `cwd`, `session/new`, `systemPrompt`)
- R034 — perform the shim-rpc clean break
- R035 — close the explicit recovery race window
- R036 — make session config durable enough for truthful restart/rebuild
- R037 — define workspace identity, reuse, cleanup, and shared access clearly
- R038 — front-load security boundaries instead of deferring them too late
- R039 — prove the result with real `gsd-pi` and `claude-code` clients

## Scope

### In Scope

- reconcile conflicting design docs around Room, Session, Runtime, Workspace, and recovery semantics
- define one authoritative ACP bootstrap story
- convert shim-rpc to the new clean-break contract
- add the missing state/protocol pieces needed for truthful recovery semantics
- decide the metadata-store direction for now (retain SQLite, defer backend replacement)
- prove the converged runtime path with real `gsd-pi` and `claude-code` bundle configurations

### Out of Scope / Non-Goals

- preserving backward compatibility with old shim-rpc names, old state/event formats, or old metadata contents
- reviving `M001-terminal` as a near-term commitment
- fully proving Codex end-to-end in this milestone
- shipping the realized Room runtime itself
- replacing SQLite with BoltDB in this milestone

## Technical Constraints

- no backward-compatibility requirement: the user explicitly does not want compatibility work carried into this wave
- real validation should use the existing bundle examples under `bin/bundles/*`, not only mock-agent fixtures
- SQLite remains the current metadata backend because the model already uses relational features; backend replacement is deferred unless convergence work reveals a concrete blocker
- the resulting contract must still leave a clean path for later Codex validation and later realized Room runtime work

## Integration Points

- `gsd-pi` bundle — required real ACP validation target for M002
- `claude-code` bundle — required real ACP validation target for M002
- `codex` — required compatibility target for the broader roadmap, but stronger proof is deferred
- SQLite metadata store — retained persistence layer during this milestone
- Unix sockets / agent-shim RPC — core transport boundary being reworked
- ACP session lifecycle — external protocol boundary that the new contract must reflect truthfully

## Open Questions

- whether the converged contract keeps an ARI-level bootstrap prompt concept or removes it in favor of explicit later prompting — current leaning: decide during planning, but avoid overlapping semantics
- how much of recovery hardening fits cleanly inside M002 versus being intentionally pushed to M003 — current leaning: converge first, harden further in follow-on
- which exact Codex proof target belongs in the next milestone after M002 — current leaning: keep it explicit in the roadmap without forcing premature E2E in this milestone
