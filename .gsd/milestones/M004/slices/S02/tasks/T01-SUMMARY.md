---
id: T01
parent: S02
milestone: M004
key_files:
  - pkg/spec/types.go
  - pkg/runtime/runtime.go
  - pkg/runtime/client_test.go
  - pkg/agentd/process.go
  - pkg/agentd/process_test.go
key_decisions:
  - resolveRoomMCPBinary uses same 3-tier pattern as shim binary (env → ./bin → PATH)
  - Room MCP env vars: OAR_AGENTD_SOCKET, OAR_ROOM_NAME, OAR_SESSION_ID, OAR_ROOM_AGENT
  - Empty RoomAgent is valid — injected as empty string env var
duration: 
verification_result: passed
completed_at: 2026-04-08T05:16:52.238Z
blocker_discovered: false
---

# T01: Added stdio transport support to spec.McpServer, extended convertMcpServers for stdio→acp mapping, and injected room MCP server in generateConfig for room sessions

**Added stdio transport support to spec.McpServer, extended convertMcpServers for stdio→acp mapping, and injected room MCP server in generateConfig for room sessions**

## What Happened

Extended spec.McpServer with Name, Command, Args, and Env fields plus new EnvVar type. Added stdio case to convertMcpServers mapping spec types to acp.McpServerStdio. Created resolveRoomMCPBinary helper using the same 3-tier resolution pattern as the shim binary. Modified generateConfig to inject a room-tools stdio MCP server when session.Room is non-empty, passing OAR_AGENTD_SOCKET, OAR_ROOM_NAME, OAR_SESSION_ID, and OAR_ROOM_AGENT as env vars. Added tests for stdio conversion branch and generateConfig room injection with three subtests covering room/no-room/empty-agent cases.

## Verification

All tests pass and build is clean:\n- go test ./pkg/runtime/ -run TestConvertMcpServers: 4 tests PASS\n- go test ./pkg/agentd/ -run TestGenerateConfig: 3 subtests PASS\n- go build ./...: clean build with exit code 0

## Verification Evidence

| # | Command | Exit Code | Verdict | Duration |
|---|---------|-----------|---------|----------|
| 1 | `go test ./pkg/runtime/ -count=1 -run TestConvertMcpServers` | 0 | ✅ pass | 3800ms |
| 2 | `go test ./pkg/agentd/ -count=1 -run TestGenerateConfig` | 0 | ✅ pass | 3800ms |
| 3 | `go build ./...` | 0 | ✅ pass | 3800ms |

## Deviations

None.

## Known Issues

None.

## Files Created/Modified

- `pkg/spec/types.go`
- `pkg/runtime/runtime.go`
- `pkg/runtime/client_test.go`
- `pkg/agentd/process.go`
- `pkg/agentd/process_test.go`
