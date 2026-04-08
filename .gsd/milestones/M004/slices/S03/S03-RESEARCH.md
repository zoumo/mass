# S03 Research: End-to-End Multi-Agent Integration Proof

## Calibration: Light Research

This is straightforward integration testing — composing existing, proven building blocks (S01 Room lifecycle + S02 room/send routing) into a comprehensive end-to-end test. All test infrastructure, helpers, and the mockagent binary are already in place. No new technology, unfamiliar APIs, or architectural decisions needed.

## Summary

S03 proves the full Room lifecycle with multi-agent message exchange in one integration test: create Room → bootstrap 2+ agents → bidirectional message exchange via room/send → verify delivery and state → clean teardown. This is the capstone proof for M004.

### What Exists (All Proven by S01/S02)

| Component | Location | Status |
|---|---|---|
| `room/create` handler | `pkg/ari/server.go:208` | ✅ Proven by `TestARIRoomLifecycle` |
| `room/status` handler | `pkg/ari/server.go:210` | ✅ Proven by `TestARIRoomLifecycle` |
| `room/send` handler | `pkg/ari/server.go:212` | ✅ Proven by `TestARIRoomSendDelivery` |
| `room/delete` handler | `pkg/ari/server.go:214` | ✅ Proven by `TestARIRoomLifecycle` |
| `deliverPrompt` helper | `pkg/ari/server.go:507` | ✅ Shared by session/prompt and room/send |
| `room-mcp-server` binary | `cmd/room-mcp-server/main.go` | ✅ Built, not tested in integration |
| `newSessionTestHarness` | `pkg/ari/server_test.go:136` | ✅ Sets up full ARI server with mockagent |
| Test helpers | `pkg/ari/server_test.go:1850-2120` | ✅ `roomCreate`, `roomStatus`, `roomDelete`, `roomSend` |
| `mockagent` binary | `internal/testutil/mockagent/main.go` | ✅ Returns `end_turn` for any prompt |

### What's NOT Yet Tested (the S03 gap)

1. **Bidirectional messaging**: Only A→B tested (`TestARIRoomSendDelivery`). No test sends B→A.
2. **Full lifecycle in one test**: `TestARIRoomLifecycle` (S01) does create→status→delete but NO message exchange. `TestARIRoomSendDelivery` (S02) does create→send but NO teardown or status checks.
3. **Room status with running members**: All status checks show members in "created" state. No test verifies `room/status` after agents are auto-started to "running".
4. **Clean teardown after messaging**: No test does stop→remove→delete after room/send.
5. **3+ agent scenario**: All existing tests use exactly 2 agents. No 3-agent topology tested.

### Key Patterns to Follow

- **Test harness**: Use `newSessionTestHarness(t)` — it sets up the full ARI server, mockagent runtime class, workspace manager, and handles cleanup on test teardown.
- **Client connection**: `h.dial(t, &nullHandler{})` returns a `jsonrpc2.Conn` for ARI calls.
- **Workspace setup**: `h.prepareWorkspaceForSession(ctx, t, client, "name")` creates a workspace and returns `(workspaceId, workspacePath)`.
- **Session creation**: `client.Call(ctx, "session/new", ari.SessionNewParams{...}, &result)`.
- **Room helpers**: `roomCreate(ctx, t, client, name, mode, labels)`, `roomStatus(ctx, t, client, name)`, `roomSend(ctx, t, client, room, targetAgent, message, senderAgent, senderId)`, `roomDelete(ctx, t, client, name)`.
- **Cleanup sequence for running sessions**: `session/stop` (transitions to "stopped") → wait 500ms → `session/remove` (deletes from store) → `room/delete` (succeeds when all members stopped/removed).
- **Timeout**: 120s for tests with real processes (`context.WithTimeout`).

### Constraint: mockagent Behavior

The `mockagent` binary (at `internal/testutil/mockagent/main.go`):
- Responds to any `Prompt` with `StopReason: EndTurn`
- Sends two `SessionUpdate` notifications: a write attempt result + "mock response" text
- Does NOT echo the message content — we can verify delivery succeeded (`Delivered==true`) but cannot verify the attributed message text reached the agent

