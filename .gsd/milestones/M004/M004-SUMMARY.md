---
id: M004
title: "Realized Room Runtime and Routing"
status: complete
completed_at: 2026-04-08T08:11:59.565Z
key_decisions:
  - D054: Communication vocabulary — mesh/star/isolated replaces broadcast/direct/hub (still valid)
  - D055: Hand-rolled minimal MCP for room-mcp-server — revisit if tool count grows (still valid)
  - D056: Simple blocking semantics for room/send with 120s timeout — revisit for L3/broadcast (still valid)
  - D057: Text-prefix attribution [room:X from:Y] — revisit for structured metadata in future (still valid)
  - D058: Shared deliverPrompt helper for session/prompt and room/send — canonical delivery path (still valid)
  - D051: Room-existence validation enforced in session/new (still valid)
key_files:
  - pkg/ari/server.go — room/create, room/status, room/delete, room/send handlers + deliverPrompt helper
  - pkg/ari/types.go — RoomCreateParams/Result, RoomStatusParams/Result, RoomMember, RoomDeleteParams, RoomSendParams/Result
  - pkg/ari/server_test.go — 47 integration tests including 5 room lifecycle + 12 routing + 2 capstone
  - pkg/meta/models.go — CommunicationMode mesh/star/isolated constants
  - pkg/meta/room.go — CreateRoom with mesh default
  - pkg/meta/schema.sql — rooms table DDL with mesh default
  - pkg/spec/types.go — McpServer stdio fields (Name, Command, Args, Env) + EnvVar type
  - pkg/runtime/runtime.go — convertMcpServers stdio branch
  - pkg/agentd/process.go — generateConfig room MCP injection + resolveRoomMCPBinary
  - cmd/room-mcp-server/main.go — MCP stdio server with room_send and room_status tools
lessons_learned:
  - Extracting shared helpers (deliverPrompt) early pays off — room/send reused the exact same auto-start→connect→prompt flow without duplication
  - Hand-rolling small protocol surfaces (~300 lines) can be simpler than adding SDK dependencies when the surface is tiny (3 MCP methods, 2 tools)
  - Multi-step integration tests that build up state incrementally with verification at each step catch ordering bugs that unit tests miss
  - Teardown guard tests (attempting operations in wrong order) are a valuable complement to happy-path tests — they prove composition under adversarial conditions
  - The 3-tier binary resolution pattern (env → ./bin → PATH) provides good flexibility for testing and deployment without complex configuration
---

# M004: Realized Room Runtime and Routing

**Turned the Room from a design-only contract into a working runtime with ARI-managed lifecycle, point-to-point message routing via room/send and room-mcp-server, and end-to-end multi-agent integration proof across 3 agents.**

## What Happened

M004 delivered the complete Room runtime layer in three slices totaling 9 tasks.

**S01 — Room Lifecycle and ARI Surface (3 tasks).** Converged the communication vocabulary from legacy broadcast/direct/hub to mesh/star/isolated (D054). Added room/create, room/status, room/delete ARI JSON-RPC handlers with 7 new types in pkg/ari/types.go. Implemented room-existence validation in session/new (D051) and active-member guards preventing room deletion while sessions are running. 5 integration tests prove the full lifecycle: create→members→status→delete.

**S02 — Routing Engine and MCP Tool Injection (4 tasks).** Built the orchestrator-driven path (room/send ARI handler) and the agent-driven path (room-mcp-server MCP stdio binary). Extracted the deliverPrompt helper (D058) so both session/prompt and room/send share identical auto-start→connect→prompt flow. Message attribution uses text-prefix format `[room:X from:Y]` (D057). The room-mcp-server binary was hand-rolled (~300 lines) rather than adding mcp-go dependency (D055). Extended spec.McpServer with stdio transport fields and added automatic MCP injection in generateConfig for room sessions. 12 integration tests including 2 full-stack tests with real mockagent processes.

**S03 — End-to-End Multi-Agent Integration Proof (2 tasks).** The capstone: TestARIMultiAgentRoundTrip creates a Room, bootstraps 3 agents, exercises bidirectional messaging (A→B, B→A, A→C) with auto-start verification and state transition checks at each step, then performs clean teardown. TestARIRoomTeardownGuards proves active-member guards and session delete protection compose correctly under adversarial ordering. Both tests pass deterministically as part of the 47-test ARI suite.

