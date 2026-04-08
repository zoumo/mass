---
estimated_steps: 36
estimated_files: 1
skills_used: []
---

# T01: Write TestARIMultiAgentRoundTrip — 3-agent bidirectional messaging with full lifecycle

Add the primary end-to-end integration test proving the M004 demo claim: Room create → 3 agent bootstrap → bidirectional message exchange → state verification → clean teardown.

This test is the capstone proof for M004, exercising the complete Room runtime in a single test function. It composes all building blocks from S01 (room lifecycle) and S02 (routing engine) into one coherent flow.

## Steps

1. Open `pkg/ari/server_test.go` and add `TestARIMultiAgentRoundTrip` after the existing `TestARIRoomSendToStoppedTarget` function (before the `var _ = (*testHarness)(nil)` suppression line at the end).

2. Test structure (follow existing patterns from `TestARIRoomSendDelivery`):
   - Use `newSessionTestHarness(t)` for full ARI server + mockagent setup
   - `context.WithTimeout` of 120s (real processes)
   - `h.dial(t, &nullHandler{})` for client connection
   - Guard with `if testing.Short() { t.Skip("requires mockagent processes") }`

3. Implement these test steps:
   a. `roomCreate(ctx, t, client, "multi-agent-room", "mesh", nil)` — create room
   b. `h.prepareWorkspaceForSession(ctx, t, client, "multi-agent-ws")` — shared workspace
   c. `session/new` × 3 (agent-a, agent-b, agent-c) in the room — use `client.Call` directly with `ari.SessionNewParams{WorkspaceId, RuntimeClass: "mockagent", Room: "multi-agent-room", RoomAgent: "agent-X"}`
   d. `roomStatus` — verify 3 members listed, all in "created" state
   e. `roomSend(ctx, t, client, "multi-agent-room", "agent-b", "hello from a", "agent-a", sessionIdA)` — A→B, verify Delivered==true
   f. `roomStatus` — verify agent-b state is "running" (auto-started by deliverPrompt)
   g. `roomSend(ctx, t, client, "multi-agent-room", "agent-a", "reply from b", "agent-b", sessionIdB)` — B→A (bidirectional proof), verify Delivered==true
   h. `roomStatus` — verify agent-a is now "running" too
   i. `roomSend(ctx, t, client, "multi-agent-room", "agent-c", "hello from a to c", "agent-a", sessionIdA)` — A→C (3rd agent), verify Delivered==true
   j. `roomStatus` — verify all 3 agents are "running"
   k. `session/stop` × 3 with `client.Call` for each session ID, then `time.Sleep(500 * time.Millisecond)` for process exit
   l. `roomDelete(ctx, t, client, "multi-agent-room")` — clean deletion (stopped sessions allowed)
   m. Verify `room/status` returns error (room not found) — call `client.Call` and assert error with `requireRPCError`

4. Assertions to include:
   - Each `roomSend` returns `Delivered==true` and non-empty `StopReason`
   - `roomStatus` member count matches expected (3 members)
   - State transitions verified via `roomStatus`: created→running after first message delivery
   - `roomDelete` succeeds after all sessions stopped
   - `room/status` after delete returns error

5. Run the test: `go test ./pkg/ari/ -count=1 -v -run TestARIMultiAgentRoundTrip -timeout 120s`

## Constraints
- The `mockagent` binary returns `StopReason: EndTurn` for any prompt — it does NOT echo message content. We verify delivery success (Delivered==true) not content fidelity.
- `roomStatus` returns a `RoomStatusResult` — check the Members slice for count and individual member state fields.
- Follow the exact session creation pattern from `TestARIRoomSendDelivery` (lines 2291-2349 of server_test.go).
- The `roomSend` helper requires all 5 args: room, targetAgent, message, senderAgent, senderId.
- After `session/stop`, wait 500ms for shim process exit before attempting room operations.

## Inputs

- `pkg/ari/server_test.go`
- `pkg/ari/server.go`
- `pkg/ari/types.go`

## Expected Output

- `pkg/ari/server_test.go`

## Verification

go test ./pkg/ari/ -count=1 -v -run TestARIMultiAgentRoundTrip -timeout 120s
