# S02 Research: Routing Engine and MCP Tool Injection

## Summary

S02 builds the inter-agent messaging layer on top of S01's Room lifecycle. Two paths converge on the same delivery mechanism:

1. **ARI `room/send` handler** — orchestrator or agentd-internal caller resolves a room member by agent name, then delivers a message via `session/prompt` on the target session.
2. **MCP tool injection** — when a session is created in a room, agentd injects a stdio-based MCP server into the agent's config; the agent calls `room_send(targetAgent, message)` → MCP server relays back to agentd → agentd delivers via `session/prompt`.

Both paths are specified by D052. M004 scope (D053) covers L1+L2: point-to-point `room_send` only. Broadcast and communication mode enforcement are deferred.

## Requirements Targeted

- **R041** (active) — Room lifecycle now has a realized runtime surface. S02 advances this by adding routing/delivery semantics.
- **R044** (active) — Additional hardening. S02 surfaces concurrent prompt delivery risks that will need follow-up.

## Recommendation

**Depth: Deep research.** This is a high-risk slice involving novel architecture (MCP tool injection has no precedent in this codebase), a new binary (`cmd/room-mcp-server`), cross-process communication (MCP server → agentd socket), and tricky concurrency (target agent busy during routing). Multiple design decisions need resolution.

## Implementation Landscape

### What Exists

| Component | Status | Location |
|---|---|---|
| Room DB (rooms table, CRUD) | ✅ Complete | `pkg/meta/room.go`, `pkg/meta/models.go` |
| Session room/roomAgent FK | ✅ Complete | `pkg/meta/session.go` |
| ARI room/create, room/status, room/delete | ✅ Complete (S01) | `pkg/ari/server.go` lines 901-1119 |
| Room-existence validation in session/new (D051) | ✅ Complete (S01) | `pkg/ari/server.go` line 460 |
| Communication vocabulary mesh/star/isolated | ✅ Complete (S01) | `pkg/meta/models.go` |
| ARI session/prompt handler | ✅ Complete | `pkg/ari/server.go` line 517 |
| ShimClient.Prompt() | ✅ Complete | `pkg/agentd/shim_client.go` line 101 |
| spec.McpServer type | ✅ Partial (http/sse only, no stdio) | `pkg/spec/types.go` line 90 |
| AcpSession.McpServers in config | ✅ Exists but unused | `pkg/spec/types.go` line 86 |
| convertMcpServers (spec→acp) | ✅ Partial (http/sse only) | `pkg/runtime/runtime.go` line 342 |
| ARI room/send handler | ❌ Not implemented | — |
| MCP tool injection in generateConfig | ❌ Not implemented | `pkg/agentd/process.go` line 183 |
| Stdio MCP server binary | ❌ Not implemented | — |
| Busy-target detection | ❌ Not implemented | — |

### Key Architecture: The Routing Path

```
Agent A                    Agent Shim A              agentd                  Agent Shim B           Agent B
   │                           │                        │                        │                    │
   ├── calls room_send ──────►│                        │                        │                    │
   │   (MCP tool)              │                        │                        │                    │
   │                           ├── room/send ──────────►│                        │                    │
   │                           │   (JSON-RPC to agentd) │                        │                    │
   │                           │                        ├── resolve target ──────┤                    │
   │                           │                        │   roomAgent→sessionId  │                    │
   │                           │                        │                        │                    │
   │                           │                        ├── session/prompt ─────►│                    │
   │                           │                        │   (ShimClient.Prompt)  ├── ACP prompt ────►│
   │                           │                        │                        │                    │
   │                           │                        │◄── prompt result ──────┤◄── response ──────┤
   │                           │◄── send result ────────┤                        │                    │
   │◄── tool result ──────────┤                        │                        │                    │
```

### Critical Design Decision: MCP Server Transport

The ACP protocol supports three MCP server transports: `stdio`, `http`, and `sse`. All agents MUST support stdio. The current `spec.McpServer` type only handles `http` and `sse`.

**Recommended: Stdio transport.** 

