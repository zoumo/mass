# S03: End-to-End Multi-Agent Integration Proof

**Goal:** Prove the full Room lifecycle with multi-agent message exchange end-to-end: create Room → bootstrap 3 agents → bidirectional message exchange via room/send → verify delivery, state transitions, and attribution → clean teardown. All via ARI.
**Demo:** After this: Full round-trip: Room create → member bootstrap → bidirectional message exchange → Room teardown. All via ARI.

## Tasks
- [x] **T01: Added TestARIMultiAgentRoundTrip proving full Room lifecycle: 3-agent bootstrap, bidirectional A↔B + A→C message delivery with state transitions, and clean teardown — all via ARI JSON-RPC** — Add the primary end-to-end integration test proving the M004 demo claim: Room create → 3 agent bootstrap → bidirectional message exchange → state verification → clean teardown.

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
  - Estimate: 30m
  - Files: pkg/ari/server_test.go
  - Verify: go test ./pkg/ari/ -count=1 -v -run TestARIMultiAgentRoundTrip -timeout 120s
- [x] **T02: Added TestARIRoomTeardownGuards proving room/delete fails with active members, session/remove fails on running sessions, and both succeed after proper stop sequence** — Add integration test proving the teardown ordering constraints: room/delete fails with active (non-stopped) members, session/remove fails on running sessions, and both succeed after proper stop sequence.

This test complements T01 by proving error paths and ordering guards work correctly during Room teardown.

## Steps

1. Open `pkg/ari/server_test.go` and add `TestARIRoomTeardownGuards` after `TestARIMultiAgentRoundTrip` (before the `var _ = (*testHarness)(nil)` suppression line).

2. Test structure:
   - Use `newSessionTestHarness(t)` for full ARI server + mockagent setup
   - `context.WithTimeout` of 120s
   - `h.dial(t, &nullHandler{})` for client connection
   - Guard with `if testing.Short() { t.Skip("requires mockagent processes") }`

3. Implement these test steps:
   a. `roomCreate(ctx, t, client, "teardown-guard-room", "mesh", nil)`
   b. `h.prepareWorkspaceForSession(ctx, t, client, "teardown-guard-ws")`
   c. `session/new` × 2 (agent-a, agent-b) in the room
   d. `roomSend` from agent-a to agent-b — makes agent-b auto-start to "running"
   e. Attempt `room/delete` while agent-b is running — assert error with `requireRPCError(t, err, jsonrpc2.CodeInvalidParams, "active member")` (the error message contains "active member(s)")
   f. Attempt `session/remove` on running agent-b — assert error with `requireRPCError(t, err, jsonrpc2.CodeInvalidParams, "")` checking for the ErrDeleteProtected pattern
   g. `session/stop` agent-b + `time.Sleep(500 * time.Millisecond)`
   h. `session/stop` agent-a (may be in created state, stop is idempotent for created→stopped)
   i. `time.Sleep(500 * time.Millisecond)` for process cleanup
   j. `room/delete` — assert success (stopped sessions don't block deletion)

4. Key assertions:
   - `room/delete` with running member returns `CodeInvalidParams` error containing "active member"
   - `session/remove` on running session returns error (ErrDeleteProtected maps to InvalidParams)
   - `room/delete` succeeds after all sessions are stopped

5. For the `session/remove` error check: look at `TestARISessionRemoveProtected` (line 1187) for the exact error assertion pattern — `requireRPCError(t, err, int64(jsonrpc2.CodeInvalidParams), "")` or check the actual error message.

6. Run: `go test ./pkg/ari/ -count=1 -v -run TestARIRoomTeardownGuards -timeout 120s`

7. Run full ARI suite to confirm no regressions: `go test ./pkg/ari/ -count=1 -short -timeout 120s`

## Constraints
- `handleRoomDelete` checks for sessions with `State != meta.SessionStateStopped` — stopped sessions are fine, only running/created ones block deletion.
- `session/remove` on a running session triggers `ErrDeleteProtected` which is mapped to `CodeInvalidParams` in the ARI handler.
- Follow the error assertion pattern from existing tests: `requireRPCError(t, err, int64(jsonrpc2.CodeInvalidParams), "substring")` where substring matches part of the error message.
- The `session/stop` call on a session in "created" state transitions it to "stopped" without error.
  - Estimate: 20m
  - Files: pkg/ari/server_test.go
  - Verify: go test ./pkg/ari/ -count=1 -v -run TestARIRoomTeardownGuards -timeout 120s && go test ./pkg/ari/ -count=1 -short -timeout 120s
