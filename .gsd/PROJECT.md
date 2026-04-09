# Project

## What This Is

Open Agent Runtime (OAR) is a layered runtime for headless coding agents. It manages ACP-speaking agent processes through `agent-shim`, a higher-level daemon in `agentd`, and a future orchestrator layer for multi-agent coordination.

## Core Value

Reliable, observable agent execution with truthful lifecycle and recovery semantics.

## Current State

M007 in progress — platform terminal state refactor. **S01, S02, S03, and S04 complete.** Storage + model foundation done (S01); shim write authority boundary enforced and RestartPolicy tryReload/alwaysNew implemented (S02); full ARI JSON-RPC surface (workspace/* + agent/*) implemented and handler-tested (S03); CLI + workspace-mcp-server + design docs updated to terminal-state model (S04). Next: S05 (Integration Tests + Final Verification).

### Completed Milestones

| Milestone | Title | Summary |
|-----------|-------|---------|
| M001 | Core runtime foundation | agent-shim, agentd, ARI socket, workspace, metadata store, ACP handshake |
| M002 | Contract convergence | ARI client/server contract alignment, JSON-RPC lifecycle |
| M003 | Recovery hardening | Fail-closed recovery, shim-vs-DB reconciliation, atomic event resume, workspace cleanup |
| M004 | Room runtime | mesh/star/isolated room modes, room/send, room-mcp-server |
| M005 | Agent model refactoring | session→agent migration, async lifecycle, agent-centric ARI surface |
| M006 | Fix golangci-lint v2 issues | 202 → 0 issues across 11 linter categories; clean lint posture established |

### What's Implemented

- `agent-shim` starts ACP agent processes, performs the ACP handshake, exposes `session/*` + `runtime/*` shim RPC surface
- `agentd` manages agents with **(workspace, name)** identity (no UUID), async lifecycle, fail-closed recovery
- **bbolt metadata store**: v1/workspaces/{name} + v1/agents/{workspace}/{name} bucket layout; full CRUD for Agent + Workspace; 37 unit tests
- **spec.Status as sole state enum**: creating/idle/running/stopped/error; meta.AgentState and meta.SessionState deleted; pkg/runtime writes "idle" to state.json
- **D088 shim write authority boundary**: post-bootstrap state transitions flow exclusively through `runtime/stateChange` notifications; direct `UpdateStatus(StatusRunning)` removed from `Start()`; `buildNotifHandler` shared method handles both `session/update` and `runtime/stateChange`
- **D089 RestartPolicy tryReload/alwaysNew**: `tryReload` reads ACP sessionId from state.json and calls `session/load` on the shim, falling back silently on any failure; `alwaysNew` (default) skips session/load entirely; constants in `pkg/meta/models.go`
- **Full ARI JSON-RPC server** (`pkg/ari/server.go`, 946 lines): all workspace/* and agent/* handlers with Unix socket, Serve/Shutdown lifecycle, slog observability. 27 tests pass (18 handler tests + 9 pre-existing client/registry tests).
  - workspace/create → pending immediately, async prepare → ready/error
  - workspace/status → registry fast-path then DB fallback
  - workspace/list → registry-tracked (ready) workspaces only
  - workspace/delete → guarded by agent existence check
  - workspace/send → recovery guard, error-state rejection, fire-and-forget ShimClient.Prompt
  - agent/create → state=creating synchronously, background Start() goroutine
  - agent/prompt → StatusIdle gate; CodeRecoveryBlocked for any other state
  - agent/cancel, agent/stop, agent/delete, agent/restart, agent/list, agent/status, agent/attach
  - Zero `agentId` fields in any response (agentToInfo helper enforces structurally)
  - CodeRecoveryBlocked (-32001) for recovery-active paths
- **InjectProcess(key, proc)** on ProcessManager: test injection hook for workspace/send and agent/prompt tests without a real shim binary
- **Turn-aware event ordering**: TurnId/StreamSeq/Phase on session/update envelopes
- **Workspace preparation** for Git/EmptyDir/Local sources with hooks and reference tracking
- **CLI tooling** (`agentdctl`) with agent/workspace/daemon subcommands using (workspace,name) identity and `parseAgentKey()` helper
  - `agentdctl workspace send` subcommand added (--workspace, --from, --to, --text flags); stale `room` command removed
- **workspace-mcp-server binary** (`cmd/workspace-mcp-server/main.go`): renamed from room-mcp-server; reads OAR_WORKSPACE_NAME; exposes workspace_send and workspace_status MCP tools; logs workspace=/agentName=/agentID= on startup; `go build ./cmd/workspace-mcp-server` clean
- **Design docs updated**: `docs/design/agentd/ari-spec.md` fully rewritten for workspace/agent model; `docs/design/agentd/agentd.md` updated to remove Session Manager, use workspace+name identity, and match spec.Status state values
- **Fully clean golangci-lint v2 posture**: 0 issues across all 11 linter categories (as of M006; M007/S05 will re-validate)

### M007 Slice Status

| Slice | Title | Status |
|-------|-------|--------|
| S01 | Storage + Model Foundation | ✅ complete |
| S02 | agentd Core Adaptation | ✅ complete |
| S03 | ARI Surface Rewrite | ✅ complete |
| S04 | CLI + workspace-mcp-server + Design Docs | ✅ complete |
| S05 | Integration Tests + Final Verification | ⬜ next |

### Lint Status (post-M006)

`golangci-lint run ./...` → **0 issues** pre-M007. M007/S05 will re-validate after all structural changes land.

### Known Pre-existing Issues

- `TestProcessManagerStart` in `pkg/agentd` fails when `bin/agent-shim` socket doesn't start (requires live mock agent); confirmed pre-existing since before M007/S01.
- Integration tests in `tests/integration/` pre-date M006; will be rewritten for new (workspace,name) identity in M007/S05.

## Milestone Sequence

- [x] M001-tvc4z0: Core runtime foundation — agent-shim, agentd, ARI socket, workspace, metadata store, ACP handshake
- [x] M001-tlbeko: Workspace phase — Git/EmptyDir/Local sources, setup/teardown hooks, workspace ARI methods
- [x] M002: Contract convergence — ARI client/server contract alignment, JSON-RPC lifecycle
- [x] M003: Recovery hardening — fail-closed recovery, shim-vs-DB reconciliation, atomic event resume
- [x] M004: Room runtime — mesh/star/isolated room modes, room/send, room-mcp-server
- [x] M005: Agent model refactoring — session→agent migration, async lifecycle, agent-centric ARI surface
- [x] M006: Fix golangci-lint v2 issues — 202→0 issues across 11 linter categories
- [ ] M007: Platform terminal state refactor — bbolt storage, unified spec.Status, (workspace,name) identity, shim write authority, Room/Session elimination
  - [x] S01: Storage + Model Foundation — bbolt store, new Agent+Workspace models, spec.StatusIdle, green build
  - [x] S02: agentd Core Adaptation — shim write authority (D088), RestartPolicy tryReload/alwaysNew (D089), 10 unit tests
  - [x] S03: ARI Surface Rewrite — 946-line server.go with all workspace/* + agent/* handlers; 27 tests pass; zero agentId fields
  - [x] S04: CLI + workspace-mcp-server + Design Docs — workspace-mcp-server binary, workspace send subcommand, room cmd removed, design docs rewritten
  - [ ] S05: Integration Tests + Final Verification (depends S04)
