# S02: Routing Engine and MCP Tool Injection

**Goal:** Agent A calls room_send MCP tool → agentd resolves target → target agent receives prompt with sender attribution. Both orchestrator-driven (room/send ARI) and agent-driven (MCP tool → room/send) paths are functional.
**Demo:** After this: Agent A calls room_send MCP tool → agentd resolves target → target agent receives prompt with sender attribution.

## Tasks
- [x] **T01: Added stdio transport support to spec.McpServer, extended convertMcpServers for stdio→acp mapping, and injected room MCP server in generateConfig for room sessions** — Add stdio transport support to spec.McpServer (Command, Args, Env fields), add spec.EnvVar type, extend convertMcpServers in pkg/runtime/runtime.go for the stdio case (mapping to acp.McpServerStdio), and modify ProcessManager.generateConfig to inject a room MCP server when the session has a non-empty Room field.

## Steps

1. In `pkg/spec/types.go`, add fields to `McpServer` struct: `Name string`, `Command string`, `Args []string`, `Env []EnvVar`. Add `EnvVar` type with `Name` and `Value` string fields. Keep existing `Type` and `URL` fields.

2. In `pkg/runtime/runtime.go`, extend `convertMcpServers` to handle `case "stdio":` — map `spec.McpServer` to `acp.McpServer{Stdio: &acp.McpServerStdio{Name, Command, Args, Env}}`. The `acp.McpServerStdio` and `acp.EnvVariable` types already exist in the ACP SDK.

3. In `pkg/runtime/client_test.go`, add `TestConvertMcpServers_StdioBranch` test: create a `spec.McpServer{Type: "stdio", Name: "room-tools", Command: "/usr/bin/room-mcp-server", Args: []string{}, Env: []spec.EnvVar{{Name: "FOO", Value: "bar"}}}`, convert, assert `result[0].Stdio` is non-nil with correct Name/Command/Args/Env, and Http/Sse are nil.

4. In `pkg/agentd/process.go`, modify `generateConfig` to inject MCP server when `session.Room != ""`. The injection adds one `spec.McpServer` with Type=stdio, Name="room-tools", Command resolved via the same pattern as shimBinary (OAR_ROOM_MCP_BINARY env → ./bin/room-mcp-server → PATH lookup), and Env containing OAR_AGENTD_SOCKET (m.config.Socket), OAR_ROOM_NAME (session.Room), OAR_SESSION_ID (session.ID), OAR_ROOM_AGENT (session.RoomAgent).

5. In `pkg/agentd/process_test.go`, add `TestGenerateConfigWithRoomMCPInjection` — create a ProcessManager, create a Session with Room/RoomAgent set, call generateConfig, assert config.AcpAgent.Session.McpServers has length 1 with correct Type/Name/Env values. Also test that sessions WITHOUT a Room have empty McpServers.

## Failure Modes

| Dependency | On error | On timeout | On malformed response |
|------------|----------|-----------|----------------------|
| room-mcp-server binary not found at runtime | generateConfig still produces config — binary resolution failure surfaces at shim spawn time, not config generation | N/A | N/A |

## Negative Tests

- Session without Room field → McpServers remains empty
- Session with Room but empty RoomAgent → still injects (RoomAgent can be empty string in env var — validation is at session/new level)
- convertMcpServers with unknown type → falls through to default (existing http branch behavior)
  - Estimate: 45m
  - Files: pkg/spec/types.go, pkg/runtime/runtime.go, pkg/runtime/client_test.go, pkg/agentd/process.go, pkg/agentd/process_test.go
  - Verify: go test ./pkg/runtime/ -count=1 -run TestConvertMcpServers && go test ./pkg/agentd/ -count=1 -run TestGenerateConfig && go build ./...
- [x] **T02: Added room/send ARI handler that resolves targetAgent→session within a room, formats attributed messages, and delivers via shared deliverPrompt helper; includes 8 integration test cases covering happy path and all error paths** — Add the `room/send` JSON-RPC method to the ARI server. This handler resolves targetAgent → sessionId within a room, formats the message with sender attribution, and delivers via the existing session/prompt path. Includes integration tests using newTestHarness (DB-only, mock process manager).

