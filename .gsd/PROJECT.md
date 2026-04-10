# Project

## What This Is

Open Agent Runtime (OAR) is a layered runtime for headless coding agents. It manages ACP-speaking agent processes through `agentd` (daemon), `agent-shim` (process shim), and a future orchestrator layer for multi-agent coordination. Modeled after containerd/runc: spec-driven, layered, single-binary operational model.

## Core Value

Reliable, observable agent execution with truthful lifecycle and recovery semantics.

## Current State

**M008 in progress — S01 ✅ S02 ✅ S03 ✅.** Binary consolidation complete. Config-free startup via `--root` fully wired. RuntimeClass is DB-persisted. CLI grammar aligned: `agentrun prompt` has `-w`/`--workspace` and `--text` flags; `workspace create` uses positional `<type> <name>` args. Socket-path overflow validated at `agentrun/create` entry (pre-DB-write `-32602` guard) with platform build-tag constants (`maxsockpath_darwin.go` / `maxsockpath_linux.go`).

**Remaining M008 slices:** S04 (cleanup + API rename to agent=template/agentrun=running instance + integration tests).

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
| M008 | CLI consolidation + API model rename | S01 ✅ / S02 ✅ / S03 ✅ / S04 ⬜ |

### What's Implemented

- `agentd` manages agents with **(workspace, name)** identity (no UUID), async lifecycle, fail-closed recovery
- `agentd` is a **cobra tree**: `agentd server` (daemon, --root flag, no config.yaml), `agentd shim` (inlined from agent-shim), `agentd workspace-mcp` (inlined from workspace-mcp-server)
- `agentdctl` is a **full-featured CLI**: agent/workspace/daemon/shim/runtime (full clients) + agentrun (prompt/create/stop/delete stubs with correct flag grammar, wired in S04)
- **Runtime entity CLI grammar (finalized):** `agentrun prompt <run-id> -w <ws> --text <text>`; `workspace create <type> <name> [--path|--url|--ref|--depth]`
- **DB-persisted runtime entities**: `meta.Runtime` in `v1/runtimes` bbolt bucket; ARI `runtime/set|get|list|delete`; `agentdctl runtime apply -f` YAML-based CLI
- **Self-fork shim**: `forkShim` uses `os.Executable()` for single-binary deployment; `OAR_SHIM_BINARY` env override for testing
- **agent-shim** starts ACP agent processes, performs the ACP handshake, exposes `session/*` + `runtime/*` shim RPC surface
- **bbolt metadata store**: `v1/workspaces/{name}` + `v1/agents/{workspace}/{name}` + `v1/runtimes/{name}` bucket layout
- **spec.Status as sole state enum**: creating/idle/running/stopped/error
- **Full ARI JSON-RPC server**: workspace/*, agent/*, runtime/* handlers over Unix socket
- **Socket path overflow guard**: `ValidateAgentSocketPath` in ProcessManager; early `-32602` at `agentrun/create` entry before any DB write; platform limits in build-tag files

## Architecture / Key Patterns

- **2-binary model (M008 target, skeleton complete):** `agentd` (server/shim/workspace-mcp subcommands) + `agentdctl` (agent/agentrun/workspace/shim/runtime resource commands)
- **ARI protocol:** JSON-RPC 2.0 over Unix domain socket
- **OCI-inspired layering:** workspace=rootfs, agent(template)=container definition, agentrun=task/running instance, shim=runc equivalent
- **bbolt buckets:** `v1/workspaces/{name}`, `v1/agents/{workspace}/{name}`, `v1/runtimes/{name}` (renamed to `v1/agents` templates + `v1/agentruns` instances in S04)
- **--root derived paths**: Options.SocketPath(), WorkspaceRoot(), BundleRoot(), MetaDBPath() — no config file needed
- **cobra package main collision avoidance:** wmcp prefix for workspace-mcp types, shim prefix for shim client types, local flag vars scoped inside constructor functions (K068)
- **cobra inline command literal extraction:** Named var required before calling Flags() — anonymous literals are unaddressable (K072)
- **runtimeApplySpec local YAML struct**: CLI YAML deserialization struct is separate from pkg/ari canonical params types (K071)
- **ari.Client.Call error surfacing**: Wraps RPC errors as plain fmt.Errorf strings — use err.Error() contains check, not errors.As(*jsonrpc2.Error) (K073)

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
- [ ] M008 — CLI consolidation + API model rename (S01 ✅ S02 ✅ S03 ✅ S04 ⬜)
