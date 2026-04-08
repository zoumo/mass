---
estimated_steps: 33
estimated_files: 3
skills_used: []
---

# T02: Implement room/send ARI handler with routing resolution and integration tests

Add the `room/send` JSON-RPC method to the ARI server. This handler resolves targetAgent → sessionId within a room, formats the message with sender attribution, and delivers via the existing session/prompt path. Includes integration tests using newTestHarness (DB-only, mock process manager).

## Steps

1. In `pkg/ari/types.go`, add two types:
   - `RoomSendParams` with fields: Room (string), TargetAgent (string), Message (string), SenderAgent (string), SenderId (string)
   - `RoomSendResult` with fields: Delivered (bool), StopReason (string, omitempty)

2. In `pkg/ari/server.go`, implement `handleRoomSend`:
   a. Unmarshal RoomSendParams, validate required fields (room, targetAgent, message)
   b. Call `store.GetRoom` — return InvalidParams if room not found
   c. Call `store.ListSessions` with Room filter — find session where RoomAgent == targetAgent
   d. If no matching session: return InvalidParams "target agent X not found in room Y"
   e. If target session state is "stopped": return InvalidParams "target agent X is stopped"
   f. Format attributed message: `[room:<roomName> from:<senderAgent>] <message>`
   g. Internally call handleSessionPrompt logic: auto-start if created, connect to shim, call client.Prompt with attributed message, return result
   h. Return RoomSendResult{Delivered: true, StopReason: result.StopReason}
   Note: Rather than calling handleSessionPrompt directly (which expects jsonrpc2 request/reply), extract the prompt delivery logic into a helper method `deliverPrompt(ctx, sessionID, text) (stopReason string, err error)` that both handleSessionPrompt and handleRoomSend can call.

3. In `pkg/ari/server.go`, register `room/send` in the Handle method's switch statement, between room/status and room/delete.

4. In `pkg/ari/server_test.go`, add integration tests:
   - `TestARIRoomSendBasic`: Create room → create 2 sessions (agent-a, agent-b) with newSessionTestHarness → prompt agent-b via room/send from agent-a → verify delivery (RoomSendResult.Delivered==true). This tests the happy path with auto-start.
   - `TestARIRoomSendErrors`: Test error cases: (1) room not found, (2) target agent not in room, (3) missing required fields. Use newTestHarness (DB-only, no real shim needed for error path tests).
   - Add helper function `roomSend(ctx, t, conn, room, targetAgent, message, senderAgent, senderId)` following the pattern of existing roomCreate/roomStatus/roomDelete helpers.

## Failure Modes

| Dependency | On error | On timeout | On malformed response |
|------------|----------|-----------|----------------------|
| Target session prompt | Return InternalError "prompt failed: <err>" | 120s timeout → return InternalError "prompt failed: context deadline exceeded" | N/A (prompt returns stopReason string) |
| Meta store (GetRoom, ListSessions) | Return InternalError with store error message | N/A (SQLite, local) | N/A |
| ProcessManager.Connect | Return InternalError "connect to session failed" | 5s connect timeout | N/A |

## Negative Tests

- Room not found → InvalidParams error
- Target agent not in room → InvalidParams error
- Target agent stopped → InvalidParams error  
- Missing room name → InvalidParams error
- Missing targetAgent → InvalidParams error
- Missing message → InvalidParams error

## Inputs

- ``pkg/ari/types.go` — existing Room types (RoomCreateParams, RoomStatusParams, etc.) as pattern`
- ``pkg/ari/server.go` — existing handleRoomCreate/handleRoomStatus/handleSessionPrompt as patterns`
- ``pkg/ari/server_test.go` — existing roomCreate/roomStatus/roomDelete helpers and TestARIRoomLifecycle as pattern`

## Expected Output

- ``pkg/ari/types.go` — RoomSendParams and RoomSendResult types added`
- ``pkg/ari/server.go` — handleRoomSend handler and deliverPrompt helper implemented, room/send registered in dispatch`
- ``pkg/ari/server_test.go` — TestARIRoomSendBasic, TestARIRoomSendErrors, and roomSend helper added`

## Verification

go test ./pkg/ari/ -count=1 -v -run 'TestARIRoomSend' && go build ./...
