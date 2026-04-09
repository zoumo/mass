---
id: S06
parent: M005
milestone: M005
provides:
  - ["RoomMember struct with agent-aligned fields (AgentState/RuntimeClass/Description) — S07 can assert on these in recovery tests", "room/send running-state update — S07 recovery tests can verify agent state after room-mediated delivery", "Clean env var surface (OAR_AGENT_ID/OAR_AGENT_NAME only) — S07 integration tests get unambiguous env var semantics", "SDK-based room-mcp-server with no deprecated protocol debt — S07 end-to-end proof can treat the MCP server as stable"]
requires:
  - slice: S03
    provides: agent/* ARI handlers, AgentManager, agents table CRUD, GetAgentByRoomName resolution
affects:
  - ["S07 — consumes agent-aligned RoomMember and clean env var surface for recovery and integration proof"]
key_files:
  - ["pkg/ari/types.go", "pkg/ari/server.go", "pkg/ari/server_test.go", "cmd/room-mcp-server/main.go", "go.mod", "go.sum", "pkg/agentd/process.go", "pkg/agentd/process_test.go"]
key_decisions:
  - ["handleRoomSend guards on both AgentStateStopped and AgentStateCreating, mirroring handleAgentPrompt", "room/send calls agents.UpdateState(running) after successful deliverPrompt — agent state becomes canonical post-delivery signal", "handleRoomStatus reads from agents table so Description and RuntimeClass are now surfaced in room/status response", "Used server.AddTool with json.RawMessage InputSchema (not generic AddTool[In,Out]) to preserve existing custom JSON schemas verbatim", "Test fixture required AgentID field because process.go uses session.AgentID (not session.ID) for OAR_AGENT_ID", "go get required before go mod tidy to avoid tidy stripping the entry before imports were compiled"]
patterns_established:
  - ["room/send post-delivery state update: after successful deliverPrompt, call agents.UpdateState(running, '') — agent state is the canonical running signal", "MCP SDK AddTool strategy: use server.AddTool with json.RawMessage InputSchema (via mcp.MustParseJSON) to preserve custom JSON schemas", "go get before go mod tidy: always go get repo@version before go mod tidy — tidy strips uncompiled entries", "Deprecated env var cleanup pattern: add absence assertions to tests before removing injections, ensuring the removal is verifiably correct"]
observability_surfaces:
  - ["room/status now exposes AgentState (from agents table), RuntimeClass, and Description — richer than previous session-state reporting", "room/send logs agents.UpdateState failure at log.Printf level if post-delivery state update fails — non-fatal, observable in daemon logs"]
drill_down_paths:
  []
duration: ""
verification_result: passed
completed_at: 2026-04-08T21:20:44.640Z
blocker_discovered: false
---

# S06: Room & MCP Agent Alignment

**Aligned room/status and room/send to the agents table, updated RoomMember to expose AgentState/Description/RuntimeClass, and rewrote room-mcp-server with modelcontextprotocol/go-sdk; deprecated OAR_SESSION_ID/OAR_ROOM_AGENT fully removed.**

## What Happened

S06 delivered two coordinated changes that close the last agent-model alignment gaps in the ARI room surface and the room-mcp-server binary.

**T01 — Room handlers aligned to agents table (pkg/ari/types.go, server.go, server_test.go)**

The `RoomMember` struct was the last public ARI type still referencing the sessions world: it carried `SessionId` and `State` (session state). T01 replaced those with `Description`, `RuntimeClass`, and `AgentState` — fields that come directly from the agents table row. `handleRoomStatus` was rewritten to call `agents.List(ctx, &meta.AgentFilter{Room: p.Name})` instead of `store.ListSessions`, building one `RoomMember` per agent with stable identity fields. `handleRoomSend` kept its `ListSessions` call only for the `targetSessionID` lookup (still needed for `deliverPrompt`), but dropped the session-state stopped guard in favour of direct agent state checks: `AgentStateStopped` and `AgentStateCreating` (mirroring `handleAgentPrompt`). After successful delivery, `handleRoomSend` now calls `agents.UpdateState(ctx, agent.ID, meta.AgentStateRunning, "")` — making agent state the canonical post-delivery running signal. Five test functions were updated (`.State` → `.AgentState`, removed `.SessionId` assertions). All 12+ ARI tests pass.

**T02 — room-mcp-server SDK rewrite + deprecated env var cleanup (cmd/room-mcp-server/main.go, go.mod, go.sum, pkg/agentd/process.go, pkg/agentd/process_test.go)**

The hand-rolled 497-line MCP JSON-RPC server was fully replaced with `modelcontextprotocol/go-sdk v0.8.0`. The new server uses `mcp.NewServer` + `server.AddTool` (with `json.RawMessage` InputSchema via `mcp.MustParseJSON` to preserve the custom schemas) + `mcp.StdioTransport`. All hand-rolled types (`mcpRequest`, `mcpResponse`, `mcpError`, the `initialize`/`tools/list`/`tools/call` dispatch loop) were deleted. The config struct was updated from `sessionID`/`roomAgent` to `agentID`/`agentName` reading `OAR_AGENT_ID`/`OAR_AGENT_NAME`. The local `ariRoomMember` type was aligned to the new `RoomMember` shape (AgentState/RuntimeClass). `process.go` had the two deprecated env var injections (`OAR_SESSION_ID`, `OAR_ROOM_AGENT`) removed, and `process_test.go` was updated so `TestGenerateConfigWithRoomMCPInjection` verifies their absence and the presence of `OAR_AGENT_ID`/`OAR_AGENT_NAME`. Two notable deviations from the plan: (1) the test fixture needed `AgentID: "sess-123"` because `process.go` reads `session.AgentID` (not `session.ID`) for `OAR_AGENT_ID`; (2) `go get` had to precede `go mod tidy` — tidy run first would strip the uncompiled entry. All agentd tests pass; `go build ./cmd/room-mcp-server` exits 0.

## Verification

All slice-level verification checks passed:
- `go build ./...` → exit 0 (full module build clean)
- `go build ./cmd/room-mcp-server` → exit 0 (SDK binary builds)
- `go test ./pkg/ari/... -count=1 -run TestARIRoomLifecycle -v -timeout 120s` → PASS (0.24s)
- `go test ./pkg/ari/... -count=1 -run TestARIRoomSendDelivery -v -timeout 120s` → PASS (0.25s)
- `go test ./pkg/agentd/... -count=1 -run TestGenerateConfigWithRoomMCPInjection -v` → PASS (3/3 subtests)
- `go test ./pkg/ari/... ./pkg/agentd/... -count=1 -timeout 120s` → pkg/ari ok 12.258s, pkg/agentd ok 6.442s

## Requirements Advanced

None.

## Requirements Validated

- R051 — room-mcp-server rewritten with modelcontextprotocol/go-sdk v0.8.0. OAR_SESSION_ID and OAR_ROOM_AGENT removed from process.go. go build ./cmd/room-mcp-server exits 0. TestGenerateConfigWithRoomMCPInjection (3 subtests) passes. go test ./pkg/agentd/... exits 0.

## New Requirements Surfaced

None.

## Requirements Invalidated or Re-scoped

None.

## Deviations

T02: Test fixture needed AgentID: "sess-123" addition (not explicitly in plan) — process.go reads session.AgentID, not session.ID, for OAR_AGENT_ID. T02: go get required before go mod tidy (tidy ran first in initial bg job, stripped the SDK entry before imports compiled).

## Known Limitations

Phase field on SessionUpdateParams is present but not populated by any code path — reserved for future phase annotation. room-mcp-server creates short-lived ARI connections per tool call (acceptable for current scale). Concurrent room/send to same target agent: no per-agent prompt mutex at this level — documented gap from S04.

## Follow-ups

S07 should verify agent state observable via room/status after a delivery cycle in the recovery integration tests. The Phase field population can be addressed in a future milestone once phase semantics are defined in the shim protocol.

## Files Created/Modified

- `pkg/ari/types.go` — RoomMember struct: removed SessionId/State, added Description/RuntimeClass/AgentState
- `pkg/ari/server.go` — handleRoomStatus queries agents table; handleRoomSend uses agent state guards and calls agents.UpdateState(running) post-delivery
- `pkg/ari/server_test.go` — Updated 5 test functions: .State → .AgentState, removed SessionId assertions
- `cmd/room-mcp-server/main.go` — Full rewrite using modelcontextprotocol/go-sdk v0.8.0 (StdioTransport + server.AddTool); config reads OAR_AGENT_ID/OAR_AGENT_NAME; ariRoomMember uses AgentState/RuntimeClass
- `go.mod` — Added github.com/modelcontextprotocol/go-sdk v0.8.0
- `go.sum` — Updated checksums for go-sdk dependency
- `pkg/agentd/process.go` — Removed OAR_SESSION_ID and OAR_ROOM_AGENT deprecated env var injections
- `pkg/agentd/process_test.go` — TestGenerateConfigWithRoomMCPInjection: added AgentID to fixture; asserts absence of deprecated vars and presence of OAR_AGENT_ID/OAR_AGENT_NAME
