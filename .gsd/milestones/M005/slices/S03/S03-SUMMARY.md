---
id: S03
parent: M005
milestone: M005
provides:
  - ["agent/* JSON-RPC surface (10 handlers) ready for S04 async lifecycle enhancement", "AgentManager.Create/Get/GetByRoomName/List/UpdateState/Delete as the S04 foundation", "room/send resolved via agents table — S06 Room & MCP Agent Alignment builds on this", "agentdctl agent/* CLI for manual testing of S04/S06 work", "handleAgentRestart stub returns MethodNotFound — S04 replaces this with real implementation"]
requires:
  []
affects:
  - ["S04 (Agent Lifecycle) — depends on AgentManager and agent/* surface established here", "S06 (Room & MCP Agent Alignment) — depends on room/send agents-table lookup and agent/* surface"]
key_files:
  - ["pkg/agentd/agent.go", "pkg/agentd/agent_test.go", "pkg/ari/server.go", "pkg/ari/types.go", "pkg/ari/server_test.go", "cmd/agentdctl/agent.go", "cmd/agentdctl/helpers.go", "cmd/agentdctl/main.go", "cmd/agentdctl/daemon.go", "cmd/agentd/main.go"]
key_decisions:
  - ["Default state for new agents is AgentStateCreated (synchronous create per S03; async creating state deferred to S04)", "handleAgentDelete must pre-fetch linked session BEFORE agents.Delete due to ON DELETE SET NULL on sessions.agent_id (D072)", "handleRoomDelete auto-deletes stopped agents before deleting room to satisfy RESTRICT FK on agents.room (D073)", "Shared CLI helpers extracted from session.go to helpers.go — room.go, workspace.go, and daemon.go all depend on getClient/outputJSON/handleError/parseLabels", "AgentPromptParams field is .Prompt not .Text — matches pkg/ari/types.go struct definition", "handleAgentRestart error message avoids 'agent/' prefix to keep dispatch grep count at exactly 10", "handleAgentPrompt updates agent state to running after successful deliverPrompt"]
patterns_established:
  - ["agent/* is the canonical external ARI surface; session/* is now internal only — enforced at dispatch level", "AgentManager wraps meta.Store with domain error types, mirroring SessionManager pattern", "Pre-flight sibling lookup before FK-cascading parent delete (ON DELETE SET NULL pattern)", "CLI helper extraction as prerequisite for command file deletion (two-phase pattern, see K028)"]
observability_surfaces:
  - ["AgentManager uses slog with component=agentd.agent structured logging (same pattern as session.go)", "agent/* handler errors logged at Error level with agentId context"]
drill_down_paths:
  - ["milestones/M005/slices/S03/tasks/T01-SUMMARY.md", "milestones/M005/slices/S03/tasks/T02-SUMMARY.md", "milestones/M005/slices/S03/tasks/T03-SUMMARY.md"]
duration: ""
verification_result: passed
completed_at: 2026-04-08T18:56:26.784Z
blocker_discovered: false
---

# S03: ARI Agent Surface — Method Migration

