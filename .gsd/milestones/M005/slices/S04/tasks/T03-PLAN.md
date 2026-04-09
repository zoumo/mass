---
estimated_steps: 25
estimated_files: 1
skills_used: []
---

# T03: Add OAR_AGENT_ID and OAR_AGENT_NAME env vars to generateConfig

In pkg/agentd/process.go generateConfig, add OAR_AGENT_ID (= session.AgentID) and OAR_AGENT_NAME (= session.RoomAgent) to the MCP server env vars, keeping OAR_SESSION_ID as a deprecated alias for backward compat until S06 removes it.

## Steps

1. In pkg/agentd/process.go, locate the mcpServers env var block (~line 280):
   ```go
   {Name: "OAR_AGENTD_SOCKET", Value: m.config.Socket},
   {Name: "OAR_ROOM_NAME",     Value: session.Room},
   {Name: "OAR_SESSION_ID",    Value: session.ID},    // deprecated: remove in S06
   {Name: "OAR_ROOM_AGENT",    Value: session.RoomAgent},
   ```
   Add two new entries and annotate the deprecated one:
   ```go
   {Name: "OAR_AGENTD_SOCKET", Value: m.config.Socket},
   {Name: "OAR_ROOM_NAME",     Value: session.Room},
   {Name: "OAR_AGENT_ID",      Value: session.AgentID},   // agent-level identity (M005)
   {Name: "OAR_AGENT_NAME",    Value: session.RoomAgent}, // agent name within room (M005)
   {Name: "OAR_SESSION_ID",    Value: session.ID},        // deprecated: alias for OAR_AGENT_ID; remove in S06
   {Name: "OAR_ROOM_AGENT",    Value: session.RoomAgent}, // deprecated: alias for OAR_AGENT_NAME; remove in S06
   ```
   (OAR_ROOM_AGENT is also a deprecated alias for OAR_AGENT_NAME — keep both for now)

2. Run go build ./... to confirm clean build.

3. Run go test ./pkg/agentd/... to confirm existing tests pass.

## Notes
- session.AgentID may be empty string for sessions not linked to an agent (edge case in test harness). This is fine — the env var is just empty, which is the same as before.
- This change is additive: no existing behavior changes, existing tests need no updates.
- OAR_SESSION_ID and OAR_ROOM_AGENT remain present as deprecated aliases; S06 removes them when room-mcp-server is rewritten to use the new names.

## Inputs

- ``pkg/agentd/process.go` — generateConfig function, mcpServers env var block (~line 280)`

## Expected Output

- ``pkg/agentd/process.go` — OAR_AGENT_ID and OAR_AGENT_NAME env vars added to generateConfig mcpServers block`

## Verification

go build ./...
go test ./pkg/agentd/... -count=1 -timeout 60s
