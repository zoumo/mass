# Project

## What This Is

Open Agent Runtime (OAR) is a layered runtime for headless coding agents. It manages ACP-speaking agent processes through `agentd` (daemon), `agentd shim` (inlined shim subcommand), and a future orchestrator layer for multi-agent coordination. Modeled after containerd/runc: spec-driven, layered, single-binary operational model.

## Core Value

Reliable, observable agent execution with truthful lifecycle and recovery semantics.

## Current State

**M013 active — S01 + S02 complete.** ARI package restructure done: api/ari/ deleted, pkg/ari root files deleted, pkg/ari/api/ + pkg/ari/server/ + pkg/ari/client/ established as canonical sub-packages; all 35+ consumers migrated; `make build` + `go test ./...` pass clean. S03 (Shim package restructure + api/ deletion) is next.

### Active Milestone

**M013 — Package Restructure: Clean api/ Boundary + Event/Runtime Colocation**

| Slice | Title | Status |
|-------|-------|--------|
| S01 | Runtime-spec consumer migration | ✅ done |
| S02 | ARI package restructure | ✅ done |
| S03 | Shim package restructure + api/ deletion | ⬜ pending |
| S04 | Events impl + ACP runtime migration + final verification | ⬜ pending |

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

### What's Implemented

- `agentd` manages agents with **(workspace, name)** identity (no UUID), async lifecycle, fail-closed recovery
- `agentd` is a **cobra tree**: `agentd server` (daemon, --root flag, no config.yaml), `agentd shim` (inlined from agent-shim), `agentd workspace-mcp` (inlined from workspace-mcp-server)
- `agentdctl` is a **full-featured CLI**: `agentdctl agent` (template CRUD: apply/get/list/delete) + `agentdctl agentrun` (lifecycle: create/list/status/prompt/stop/delete/restart/attach/cancel) + workspace/daemon/shim commands
- **ARI surface (final):** `workspace/*` (create/status/list/delete/send) + `agent/*` (set/get/list/delete — AgentTemplate CRUD) + `agentrun/*` (create/prompt/cancel/stop/delete/restart/list/status/attach — running instance lifecycle)
- **pkg/ari tri-split (M013/S02):** `pkg/ari/api` (pure types + ARI method constants), `pkg/ari/server` (interfaces, Registry, RPC dispatch), `pkg/ari/client` (typed ARIClient + simple Client); api/ari/ directory deleted
- **pkg/runtime-spec/api as sole Status/EnvVar home (M013/S01):** api/runtime/ deleted; api/types.go deleted; all consumers use `apiruntime "github.com/zoumo/oar/pkg/runtime-spec/api"`
- **Typed Service Interfaces (M012):** `api/shim.ShimService` interface + ARI service interfaces in pkg/ari/server — all implementations satisfy typed contracts
- **Typed implementations (M012):** `pkg/shim/server.Service` implements ShimService; `pkg/ari/server.Service` (adapter pattern) implements all three ARI services; `pkg/ari/client.ARIClient` and `pkg/shim/client` are Dial helpers
- **pkg/jsonrpc unified transport (M012):** Single JSON-RPC transport used by both ARI server and shim server; legacy pkg/rpc, pkg/ari/server.go monolith, and pkg/agentd/shim_client.go all deleted
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
- **pkg/ari tri-split pattern (M013/S02):** api/ for pure types+constants, server/ for interfaces+dispatch, client/ for dial helpers; canonical import `pkgariapi "github.com/zoumo/oar/pkg/ari/api"`
- **Typed service interface pattern (M012):** api/shim.ShimService + pkg/ari/server.*Service interfaces; implementations in pkg/shim/server + pkg/ari/server; Dial helpers in pkg/shim/client + pkg/ari/client
- **Adapter pattern for conflicting method names (M012):** WorkspaceService.List and AgentService.List have identical Go signatures — use three thin unexported adapter structs embedding *Service (K077, D112)
- **cmd entrypoint pattern (M012):** net.Listen (explicit lifecycle) + jsonrpc.NewServer + Register(srv, svc) + srv.Serve(ln) in goroutine + srv.Shutdown(ctx) on signal
- **Test server cleanup order (M012):** ln.Close() then srv.Shutdown(ctx) — closing listener unblocks Accept() so Serve() goroutine exits cleanly (K080, D114)
- **Test file deletion safety (M012):** Before deleting *_test.go, grep for cross-file dependencies in the same package; extract shared infrastructure to a new file (K079, D113)
- **Named type cascade migration (M013/S01):** When a Go named type moves packages, migrate all files passing it across package boundaries in the same build wave; compile errors are the dependency map (K083)
- **Same-package type qualification (M013/S02):** When moving types and consumers into the same new sub-package, strip qualifiers from types now in the same package (K084); same for Register functions (K085)
- **Dual-import consolidation (M013/S02 Rule B):** Files using both `api/ari` types AND bare `api` method constants consolidate to single `pkgariapi "pkg/ari/api"` — method constants are now in pkg/ari/api/methods.go
- **bbolt buckets:** `v1/workspaces/{name}`, `v1/agentruns/{workspace}/{name}`, `v1/agents/{name}`
- **--root derived paths**: Options.SocketPath(), WorkspaceRoot(), BundleRoot(), MetaDBPath() — no config file needed
- **cobra package main collision avoidance:** wmcp prefix for workspace-mcp types, shim prefix for shim client types, local flag vars scoped inside constructor functions (K068)
- **Three-layer rename discipline**: meta layer → ari types → ari server + CLI must compile as a unit — never layer-by-layer (K074)
- **runtimeApplySpec local YAML struct**: CLI YAML deserialization struct is separate from pkg/ari canonical params types (K071)
- **ari.Client.Call error surfacing**: Wraps RPC errors as plain fmt.Errorf strings — use err.Error() contains check, not errors.As(*jsonrpc2.Error) (K073)
- **macOS socket path limit**: t.TempDir() paths often exceed 104-byte Unix socket limit; use os.MkdirTemp("/tmp", "oar-*") for socket-sensitive tests (K075)
- **pkg/jsonrpc notifCh race**: Client.enqueueNotification has a pre-existing send-on-closed-channel race visible under -count=3; single-run go test ./... is the acceptance bar (K078)
- **rg exit code semantics**: exit 1 = no matches (not a failure); zero-match verification gates must use `! rg PATTERN` (K082)

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
- [ ] M013 — Package Restructure: Clean api/ Boundary + Event/Runtime Colocation (S01 ✅, S02 ✅, S03-S04 pending)
