# Project

## What This Is

Open Agent Runtime (OAR) is a layered runtime for headless coding agents. It manages ACP-speaking agent processes through `agentd` (daemon), `agentd shim` (inlined shim subcommand), and a future orchestrator layer for multi-agent coordination. Modeled after containerd/runc: spec-driven, layered, single-binary operational model.

## Core Value

Reliable, observable agent execution with truthful lifecycle and recovery semantics.

## Current State

**M012 complete — all 6 slices done.** Codebase refactor is complete: typed Service Interfaces (api/shim, api/ari), pkg/jsonrpc unified transport, directory restructure, and cleanup of all legacy packages.

**No active milestone.** M012 was the most recent completed milestone.

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
| M012 | Codebase Refactor: Service Interface + Unified RPC + Directory Restructure | Typed service interfaces, pkg/jsonrpc unified transport, ARI wire contract convergence, adapter pattern, legacy package cleanup; make build + go test ./... pass with zero legacy references |

### M012 Status (Complete)

| Slice | Title | Status |
|-------|-------|--------|
| S01 | Phase 0: pkg/jsonrpc framework | ✅ complete |
| S02 | Phase 1: JSON output preservation | ✅ complete |
| S03 | Phase 2: ARI wire contract convergence | ✅ complete |
| S04 | Phase 3: Service Interface definitions | ✅ complete |
| S05 | Phase 4: Implementation Migration | ✅ complete |
| S06 | Phase 5: Cleanup | ✅ complete |

### What's Implemented

- `agentd` manages agents with **(workspace, name)** identity (no UUID), async lifecycle, fail-closed recovery
- `agentd` is a **cobra tree**: `agentd server` (daemon, --root flag, no config.yaml), `agentd shim` (inlined from agent-shim), `agentd workspace-mcp` (inlined from workspace-mcp-server)
- `agentdctl` is a **full-featured CLI**: `agentdctl agent` (template CRUD: apply/get/list/delete) + `agentdctl agentrun` (lifecycle: create/list/status/prompt/stop/delete/restart/attach/cancel) + workspace/daemon/shim commands
- **ARI surface (final):** `workspace/*` (create/status/list/delete/send) + `agent/*` (set/get/list/delete — AgentTemplate CRUD) + `agentrun/*` (create/prompt/cancel/stop/delete/restart/list/status/attach — running instance lifecycle)
- **Typed Service Interfaces (M012):** `api/shim.ShimService` interface + `api/ari.WorkspaceService`, `AgentRunService`, `AgentService` interfaces — all implementations satisfy these typed contracts
- **Typed implementations (M012):** `pkg/shim/server.Service` implements ShimService; `pkg/ari/server.Service` (adapter pattern) implements all three ARI services; `pkg/ari/client.ARIClient` and `pkg/shim/client` are Dial helpers
- **pkg/jsonrpc unified transport (M012):** Single JSON-RPC transport used by both ARI server and shim server; cmd entrypoints use explicit net.Listen + jsonrpc.Server pattern; legacy pkg/rpc, pkg/ari/server.go monolith, and pkg/agentd/shim_client.go all deleted
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
- **Typed service interface pattern (M012):** api/shim.ShimService + api/ari.*Service interfaces; implementations in pkg/shim/server + pkg/ari/server; Dial helpers in pkg/shim/client + pkg/ari/client
- **Adapter pattern for conflicting method names (M012):** WorkspaceService.List and AgentService.List have identical Go signatures — use three thin unexported adapter structs embedding *Service (K077, D112)
- **cmd entrypoint pattern (M012):** net.Listen (explicit lifecycle) + jsonrpc.NewServer + Register(srv, svc) + srv.Serve(ln) in goroutine + srv.Shutdown(ctx) on signal
- **Test server cleanup order (M012):** ln.Close() then srv.Shutdown(ctx) — closing listener unblocks Accept() so Serve() goroutine exits cleanly (K080, D114)
- **Test file deletion safety (M012):** Before deleting *_test.go, grep for cross-file dependencies in the same package; extract shared infrastructure to a new file (K079, D113)
- **bbolt buckets:** `v1/workspaces/{name}`, `v1/agentruns/{workspace}/{name}`, `v1/agents/{name}`
- **--root derived paths**: Options.SocketPath(), WorkspaceRoot(), BundleRoot(), MetaDBPath() — no config file needed
- **cobra package main collision avoidance:** wmcp prefix for workspace-mcp types, shim prefix for shim client types, local flag vars scoped inside constructor functions (K068)
- **Three-layer rename discipline**: meta layer → ari types → ari server + CLI must compile as a unit — never layer-by-layer (K074)
- **runtimeApplySpec local YAML struct**: CLI YAML deserialization struct is separate from pkg/ari canonical params types (K071)
- **ari.Client.Call error surfacing**: Wraps RPC errors as plain fmt.Errorf strings — use err.Error() contains check, not errors.As(*jsonrpc2.Error) (K073)
- **macOS socket path limit**: t.TempDir() paths often exceed 104-byte Unix socket limit; use os.MkdirTemp("/tmp", "oar-*") for socket-sensitive tests (K075)
- **pkg/jsonrpc notifCh race**: Client.enqueueNotification has a pre-existing send-on-closed-channel race visible under -count=3; single-run go test ./... is the acceptance bar (K078)

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
- [x] M012 — Codebase Refactor: Service Interface + Unified RPC + Directory Restructure
