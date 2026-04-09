# S06: Room & MCP Agent Alignment

**Goal:** Align room/status and room/send to use the agents table (not sessions), update RoomMember to expose AgentState/Description/RuntimeClass, ensure room/send updates agent state to running after delivery, and rewrite room-mcp-server using modelcontextprotocol/go-sdk with updated env vars.
**Demo:** After this: TBD

## Tasks
- [x] **T01: Replaced session-table lookups in room/status and room/send with agents-table; RoomMember now exposes AgentState/Description/RuntimeClass; room/send sets agent state to running after delivery** — Three coordinated changes in pkg/ari/:

1. **pkg/ari/types.go** — Update RoomMember struct:
   - Remove `SessionId string` field
   - Remove `State string` field
   - Add `Description string` with `json:"description,omitempty"`
   - Add `RuntimeClass string` with `json:"runtimeClass"`
   - Add `AgentState string` with `json:"agentState"`

2. **pkg/ari/server.go** — Update handleRoomStatus:
   - Replace `h.srv.store.ListSessions(ctx, &meta.SessionFilter{Room: p.Name})` with `h.srv.agents.List(ctx, &meta.AgentFilter{Room: p.Name})`
   - Update comment block above the function to describe agents-table lookup
   - Replace the members loop: for each agent, build RoomMember{AgentName: a.Name, Description: a.Description, RuntimeClass: a.RuntimeClass, AgentState: string(a.State)}
   - Remove the `sessions` variable and any `meta.SessionFilter` imports that become unused

3. **pkg/ari/server.go** — Update handleRoomSend:
   - Remove the ListSessions call for the stopped guard (lines ~1810-1835): instead use `agent.State == meta.AgentStateStopped` directly (agent is already fetched via GetAgentByRoomName)
   - Also guard on `agent.State == meta.AgentStateCreating` (mirrors handleAgentPrompt guard)
   - Remove `targetState meta.SessionState` variable and the ListSessions for session state lookup
   - Keep the ListSessions call that finds `targetSessionID` (still needed to get session ID for deliverPrompt)
   - After successful `h.deliverPrompt(ctx, targetSessionID, attributedMsg)`, add: `if updateErr := h.srv.agents.UpdateState(ctx, agent.ID, meta.AgentStateRunning, ""); updateErr != nil { log.Printf("ari: room/send: failed to update agent state: %v", updateErr) }`
   - Update the function comment block to reflect the new flow

4. **pkg/ari/server_test.go** — Update assertions:
   - In TestARIRoomLifecycle (around line 1803): change `memberMap["agent-a"].State` → `memberMap["agent-a"].AgentState`; update expected values from session states to agent states ("created" or "running" is still valid for AgentState); remove any `SessionId` assertions
   - In TestARIRoomSendDelivery (around line 2155): change `memberMap["agent-b"].State` → `memberMap["agent-b"].AgentState`; update comment from "session state" to "agent state"
   - In all other test locations that use RoomMember.State or .SessionId: update to AgentState; verify these compile
   - Search for all uses: `rg 'RoomMember|memberMap\[' pkg/ari/server_test.go` to find all callsites
  - Estimate: 2-3 hours
  - Files: pkg/ari/types.go, pkg/ari/server.go, pkg/ari/server_test.go
  - Verify: go test ./pkg/ari/... -count=1 -timeout 120s
go test ./pkg/ari/... -count=1 -run TestARIRoomLifecycle -v
go test ./pkg/ari/... -count=1 -run TestARIRoomSendDelivery -v
go build ./...
- [x] **T02: Rewrote room-mcp-server using modelcontextprotocol/go-sdk (StdioTransport + server.AddTool); removed deprecated OAR_SESSION_ID and OAR_ROOM_AGENT env var injections; config now uses agentID/agentName** — Four coordinated changes across three files:

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
  - Estimate: 3-4 hours
  - Files: go.mod, go.sum, cmd/room-mcp-server/main.go, pkg/agentd/process.go, pkg/agentd/process_test.go
  - Verify: go build ./cmd/room-mcp-server
go test ./pkg/agentd/... -count=1 -run TestGenerateConfigWithRoomMCPInjection -v
go test ./pkg/agentd/... -count=1 -timeout 120s
go test ./... -count=1 -timeout 120s
