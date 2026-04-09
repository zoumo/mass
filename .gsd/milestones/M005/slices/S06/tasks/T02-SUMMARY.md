---
id: T02
parent: S06
milestone: M005
key_files:
  - cmd/room-mcp-server/main.go
  - go.mod
  - go.sum
  - pkg/agentd/process.go
  - pkg/agentd/process_test.go
key_decisions:
  - Used server.AddTool with json.RawMessage InputSchema (not generic AddTool[In,Out]) to preserve existing custom JSON schemas
  - Test fixture required AgentID field because process.go uses session.AgentID (not session.ID) for OAR_AGENT_ID
  - go get required before go mod tidy to avoid tidy stripping the entry before imports were compiled
duration: 
verification_result: passed
completed_at: 2026-04-08T21:14:39.478Z
blocker_discovered: false
---

# T02: Rewrote room-mcp-server using modelcontextprotocol/go-sdk (StdioTransport + server.AddTool); removed deprecated OAR_SESSION_ID and OAR_ROOM_AGENT env var injections; config now uses agentID/agentName

**Rewrote room-mcp-server using modelcontextprotocol/go-sdk (StdioTransport + server.AddTool); removed deprecated OAR_SESSION_ID and OAR_ROOM_AGENT env var injections; config now uses agentID/agentName**

## What Happened

Four coordinated changes: (1) go.mod updated with github.com/modelcontextprotocol/go-sdk v0.8.0 via go get + go mod tidy. (2) cmd/room-mcp-server/main.go fully rewritten — dropped all hand-rolled MCP JSON-RPC types, kept callARI/nullHandler/log-setup/schemas, updated ariRoomMember to use AgentState/RuntimeClass, config now reads OAR_AGENT_ID/OAR_AGENT_NAME, main() uses mcp.NewServer + server.AddTool + mcp.StdioTransport. (3) pkg/agentd/process.go removed OAR_SESSION_ID and OAR_ROOM_AGENT deprecated env var injections. (4) pkg/agentd/process_test.go updated TestGenerateConfigWithRoomMCPInjection: fixture now sets AgentID, assertions verify absence of deprecated vars and presence of OAR_AGENT_ID/OAR_AGENT_NAME.

## Verification

go build ./cmd/room-mcp-server (exit 0). go test ./pkg/agentd/... -run TestGenerateConfigWithRoomMCPInjection -v (3/3 sub-tests PASS). go test ./pkg/agentd/... -count=1 -timeout 120s (ok 5.9s). go test ./... -count=1 -timeout 120s (all packages ok; pkg/rpc and pkg/runtime flaky failures were pre-existing and passed on re-run).

## Verification Evidence

| # | Command | Exit Code | Verdict | Duration |
|---|---------|-----------|---------|----------|
| 1 | `go build ./cmd/room-mcp-server` | 0 | ✅ pass | 506ms |
| 2 | `go test ./pkg/agentd/... -count=1 -run TestGenerateConfigWithRoomMCPInjection -v` | 0 | ✅ pass | 2209ms |
| 3 | `go test ./pkg/agentd/... -count=1 -timeout 120s` | 0 | ✅ pass | 15900ms |
| 4 | `go test ./... -count=1 -timeout 120s (second run)` | 0 | ✅ pass | 20000ms |

## Deviations

Test fixture needed AgentID: \"sess-123\" addition (not explicitly in plan). go get required before go mod tidy (tidy ran first during initial bg job, stripped entry).

## Known Issues

None.

## Files Created/Modified

- `cmd/room-mcp-server/main.go`
- `go.mod`
- `go.sum`
- `pkg/agentd/process.go`
- `pkg/agentd/process_test.go`
