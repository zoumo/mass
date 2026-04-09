# Project

## What This Is

Open Agent Runtime (OAR) is a layered runtime for headless coding agents. It manages ACP-speaking agent processes through `agent-shim`, a higher-level daemon in `agentd`, and a future orchestrator layer for multi-agent coordination.

## Core Value

Reliable, observable agent execution with truthful lifecycle and recovery semantics.

## Current State

M007 in progress — platform terminal state refactor. **S01 complete.** Storage + model foundation is done: bbolt replaces SQLite, spec.Status is the sole state enum, Agent identity is (workspace, name), Session/Room/AgentState/SessionState concepts are fully deleted, `go build ./...` is green. Next: S02 (shim write authority + RestartPolicy).

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
- **Agent-centric ARI surface**: pkg/ari/types.go with new Workspace/Agent types; server.go is a compilable stub (full handler rewrite in S03)
- **Turn-aware event ordering**: TurnId/StreamSeq/Phase on session/update envelopes
- **Workspace preparation** for Git/EmptyDir/Local sources with hooks and reference tracking
- **CLI tooling** (`agentdctl`) with agent/workspace/daemon subcommands using (workspace,name) identity and `parseAgentKey()` helper
- **Fully clean golangci-lint v2 posture**: 0 issues across all 11 linter categories (as of M006; M007/S05 will re-validate)

### M007 Slice Status

| Slice | Title | Status |
|-------|-------|--------|
| S01 | Storage + Model Foundation | ✅ complete |
| S02 | agentd Core Adaptation | ⬜ next |
| S03 | ARI Surface Rewrite | ⬜ |
| S04 | CLI + workspace-mcp-server + Design Docs | ⬜ |
| S05 | Integration Tests + Final Verification | ⬜ |

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
  - [ ] S02: agentd Core Adaptation — shim write authority, RestartPolicy, tryReload/alwaysNew
  - [ ] S03: ARI Surface Rewrite — workspace/create→agent/create→agent/prompt lifecycle
  - [ ] S04: CLI + workspace-mcp-server + Design Docs
  - [ ] S05: Integration Tests + Final Verification
