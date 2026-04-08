---
estimated_steps: 26
estimated_files: 1
skills_used: []
---

# T03: Build room-mcp-server stdio binary with room_send and room_status tools

Create `cmd/room-mcp-server/main.go` — a minimal MCP server over stdio that exposes two tools: `room_send` (routes a message to a target agent) and `room_status` (returns current room membership). The binary reads MCP JSON-RPC from stdin, writes responses to stdout, and communicates with agentd via the ARI Unix socket (provided as OAR_AGENTD_SOCKET env var).

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

## Inputs

- ``pkg/ari/types.go` — RoomSendParams, RoomSendResult, RoomStatusParams, RoomStatusResult types for ARI call payloads`
- ``pkg/ari/server_test.go` — dial pattern for connecting to ARI Unix socket via sourcegraph/jsonrpc2`

## Expected Output

- ``cmd/room-mcp-server/main.go` — complete MCP stdio server binary with room_send and room_status tools`

## Verification

go build ./cmd/room-mcp-server && go vet ./cmd/room-mcp-server
