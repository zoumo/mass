# Project

## What This Is

Open Agent Runtime (OAR) is a layered runtime for headless coding agents. It manages ACP-speaking agent processes through `agentd` (daemon), `agentd shim` (inlined shim subcommand), and a future orchestrator layer for multi-agent coordination. Modeled after containerd/runc: spec-driven, layered, single-binary operational model.

## Core Value

Reliable, observable agent execution with truthful lifecycle and recovery semantics.

## Current State

**M008 complete — all 4 slices done.** Binary consolidation, config-free startup, DB-persisted AgentTemplate (was RuntimeClass), resource-first CLI grammar (`agentdctl agent` / `agentdctl agentrun`), and full API rename (agent=template definition, agentrun=running instance) are all in place. All integration tests pass.

**No active milestones.** The platform is stable at M008.

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
| M008 | CLI consolidation + API model rename | 2-binary model (agentd + agentdctl), --root startup, DB-persisted AgentTemplate, resource-first grammar, agent=template/agentrun=instance rename; all 8 integration tests pass |

### What's Implemented

- `agentd` manages agents with **(workspace, name)** identity (no UUID), async lifecycle, fail-closed recovery
- `agentd` is a **cobra tree**: `agentd server` (daemon, --root flag, no config.yaml), `agentd shim` (inlined from agent-shim), `agentd workspace-mcp` (inlined from workspace-mcp-server)
- `agentdctl` is a **full-featured CLI**: `agentdctl agent` (template CRUD: apply/get/list/delete) + `agentdctl agentrun` (lifecycle: create/list/status/prompt/stop/delete/restart/attach/cancel) + workspace/daemon/shim commands
- **ARI surface (final):** `workspace/*` (create/status/list/delete/send) + `agent/*` (set/get/list/delete — AgentTemplate CRUD) + `agentrun/*` (create/prompt/cancel/stop/delete/restart/list/status/attach — running instance lifecycle)
- **DB-persisted agent templates**: `meta.AgentTemplate` in `v1/agents` bbolt bucket; ARI `agent/set|get|list|delete`; `agentdctl agent apply -f` YAML-based CLI
- **bbolt metadata store**: `v1/workspaces/{name}` + `v1/agentruns/{workspace}/{name}` + `v1/agents/{name}` bucket layout
- **Self-fork shim**: `forkShim` uses `os.Executable()` for single-binary deployment; `OAR_SHIM_BINARY` env override for testing
- **agent-shim** starts ACP agent processes, performs the ACP handshake, exposes `session/*` + `runtime/*` shim RPC surface
- **spec.Status as sole state enum**: creating/idle/running/stopped/error
- **Socket path overflow guard**: `ValidateAgentSocketPath` in ProcessManager; early `-32602` at `agentrun/create` entry before any DB write; platform limits in build-tag files

## Architecture / Key Patterns

- **2-binary model:** `agentd` (server/shim/workspace-mcp subcommands) + `agentdctl` (agent/agentrun/workspace/shim resource commands)
- **ARI protocol:** JSON-RPC 2.0 over Unix domain socket
- **OCI-inspired layering:** workspace=rootfs, agent (AgentTemplate)=container definition, agentrun=task/running instance, shim=runc equivalent
- **bbolt buckets:** `v1/workspaces/{name}`, `v1/agentruns/{workspace}/{name}`, `v1/agents/{name}`
- **--root derived paths**: Options.SocketPath(), WorkspaceRoot(), BundleRoot(), MetaDBPath() — no config file needed
- **cobra package main collision avoidance:** wmcp prefix for workspace-mcp types, shim prefix for shim client types, local flag vars scoped inside constructor functions (K068)
- **Three-layer rename discipline**: meta layer → ari types → ari server + CLI must compile as a unit — never layer-by-layer (K074)
- **runtimeApplySpec local YAML struct**: CLI YAML deserialization struct is separate from pkg/ari canonical params types (K071)
- **ari.Client.Call error surfacing**: Wraps RPC errors as plain fmt.Errorf strings — use err.Error() contains check, not errors.As(*jsonrpc2.Error) (K073)
- **macOS socket path limit**: t.TempDir() paths often exceed 104-byte Unix socket limit; use os.MkdirTemp("/tmp", "oar-*") for socket-sensitive tests (K075)

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
- [x] M008 — CLI consolidation + API model rename
