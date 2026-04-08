---
estimated_steps: 21
estimated_files: 1
skills_used: []
---

# T04: Full-stack integration test: room/send message delivery via real sessions

Add an integration test using `newSessionTestHarness` that proves the complete room/send delivery path: create a room, create two sessions with mockagent runtime, send a message from agent-a to agent-b via room/send, and verify agent-b receives the attributed prompt. Also verify error paths with real sessions.

## Steps

1. In `pkg/ari/server_test.go`, add `TestARIRoomSendDelivery`:
   a. Use `newSessionTestHarness(t)` to get a harness with mockagent support
   b. Create room "routing-test" via room/create
   c. Prepare a workspace
   d. Create session for agent-a (room="routing-test", roomAgent="agent-a")
   e. Create session for agent-b (room="routing-test", roomAgent="agent-b")
   f. Call room/send: room="routing-test", targetAgent="agent-b", message="hello from architect", senderAgent="agent-a"
   g. Assert RoomSendResult.Delivered == true
   h. Assert RoomSendResult.StopReason is non-empty (mockagent returns "end_turn")
   i. Verify via session/status that agent-b's state is now "running" (auto-started by room/send)

2. Add `TestARIRoomSendToStoppedTarget`:
   a. Create room, create 2 sessions, start agent-b, then stop agent-b
   b. Call room/send targeting stopped agent-b
   c. Assert error contains "not running" or "stopped"

3. Run the full ARI test suite to verify no regressions: `go test ./pkg/ari/ -count=1 -short`

## Must-Haves

- TestARIRoomSendDelivery proves end-to-end message routing with real mockagent processes
- TestARIRoomSendToStoppedTarget proves stopped-target error handling
- All pre-existing ARI tests continue to pass

## Inputs

- ``pkg/ari/server.go` — room/send handler from T02`
- ``pkg/ari/server_test.go` — roomSend helper and existing test patterns from T02`
- ``pkg/ari/types.go` — RoomSendParams, RoomSendResult from T02`

## Expected Output

- ``pkg/ari/server_test.go` — TestARIRoomSendDelivery and TestARIRoomSendToStoppedTarget added`

## Verification

go test ./pkg/ari/ -count=1 -v -run 'TestARIRoomSend' -timeout 120s && go test ./pkg/ari/ -count=1 -short
