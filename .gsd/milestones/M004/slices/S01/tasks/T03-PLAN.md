---
estimated_steps: 28
estimated_files: 1
skills_used: []
---

# T03: Add ARI room lifecycle integration tests

Add comprehensive integration tests to pkg/ari/server_test.go exercising the full Room lifecycle through the ARI JSON-RPC surface. These tests are the slice's verification — they prove the demo claim.

## Steps

1. Add helper functions to `pkg/ari/server_test.go` (or `pkg/ari/room_test.go` if cleaner) for room RPC calls:
   - `roomCreate(conn, name, mode string, labels map[string]string)` → calls 'room/create'
   - `roomStatus(conn, name string)` → calls 'room/status'
   - `roomDelete(conn, name string)` → calls 'room/delete'

2. Add `TestARIRoomLifecycle` — the primary end-to-end test:
   - Create a room ('test-room', mode='mesh')
   - Prepare a workspace (emptyDir)
   - Create 2 sessions with room='test-room', roomAgent='agent-a' and 'agent-b'
   - Call room/status → verify 2 members with correct agentName/sessionId/state
   - Stop both sessions (session/stop)
   - Delete the room (room/delete) → verify success
   - Call room/status → verify room not found error

3. Add `TestARIRoomCreateDuplicate` — verify room/create rejects duplicate names

4. Add `TestARIRoomDeleteWithActiveMembers` — verify room/delete rejects when non-stopped sessions exist

5. Add `TestARISessionNewRoomValidation` — verify session/new rejects:
   - room='nonexistent' → error mentioning room/create
   - room='test-room' with empty roomAgent → error requiring roomAgent

6. Add `TestARIRoomCommunicationModes` — create rooms with mesh/star/isolated, verify room/status returns correct mode

7. Run all ARI tests to ensure nothing is broken.

## Must-Haves

- [ ] TestARIRoomLifecycle passes: create room → 2 sessions → status shows members → stop → delete
- [ ] TestARIRoomCreateDuplicate passes
- [ ] TestARIRoomDeleteWithActiveMembers passes
- [ ] TestARISessionNewRoomValidation passes (D051 enforcement)
- [ ] TestARIRoomCommunicationModes passes (mesh/star/isolated)
- [ ] All existing ARI tests still pass

## Inputs

- ``pkg/ari/server_test.go` — existing test harness (newTestHarness, newSessionTestHarness) and RPC patterns`
- ``pkg/ari/types.go` — Room ARI types from T02`
- ``pkg/ari/server.go` — room/* handlers from T02`

## Expected Output

- ``pkg/ari/server_test.go` — Room lifecycle integration tests`

## Verification

go test ./pkg/ari/ -count=1 -v -run 'TestARIRoom|TestARISessionNewRoom' && go test ./pkg/ari/ -count=1 -short
