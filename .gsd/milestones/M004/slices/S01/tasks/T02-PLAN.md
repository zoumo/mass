---
estimated_steps: 26
estimated_files: 2
skills_used: []
---

# T02: Add Room ARI types and implement room/create, room/status, room/delete handlers

Add the Room ARI request/response types to pkg/ari/types.go and implement the three room/* handlers in pkg/ari/server.go. This is the core of S01 — it exposes Room lifecycle through the ARI JSON-RPC surface.

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

## Inputs

- ``pkg/ari/types.go` — existing ARI types to follow conventions from`
- ``pkg/ari/server.go` — existing handler patterns to follow`
- ``pkg/meta/models.go` — CommunicationMode constants from T01`
- ``pkg/meta/room.go` — Store.CreateRoom/GetRoom/DeleteRoom APIs`
- ``pkg/meta/session.go` — Store.ListSessions with Room filter`

## Expected Output

- ``pkg/ari/types.go` — Room ARI types added`
- ``pkg/ari/server.go` — room/* handlers + session/new room validation`

## Verification

go build ./... && go vet ./pkg/ari/
