# S06 Research: Room & MCP Agent Alignment

## Summary

S06 is **targeted, medium-complexity work** using known patterns. Two parallel deliverables:
1. **room/status + room/send alignment** — switch from sessions table to agents table in `handleRoomStatus`; add `Description`/`RuntimeClass`/`AgentState` to `RoomMember`; remove `SessionId`; update `handleRoomSend` to also update agent state to `running` after delivery (critical correctness fix).
2. **room-mcp-server SDK rewrite** — replace 497-line hand-rolled MCP protocol with `modelcontextprotocol/go-sdk v0.8.0`; switch env vars from `OAR_SESSION_ID`/`OAR_ROOM_AGENT` to `OAR_AGENT_ID`/`OAR_AGENT_NAME`.

No novel architecture. Both deliverables build on patterns already established in S03–S05. SDK is already in the Go module cache at `v0.8.0`.

---

## Requirements Owned

- **R051**: room-mcp-server rewritten with `modelcontextprotocol/go-sdk`. Env vars switch from `OAR_SESSION_ID` to `OAR_AGENT_NAME`/`OAR_AGENT_ID`/`OAR_ROOM_NAME`. Validation: existing room/send tests pass with SDK-based server; env vars use agent identity.

---

## Implementation Landscape

### 1. room/status — Members from Agents Table

**Current behavior** (`handleRoomStatus` in `pkg/ari/server.go`):
- Calls `store.ListSessions(ctx, &meta.SessionFilter{Room: p.Name})` → iterates sessions
- Builds `RoomMember` from: `s.RoomAgent` (agentName), `s.ID` (sessionId), `s.State` (session state)

**Target behavior** (per `docs/design/agentd/ari-spec.md`):
```json
"members": [
  {
    "agentName": "architect",
    "description": "Designs the module structure.",
    "runtimeClass": "claude",
    "agentState": "running"
  }
]
```
- `Internal sessionId is not surfaced.`
- Source of truth: agents table, not sessions table
- Use `h.srv.agents.List(ctx, &meta.AgentFilter{Room: p.Name})` → iterate agents
- `RoomMember.State` → `RoomMember.AgentState` (field rename + value changes from session state to agent state)

**`RoomMember` struct change** (`pkg/ari/types.go`):
```go
// Before
type RoomMember struct {
    AgentName string `json:"agentName"`
    SessionId string `json:"sessionId"`
    State     string `json:"state"`
}

// After
type RoomMember struct {
    AgentName    string `json:"agentName"`
    Description  string `json:"description,omitempty"`
    RuntimeClass string `json:"runtimeClass"`
    AgentState   string `json:"agentState"`
}
```

### 2. handleRoomSend — Agent State Update (Critical)

**Current behavior**: calls `deliverPrompt`, returns `RoomSendResult`. Does NOT update agent state.

**Problem**: `TestARIRoomSendDelivery` asserts `memberMap["agent-b"].State == "running"` via `room/status` after a `room/send`. Once `room/status` switches to agents table (agentState), agent-b's state will still be `"created"` (not updated), causing the test to fail.

**Fix**: Add `h.srv.agents.UpdateState(ctx, agent.ID, meta.AgentStateRunning, "")` in `handleRoomSend` after successful `deliverPrompt`. This mirrors what `handleAgentPrompt` does (line 1164 in server.go). Note: `handleRoomSend` already looks up the `agent` object (via `store.GetAgentByRoomName`) — reuse it for the UpdateState call.

**Stopped guard update**: The current `handleRoomSend` guards on session state `SessionStateStopped`. After alignment, this should guard on `agent.State == meta.AgentStateStopped` (no session lookup needed — agent already fetched). This eliminates the session table lookup for the stopped check.

### 3. room-mcp-server SDK Rewrite

**Current**: `cmd/room-mcp-server/main.go` — 497 lines of hand-rolled MCP JSON-RPC protocol (parse, dispatch, lifecycle management).

**Target**: Rewrite using `github.com/modelcontextprotocol/go-sdk v0.8.0`. Module is cached locally at `~/go/pkg/mod/github.com/modelcontextprotocol/go-sdk@v0.8.0/`.

**SDK Pattern** (from `examples/server/memory/main.go`):
```go
server := mcp.NewServer(&mcp.Implementation{Name: "room-mcp-server", Version: "0.1.0"}, nil)

mcp.AddTool(server, &mcp.Tool{
    Name:        "room_send",
    Description: "Send a message to another agent in the current room",
    InputSchema: roomSendSchema, // json.RawMessage (keep existing schemas)
}, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
    // business logic — callARI remains via sourcegraph/jsonrpc2
    ...
    return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: text}}}, nil
})

// Stdio transport for MCP (stdin/stdout)
if err := server.Run(context.Background(), &mcp.StdioTransport{}); err != nil {
    log.Printf("Server failed: %v", err)
}
```

