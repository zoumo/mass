# Project

## What This Is

Open Agent Runtime (OAR) is a layered runtime for headless coding agents. It manages ACP-speaking agent processes through `agent-shim`, a higher-level daemon in `agentd`, and a future orchestrator layer for multi-agent coordination.

## Core Value

The thing that must stay true is reliable, observable agent execution with truthful lifecycle and recovery semantics. If scope has to shrink, the runtime still needs to launch real ACP agents, manage them cleanly, and tell the truth about their state, ownership boundaries, and restart behavior.

## Current State

Implemented today:
- `agent-shim` can start an ACP agent process, perform the ACP handshake, and expose the current shim RPC implementation surface
- `agentd` can manage sessions, runtime classes, workspaces, metadata, and ARI session/workspace methods
- workspace preparation exists for Git / EmptyDir / Local sources, with hooks and reference tracking
- integration tests already prove the assembled path `agentd -> agent-shim -> mockagent`
- real bundle examples exist under `bin/bundles/claude-code` and `bin/bundles/gsd-pi`
- M002/S01 converged the design contract across `docs/design/*`: bootstrap semantics, Room ownership, workspace host-impact boundaries, and the clean-break shim target now have one documented authority map
- the repo now has a mechanical design-proof surface: `scripts/verify-m002-s01-contract.sh` plus `go test ./pkg/spec -run TestExampleBundlesAreValid -count=1`

Current gap:
- the runtime implementation still needs to adopt the converged clean-break shim surface (`session/*` + `runtime/*`)
- durable restart/reconnect truth, replay reconstruction, and fail-closed recovery semantics are still design-complete but not implementation-complete
- real CLI integration proof against the converged contract still needs to be run in later M002 slices
- the old `M001-terminal` direction is no longer part of the near-term plan

## Architecture / Key Patterns

Layered architecture:
- orchestrator / Room desired state (future)
- ARI in `agentd` for realized runtime state and control
- shim RPC in `agent-shim`
- ACP toward real agent CLIs (`gsd-pi`, `claude-code`, later `codex`)

Established patterns:
- `session/new` is configuration/bootstrap only; work enters through later `session/prompt`
- `agentRoot.path` is the bundle input; resolved `cwd` is derived at runtime, not independently authored state
- OAR `sessionId` and ACP `sessionId` are separate identities and must stay separate in docs and code
- Room ownership is split intentionally: orchestrator owns desired state, agentd/ARI own the realized runtime projection
- workspace boundaries are explicit: Local sources are unmanaged attachments, hooks execute host commands, env precedence is inherited host env → runtime class env → session overrides, and shared workspaces imply shared visibility/write risk
- shim protocol authority is split intentionally: `runtime-spec.md` owns socket/state-dir layout, `shim-rpc-spec.md` owns method/notification/replay semantics, and `agent-shim.md` is descriptive only
- SQLite metadata with WAL mode remains the current persistence model; backend replacement is deferred unless concrete limits appear
- documentation convergence in this area is guarded by a two-part proof surface: the contract verifier script plus checked-in example bundle validation

## Capability Contract

See `.gsd/REQUIREMENTS.md` for the explicit capability contract, requirement status, and coverage mapping.

## Milestone Sequence

- [x] M001-tvc4z0: agentd Core — Session + Process management, ARI service, integration tests
- [x] M001-tlbeko: Workspace Manager — Workspace spec, source handlers, hooks, workspace ARI methods
- [ ] M002: Contract Convergence and ACP Runtime Truthfulness — S01 converged the design contract; S02–S04 remain to land shim clean break, truthful recovery, and real CLI verification
- [ ] M003: Recovery and Safety Hardening — harden restart, state rebuild, cleanup safety, and stronger cross-client confidence
- [ ] M004: Realized Room Runtime — land implementable Room ownership, routing, and delivery semantics on a stable base