Reasons:
1. All ACP agents MUST support stdio — guaranteed compatibility
2. No network port management — stdio is process-local
3. The MCP server binary is a simple Go program that reads JSON-RPC from stdin and writes to stdout
4. The MCP server communicates back to agentd via the agentd ARI socket (already known via env var)
5. The agent runtime (ACP client) spawns and manages the MCP server process lifecycle automatically

This requires:
- Extending `spec.McpServer` to support stdio (add `Command`, `Args`, `Env` fields alongside existing `Type`/`URL`)
- Extending `convertMcpServers` in `pkg/runtime/runtime.go` to handle the `stdio` case
- Building a `cmd/room-mcp-server` binary

### Stdio MCP Server Design

The `room-mcp-server` binary is a minimal MCP server over stdio:

```
Agent Runtime (ACP client)
   ├── spawns room-mcp-server via stdio
   ├── MCP initialize handshake
   ├── tools/list → returns [room_send, room_status]
   └── tools/call room_send(targetAgent, message)
         └── room-mcp-server → JSON-RPC room/send to agentd socket
                                └── agentd → session/prompt on target
```

**Environment variables passed to MCP server:**
- `OAR_AGENTD_SOCKET` — path to agentd's ARI socket
- `OAR_ROOM_NAME` — the room this session belongs to  
- `OAR_SESSION_ID` — this session's ID (for sender attribution)
- `OAR_ROOM_AGENT` — this session's agent name in the room

**Tools exposed:**
- `room_send(targetAgent: string, message: string)` → routes message to target via `room/send` ARI
- `room_status()` → returns current room membership via `room/status` ARI

### ARI `room/send` Handler Design

New ARI method at `pkg/ari/server.go`:

```go
type RoomSendParams struct {
    Room        string `json:"room"`        // room name
    TargetAgent string `json:"targetAgent"` // target agent name in room
    Message     string `json:"message"`     // message text
    SenderAgent string `json:"senderAgent"` // sender agent name (attribution)
    SenderId    string `json:"senderId"`    // sender session ID
}

type RoomSendResult struct {
    Delivered  bool   `json:"delivered"`
    StopReason string `json:"stopReason,omitempty"` // from target's prompt result
}
```

Resolution flow:
1. Validate room exists
2. List sessions in room, find session where `roomAgent == targetAgent`
3. Validate target session is in running state (or created — auto-start applies)
4. Format message with sender attribution: `[From agent "architect"]: <message>`
5. Call `handleSessionPrompt` internally (or directly via `processes.Connect` + `client.Prompt`)
6. Return delivery result

### Busy-Target Semantics

Per the research doc (DES-007), when the target is busy: return `agent busy` error. No queuing.

**Detection mechanism:** The `session/prompt` call to the shim is synchronous — it blocks until the turn completes. If a second prompt arrives while one is in-flight, the ACP protocol behavior depends on the agent implementation. For M004 L2, we can:

1. **Simple approach (recommended for L2):** Just forward the prompt — the ShimClient.Prompt call will block until the agent finishes the current turn and then processes the routed message. The 120s timeout provides the safety valve.
2. **Advanced approach (deferred):** Add a per-session prompt mutex in ProcessManager to detect busy sessions and return an immediate error. This is better for L3/broadcast where partial-success matters.

For L2 (point-to-point only), the simple approach is acceptable. The orchestrator or sending agent can handle the timeout.

### MCP Tool Injection Point

In `pkg/agentd/process.go`, `generateConfig()` currently doesn't set `AcpAgent.Session.McpServers`. When a session has a non-empty `Room`, injection adds the room MCP server:

```go
if session.Room != "" {
    cfg.AcpAgent.Session.McpServers = []spec.McpServer{
        {
            Type:    "stdio",
            Name:    "room-tools",
            Command: roomMcpBinary, // resolved path to room-mcp-server
            Args:    []string{},
            Env: []spec.EnvVar{
                {Name: "OAR_AGENTD_SOCKET", Value: m.config.Socket},
                {Name: "OAR_ROOM_NAME", Value: session.Room},
                {Name: "OAR_SESSION_ID", Value: session.ID},
                {Name: "OAR_ROOM_AGENT", Value: session.RoomAgent},
            },
        },
    }
}
```