**Key SDK types**:
- `mcp.NewServer(*Implementation, *ServerOptions)` — creates server
- `mcp.AddTool[In,Out](s, t, h)` — generic typed handler (or `s.AddTool(t, h)` for raw)
- `mcp.CallToolRequest`, `mcp.CallToolResult`, `mcp.TextContent` — request/response
- `mcp.StdioTransport{}` — stdin/stdout transport (replaces entire scanner/encoder loop)
- `server.Run(ctx, transport)` — blocking run loop

**ARI client**: Keep the existing `callARI` function unchanged — it uses `sourcegraph/jsonrpc2` to dial the agentd Unix socket. SDK only replaces the MCP server protocol side.

**Env var changes** (in both `cmd/room-mcp-server/main.go` and `pkg/agentd/process.go`):
| Old | New | Status |
|---|---|---|
| `OAR_SESSION_ID` | `OAR_AGENT_ID` | Remove from process.go injection + remove from room-mcp loadConfig |
| `OAR_ROOM_AGENT` | `OAR_AGENT_NAME` | Remove from process.go injection + remove from room-mcp loadConfig |
| `OAR_ROOM_NAME` | `OAR_ROOM_NAME` | Unchanged |
| `OAR_AGENTD_SOCKET` | `OAR_AGENTD_SOCKET` | Unchanged |

**process.go** (lines 282–285): Remove the two `deprecated` lines:
```go
{Name: "OAR_SESSION_ID", Value: session.ID},        // DELETE
{Name: "OAR_ROOM_AGENT", Value: session.RoomAgent},  // DELETE
```

**Config struct** in new room-mcp-server:
```go
type config struct {
    agentdSocket string // OAR_AGENTD_SOCKET
    roomName     string // OAR_ROOM_NAME
    agentID      string // OAR_AGENT_ID (replaces sessionID)
    agentName    string // OAR_AGENT_NAME (replaces roomAgent)
}
```
`OAR_AGENT_ID` is required (validation mirrors old `OAR_SESSION_ID` requirement).

### 4. Dependency Addition

Add to `go.mod`:
```
require (
    github.com/modelcontextprotocol/go-sdk v0.8.0
    ...
)
```
SDK's deps (`github.com/golang-jwt/jwt/v5`, `github.com/google/jsonschema-go`, `github.com/yosida95/uritemplate/v3`) are already in `go.sum` from prior work. Run `go mod tidy` after adding to clean up.

---

## File-by-File Change Map

| File | Change |
|---|---|
| `cmd/room-mcp-server/main.go` | Full rewrite: SDK-based server, updated config/env vars, keep `callARI` |
| `go.mod` | Add `github.com/modelcontextprotocol/go-sdk v0.8.0` |
| `go.sum` | Auto-updated by `go mod tidy` |
| `pkg/ari/types.go` | `RoomMember`: remove `SessionId`/`State`, add `Description`/`RuntimeClass`/`AgentState` |
| `pkg/ari/server.go` | `handleRoomStatus`: use `agents.List` not `store.ListSessions`; `handleRoomSend`: use `agent.State` for stopped guard + call `UpdateState(running)` after delivery |
| `pkg/ari/server_test.go` | Update `RoomMember` field assertions (State→AgentState, add Description/RuntimeClass, remove SessionId); update `TestARIRoomSendDelivery` state assertion comment |
| `pkg/agentd/process.go` | Remove `OAR_SESSION_ID` and `OAR_ROOM_AGENT` env vars from `generateConfig` MCP injection block |
| `pkg/agentd/process_test.go` | Update `TestGenerateConfigWithRoomMCPInjection`: remove assertions on `OAR_SESSION_ID`/`OAR_ROOM_AGENT`, verify they are absent |

---

## Natural Seams (Task Decomposition)

The work divides cleanly into **two independent tasks** that can be validated separately:

**T01 — room/status + room/send alignment + test updates** (no new dependencies)
- `pkg/ari/types.go`: Update `RoomMember` struct
- `pkg/ari/server.go`: Update `handleRoomStatus` (agents table) + `handleRoomSend` (stopped guard + UpdateState)
- `pkg/ari/server_test.go`: Update assertions for new `RoomMember` shape
- Verify: `go test ./pkg/ari/... -count=1` → 64+ tests pass

**T02 — room-mcp-server SDK rewrite + env var cleanup** (requires go.mod change)
- Add `github.com/modelcontextprotocol/go-sdk v0.8.0` to `go.mod`; run `go mod tidy`
- Rewrite `cmd/room-mcp-server/main.go`
- `pkg/agentd/process.go`: Remove deprecated env vars
- `pkg/agentd/process_test.go`: Update env var assertions
- Verify: `go build ./cmd/room-mcp-server`, `go test ./pkg/agentd/...`

