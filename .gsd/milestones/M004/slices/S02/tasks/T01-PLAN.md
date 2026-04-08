---
estimated_steps: 15
estimated_files: 5
skills_used: []
---

# T01: Extend spec types for stdio MCP and inject room MCP server in generateConfig

Add stdio transport support to spec.McpServer (Command, Args, Env fields), add spec.EnvVar type, extend convertMcpServers in pkg/runtime/runtime.go for the stdio case (mapping to acp.McpServerStdio), and modify ProcessManager.generateConfig to inject a room MCP server when the session has a non-empty Room field.

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

## Inputs

- ``pkg/spec/types.go` — existing McpServer struct with Type/URL fields`
- ``pkg/runtime/runtime.go` — existing convertMcpServers function (lines 340-354)`
- ``pkg/runtime/client_test.go` — existing TestConvertMcpServers_SSEBranch/HTTPBranch tests as pattern`
- ``pkg/agentd/process.go` — existing generateConfig method (lines 251-288)`
- ``pkg/agentd/process_test.go` — existing TestProcessManagerStart as pattern for ProcessManager setup`

## Expected Output

- ``pkg/spec/types.go` — McpServer extended with Name/Command/Args/Env fields, new EnvVar type`
- ``pkg/runtime/runtime.go` — convertMcpServers handles stdio case`
- ``pkg/runtime/client_test.go` — TestConvertMcpServers_StdioBranch added`
- ``pkg/agentd/process.go` — generateConfig injects room MCP server for room sessions`
- ``pkg/agentd/process_test.go` — TestGenerateConfigWithRoomMCPInjection added`

## Verification

go test ./pkg/runtime/ -count=1 -run TestConvertMcpServers && go test ./pkg/agentd/ -count=1 -run TestGenerateConfig && go build ./...