The milestone vision — "Turn the Room from a design-only contract into a working runtime" — is fully realized. Orchestrators can create Rooms, attach member sessions, and agents can exchange point-to-point messages through agentd-mediated routing.

## Success Criteria Results

- [x] **S01 demo: Room lifecycle via ARI** — Orchestrator can create a Room, create 2 member sessions pointing at it, query room/status to see both members, stop sessions, and delete the Room. *Evidence: TestARIRoomLifecycle (S01) and TestARIMultiAgentRoundTrip (S03) prove this exact flow.*
- [x] **S02 demo: Point-to-point routing** — Agent A calls room_send MCP tool → agentd resolves target → target agent receives prompt with sender attribution. *Evidence: TestARIRoomSendDelivery proves delivery with real mockagent processes. room-mcp-server binary compiles and passes vet. TestGenerateConfigWithRoomMCPInjection proves MCP injection.*
- [x] **S03 demo: Full round-trip integration** — Room create → member bootstrap → bidirectional message exchange → Room teardown. All via ARI. *Evidence: TestARIMultiAgentRoundTrip (13-step, 3-agent, bidirectional A↔B + A→C) proves the complete flow.*
- [x] **Communication vocabulary converged** — mesh/star/isolated replaces broadcast/direct/hub. *Evidence: 8 meta tests + TestARIRoomCommunicationModes all use new vocabulary.*
- [x] **Room-existence validation enforced** — session/new rejects nonexistent room references. *Evidence: TestARISessionNewRoomValidation (S01).*
- [x] **Active-member guard** — room/delete refuses deletion with active members. *Evidence: TestARIRoomDeleteWithActiveMembers (S01) + TestARIRoomTeardownGuards (S03).*
- [x] **Full test suite passes** — `go build ./...` exit 0, `go test ./pkg/ari/ -short` all 47 tests pass, `go test ./pkg/meta/ -run TestRoom` 8/8 pass.

## Definition of Done Results

- [x] **S01 complete** — All 3 tasks done (T01 vocabulary, T02 handlers, T03 tests). Summary and UAT exist.
- [x] **S02 complete** — All 4 tasks done (T01 spec/injection, T02 room/send handler, T03 room-mcp-server, T04 integration tests). Summary and UAT exist.
- [x] **S03 complete** — Both tasks done (T01 round-trip test, T02 teardown guards test). Summary and UAT exist.
- [x] **All slice summaries exist** — S01-SUMMARY.md, S02-SUMMARY.md, S03-SUMMARY.md all present on disk.
- [x] **Cross-slice integration verified** — S03 composes S01+S02 end-to-end; no boundary mismatches detected.
- [x] **No regressions** — Full ARI test suite (47 tests) passes with -short flag.

## Requirement Outcomes

**R041 (differentiator): active → validated**
- R041 required "a realized Room runtime with explicit ownership, routing, and delivery semantics."
- M004 delivers: room/create, room/status, room/delete ARI handlers (ownership); room/send with target resolution and sender attribution (routing); deliverPrompt helper with auto-start semantics (delivery).
- Evidence: TestARIMultiAgentRoundTrip proves end-to-end Room runtime with 3-agent bidirectional messaging.
- Transition: active → validated (fully realized).

## Deviations

Minor deviations only: T01/S01 also updated pkg/meta/session_test.go (2 references) not in original plan. S03/T01 skipped testing.Short() guard since no other tests in the file use it. No significant scope changes — all 9 tasks completed as planned across 3 slices.

## Follow-ups

- Per-session prompt mutex for busy-target detection needed when L3/broadcast is implemented
- Structured message attribution (separate metadata fields) may be needed for message threading
- Consider adopting mcp-go SDK if room-mcp-server tool count grows beyond 2-3 tools
- Only point-to-point routing implemented — broadcast/star/isolated mode enforcement deferred
- room-mcp-server creates short-lived ARI connections per tool call (acceptable for L2 scale, revisit for L3)