**Constraint:** MCP servers can only be injected at ACP session creation time (`NewSessionRequest.McpServers`). They cannot be added to running sessions. This aligns with the existing flow — session/new is configuration-only bootstrap.

### Spec Type Extensions Needed

`pkg/spec/types.go` — `McpServer` must support stdio:

```go
type McpServer struct {
    Type    string   `json:"type"`              // "http", "sse", or "stdio"
    URL     string   `json:"url,omitempty"`     // for http/sse
    Name    string   `json:"name,omitempty"`    // human-readable name
    Command string   `json:"command,omitempty"` // for stdio
    Args    []string `json:"args,omitempty"`    // for stdio
    Env     []EnvVar `json:"env,omitempty"`     // for stdio
}

type EnvVar struct {
    Name  string `json:"name"`
    Value string `json:"value"`
}
```

`pkg/runtime/runtime.go` — `convertMcpServers` must handle stdio:

```go
case "stdio":
    envVars := make([]acp.EnvVariable, len(s.Env))
    for i, e := range s.Env {
        envVars[i] = acp.EnvVariable{Name: e.Name, Value: e.Value}
    }
    result = append(result, acp.McpServer{Stdio: &acp.McpServerStdio{
        Name:    s.Name,
        Command: s.Command,
        Args:    s.Args,
        Env:     envVars,
    }})
```

### Message Attribution Format

When delivering a routed message, the target agent needs to know who sent it. The simplest approach for L2:

```
[room:backend-refactor from:architect] Implement the JWT auth module using the existing middleware pattern.
```

This is a text prefix on the prompt delivered via `session/prompt`. No structural changes to `SessionPromptParams` needed for L2.

### Go MCP SDK

The `room-mcp-server` binary needs to speak MCP protocol over stdio. Two options:

1. **Use `github.com/mark3labs/mcp-go`** — the standard Go MCP SDK. Well-maintained, handles all protocol plumbing.
2. **Hand-roll minimal JSON-RPC** — the MCP protocol over stdio is just JSON-RPC 2.0 with specific methods. Since we only need `initialize`, `tools/list`, and `tools/call`, this is ~200 lines of code.

**Recommendation:** Use `mcp-go` if it doesn't add heavy dependencies. Otherwise hand-roll — the surface is tiny.

### Test Strategy

1. **Unit tests for routing resolution:** Given a room with members, resolve targetAgent → sessionId correctly. Test not-found, stopped-member, ambiguous cases.
2. **Integration test for ARI `room/send`:** Using `newTestHarness` (no real shim), create room + sessions in DB, call `room/send`, verify it attempts prompt on correct session.
3. **Integration test with real shim (newSessionTestHarness):** Full E2E — create room, create 2 sessions with mockagent, send `room/send` from orchestrator → target agent receives prompt with attribution.
4. **MCP tool injection test:** Verify that `generateConfig` adds McpServers when session has Room.
5. **Spec type tests:** Verify `convertMcpServers` handles stdio transport correctly.

**Note:** Testing the full MCP tool injection E2E (agent calls room_send MCP tool → agentd routes → target receives) requires the mockagent to actually call an MCP tool, which it currently doesn't support. For M004 L2, the integration test can verify:
- The MCP server binary is injected into config.json correctly
- The `room/send` ARI method routes correctly
- These two pieces compose into the full flow

## Natural Seams (Task Decomposition)

### T01: Spec Type Extensions for Stdio MCP
- Extend `spec.McpServer` with stdio fields (Command, Args, Env, Name)
- Add `spec.EnvVar` type
- Extend `convertMcpServers` in `pkg/runtime/runtime.go` for stdio case
- Unit tests for the new conversion path
- **Files:** `pkg/spec/types.go`, `pkg/runtime/runtime.go`, `pkg/runtime/client_test.go`

