# Project

## What This Is

Open Agent Runtime (OAR) is a layered runtime for headless coding agents. It manages agent processes through `agent-shim`, a higher-level daemon in `agentd`, and a future orchestrator layer for multi-agent coordination.

## Core Value

The one thing that must keep working is reliable, observable ACP-based agent execution with truthful lifecycle state. If scope has to shrink, the runtime still needs to launch real ACP agents, manage them cleanly, and tell the truth about their state and recovery behavior.

## Current State

Implemented today:
- `agent-shim` can start an ACP agent process, perform the ACP handshake, and expose a shim RPC surface
- `agentd` can manage sessions, runtime classes, workspaces, metadata, and ARI session/workspace methods
- workspace preparation exists for Git / EmptyDir / Local sources, with hooks and reference tracking
- integration tests already prove the assembled path `agentd -> agent-shim -> mockagent`
- real bundle examples exist under `bin/bundles/claude-code` and `bin/bundles/gsd-pi`

Current gap:
- the design contract has drifted across `docs/design/*`
- shim-rpc still carries an older naming/event model
- recovery, state truthfulness, and Room semantics are not yet cleanly converged
- the old `M001-terminal` direction is no longer part of the near-term plan

## Architecture / Key Patterns

Layered architecture:
- orchestrator / room intent (future)
- ARI in `agentd`
- shim RPC in `agent-shim`
- ACP toward real agent CLIs (`gsd-pi`, `claude-code`, later `codex`)

Established patterns:
- session metadata is separate from runtime execution
- workspaces are declarative inputs with managed/unmanaged ownership differences
- typed events are the internal runtime surface, but that surface is now being re-evaluated against ACP rather than treated as permanently independent
- SQLite metadata with WAL mode is the current persistence model; backend replacement is deferred unless convergence work reveals a concrete reason to change

## Capability Contract

See `.gsd/REQUIREMENTS.md` for the explicit capability contract, requirement status, and coverage mapping.

## Milestone Sequence

- [x] M001-tvc4z0: agentd Core — Session + Process management, ARI service, integration tests
- [x] M001-tlbeko: Workspace Manager — Workspace spec, source handlers, hooks, workspace ARI methods
- [ ] M002: Contract Convergence and ACP Runtime Truthfulness — 收口设计契约、shim-rpc clean break、恢复语义与真实 CLI 证明
- [ ] M003: Recovery and Safety Hardening — harden restart, state rebuild, cleanup safety, and stronger cross-client confidence
- [ ] M004: Realized Room Runtime — land implementable Room ownership, routing, and delivery semantics on a stable base
