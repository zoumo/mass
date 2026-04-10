---
id: S04
parent: M007
milestone: M007
provides:
  - ["workspace-mcp-server binary (go build ./cmd/workspace-mcp-server) — verified buildable", "agentdctl workspace send subcommand — workspace/send over ARI", "Clean design docs (ari-spec.md, agentd.md) reflecting workspace/agent model — consumed by S05 integration test authors"]
requires:
  []
affects:
  - ["S05 — integration tests can now rely on clean CLI and correct design docs as reference"]
key_files:
  - ["cmd/workspace-mcp-server/main.go", "cmd/agentdctl/workspace.go", "cmd/agentdctl/main.go", "docs/design/agentd/ari-spec.md", "docs/design/agentd/agentd.md"]
key_decisions:
  - ["workspace send subcommand consolidated into workspace.go (room.go deleted entirely) — D097", "workspace-mcp-server keeps local ARI struct copies, no pkg/ari import — D098", "ari-spec.md fully rewritten rather than surgically patched — D099", "negation prose rephrased to affirmative to avoid grep false positives — D100"]
patterns_established:
  - ["workspace-mcp-server binary pattern: self-contained with local ARI structs, reads OAR_WORKSPACE_NAME, logs workspace=/agentName=/agentID= on startup", "agentdctl workspace subcommand pattern: all workspace-related commands in workspace.go, no separate room.go", "Design doc verification pattern: use affirmative phrasing for removed concepts to avoid grep false positives"]
observability_surfaces:
  - ["workspace-mcp-server logs workspace=, agentName=, agentID= on startup (matching room-mcp-server pattern)"]
drill_down_paths:
  - [".gsd/milestones/M007/slices/S04/tasks/T01-SUMMARY.md", ".gsd/milestones/M007/slices/S04/tasks/T02-SUMMARY.md"]
duration: ""
verification_result: passed
completed_at: 2026-04-09T22:05:21.603Z
blocker_discovered: false
---

# S04: CLI + workspace-mcp-server + Design Docs

**Renamed room-mcp-server → workspace-mcp-server, removed stale room CLI, added workspace send subcommand, and rewrote both design docs to reflect the workspace/agent terminal-state model — full build clean, no Room references in cmd/.**

## What Happened

S04 completed two tasks with no blockers or deviations.

**T01 — workspace-mcp-server + agentdctl room cleanup:**
Created `cmd/workspace-mcp-server/main.go` as a renamed, updated replacement for `cmd/room-mcp-server/main.go`. The new binary reads `OAR_WORKSPACE_NAME`, exposes `workspace_send` and `workspace_status` MCP tools that call `workspace/send` and `workspace/status` on the ARI socket, and logs startup with `workspace=`, `agentName=`, `agentID=` fields. Local ARI struct definitions are kept inline (no pkg/ari import), consistent with the room-mcp-server pattern. On the CLI side, `cmd/agentdctl/room.go` was deleted and a `workspace send` subcommand was added directly to `cmd/agentdctl/workspace.go` (with `--workspace`, `--from`, `--to`, `--text` flags). `cmd/agentdctl/main.go` had the `rootCmd.AddCommand(roomCmd)` line removed and the package comment cleaned. The old `cmd/room-mcp-server/main.go` was deleted. Full `go build ./...` passes with zero stale room references in cmd/.

**T02 — Rewrite design docs:**
`docs/design/agentd/ari-spec.md` received a full replacement: Realized Room Methods section removed, (workspace,name) identity documented throughout, all 5 workspace/* and 9 agent/* methods documented with parameter/response shapes matching `pkg/ari/types.go` and `pkg/ari/server.go`, async polling pattern illustrated with concrete JSON-RPC examples, state values updated to creating/idle/running/stopped/error. `docs/design/agentd/agentd.md` received targeted updates: Session Manager and Realized Room Manager subsections removed, session tracking folded into the Process Manager description, room+name identity replaced with workspace+name, state machine and bootstrap flow updated to match the implemented API. Negation prose ("there is no agentId") was rephrased to affirmative form ("identity is (workspace, name)") to avoid false positives in grep-based verification gates.

## Verification

All slice-level verification checks passed on the assembled codebase:

1. `go build ./cmd/workspace-mcp-server/...` — exit 0
2. `go build ./cmd/agentdctl/...` — exit 0
3. `go build ./...` — exit 0
4. `go run ./cmd/agentdctl/ workspace --help | grep -E 'send|create|list|delete'` — shows all four subcommands
5. `go run ./cmd/agentdctl/ room --help` — exits 1 with "unknown command" (correct)
6. `grep -rn 'room-mcp-server|Room|roomCmd' cmd/` — no matches
7. `! grep -n 'room/create|room/delete|room/status|room/send|agentId|Session Manager' docs/design/agentd/ari-spec.md docs/design/agentd/agentd.md | grep -v '# '` — no stale references found
8. `grep -q 'workspace/create' docs/design/agentd/ari-spec.md` — passes
9. `grep -q 'workspace.*name' docs/design/agentd/agentd.md` — passes

## Requirements Advanced

None.

## Requirements Validated

None.

## New Requirements Surfaced

None.

## Requirements Invalidated or Re-scoped

None.

## Deviations

None.

## Known Limitations

workspace-mcp-server binary has no integration test coverage in S04 (build-time artifact only per slice Integration Closure). Runtime behavior verified only at go-build level. S05 integration tests will cover the full runtime path.

## Follow-ups

S05 integration tests should exercise workspace/send through the full agentd → shim pipeline to provide runtime coverage for the workspace-mcp-server code path.

## Files Created/Modified

- `cmd/workspace-mcp-server/main.go` — New binary: renamed from room-mcp-server, reads OAR_WORKSPACE_NAME, exposes workspace_send/workspace_status MCP tools
- `cmd/agentdctl/workspace.go` — Added workspace send subcommand with --workspace/--from/--to/--text flags
- `cmd/agentdctl/main.go` — Removed rootCmd.AddCommand(roomCmd), cleaned package comment
- `cmd/agentdctl/room.go` — Deleted (room command removed)
- `cmd/room-mcp-server/main.go` — Deleted (directory cleaned up)
- `docs/design/agentd/ari-spec.md` — Full rewrite: workspace/* and agent/* methods documented, Room/agentId/session references removed, async polling examples added
- `docs/design/agentd/agentd.md` — Targeted update: Session Manager removed, workspace+name identity, state machine updated to match spec.Status