### T02: ARI `room/send` Handler
- Add `RoomSendParams`, `RoomSendResult` types to `pkg/ari/types.go`
- Implement `handleRoomSend` in `pkg/ari/server.go`:
  - Route dispatch: room → targetAgent → sessionId → session/prompt
  - Sender attribution in message text
  - Error cases: room not found, target not found, target not running
- Register `room/send` in method dispatch
- Integration tests using `newTestHarness` (DB-only, no real shim)
- **Files:** `pkg/ari/types.go`, `pkg/ari/server.go`, `pkg/ari/server_test.go`

### T03: MCP Tool Injection in ProcessManager
- Modify `generateConfig` to inject room MCP server when session has Room
- Add MCP server binary resolution logic (similar to shim binary resolution)
- Unit/integration tests verifying config.json contains McpServers for room sessions
- **Files:** `pkg/agentd/process.go`, `pkg/agentd/process_test.go`

### T04: Room MCP Server Binary
- Create `cmd/room-mcp-server/main.go` — stdio MCP server
- Implements MCP protocol: initialize, tools/list, tools/call
- `room_send` tool: connects to agentd socket, calls `room/send` ARI
- `room_status` tool: connects to agentd socket, calls `room/status` ARI
- Build integration: add to Makefile/build scripts
- **Files:** `cmd/room-mcp-server/main.go`, `Makefile` (if exists)

### T05: End-to-End Integration Test
- Test with `newSessionTestHarness`: room/create → 2 sessions → room/send → verify target receives attributed prompt
- Verify room/send error cases: nonexistent room, nonexistent target, stopped target
- **Files:** `pkg/ari/server_test.go`

## Risks and Mitigations

| Risk | Severity | Mitigation |
|---|---|---|
| MCP tool injection requires stdio transport not yet supported in spec | Medium | T01 extends spec types — straightforward, low risk |
| room-mcp-server binary needs MCP protocol implementation | Medium | Use mcp-go SDK or hand-roll minimal protocol (tiny surface) |
| Concurrent prompt delivery to busy agents | Medium | L2 uses simple blocking approach — defer mutex-based busy detection to L3 |
| room-mcp-server binary resolution at runtime | Low | Follow existing shimBinary resolution pattern (env var → relative → PATH) |
| Testing MCP tool injection E2E requires mockagent to call MCP tools | Medium | Test the two halves independently (injection + routing) — full E2E deferred to S03 |
| Session/prompt 120s timeout on routed messages | Low | Acceptable for L2; configurable timeout is a future enhancement |

## Skills Discovered

No additional skills needed. The work involves Go standard library patterns, JSON-RPC 2.0 (already in codebase via `sourcegraph/jsonrpc2`), and potentially `mcp-go` SDK for the MCP server binary.

## Files Likely Touched

| File | Change |
|---|---|
| `pkg/spec/types.go` | Add stdio fields to McpServer, add EnvVar type |
| `pkg/runtime/runtime.go` | Extend convertMcpServers for stdio |
| `pkg/runtime/client_test.go` | Test stdio conversion |
| `pkg/ari/types.go` | Add RoomSendParams, RoomSendResult |
| `pkg/ari/server.go` | Add handleRoomSend, register room/send method |
| `pkg/ari/server_test.go` | Integration tests for room/send |
| `pkg/agentd/process.go` | MCP injection in generateConfig |
| `pkg/agentd/process_test.go` | Test config generation with room |
| `cmd/room-mcp-server/main.go` | New binary — stdio MCP server |
| `go.mod` / `go.sum` | If adding mcp-go dependency |

## What to Build First

**T01 (spec types) → T02 (ARI handler) → T03 (injection) → T04 (MCP binary) → T05 (E2E test)**

T01 is foundational — spec types must exist before anything uses them. T02 (routing handler) is the highest-value piece and can be tested independently of MCP injection. T03 and T04 can be done in either order but T03 is simpler. T05 ties everything together.

The riskiest piece is T04 (MCP server binary) because it introduces a new binary, a new protocol implementation, and cross-process communication. However, it can be tested in isolation.
