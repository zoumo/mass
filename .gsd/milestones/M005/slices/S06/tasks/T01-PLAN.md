---
estimated_steps: 24
estimated_files: 3
skills_used: []
---

# T01: Align room/status and room/send to agents table + update tests

Three coordinated changes in pkg/ari/:

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

## Inputs

- ``pkg/ari/types.go` — current RoomMember struct (has SessionId/State, missing Description/RuntimeClass/AgentState)`
- ``pkg/ari/server.go` — handleRoomStatus uses ListSessions; handleRoomSend uses session state for stopped guard and does not call UpdateState after delivery`
- ``pkg/ari/server_test.go` — test assertions reference .State and .SessionId on RoomMember`

## Expected Output

- ``pkg/ari/types.go` — RoomMember has Description/RuntimeClass/AgentState; SessionId and State removed`
- ``pkg/ari/server.go` — handleRoomStatus uses agents.List; handleRoomSend uses agent.State for guards + calls UpdateState(running) after delivery`
- ``pkg/ari/server_test.go` — all assertions updated to use .AgentState; no .State or .SessionId references on RoomMember`

## Verification

go test ./pkg/ari/... -count=1 -timeout 120s
go test ./pkg/ari/... -count=1 -run TestARIRoomLifecycle -v
go test ./pkg/ari/... -count=1 -run TestARIRoomSendDelivery -v
go build ./...