This means the integration test proves **routing correctness** (right agent receives the prompt) and **delivery success** (stopReason returned), but NOT **message content fidelity**. This is acceptable for L2 — content fidelity can be verified when a more sophisticated test agent is available.

### Constraint: room/delete Active-Member Guard

`handleRoomDelete` at `pkg/ari/server.go:1140` refuses deletion when non-stopped sessions exist. For clean teardown, the test must:
1. `session/stop` all running sessions
2. Wait for shim process to fully exit (500ms sleep per S02 pattern)
3. Then `session/remove` each session  
4. Then `room/delete` — only succeeds after all members are removed or stopped

Note: `room/delete` actually checks for `State != stopped` so stopped sessions are fine — they don't need to be removed first. But `session/remove` on a running session returns `ErrDeleteProtected`.

## Recommendation

### Test Structure: 2 Integration Tests in `pkg/ari/server_test.go`

**Test 1: `TestARIMultiAgentRoundTrip`** — The primary end-to-end proof
1. `room/create` "integration-room" with mesh mode
2. `workspace/prepare` shared workspace
3. `session/new` × 3 agents (agent-a, agent-b, agent-c) in the room
4. `room/status` — verify 3 members, all "created" state
5. `room/send` agent-a → agent-b — verify Delivered==true
6. `room/status` — verify agent-b is "running" (auto-started)
7. `room/send` agent-b → agent-a — verify Delivered==true (bidirectional proof)
8. `room/status` — verify agent-a and agent-b are "running"
9. `room/send` agent-a → agent-c — verify Delivered==true (3rd agent)
10. `room/status` — verify all 3 running
11. `session/stop` × 3, wait for exit
12. `session/remove` × 3 (or just stop — delete checks for non-stopped)
13. `room/delete` — verify success
14. `room/status` — verify not-found error

**Test 2: `TestARIRoomTeardownGuards`** — Proves teardown ordering constraints
1. Create room + 2 sessions + send message (one agent running)
2. Attempt `room/delete` while agent-b is running → expect error
3. Attempt `session/remove` while agent-b is running → expect error (ErrDeleteProtected)
4. `session/stop` agent-b → wait
5. `room/delete` now succeeds (stopped sessions allowed)

### Implementation Notes

- All code goes in `pkg/ari/server_test.go` — no production code changes needed
- Uses existing test helpers (`roomCreate`, `roomSend`, `roomStatus`, `roomDelete`)
- Uses existing `newSessionTestHarness` — no new test infrastructure
- The `requireRPCError` helper at line 1833 is available for error assertions
- Tests should NOT be short-skippable (they need real processes) — guard with `if testing.Short() { t.Skip("requires mockagent processes") }`

### Requirements This Slice Advances

- **R041** (active, differentiator): "realized Room runtime with explicit ownership, routing, and delivery semantics" — S03 provides the integration proof that the realized Room runtime works end-to-end. This is the validation evidence for R041.
- **R024** (deferred, differentiator): "Room Manager with member tracking, MCP tool injection, message routing" — S03 proves member tracking and message routing work together. R024 can be advanced to validated if the milestone chooses.

## Implementation Landscape

```
pkg/ari/server_test.go  (THE file — add 2 tests, ~150-200 lines total)
  ├── TestARIMultiAgentRoundTrip   (primary proof: 3 agents, bidirectional, full lifecycle)
  └── TestARIRoomTeardownGuards    (ordering constraints during teardown)

Existing infrastructure used (no changes needed):
  ├── newSessionTestHarness(t)     — full ARI server + mockagent
  ├── roomCreate/roomStatus/roomSend/roomDelete helpers
  ├── h.prepareWorkspaceForSession — workspace setup
  ├── requireRPCError              — error assertion
  └── internal/testutil/mockagent  — stub agent binary
```

No production code, no new files, no new dependencies. Pure integration test composition.

## Skills Discovered

No new skills needed — this is Go integration testing using existing project infrastructure.