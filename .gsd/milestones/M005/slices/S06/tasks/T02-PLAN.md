---
estimated_steps: 58
estimated_files: 5
skills_used: []
---

# T02: Rewrite room-mcp-server with go-sdk + remove deprecated env vars

Four coordinated changes across three files:

1. **go.mod** — Add the SDK dependency:
   - Add `github.com/modelcontextprotocol/go-sdk v0.8.0` to the require block
   - Run `go mod tidy` to update go.sum (SDK is in local module cache at ~/go/pkg/mod/github.com/modelcontextprotocol/go-sdk@v0.8.0/ — no network needed)

2. **cmd/room-mcp-server/main.go** — Full rewrite using SDK:
   
   Keep these unchanged from the original:
   - `callARI` function (uses sourcegraph/jsonrpc2 to dial agentd Unix socket)
   - `nullHandler` type
   - The log-to-file setup in main() for OAR_STATE_DIR
   - The two JSON schema vars: `roomSendSchema` and `roomStatusSchema`
   - The local ARI types: `ariRoomSendParams`, `ariRoomSendResult`, `ariRoomStatusParams`, `ariRoomStatusResult`
   - Update `ariRoomMember` to match new RoomMember shape (remove SessionId/State, add AgentState)

   Update the config struct and loadConfig:
   ```go
   type config struct {
       agentdSocket string // OAR_AGENTD_SOCKET
       roomName     string // OAR_ROOM_NAME
       agentID      string // OAR_AGENT_ID (was sessionID/OAR_SESSION_ID)
       agentName    string // OAR_AGENT_NAME (was roomAgent/OAR_ROOM_AGENT)
   }
   // loadConfig: read OAR_AGENT_ID (required), OAR_AGENT_NAME (optional)
   // Validation: OAR_AGENT_ID required (mirrors old OAR_SESSION_ID requirement)
   ```

   Replace the entire MCP protocol scaffolding with SDK:
   ```go
   import "github.com/modelcontextprotocol/go-sdk/mcp"
   
   // In main():
   server := mcp.NewServer(&mcp.Implementation{Name: "room-mcp-server", Version: "0.1.0"}, nil)
   mcp.AddTool(server, &mcp.Tool{Name: "room_send", Description: "...", InputSchema: roomSendSchemaObj}, roomSendHandler(cfg))
   mcp.AddTool(server, &mcp.Tool{Name: "room_status", Description: "...", InputSchema: roomStatusSchemaObj}, roomStatusHandler(cfg))
   if err := server.Run(context.Background(), &mcp.StdioTransport{}); err != nil {
       log.Printf("Server failed: %v", err)
   }
   ```
   
   Note: mcp.AddTool requires the InputSchema to be an *mcp.JSONSchema (not json.RawMessage). Use `mcp.MustParseJSON[*mcp.JSONSchema](roomSendSchema)` or define the schema as a Go struct. The simplest approach: define `type roomSendInput struct { TargetAgent string; Message string }` and `type roomStatusInput struct {}` and use the generic `mcp.AddTool[roomSendInput, any]` form which auto-derives the schema, OR use `server.AddTool` with a raw ToolHandler if the schema must be custom JSON.
   
   IMPORTANT: Check the SDK's actual AddTool signature carefully. The `AddTool[In, Out]` generic expects `ToolHandlerFor[In, Out]` which has signature `func(ctx, *CallToolRequest, In) (*CallToolResult, Out, error)`. The tool handler needs to produce a `*mcp.CallToolResult` with `Content []mcp.Content` where each item is `&mcp.TextContent{Text: "..."}`. Drop all hand-rolled JSON-RPC types (mcpRequest, mcpResponse, mcpError, etc.) — the SDK handles the protocol layer entirely.

   For SenderAgent in room_send ARI params: use `cfg.agentName` (was `cfg.roomAgent`). For SenderId: use `cfg.agentID` (was `cfg.sessionID`).

   The room_status output format: update the member formatting to use `m.AgentState` instead of `m.State` / `m.SessionId`:
   ```go
   sb.WriteString(fmt.Sprintf("  - %s [%s] state: %s\n", m.AgentName, m.RuntimeClass, m.AgentState))
   ```

3. **pkg/agentd/process.go** — Remove deprecated env vars:
   - Remove the two lines injecting OAR_SESSION_ID and OAR_ROOM_AGENT (lines ~284-285):
     ```go
     // DELETE:
     {Name: "OAR_SESSION_ID", Value: session.ID},         // deprecated
     {Name: "OAR_ROOM_AGENT", Value: session.RoomAgent},   // deprecated
     ```
   - Keep OAR_AGENT_ID and OAR_AGENT_NAME (lines ~282-283) unchanged

4. **pkg/agentd/process_test.go** — Update TestGenerateConfigWithRoomMCPInjection:
   - Remove assertions that check `envMap["OAR_SESSION_ID"] == "sess-123"` (around line 326)
   - Remove assertions that check `envMap["OAR_ROOM_AGENT"]` (around lines 329-368)
   - Add assertions that verify OAR_SESSION_ID and OAR_ROOM_AGENT are ABSENT from the env map
   - Verify OAR_AGENT_ID and OAR_AGENT_NAME are present with correct values

## Inputs

- ``cmd/room-mcp-server/main.go` — current 497-line hand-rolled MCP server (callARI function and ARI types to preserve)`
- ``go.mod` — current module file without modelcontextprotocol/go-sdk`
- ``pkg/agentd/process.go` — generateConfig has deprecated OAR_SESSION_ID and OAR_ROOM_AGENT injections`
- ``pkg/agentd/process_test.go` — TestGenerateConfigWithRoomMCPInjection asserts on OAR_SESSION_ID and OAR_ROOM_AGENT`

## Expected Output

- ``cmd/room-mcp-server/main.go` — SDK-based server using mcp.NewServer/mcp.AddTool/mcp.StdioTransport; config uses agentID/agentName; ariRoomMember uses AgentState`
- ``go.mod` — includes github.com/modelcontextprotocol/go-sdk v0.8.0`
- ``go.sum` — updated by go mod tidy`
- ``pkg/agentd/process.go` — OAR_SESSION_ID and OAR_ROOM_AGENT injections removed`
- ``pkg/agentd/process_test.go` — TestGenerateConfigWithRoomMCPInjection verifies absence of OAR_SESSION_ID and OAR_ROOM_AGENT, presence of OAR_AGENT_ID and OAR_AGENT_NAME`

## Verification

go build ./cmd/room-mcp-server
go test ./pkg/agentd/... -count=1 -run TestGenerateConfigWithRoomMCPInjection -v
go test ./pkg/agentd/... -count=1 -timeout 120s
go test ./... -count=1 -timeout 120s
