---
id: S02
parent: M004
milestone: M004
provides:
  - ["room/send ARI method for orchestrator-driven inter-agent messaging", "deliverPrompt helper reusable by future delivery methods", "room-mcp-server binary for agent-initiated messaging via MCP tools", "stdio transport support in spec.McpServer and convertMcpServers", "Room MCP server injection in generateConfig for room sessions"]
requires:
  []
affects:
  - ["S03 — End-to-End Multi-Agent Integration Proof (uses room/send for bidirectional message exchange)"]
key_files:
  - ["pkg/spec/types.go", "pkg/runtime/runtime.go", "pkg/runtime/client_test.go", "pkg/agentd/process.go", "pkg/agentd/process_test.go", "pkg/ari/types.go", "pkg/ari/server.go", "pkg/ari/server_test.go", "cmd/room-mcp-server/main.go"]
key_decisions:
  - ["D055: Hand-rolled minimal MCP JSON-RPC over stdio for room-mcp-server", "D056: Simple blocking semantics for room/send (120s timeout safety valve)", "D057: Text prefix attribution format [room:X from:Y] message", "D058: Shared deliverPrompt helper for session/prompt and room/send", "D059: Room MCP binary resolution uses 3-tier pattern (env → ./bin → PATH)"]
patterns_established:
  - ["deliverPrompt(ctx, sessionID, text) as the canonical prompt delivery helper — all future delivery paths (broadcast, relay) should use this", "Room MCP injection pattern: generateConfig checks session.Room and injects stdio MCP server with env vars for agentd connection", "Attributed message format: [room:<name> from:<sender>] <message> — agents parse this prefix to identify sender", "Hand-rolled MCP stdio protocol for small tool surfaces — revisit if MCP tool count grows significantly"]
observability_surfaces:
  - ["ari: room/send delivering message from X to Y in room Z (session ID) — logged on every room/send call", "ari: deliverPrompt auto-starting session ID — logged when auto-start triggers", "ari: deliverPrompt completed for session ID, stopReason=X — logged on delivery completion"]
drill_down_paths:
  []
duration: ""
verification_result: passed
completed_at: 2026-04-08T06:03:52.560Z
blocker_discovered: false
---

# S02: Routing Engine and MCP Tool Injection

**Implemented point-to-point message routing between room members via room/send ARI handler and room-mcp-server MCP stdio binary, with deliverPrompt helper, sender attribution, and 12 integration tests covering happy paths and all error cases.**

## What Happened

This slice built the complete routing engine for inter-agent messaging within Rooms, delivering both the orchestrator-driven path (room/send ARI method) and the agent-driven path (room-mcp-server MCP binary injected at session bootstrap).

**T01 — Spec Types and MCP Injection (spec + agentd layers)**
Extended `spec.McpServer` with stdio transport fields (Name, Command, Args, Env) and added `spec.EnvVar` type. Extended `convertMcpServers` in the runtime layer to handle the stdio→acp mapping case. Modified `generateConfig` in the agentd process manager to automatically inject a room-tools MCP server into the session config when the session has a non-empty Room field. The injection passes four env vars (OAR_AGENTD_SOCKET, OAR_ROOM_NAME, OAR_SESSION_ID, OAR_ROOM_AGENT) so the MCP server knows how to connect back to agentd. Binary resolution uses the established 3-tier pattern (env → ./bin → PATH).

**T02 — room/send ARI Handler (ARI layer)**
Added `RoomSendParams` and `RoomSendResult` types. Extracted the `deliverPrompt` helper from `handleSessionPrompt` so both session/prompt and room/send share the same auto-start→connect→prompt flow. Implemented `handleRoomSend` with full validation: room existence check, target agent lookup via ListSessions, stopped-target guard, attributed message formatting `[room:X from:Y] message`, and prompt delivery via the shared helper. Registered as `room/send` in the dispatch switch. Added 8 test cases: 1 happy path with real mockagent + 6 negative subtests (room not found, target not in room, target stopped, missing room, missing targetAgent, missing message) + 1 basic delivery test.