## Steps

1. In `pkg/ari/types.go`, add two types:
   - `RoomSendParams` with fields: Room (string), TargetAgent (string), Message (string), SenderAgent (string), SenderId (string)
   - `RoomSendResult` with fields: Delivered (bool), StopReason (string, omitempty)

2. In `pkg/ari/server.go`, implement `handleRoomSend`:
   a. Unmarshal RoomSendParams, validate required fields (room, targetAgent, message)
   b. Call `store.GetRoom` — return InvalidParams if room not found
   c. Call `store.ListSessions` with Room filter — find session where RoomAgent == targetAgent
   d. If no matching session: return InvalidParams "target agent X not found in room Y"
   e. If target session state is "stopped": return InvalidParams "target agent X is stopped"
   f. Format attributed message: `[room:<roomName> from:<senderAgent>] <message>`
   g. Internally call handleSessionPrompt logic: auto-start if created, connect to shim, call client.Prompt with attributed message, return result
   h. Return RoomSendResult{Delivered: true, StopReason: result.StopReason}
   Note: Rather than calling handleSessionPrompt directly (which expects jsonrpc2 request/reply), extract the prompt delivery logic into a helper method `deliverPrompt(ctx, sessionID, text) (stopReason string, err error)` that both handleSessionPrompt and handleRoomSend can call.

3. In `pkg/ari/server.go`, register `room/send` in the Handle method's switch statement, between room/status and room/delete.

4. In `pkg/ari/server_test.go`, add integration tests:
   - `TestARIRoomSendBasic`: Create room → create 2 sessions (agent-a, agent-b) with newSessionTestHarness → prompt agent-b via room/send from agent-a → verify delivery (RoomSendResult.Delivered==true). This tests the happy path with auto-start.
   - `TestARIRoomSendErrors`: Test error cases: (1) room not found, (2) target agent not in room, (3) missing required fields. Use newTestHarness (DB-only, no real shim needed for error path tests).
   - Add helper function `roomSend(ctx, t, conn, room, targetAgent, message, senderAgent, senderId)` following the pattern of existing roomCreate/roomStatus/roomDelete helpers.

## Failure Modes

| Dependency | On error | On timeout | On malformed response |
|------------|----------|-----------|----------------------|
| Target session prompt | Return InternalError "prompt failed: <err>" | 120s timeout → return InternalError "prompt failed: context deadline exceeded" | N/A (prompt returns stopReason string) |
| Meta store (GetRoom, ListSessions) | Return InternalError with store error message | N/A (SQLite, local) | N/A |
| ProcessManager.Connect | Return InternalError "connect to session failed" | 5s connect timeout | N/A |

## Negative Tests

- Room not found → InvalidParams error
- Target agent not in room → InvalidParams error
- Target agent stopped → InvalidParams error  
- Missing room name → InvalidParams error
- Missing targetAgent → InvalidParams error
- Missing message → InvalidParams error
  - Estimate: 1h30m
  - Files: pkg/ari/types.go, pkg/ari/server.go, pkg/ari/server_test.go
  - Verify: go test ./pkg/ari/ -count=1 -v -run 'TestARIRoomSend' && go build ./...
- [x] **T03: Created cmd/room-mcp-server/main.go — a minimal MCP stdio server exposing room_send and room_status tools that connect to agentd via ARI Unix socket** — Create `cmd/room-mcp-server/main.go` — a minimal MCP server over stdio that exposes two tools: `room_send` (routes a message to a target agent) and `room_status` (returns current room membership). The binary reads MCP JSON-RPC from stdin, writes responses to stdout, and communicates with agentd via the ARI Unix socket (provided as OAR_AGENTD_SOCKET env var).

The MCP protocol surface is tiny (initialize, tools/list, tools/call), so hand-roll a minimal JSON-RPC 2.0 implementation rather than adding a heavy external dependency. The existing `sourcegraph/jsonrpc2` library is already in the project for the agentd ARI client side and can be used for the outbound connection to agentd.

## Steps

