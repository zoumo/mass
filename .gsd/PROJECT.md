# Project

## What This Is

Open Agent Runtime (OAR) is a layered runtime for headless coding agents. It manages ACP-speaking agent processes through `agent-shim`, a higher-level daemon in `agentd`, and a future orchestrator layer for multi-agent coordination.

## Core Value

Reliable, observable agent execution with truthful lifecycle and recovery semantics.

## Current State

M007 in progress — platform terminal state refactor: bbolt storage, unified spec.Status, (workspace,name) identity, shim write authority, Room/Session elimination.

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
- `agentd` manages agents (external, room+name identity) and sessions (internal runtime instances), async lifecycle, fail-closed recovery
- **Agent-centric ARI surface**: `agent/create`, `agent/prompt`, `agent/cancel`, `agent/stop`, `agent/delete`, `agent/restart`, `agent/list`, `agent/status`, `agent/attach`, `agent/detach`
- **Turn-aware event ordering**: TurnId/StreamSeq/Phase on session/update envelopes
- **Room runtime**: mesh/star/isolated modes, room/send, room-mcp-server (SDK-based)
- **Workspace preparation** for Git/EmptyDir/Local sources with hooks and reference tracking
- **CLI tooling** (`agentdctl`) with agent/workspace/daemon subcommands
- **Fully clean golangci-lint v2 posture**: 0 issues across all 11 linter categories (as of M006)

### Lint Status (post-M006)

`golangci-lint run ./...` → **0 issues** — all 11 linter categories clean.

| Linter | M006 start | Final |
|--------|-----------|-------|
| gci | 28 | ✅ 0 |
| gofumpt | 28 | ✅ 0 |
| unconvert | 22 | ✅ 0 |
| copyloopvar | 1 | ✅ 0 |
| ineffassign | 1 | ✅ 0 |
| misspell | ~9 | ✅ 0 |
| unparam | ~8 | ✅ 0 |
| unused | 12 | ✅ 0 |
| errorlint | 17 | ✅ 0 |
| gocritic | 45 | ✅ 0 |
| testifylint | 31 | ✅ 0 |

### Known Pre-existing Issues

- Integration test failures in `tests/integration/` (5 tests related to prompt acceptance) pre-date M006; resolved as part of M007/S05 (tests rewritten for new (workspace,name) identity).

## Milestone Sequence

- [x] M001-tvc4z0: Core runtime foundation — agent-shim, agentd, ARI socket, workspace, metadata store, ACP handshake
- [x] M001-tlbeko: Workspace phase — Git/EmptyDir/Local sources, setup/teardown hooks, workspace ARI methods
- [x] M002: Contract convergence — ARI client/server contract alignment, JSON-RPC lifecycle
- [x] M003: Recovery hardening — fail-closed recovery, shim-vs-DB reconciliation, atomic event resume
- [x] M004: Room runtime — mesh/star/isolated room modes, room/send, room-mcp-server
- [x] M005: Agent model refactoring — session→agent migration, async lifecycle, agent-centric ARI surface
- [x] M006: Fix golangci-lint v2 issues — 202→0 issues across 11 linter categories
- [ ] M007: Platform terminal state refactor — bbolt storage, unified spec.Status, (workspace,name) identity, shim write authority, Room/Session elimination
