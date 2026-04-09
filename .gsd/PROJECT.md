# Project

## What This Is

Open Agent Runtime (OAR) is a layered runtime for headless coding agents. It manages ACP-speaking agent processes through `agent-shim`, a higher-level daemon in `agentd`, and a future orchestrator layer for multi-agent coordination.

## Core Value

Reliable, observable agent execution with truthful lifecycle and recovery semantics.

## Current State

### Active Milestone: M006 — Fix golangci-lint v2 issues

| Slice | Title | Status |
|-------|-------|--------|
| S01 | Auto-fix: gci + gofumpt formatting (56 issues) | ✅ complete |
| S02 | Auto-fix: unconvert + copyloopvar + ineffassign (24 issues) | ✅ complete |
| S03 | Manual: misspell + unparam (17 issues) | ✅ complete |
| S04 | Manual: unused dead code (12 issues) | ✅ complete |
| S05 | Manual: errorlint — type assertions on errors (17 issues) | ✅ complete |
| S06 | Manual: gocritic (45 issues) | ✅ complete |
| S07 | Manual: testifylint (31 issues) | ⬜ next |

**Current lint posture:** gci, gofumpt, unconvert, copyloopvar, ineffassign, misspell, unparam, unused, errorlint, and gocritic are all clean (zero findings). S07 (testifylint — 5 remaining findings) is the final slice.

### Lint Status (M006)

| Linter | Findings at M006 start | Current |
|--------|----------------------|---------|
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
| testifylint | 31 | ⬜ 5 remain (S07) |

### Completed Milestones

**M001–M005** — Core runtime, contract convergence, recovery hardening, room runtime, agent model refactoring. All complete. See git history for details.

### What's Implemented

- `agent-shim` starts ACP agent processes, performs the ACP handshake, exposes `session/*` + `runtime/*` shim RPC surface
- `agentd` manages agents (external, room+name identity) and sessions (internal runtime instances), async lifecycle, fail-closed recovery
- **Agent-centric ARI surface**: `agent/create`, `agent/prompt`, `agent/cancel`, `agent/stop`, `agent/delete`, `agent/restart`, `agent/list`, `agent/status`, `agent/attach`, `agent/detach`
- **Turn-aware event ordering**: TurnId/StreamSeq/Phase on session/update envelopes
- **Room runtime**: mesh/star/isolated modes, room/send, room-mcp-server (SDK-based)
- **Workspace preparation** for Git/EmptyDir/Local sources with hooks and reference tracking
- **CLI tooling** (`agentdctl`) with agent/workspace/daemon subcommands
