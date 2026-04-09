# S06: Room & MCP Agent Alignment — UAT

**Milestone:** M005
**Written:** 2026-04-08T21:20:44.641Z

# S06 UAT — Room & MCP Agent Alignment

## Preconditions

- `agentd` binary built from current HEAD (`go build ./...` clean)
- `room-mcp-server` binary built (`go build ./cmd/room-mcp-server`)
- Go test toolchain available (`go test ./...` baseline passes)
- No external services required — all tests use in-process agentd with mockagent

---

## Test Cases

### TC-01: room/status returns AgentState/RuntimeClass/Description in RoomMember

**Precondition:** Room exists with ≥1 agent in created/running/stopped state.

**Steps:**
1. Call `room/create` with a test room.
2. Call `agent/create` targeting the room.
3. Poll `agent/status` until `created`.
4. Call `room/status` with the room name.
5. Inspect the `members` array in the response.

**Expected:**
- Each member has `agentName`, `runtimeClass`, and `agentState` fields.
- `agentState` reflects the agent's current state (e.g. `"created"`).
- No `sessionId` or `state` fields appear in member objects.
- `description` is present (may be empty string) if the agent was created with a description.

**Test coverage:** `TestARIRoomLifecycle` — verifies member fields after agent creation.

---

### TC-02: room/send transitions agent state to running after delivery

**Precondition:** Room with two agents (sender and receiver), both in `created` state with live sessions.

**Steps:**
1. Set up room + two agents (agent-a as sender, agent-b as receiver).
2. Confirm `room/status` shows both agents with `agentState: "created"` or `"running"`.
3. Call `room/send` targeting agent-b with a message from agent-a.
4. Wait for delivery to complete.
5. Call `room/status` again.

**Expected:**
- `room/send` returns `{"delivered": true}`.
- `room/status` after delivery shows agent-b with `agentState: "running"` (set by `agents.UpdateState` post-delivery).

**Test coverage:** `TestARIRoomSendDelivery` — verifies state transition after delivery.

---

### TC-03: room/send rejects delivery to stopped agent

**Precondition:** Room with one target agent in `stopped` state.

**Steps:**
1. Create room + agent, bring agent to created state.
2. Call `agent/stop` to stop the agent.
3. Attempt `room/send` targeting the stopped agent.

**Expected:**
- `room/send` returns a JSON-RPC error (not panicking or hanging).
- Error indicates the agent is not in a deliverable state.

**Test coverage:** `TestARIRoomSendDelivery` — stopped-agent guard sub-case.

---

### TC-04: room/send rejects delivery to creating agent

**Precondition:** Agent in `creating` state (async bootstrap not yet complete).

**Steps:**
1. Create room.
2. Call `agent/create` but capture the agentId before polling to `created`.
3. Immediately call `room/send` targeting the creating agent.

**Expected:**
- `room/send` returns a JSON-RPC error referencing `creating` state.
- No session lookup is attempted.

**Test coverage:** `handleRoomSend` code path — mirrors `handleAgentPrompt` creating-state guard.

---

### TC-05: room-mcp-server binary starts, registers tools, responds to tools/list

**Precondition:** `room-mcp-server` binary built. Environment variables set:
- `OAR_AGENTD_SOCKET=/tmp/test.sock` (does not need to exist for tools/list)
- `OAR_AGENT_ID=test-agent-id`
- `OAR_AGENT_NAME=test-agent`
- `OAR_ROOM_NAME=test-room`

**Steps:**
1. Launch `room-mcp-server` with the above env vars.
2. Send MCP `initialize` request over stdin.
3. Send `tools/list` request.
4. Check the response.

**Expected:**
- Server responds to `initialize` with server info `{name: "room-mcp-server", version: "0.1.0"}`.
- `tools/list` returns exactly two tools: `room_send` and `room_status`.
- `room_send` input schema has `targetAgent` and `message` fields.
- `room_status` input schema has no required fields.

**Test coverage:** Binary build test (`go build ./cmd/room-mcp-server`); schema preserved via `mcp.MustParseJSON`.

---

### TC-06: OAR_SESSION_ID and OAR_ROOM_AGENT are absent from generated session env

**Precondition:** Session configured with a Room MCP server injection (session.Room set).

**Steps:**
1. Call `generateConfig` with a session that has `Room` set and `AgentID` set.
2. Inspect the env map for the injected MCP server.

**Expected:**
- `OAR_SESSION_ID` key is NOT present in the env map.
- `OAR_ROOM_AGENT` key is NOT present in the env map.
- `OAR_AGENT_ID` is present with the value from `session.AgentID`.
- `OAR_AGENT_NAME` is present with the value from `session.RoomAgent`.

**Test coverage:** `TestGenerateConfigWithRoomMCPInjection` — 3 subtests including explicit absence assertions.

---

### TC-07: room-mcp-server startup fails gracefully if OAR_AGENT_ID missing

**Precondition:** `room-mcp-server` binary built. `OAR_AGENT_ID` not set in environment.

**Steps:**
1. Launch `room-mcp-server` without `OAR_AGENT_ID`.
2. Observe exit code and stderr.

**Expected:**
- Server exits with non-zero code.
- Stderr contains a message indicating `OAR_AGENT_ID` is required.

**Test coverage:** `loadConfig` validation in cmd/room-mcp-server/main.go.

---

### TC-08: Full room lifecycle with SDK-based room-mcp-server (integration)

**Precondition:** End-to-end test environment with live agentd and mockagent.

**Steps:**
1. Create room.
2. Create two agents; poll both to `created` state.
3. Check `room/status` — verify `agentState` and `runtimeClass` visible in members.
4. Call `room/send` from agent-a to agent-b.
5. Verify delivery returns `{"delivered": true}`.
6. Call `room/status` again — verify agent-b shows `agentState: "running"`.
7. Stop both agents, delete both, delete room.

**Expected:**
- All steps complete without error.
- Agent states progress correctly through the delivery cycle.
- Room deletion succeeds after agents are stopped.

**Test coverage:** `TestARIRoomLifecycle` + `TestARIRoomSendDelivery` together.

---

## Edge Cases

- **Sender = Receiver:** room/send with targetAgent matching the sending agent — should succeed (self-message, semantics left to the agent)
- **Empty room:** room/status on a room with zero agents returns `members: []`
- **Missing OAR_ROOM_NAME:** room-mcp-server started without `OAR_ROOM_NAME` — room_status falls back to empty name; room_send produces an error from agentd on unknown room
- **Concurrent room/send:** two room/send calls to the same target agent overlap — each goes through deliverPrompt independently; no per-agent mutex at this level (documented gap)

