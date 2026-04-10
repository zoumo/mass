# Project

## What This Is

Open Agent Runtime (OAR) is a layered runtime for headless coding agents. It manages ACP-speaking agent processes through `agentd` (daemon), `agent-shim` (process shim), and a future orchestrator layer for multi-agent coordination. Modeled after containerd/runc: spec-driven, layered, single-binary operational model.

## Core Value

Reliable, observable agent execution with truthful lifecycle and recovery semantics.

## Current State

**M007 complete.** Platform terminal state refactor done: bbolt replaces SQLite, `spec.Status` (creating/idle/running/stopped/error) is the sole state enum, Room/Session concepts eliminated, `(workspace, name)` identity throughout, shim-only post-bootstrap state writes enforced (D088), RestartPolicy tryReload/alwaysNew governs recovery (D089). Integration tests pass (`go test ./tests/integration/... -v -timeout 120s` → 7 PASS + 2 SKIP), lint clean (0 issues).

**M008 planned.** CLI consolidation: 5 binaries → 2 (`agentd` + `agentdctl`), `--root`-derived config (no config.yaml), RuntimeClass elevated to DB entity, resource-first CLI grammar, and API model rename (agent=template, agentrun=running instance).

### Completed Milestones

| Milestone | Title | Summary |
|-----------|-------|---------|
| M001 | Core runtime foundation | agent-shim, agentd, ARI socket, workspace, metadata store, ACP handshake |
| M002 | Contract convergence | ARI client/server contract alignment, JSON-RPC lifecycle |
| M003 | Recovery hardening | Fail-closed recovery, shim-vs-DB reconciliation, atomic event resume, workspace cleanup |
| M004 | Room runtime | mesh/star/isolated room modes, room/send, room-mcp-server |
| M005 | Agent model refactoring | session→agent migration, async lifecycle, agent-centric ARI surface |
| M006 | Fix golangci-lint v2 issues | 202 → 0 issues across 11 linter categories; clean lint posture established |
| M007 | Platform terminal state refactor | bbolt storage, unified spec.Status (idle replaces created), (workspace,name) identity, shim write authority, Room/Session elimination; all integration tests pass; 0 lint issues |

### Active Milestones

| Milestone | Title | Summary |
|-----------|-------|---------|
| M008 | CLI consolidation + API model rename | 5→2 binary consolidation, --root config, runtime entity, agent/agentrun API model |

### What's Implemented

- `agentd` manages agents with **(workspace, name)** identity (no UUID), async lifecycle, fail-closed recovery
- `agent-shim` starts ACP agent processes, performs the ACP handshake, exposes `session/*` + `runtime/*` shim RPC surface
- **bbolt metadata store**: `v1/workspaces/{name}` + `v1/agents/{workspace}/{name}` bucket layout
- **spec.Status as sole state enum**: creating/idle/running/stopped/error
- **Full ARI JSON-RPC server**: workspace/* and agent/* handlers over Unix socket
- **workspace-mcp-server**: MCP server exposing workspace_send + workspace_status tools
- **agentdctl CLI**: agent/workspace/daemon subcommands (currently verb-first grammar)

## Architecture / Key Patterns

- **5-binary model (current, pre-M008):** `agentd`, `agentdctl`, `agent-shim`, `agent-shim-cli`, `workspace-mcp-server`
- **2-binary model (M008 target):** `agentd` (server/shim/workspace-mcp subcommands) + `agentdctl` (agent/agentrun/workspace/shim resource commands)
- **ARI protocol:** JSON-RPC 2.0 over Unix domain socket
- **OCI-inspired layering:** workspace=rootfs, agent(template)=container definition, agentrun=task/running instance, shim=runc equivalent
- **bbolt buckets:** `v1/workspaces/{name}`, `v1/agents/{workspace}/{name}`, `v1/runtimes/{name}` (added M008 S02, renamed v1/agents in M008 S04)

## Capability Contract

See `.gsd/REQUIREMENTS.md` for the explicit capability contract, requirement status, and coverage mapping.

## Milestone Sequence

- [x] M001 — Core runtime foundation
- [x] M002 — Contract convergence
- [x] M003 — Recovery hardening
- [x] M004 — Room runtime
- [x] M005 — Agent model refactoring
- [x] M006 — Fix golangci-lint v2 issues
- [x] M007 — Platform terminal state refactor
- [ ] M008 — CLI consolidation + API model rename (5→2 binaries, --root config, agent/agentrun model)
