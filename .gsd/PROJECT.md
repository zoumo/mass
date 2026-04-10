# Project

## What This Is

Open Agent Runtime (OAR) is a layered runtime for headless coding agents. It manages ACP-speaking agent processes through `agentd` (daemon), `agent-shim` (process shim), and a future orchestrator layer for multi-agent coordination. Modeled after containerd/runc: spec-driven, layered, single-binary operational model.

## Core Value

Reliable, observable agent execution with truthful lifecycle and recovery semantics.

## Current State

**M008 in progress — S01 complete.** Binary skeleton reorganization done: `cmd/agentd` is now a cobra tree (server/shim/workspace-mcp), `cmd/agentdctl` extended with shimCmd (full client) and agentrunCmd (9-subcommand stub), Makefile produces only `bin/agentd` + `bin/agentdctl`. `go build ./...` and `go vet ./...` both clean. Legacy cmd/ directories remain for S04 deletion.

**Remaining M008 slices:** S02 (--root config + RuntimeClass DB entity + self-fork shim), S03 (CLI grammar alignment + socket validation), S04 (cleanup + API rename to agent/agentrun + integration tests).

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

| Milestone | Title | Slice Progress |
|-----------|-------|---------------|
| M008 | CLI consolidation + API model rename | S01 ✅ / S02 ⬜ / S03 ⬜ / S04 ⬜ |

### What's Implemented

- `agentd` manages agents with **(workspace, name)** identity (no UUID), async lifecycle, fail-closed recovery
- `agentd` is now a **cobra tree**: `agentd server` (daemon), `agentd shim` (inlined from agent-shim), `agentd workspace-mcp` (inlined from workspace-mcp-server)
- `agentdctl` is now a **full-featured CLI**: agent/workspace/daemon/shim (full client) + agentrun (stubs, wired in S04)
- `agent-shim` starts ACP agent processes, performs the ACP handshake, exposes `session/*` + `runtime/*` shim RPC surface
- **bbolt metadata store**: `v1/workspaces/{name}` + `v1/agents/{workspace}/{name}` bucket layout
- **spec.Status as sole state enum**: creating/idle/running/stopped/error
- **Full ARI JSON-RPC server**: workspace/* and agent/* handlers over Unix socket
- **agentdctl CLI**: agent/workspace/daemon/shim subcommands (resource-first grammar finalized in S03)

## Architecture / Key Patterns

- **2-binary model (M008 target, skeleton complete):** `agentd` (server/shim/workspace-mcp subcommands) + `agentdctl` (agent/agentrun/workspace/shim resource commands)
- **ARI protocol:** JSON-RPC 2.0 over Unix domain socket
- **OCI-inspired layering:** workspace=rootfs, agent(template)=container definition, agentrun=task/running instance, shim=runc equivalent
- **bbolt buckets:** `v1/workspaces/{name}`, `v1/agents/{workspace}/{name}`; `v1/runtimes/{name}` added in S02, renamed to `v1/agents` (templates) in S04
- **cobra package main collision avoidance:** wmcp prefix for workspace-mcp types, shim prefix for shim client types, local flag vars scoped inside constructor functions (K068)

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
  - [x] S01 — Binary skeleton reorganization (cobra tree, shim+agentrun stubs, Makefile)
  - [ ] S02 — --root config + RuntimeClass DB entity + self-fork shim
  - [ ] S03 — CLI grammar alignment + socket validation
  - [ ] S04 — Cleanup + API rename (agent/agentrun) + integration tests