1. Create `cmd/room-mcp-server/main.go` with a main() function that:
   a. Reads required env vars: OAR_AGENTD_SOCKET, OAR_ROOM_NAME, OAR_SESSION_ID, OAR_ROOM_AGENT
   b. Sets up a JSON-RPC 2.0 reader/writer over stdin/stdout
   c. Implements the MCP protocol handshake:
      - On `initialize` request: respond with server info and capabilities (tools capability)
      - On `notifications/initialized`: acknowledge (no-op notification)
   d. Implements `tools/list`: return two tools:
      - `room_send` with inputSchema {targetAgent: string, message: string}
      - `room_status` with no required inputs
   e. Implements `tools/call`:
      - For `room_send`: connect to agentd socket, call `room/send` ARI method with Room=OAR_ROOM_NAME, TargetAgent=params.targetAgent, Message=params.message, SenderAgent=OAR_ROOM_AGENT, SenderId=OAR_SESSION_ID. Return the result as MCP text content.
      - For `room_status`: connect to agentd socket, call `room/status` ARI method with Name=OAR_ROOM_NAME. Return member list as formatted MCP text content.

2. For the agentd ARI client connection, use `sourcegraph/jsonrpc2` with a Unix socket net.Dial, matching the pattern used in ARI server tests (h.dial in server_test.go).

3. The MCP JSON-RPC over stdio uses bare JSON-RPC 2.0 messages (one JSON object per line, newline-delimited). Implement a simple scanner that reads JSON objects from stdin and writes JSON objects to stdout.

4. Add a build verification: `go build ./cmd/room-mcp-server` compiles cleanly.

## Must-Haves

- Binary compiles with `go build ./cmd/room-mcp-server`
- MCP initialize handshake responds with tool capability
- tools/list returns room_send and room_status with correct schemas
- tools/call room_send connects to agentd and calls room/send
- tools/call room_status connects to agentd and calls room/status
- Env vars OAR_AGENTD_SOCKET, OAR_ROOM_NAME, OAR_SESSION_ID, OAR_ROOM_AGENT are read at startup
- Errors from agentd are propagated as MCP tool errors (isError: true)
  - Estimate: 2h
  - Files: cmd/room-mcp-server/main.go
  - Verify: go build ./cmd/room-mcp-server && go vet ./cmd/room-mcp-server
- [x] **T04: Full-stack integration test: room/send message delivery via real sessions** — Add an integration test using `newSessionTestHarness` that proves the complete room/send delivery path: create a room, create two sessions with mockagent runtime, send a message from agent-a to agent-b via room/send, and verify agent-b receives the attributed prompt. Also verify error paths with real sessions.

## Steps

1. In `pkg/ari/server_test.go`, add `TestARIRoomSendDelivery`:
   a. Use `newSessionTestHarness(t)` to get a harness with mockagent support
   b. Create room "routing-test" via room/create
   c. Prepare a workspace
   d. Create session for agent-a (room="routing-test", roomAgent="agent-a")
   e. Create session for agent-b (room="routing-test", roomAgent="agent-b")
   f. Call room/send: room="routing-test", targetAgent="agent-b", message="hello from architect", senderAgent="agent-a"
   g. Assert RoomSendResult.Delivered == true
   h. Assert RoomSendResult.StopReason is non-empty (mockagent returns "end_turn")
   i. Verify via session/status that agent-b's state is now "running" (auto-started by room/send)

2. Add `TestARIRoomSendToStoppedTarget`:
   a. Create room, create 2 sessions, start agent-b, then stop agent-b
   b. Call room/send targeting stopped agent-b
   c. Assert error contains "not running" or "stopped"

3. Run the full ARI test suite to verify no regressions: `go test ./pkg/ari/ -count=1 -short`

## Must-Haves

- TestARIRoomSendDelivery proves end-to-end message routing with real mockagent processes
- TestARIRoomSendToStoppedTarget proves stopped-target error handling
- All pre-existing ARI tests continue to pass
  - Estimate: 1h
  - Files: pkg/ari/server_test.go
  - Verify: go test ./pkg/ari/ -count=1 -v -run 'TestARIRoomSend' -timeout 120s && go test ./pkg/ari/ -count=1 -short
