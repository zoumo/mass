# Project

## What This Is

Open Agent Runtime (OAR) — a layered agent runtime architecture inspired by containerd/runc. Manages agent processes through a shim layer (agent-shim), a high-level daemon (agentd), and an orchestrator layer for multi-agent coordination.

## Core Value

Enable reliable, observable, headless agent execution with proper lifecycle management — from single agent sessions to multi-agent rooms with shared workspaces and inter-agent communication.

## Current State

**Implemented (Phase 1 — agent-shim layer):**
- `pkg/spec` — OAR Runtime Spec types, config parsing, state management
- `pkg/runtime` — Manager: agent process lifecycle, ACP handshake, permissions
- `pkg/events` — Typed event stream, EventLog (JSONL), ACP→Event translator
- `pkg/rpc` — JSON-RPC 2.0 server over Unix socket (shim RPC)
- `cmd/agent-shim` — CLI entry point with full startup flow
- `cmd/agent-shim-cli` — Interactive management client

**Not yet implemented:**
- `agentd` — High-level daemon (Workspace/Session/Process/Room Manager, ARI, Metadata Store)
- `Orchestrator` — Room lifecycle, multi-agent coordination

## Architecture / Key Patterns

Layered architecture (containerd-inspired):
```
orchestrator (room lifecycle, multi-agent coordination)
    ↓ ARI protocol
agentd (session/workspace/process/room management)
    ↓ shim RPC
agent-shim (single agent process management)
    ↓ ACP protocol
agent (claude-code, gemini-cli, gsd, etc.)
```

Key patterns:
- Session = metadata, Process = execution (containerd Container/Task separation)
- RuntimeClass registry (K8s RuntimeClass pattern for agent type resolution)
- Typed events (ACP is implementation detail, typed events are core protocol)
- Unix socket RPC (agentd ↔ shim, orchestrator ↔ agentd)

## Capability Contract

See `.gsd/REQUIREMENTS.md` for the explicit capability contract, requirement status, and coverage mapping.

## Milestone Sequence

- [ ] M001-tvc4z0: Phase 2 — agentd Core — Session + Process management, ARI service
- [ ] M001-tlbeko: Phase 3 — Workspace Manager — Workspace spec, source handlers, hooks