**T03 — room-mcp-server Binary (MCP layer)**
Created `cmd/room-mcp-server/main.go` — a minimal MCP server over stdio (newline-delimited JSON-RPC 2.0). Implements the MCP protocol handshake (initialize/notifications/initialized), tools/list (returns room_send and room_status with JSON schemas), and tools/call (dispatches to handlers that connect to agentd via sourcegraph/jsonrpc2 over Unix socket). Hand-rolled the MCP protocol surface (~300 lines) rather than adding an mcp-go dependency, since only 3 MCP methods and 2 tools are needed. Errors from agentd are propagated as MCP tool results with isError: true.

**T04 — Full-Stack Integration Tests**
Added `TestARIRoomSendDelivery` — end-to-end proof with real mockagent processes: create room, create two sessions, send message from agent-a→agent-b, verify Delivered==true, StopReason=="end_turn", and agent-b auto-started to "running" state. Added `TestARIRoomSendToStoppedTarget` — full lifecycle test: start→stop agent-b, then verify room/send returns "is stopped" error. Both tests exercise the complete routing path with real shim processes, unlike the unit-level error tests in T02.

The full ARI test suite passes with no regressions (all tests in pkg/ari/ pass with -short).

## Verification

All slice verification checks passed:
- `go test ./pkg/runtime/ -count=1 -run TestConvertMcpServers` → PASS (4 tests including new stdio branch)
- `go test ./pkg/agentd/ -count=1 -run TestGenerateConfig` → PASS (3 subtests: room injection, no-room, empty-agent)
- `go test ./pkg/ari/ -count=1 -v -run TestARIRoomSend -timeout 120s` → PASS (4 test functions, 12 total cases)
- `go build ./...` → exit 0 (clean build including cmd/room-mcp-server)
- `go build ./cmd/room-mcp-server && go vet ./cmd/room-mcp-server` → exit 0
- `go test ./pkg/ari/ -count=1 -short -timeout 120s` → PASS (full ARI suite, no regressions)

## Requirements Advanced

None.

## Requirements Validated

None.

## New Requirements Surfaced

None.

## Requirements Invalidated or Re-scoped

None.

## Deviations

None. All 4 tasks completed as planned.

## Known Limitations

["room/send blocks until target agent completes its turn — no busy detection or queuing", "Attribution is text-prefix only — no structured metadata for programmatic parsing by agents", "room-mcp-server creates a new ARI connection per tool call (short-lived, acceptable for L2 scale)", "Only point-to-point routing implemented — broadcast/star/isolated mode enforcement deferred"]

## Follow-ups

["Per-session prompt mutex for busy-target detection needed when L3/broadcast is implemented", "Structured message attribution (separate metadata fields) may be needed for message threading in future milestones", "Consider adopting mcp-go SDK if room-mcp-server tool count grows beyond 2-3 tools"]

## Files Created/Modified

- `pkg/spec/types.go` — Added Name, Command, Args, Env fields to McpServer struct; added EnvVar type
- `pkg/runtime/runtime.go` — Added stdio case to convertMcpServers mapping spec.McpServer to acp.McpServerStdio
- `pkg/runtime/client_test.go` — Added TestConvertMcpServers_StdioBranch test
- `pkg/agentd/process.go` — Added resolveRoomMCPBinary helper; modified generateConfig to inject room MCP server when session.Room is non-empty
- `pkg/agentd/process_test.go` — Added TestGenerateConfigWithRoomMCPInjection with 3 subtests
- `pkg/ari/types.go` — Added RoomSendParams and RoomSendResult types
- `pkg/ari/server.go` — Extracted deliverPrompt helper; implemented handleRoomSend; registered room/send in dispatch
- `pkg/ari/server_test.go` — Added TestARIRoomSendBasic, TestARIRoomSendErrors (6 subtests), TestARIRoomSendDelivery, TestARIRoomSendToStoppedTarget
- `cmd/room-mcp-server/main.go` — Created minimal MCP stdio server with room_send and room_status tools
