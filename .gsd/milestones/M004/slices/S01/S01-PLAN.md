# S01: Room Lifecycle and ARI Surface

**Goal:** Orchestrator can create a Room via ARI, create member sessions pointing at it, inspect realized membership via room/status, and tear down cleanly — all with the converged mesh/star/isolated communication vocabulary.
**Demo:** After this: Orchestrator can create a Room, create 2 member sessions pointing at it, query room/status to see both members, stop sessions, and delete the Room.

## Tasks
- [x] **T01: Replaced broadcast/direct/hub CommunicationMode constants with mesh/star/isolated across models, schema, room logic, and all tests** — Update CommunicationMode constants in pkg/meta/models.go from broadcast/direct/hub to mesh/star/isolated per D054. Update the schema.sql default from 'broadcast' to 'mesh'. Update CreateRoom default. Update all tests in pkg/meta/room_test.go to use the new vocabulary.

This is a prerequisite for all room ARI work — the vocabulary must be correct before we expose it through ARI.

## Steps

1. In `pkg/meta/models.go`, replace the three CommunicationMode constants:
   - `CommunicationModeBroadcast = "broadcast"` → `CommunicationModeMesh = "mesh"`
   - `CommunicationModeDirect = "direct"` → `CommunicationModeStar = "star"`
   - `CommunicationModeHub = "hub"` → `CommunicationModeIsolated = "isolated"`
2. In `pkg/meta/room.go`, update the default in CreateRoom from `CommunicationModeBroadcast` to `CommunicationModeMesh`.
3. In `pkg/meta/schema.sql`, change the rooms table default from `'broadcast'` to `'mesh'`.
4. In `pkg/meta/room_test.go`, update all test references from the old vocabulary to the new (broadcast→mesh, direct→star, hub→isolated).
5. Search the codebase for any other references to the old constants and update them.
6. Run `go build ./...` and `go test ./pkg/meta/ -count=1` to confirm everything compiles and passes.

## Must-Haves

- [ ] Three new constants: CommunicationModeMesh, CommunicationModeStar, CommunicationModeIsolated
- [ ] Old constants (Broadcast/Direct/Hub) removed
- [ ] schema.sql default updated to 'mesh'
- [ ] All pkg/meta tests pass with new vocabulary
  - Estimate: 20m
  - Files: pkg/meta/models.go, pkg/meta/room.go, pkg/meta/schema.sql, pkg/meta/room_test.go
  - Verify: go build ./... && go test ./pkg/meta/ -count=1 -v -run TestRoom
- [x] **T02: Added Room ARI types and room/create, room/status, room/delete handlers plus session/new room-existence validation** — Add the Room ARI request/response types to pkg/ari/types.go and implement the three room/* handlers in pkg/ari/server.go. This is the core of S01 — it exposes Room lifecycle through the ARI JSON-RPC surface.

Also add explicit room-existence validation to the existing session/new handler per D051: when room is non-empty, verify the room exists before creating the session.

## Steps

1. Add Room ARI types to `pkg/ari/types.go`:
   - `RoomCreateParams` with fields: Name (string, required), Labels (map[string]string, optional), Communication with Mode field (string, optional, defaults to 'mesh')
   - `RoomCreateResult` with fields: Name, CommunicationMode, CreatedAt
   - `RoomStatusParams` with field: Name (string, required)
   - `RoomStatusResult` with fields: Name, Labels, CommunicationMode, Members ([]RoomMember), CreatedAt, UpdatedAt
   - `RoomMember` with fields: AgentName, SessionId, State
   - `RoomDeleteParams` with field: Name (string, required)

2. Add room/* handlers to `pkg/ari/server.go`:
   - Register 'room/create', 'room/status', 'room/delete' in the Handle switch statement
   - `handleRoomCreate`: unmarshal params, validate name non-empty, map communication mode string to CommunicationMode constant (default 'mesh'), call store.CreateRoom, return result
   - `handleRoomStatus`: unmarshal params, call store.GetRoom (return error if nil), call store.ListSessions with Room filter to get members, build RoomMember list from matching sessions, return result
   - `handleRoomDelete`: unmarshal params, call store.ListSessions with Room filter, check no non-stopped sessions exist (return error if active members), call store.DeleteRoom, return result

3. Add room-existence validation to `handleSessionNew`:
   - After unmarshaling params, if p.Room is non-empty: call store.GetRoom(ctx, p.Room)
   - If room not found, return InvalidParams error: 'room "X" does not exist; call room/create first'
   - If p.Room is non-empty and p.RoomAgent is empty, return InvalidParams error: 'roomAgent is required when room is specified'

## Must-Haves

- [ ] RoomCreateParams/Result, RoomStatusParams/Result, RoomMember, RoomDeleteParams types in types.go
- [ ] room/create handler creates room via store and returns result
- [ ] room/status handler returns room metadata + realized member list
- [ ] room/delete handler refuses deletion when active members exist, succeeds otherwise
- [ ] session/new validates room existence when room field is non-empty (D051)
- [ ] session/new requires roomAgent when room is specified
  - Estimate: 45m
  - Files: pkg/ari/types.go, pkg/ari/server.go
  - Verify: go build ./... && go vet ./pkg/ari/
- [x] **T03: Added 5 integration tests exercising full Room lifecycle through ARI JSON-RPC: create→members→status→delete, duplicate rejection, active-member guard, room-existence validation, and communication mode coverage** — Add comprehensive integration tests to pkg/ari/server_test.go exercising the full Room lifecycle through the ARI JSON-RPC surface. These tests are the slice's verification — they prove the demo claim.

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
  - Estimate: 45m
  - Files: pkg/ari/server_test.go
  - Verify: go test ./pkg/ari/ -count=1 -v -run 'TestARIRoom|TestARISessionNewRoom' && go test ./pkg/ari/ -count=1 -short
