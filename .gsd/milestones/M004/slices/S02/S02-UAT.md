# S02: Routing Engine and MCP Tool Injection — UAT

**Milestone:** M004
**Written:** 2026-04-08T06:03:52.561Z

## UAT: S02 — Routing Engine and MCP Tool Injection

### Preconditions
- `go build ./...` compiles cleanly
- `bin/agent-shim` binary exists (built from cmd/agent-shim)
- `cmd/room-mcp-server` compiles via `go build ./cmd/room-mcp-server`
- SQLite metadata store initializes successfully

---

### Test Case 1: stdio MCP Server Spec Conversion
**Objective:** Verify spec.McpServer with stdio transport converts correctly to acp.McpServerStdio.

1. Run `go test ./pkg/runtime/ -count=1 -v -run TestConvertMcpServers_StdioBranch`
2. **Expected:** Test passes. The converted result has `Stdio` non-nil with correct Name, Command, Args, and Env. `Http` and `Sse` are nil.

### Test Case 2: Room MCP Injection in generateConfig
**Objective:** Verify generateConfig injects room-tools MCP server when session has a Room field.

1. Run `go test ./pkg/agentd/ -count=1 -v -run TestGenerateConfigWithRoomMCPInjection`
2. **Expected:**
   - Subtest "with room": McpServers has length 1, Type="stdio", Name="room-tools", Env contains OAR_AGENTD_SOCKET, OAR_ROOM_NAME, OAR_SESSION_ID, OAR_ROOM_AGENT
   - Subtest "without room": McpServers is empty
   - Subtest "with room empty agent": McpServers has length 1, OAR_ROOM_AGENT env var present with empty value

### Test Case 3: room/send Happy Path with Real Shim
**Objective:** Verify point-to-point message delivery from agent-a to agent-b within a room.

1. Run `go test ./pkg/ari/ -count=1 -v -run TestARIRoomSendBasic`
2. **Expected:**
   - Room "send-room" created successfully
   - Two sessions created with roomAgent "agent-a" and "agent-b"
   - room/send from agent-a→agent-b delivers message
   - RoomSendResult.Delivered == true
   - RoomSendResult.StopReason is non-empty (mockagent returns "end_turn")

### Test Case 4: room/send Error Paths
**Objective:** Verify all 6 error cases return correct InvalidParams errors.

1. Run `go test ./pkg/ari/ -count=1 -v -run TestARIRoomSendErrors`
2. **Expected:**
   - "room not found" → error contains "room not found" or "not found"
   - "target agent not in room" → error contains "not found in room"
   - "target agent stopped" → error contains "is stopped"
   - "missing room" → error contains "room" required field message
   - "missing targetAgent" → error contains "targetAgent" required field message
   - "missing message" → error contains "message" required field message

### Test Case 5: Full-Stack End-to-End Delivery
**Objective:** Verify complete routing path with real mockagent processes, including auto-start.

1. Run `go test ./pkg/ari/ -count=1 -v -run TestARIRoomSendDelivery`
2. **Expected:**
   - Room "routing-test" created
   - Two sessions created for agent-a and agent-b
   - room/send successfully delivers message from agent-a→agent-b
   - Delivered == true, StopReason == "end_turn"
   - session/status for agent-b shows state == "running" (auto-started by deliverPrompt)

### Test Case 6: Stopped Target Rejection with Real Lifecycle
**Objective:** Verify room/send rejects delivery to a stopped agent after full start→stop lifecycle.

1. Run `go test ./pkg/ari/ -count=1 -v -run TestARIRoomSendToStoppedTarget`
2. **Expected:**
   - agent-b started via session/prompt then stopped via session/stop
   - session/status confirms agent-b state == "stopped"
   - room/send targeting stopped agent-b returns error containing "stopped"

### Test Case 7: room-mcp-server Binary Compilation
**Objective:** Verify the MCP server binary compiles and passes static analysis.

1. Run `go build ./cmd/room-mcp-server`
2. Run `go vet ./cmd/room-mcp-server`
3. **Expected:** Both exit 0 with no errors or warnings.

### Test Case 8: Full ARI Suite Regression Check
**Objective:** Verify no regressions in the complete ARI test suite after S02 changes.

1. Run `go test ./pkg/ari/ -count=1 -short -timeout 120s`
2. **Expected:** All tests pass, including pre-existing session/workspace/room tests.

### Test Case 9: session/prompt Still Works After deliverPrompt Refactor
**Objective:** Verify the deliverPrompt extraction did not break the original session/prompt path.

1. Run `go test ./pkg/ari/ -count=1 -v -run TestARISessionPrompt`
2. **Expected:** All session/prompt tests pass unchanged.

---

### Edge Cases Verified by Automated Tests

| Edge Case | Test | Expected Behavior |
|-----------|------|-------------------|
| Session without Room field | TestGenerateConfig/without_room | McpServers remains empty |
| Session with Room but empty RoomAgent | TestGenerateConfig/with_room_empty_agent | MCP server injected, OAR_ROOM_AGENT env var is empty string |
| Unknown MCP server type | TestConvertMcpServers (existing) | Falls through to default http branch |
| room/send to non-existent room | TestARIRoomSendErrors/room_not_found | InvalidParams error |
| room/send to non-member agent | TestARIRoomSendErrors/target_agent_not_in_room | InvalidParams error |
| room/send to stopped agent | TestARIRoomSendErrors/target_agent_stopped + TestARIRoomSendToStoppedTarget | InvalidParams error |
| room/send auto-starts created session | TestARIRoomSendDelivery | Session transitions from created→running |
