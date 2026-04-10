# Project

## What This Is

Open Agent Runtime (OAR) is a layered runtime for headless coding agents. It manages ACP-speaking agent processes through `agent-shim`, a higher-level daemon in `agentd`, and a future orchestrator layer for multi-agent coordination.

## Core Value

Reliable, observable agent execution with truthful lifecycle and recovery semantics.

## Current State

**M007 complete.** All five slices delivered. The platform terminal state refactor is done: bbolt replaces SQLite, `spec.Status` (creating/idle/running/stopped/error) is the sole state enum, Room/Session concepts eliminated, `(workspace, name)` identity throughout, shim-only post-bootstrap state writes enforced (D088), RestartPolicy tryReload/alwaysNew governs recovery (D089). Integration tests pass (`go test ./tests/integration/... -v -timeout 120s` → 7 PASS + 2 SKIP), lint is clean (`golangci-lint run ./... → 0 issues`), `bin/workspace-mcp-server` built, 0 banned references.

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

### What's Implemented

- `agent-shim` starts ACP agent processes, performs the ACP handshake, exposes `session/*` + `runtime/*` shim RPC surface
- `agentd` manages agents with **(workspace, name)** identity (no UUID), async lifecycle, fail-closed recovery
- **bbolt metadata store**: `v1/workspaces/{name}` + `v1/agents/{workspace}/{name}` bucket layout; full CRUD for Agent + Workspace; 37 unit tests
- **spec.Status as sole state enum**: creating/idle/running/stopped/error; meta.AgentState and meta.SessionState deleted; pkg/runtime writes "idle" to state.json after ACP handshake and after each prompt turn
- **D088 shim write authority boundary**: post-bootstrap state transitions flow exclusively through `runtime/stateChange` notifications; direct `UpdateStatus(StatusRunning)` removed from `Start()`; `buildNotifHandler` shared method handles both `session/update` and `runtime/stateChange`
- **D089 RestartPolicy tryReload/alwaysNew**: `tryReload` reads ACP sessionId from state.json and calls `session/load` on the shim after Subscribe (critical ordering), falling back silently on any failure; `alwaysNew` (default) skips session/load entirely; constants in `pkg/meta/models.go`
- **Full ARI JSON-RPC server** (`pkg/ari/server.go`, 946 lines): all workspace/* and agent/* handlers with Unix socket, Serve/Shutdown lifecycle, slog observability at every handler entry. 22 unit tests pass.
  - workspace/create → pending immediately, async prepare → ready/error
  - workspace/status → registry fast-path then DB fallback
  - workspace/list → registry-tracked (ready) workspaces only
  - workspace/delete → guarded by agent existence check (CodeRecoveryBlocked -32001 if agents exist)
  - workspace/send → recovery guard, error-state rejection, fire-and-forget ShimClient.Prompt
  - agent/create → state=creating synchronously, background Start() goroutine
  - agent/prompt → StatusIdle gate; CodeRecoveryBlocked for any other state
  - agent/cancel, agent/stop, agent/delete, agent/restart, agent/list, agent/status, agent/attach
  - **Zero `agentId` fields** in any response (agentToInfo helper enforces structurally)
- **InjectProcess(key, proc)** on ProcessManager: test injection hook for workspace/send and agent/prompt tests without a real shim binary
- **Turn-aware event ordering**: TurnId/StreamSeq/Phase on session/update envelopes
- **Workspace preparation** for Git/EmptyDir/Local sources with hooks and reference tracking
- **CLI tooling** (`agentdctl`) with agent/workspace/daemon subcommands using (workspace,name) identity and `parseAgentKey()` helper
  - `agentdctl workspace send` subcommand (--workspace, --from, --to, --text flags); stale `room` command removed
- **workspace-mcp-server binary** (`cmd/workspace-mcp-server/main.go`): renamed from room-mcp-server; reads OAR_WORKSPACE_NAME; exposes workspace_send and workspace_status MCP tools; self-contained local ARI structs
- **Design docs**: `docs/design/agentd/ari-spec.md` fully rewritten for workspace/agent model (all 14 methods documented with params/responses); `docs/design/agentd/agentd.md` updated to remove Session Manager, use workspace+name identity, and match spec.Status state values
- **Integration tests** (`tests/integration/`): All 9 integration tests pass (7 PASS + 2 SKIP for missing ANTHROPIC_API_KEY); 5 test files fully rewritten for new workspace/agent ARI model; concurrent, e2e, lifecycle, restart, and real-CLI families covered
- **Clean golangci-lint v2 posture**: `golangci-lint run ./... → 0 issues`

### Infrastructure Fixes (M007/S05)

Three pre-existing bugs in `pkg/agentd/process.go` discovered and fixed during integration test rewrite:
1. **Shim socket path mismatch (D101)**: `forkShim` now passes `filepath.Base(stateDir)` (hyphenated `workspace-name`) as `--id` instead of slash-separated `workspace/name`; shim and agentd now agree on socket location
2. **Missed idle notification (D102)**: After Subscribe, agentd now reads `runtime/status` to bootstrap DB to idle if shim is already past the creating state (SetStateChangeHook is registered after Create() returns, so the first notification fires before the hook is set)
3. **Stale socket files**: `os.Remove(socketPath)` called before fork to clear leftover sockets from prior test runs

## Milestone Sequence

- [x] M001-tvc4z0: Core runtime foundation — agent-shim, agentd, ARI socket, workspace, metadata store, ACP handshake
- [x] M001-tlbeko: Workspace phase — Git/EmptyDir/Local sources, setup/teardown hooks, workspace ARI methods
- [x] M002: Contract convergence — ARI client/server contract alignment, JSON-RPC lifecycle
- [x] M003: Recovery hardening — fail-closed recovery, shim-vs-DB reconciliation, atomic event resume
- [x] M004: Room runtime — mesh/star/isolated room modes, room/send, room-mcp-server
- [x] M005: Agent model refactoring — session→agent migration, async lifecycle, agent-centric ARI surface
- [x] M006: Fix golangci-lint v2 issues — 202→0 issues across 11 linter categories
- [x] M007: Platform terminal state refactor — bbolt storage, unified spec.Status, (workspace,name) identity, shim write authority, Room/Session elimination
  - [x] S01: Storage + Model Foundation — bbolt store, new Agent+Workspace models, spec.StatusIdle, green build
  - [x] S02: agentd Core Adaptation — shim write authority (D088), RestartPolicy tryReload/alwaysNew (D089), 10 unit tests
  - [x] S03: ARI Surface Rewrite — server.go with all workspace/* + agent/* handlers; 22 tests pass; zero agentId fields
  - [x] S04: CLI + workspace-mcp-server + Design Docs — workspace-mcp-server binary, workspace send subcommand, room cmd removed, design docs rewritten
  - [x] S05: Integration Tests + Final Verification — all 9 integration tests pass; golangci-lint → 0 issues; 3 infra bugs fixed

## Lint Status

`golangci-lint run ./...` → **0 issues** (validated M007/S05)

## Known Open Work

- session/load handler in real agent-shim binary (shim side of D089 tryReload; shim-side is currently a no-op; end-to-end proof deferred)
- Integration test for workspace/send message delivery (currently only unit-tested in S03)
- Workspace filesystem isolation: Workspace.Status.Path exists in the model but workspace/create does not yet provision a real filesystem directory
- TestRealCLI_GsdPi and TestRealCLI_ClaudeCode skip without ANTHROPIC_API_KEY; a mock-LLM CI toggle could enable lightweight functional verification
