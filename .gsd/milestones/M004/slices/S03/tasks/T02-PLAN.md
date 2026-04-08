---
estimated_steps: 32
estimated_files: 1
skills_used: []
---

# T02: Write TestARIRoomTeardownGuards — teardown ordering constraint proof

Add integration test proving the teardown ordering constraints: room/delete fails with active (non-stopped) members, session/remove fails on running sessions, and both succeed after proper stop sequence.

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

## Inputs

- `pkg/ari/server_test.go`
- `pkg/ari/server.go`

## Expected Output

- `pkg/ari/server_test.go`

## Verification

go test ./pkg/ari/ -count=1 -v -run TestARIRoomTeardownGuards -timeout 120s && go test ./pkg/ari/ -count=1 -short -timeout 120s