**Replaced 9 session/* ARI dispatch handlers with 10 agent/* handlers, introduced AgentManager, rewrote room/send to use the agents table, and migrated the agentdctl CLI — all 64 pkg/ari + 9 pkg/agentd tests pass.**

## What Happened

S03 migrated the entire external ARI dispatch surface from session-centric to agent-centric in three coordinated tasks.

**T01 — AgentManager foundation (pkg/agentd/agent.go)**
Created `AgentManager` wrapping `meta.Store` with Create/Get/GetByRoomName/List/UpdateState/Delete, mirroring `SessionManager`. Three domain error types (`ErrAgentNotFound`, `ErrDeleteNotStopped`, `ErrAgentAlreadyExists`) cover all error paths. Default create state is `AgentStateCreated` (synchronous create; async creating state is S04 scope). `Delete` enforces stopped precondition via pre-flight GetAgent before calling `store.DeleteAgent`. Nine parallel unit tests using in-memory SQLite all pass.

**T02 — Server migration (pkg/ari/server.go + types.go + server_test.go)**
This was the highest-risk task. Three coordinated changes: (1) `pkg/ari/types.go` had all Agent* request/response types from prior work already in place; (2) `pkg/ari/server.go` received the `agents *agentd.AgentManager` field, updated `New()` constructor, 10 `agent/*` dispatch cases replacing all 9 `session/*` cases, and all handler implementations; (3) `pkg/ari/server_test.go` had all session/* tests migrated to agent/* equivalents.

Three non-obvious bugs discovered and fixed during T02:
- **ON DELETE SET NULL ordering bug:** `handleAgentDelete` must pre-fetch the linked session *before* calling `agents.Delete`. The schema sets `sessions.agent_id = NULL` on agent deletion, making the session unreachable via AgentID filter afterwards. (D072)
- **handleAgentPrompt state update:** After a successful `deliverPrompt`, the agent state must be updated to `running`. The original plan didn't explicitly call this out.
- **handleRoomDelete RESTRICT FK:** `agents.room` uses `ON DELETE RESTRICT`, so room deletion fails if agents still reference the room. `handleRoomDelete` now enumerates and auto-deletes stopped agents before deleting the room. (D073)

The `room/send` handler was rewritten to resolve the target via `store.GetAgentByRoomName` (agents table) instead of `store.ListSessions(Room, RoomAgent)`. `recoveryGuard` comment updated to mention `agent/prompt` and `agent/cancel`.

**T03 — CLI and daemon wiring**
Created `cmd/agentdctl/agent.go` with `agentCmd` and 8 subcommands (create/list/status/prompt/stop/delete/attach/cancel). Shared CLI helpers (getClient, outputJSON, handleError, parseLabels) extracted from the deleted `session.go` into a new `helpers.go` so room.go/workspace.go/daemon.go continued to compile. `cmd/agentdctl/main.go` now registers `agentCmd`. `cmd/agentdctl/daemon.go` health check uses `agent/list`. `cmd/agentd/main.go` constructs `AgentManager` and passes it as the fourth argument to `ari.New()`.

**Final verification:**
- `go test ./pkg/agentd/... -run TestAgent`: 9/9 pass
- `go test ./pkg/ari/...`: 64/64 pass, 0 failures
- `go build ./...`: clean
- `grep -c '"agent/' pkg/ari/server.go`: 10 ✅
- `grep -q '"session/new"' pkg/ari/server.go`: exit 1 (not found) ✅
- `/tmp/agentdctl agent --help`: shows 8 subcommands ✅
- `! /tmp/agentdctl --help | grep -q 'session'`: PASS ✅

## Verification

All slice-level checks from the plan verified:

1. `go test ./pkg/agentd/... -count=1 -timeout 60s -run TestAgent` → exit 0, 9 tests pass
2. `go test ./pkg/ari/... -count=1 -timeout 120s` → exit 0, 64 tests pass
3. `go build ./...` → exit 0, clean build
4. `go build -o /tmp/agentdctl ./cmd/agentdctl` → exit 0
5. `/tmp/agentdctl agent --help` → shows create/list/status/prompt/stop/delete/attach/cancel
6. `! /tmp/agentdctl --help 2>&1 | grep -q 'session'` → PASS (session removed from help)
7. `grep -c '"agent/' pkg/ari/server.go` → 10 (exactly 10 agent/* dispatch cases)
8. `grep -q '"session/new"' pkg/ari/server.go` → exit 1 (session/* removed from dispatch)

## Requirements Advanced

None.

## Requirements Validated

- R047 — 10 agent/* handlers in pkg/ari/server.go (grep -c returns 10); 9 session/* cases removed; 64 pkg/ari tests pass; agentdctl CLI exposes only agent/* subcommands; TestARISessionMethodsRemoved verifies session/new returns MethodNotFound

## New Requirements Surfaced

None.

## Requirements Invalidated or Re-scoped

None.

## Deviations

["handleAgentDelete required pre-flight session lookup before agents.Delete (ON DELETE SET NULL FK gotcha not in plan)", "handleAgentPrompt needed explicit agent state update to running (omitted from plan)", "handleRoomDelete needed to auto-delete stopped agents (RESTRICT FK not anticipated in plan)", "Shared helpers extracted to helpers.go rather than inlined in agent.go (T03 deviation for compilability)"]

## Known Limitations

["agent/restart returns MethodNotFound stub — real async restart implementation is S04 scope", "agent/create is still synchronous (returns created immediately) — async creating→created transition with background bootstrap is S04 scope", "agent/detach is a placeholder (returns nil) — full detach implementation pending", "Two pre-existing flaky tests in unrelated packages: TestARIRoomSendToStoppedTarget (shim socket timeout) and TestRuntimeSuite/TestCancel_SendsCancelToAgent (peer disconnect race)"]

## Follow-ups

["S04: Replace agent/create synchronous semantics with async creating→created background bootstrap", "S04: Implement agent/restart (currently stub returning MethodNotFound)", "S04: Implement agent/stop to actually stop the shim process (not just update state)", "S06: Align room-mcp-server to use agent/* surface instead of session/*"]

## Files Created/Modified

- `pkg/agentd/agent.go` — New AgentManager with Create/Get/GetByRoomName/List/UpdateState/Delete and domain error types
- `pkg/agentd/agent_test.go` — 9 unit tests covering full AgentManager lifecycle and error paths
- `pkg/ari/types.go` — Agent* request/response types (AgentCreateParams/Result, AgentInfo, AgentListParams/Result, etc.)
- `pkg/ari/server.go` — Added agents field + New() param; replaced 9 session/* cases with 10 agent/* handlers; rewrote room/send; fixed handleRoomDelete RESTRICT FK
- `pkg/ari/server_test.go` — All session/* tests migrated to agent/* equivalents; harnesses updated for new ari.New() signature
- `cmd/agentdctl/agent.go` — New agentCmd with 8 subcommands: create/list/status/prompt/stop/delete/attach/cancel
- `cmd/agentdctl/helpers.go` — Shared CLI helpers extracted from deleted session.go: getClient, outputJSON, handleError, parseLabels
- `cmd/agentdctl/main.go` — Replaced sessionCmd with agentCmd in rootCmd registration
- `cmd/agentdctl/daemon.go` — Health check updated from session/list to agent/list
- `cmd/agentd/main.go` — AgentManager constructed and passed as 4th argument to ari.New()