**Recommended order**: T01 first (simpler, de-risks server logic changes), T02 second (SDK dependency).

---

## Risks and Gotchas

**Risk 1: handleRoomSend state update causes test assertion divergence**
The current `TestARIRoomSendDelivery` asserts `memberMap["agent-b"].State == "running"` using `room/status`. After T01:
- `room/status` uses agentState → field is now `AgentState` (not `State`)
- `handleRoomSend` MUST call `agents.UpdateState(ctx, agent.ID, AgentStateRunning, "")` after delivery or the test assertion fails
- The test comment says "session state" but must be updated to "agent state"

**Risk 2: handleRoomSend stopped guard — session vs agent state**
Currently: guards on `targetState == meta.SessionStateStopped` (session table lookup)
After T01: should guard on `agent.State == meta.AgentStateStopped` (already fetched from agents table)
This eliminates the session lookup for the stopped check, which is correct and simpler.

**Risk 3: SDK module transitive deps**
`modelcontextprotocol/go-sdk v0.8.0` requires `golang.org/x/tools v0.34.0` which is NOT currently in `go.sum`. `go mod tidy` will add it. This should be clean but may create go.sum noise.

**Risk 4: go.sum completeness for SDK**
Some of the SDK's deps (`golang.org/x/tools v0.34.0`) are new. Run `go mod tidy` after adding the dependency. The SDK is already in the local module cache so no network access needed.

**Risk 5: RoomMember field removal breaks callsites**
`SessionId` field removal will cause compile errors in any code that reads it. Only known consumer is the test that checks member attributes. The `ariRoomMember` type in `cmd/room-mcp-server/main.go` also has `SessionId` — this type disappears in the SDK rewrite, so no explicit migration needed there.

**Risk 6: room-mcp-server unit tests**
There are currently NO unit tests for `cmd/room-mcp-server/main.go`. R051 validation says "existing multi-agent integration tests pass." The primary evidence will be: (a) `go build` succeeds, (b) process injection tests in `pkg/agentd/process_test.go` pass (config generation), (c) room/send tests in `pkg/ari/server_test.go` pass (agentd-side behavior). Adding a minimal unit test for the SDK server (using `mcp.NewInMemoryTransports()`) would be valuable but is not strictly required for R051 validation.

---

## Verification Commands

After T01:
```bash
go test ./pkg/ari/... -count=1 -timeout 120s                       # 64+ tests pass
go test ./pkg/ari/... -count=1 -run TestARIRoomLifecycle -v         # members have AgentState/Description/RuntimeClass
go test ./pkg/ari/... -count=1 -run TestARIRoomSendDelivery -v      # agent-b AgentState = "running" after delivery
go build ./...                                                       # clean build
```

After T02:
```bash
go build ./cmd/room-mcp-server                                       # builds with SDK
go test ./pkg/agentd/... -count=1 -run TestGenerateConfigWithRoomMCPInjection -v  # no OAR_SESSION_ID/OAR_ROOM_AGENT
go test ./pkg/agentd/... -count=1 -timeout 120s                     # all agentd tests pass
go test ./... -count=1 -timeout 120s                                 # full suite passes
```

---

## Forward Intelligence for Planner

1. **S07 dependency**: S07 is Recovery & Integration Proof — it depends on S06. The key thing S07 builds on from S06 is correct `agentState` in `room/status` and correct env vars. Make sure both tasks close cleanly before declaring S06 done.

2. **Flaky pre-existing tests**: `TestARIRoomSendToStoppedTarget` (shim socket timeout race) and `TestRuntimeSuite/TestCancel_SendsCancelToAgent` (peer disconnect race) are pre-existing flaky tests noted in S03 summary. Do not count their occasional failure as a regression from S06 changes.

3. **handleRoomSend stopped guard**: After switching to agentState, the `creating` state also means "not ready for prompts." Consider whether `creating` should also be rejected. Per S04's design, `handleAgentPrompt` rejects `creating` state (line 1133 in server.go). `handleRoomSend` should mirror this guard using agent.State, not session state.

4. **ariRoomMember in room-mcp-server**: The current `main.go` has a local `ariRoomMember` struct with `AgentName`, `SessionId`, `State`. In the new SDK-based server, the `room_status` tool will receive new member shape. Update the local ARI types in room-mcp-server to match new `RoomMember` (drop `sessionId`, use `agentState`).

5. **SDK version constraint**: `go-sdk v0.8.0` requires `go 1.23.0` minimum. Project uses `go 1.24.13` — no issue.
