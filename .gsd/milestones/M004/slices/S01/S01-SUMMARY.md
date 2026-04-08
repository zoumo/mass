---
id: S01
parent: M004
milestone: M004
provides:
  - ["room/create, room/status, room/delete ARI handlers", "RoomCreateParams/Result, RoomStatusParams/Result, RoomMember, RoomDeleteParams types", "mesh/star/isolated CommunicationMode constants", "Room-existence validation in session/new (D051)"]
requires:
  []
affects:
  - ["S02 — Routing Engine builds on room membership to implement message delivery", "S03 — End-to-End proof exercises the full room lifecycle established here"]
key_files:
  - ["pkg/meta/models.go", "pkg/meta/room.go", "pkg/meta/schema.sql", "pkg/meta/room_test.go", "pkg/meta/session_test.go", "pkg/ari/types.go", "pkg/ari/server.go", "pkg/ari/server_test.go"]
key_decisions:
  - ["D054 implemented: mesh/star/isolated replaces broadcast/direct/hub", "D051 enforced: room/create required before session/new can reference a room", "RoomCreateParams uses nested Communication.Mode field for extensibility", "Sessions created in 'created' state (no shim start) sufficient for room membership testing"]
patterns_established:
  - ["Room ARI handler pattern: validate params → call store → build result with realized member list", "Active-member guard pattern: room/delete checks for non-stopped sessions before allowing deletion", "Room-existence validation in session/new: fail-fast with actionable error message suggesting room/create", "Test harness split: newTestHarness for room-only tests, newSessionTestHarness for tests needing real sessions"]
observability_surfaces:
  - none
drill_down_paths:
  - [".gsd/milestones/M004/slices/S01/tasks/T01-SUMMARY.md", ".gsd/milestones/M004/slices/S01/tasks/T02-SUMMARY.md", ".gsd/milestones/M004/slices/S01/tasks/T03-SUMMARY.md"]
duration: ""
verification_result: passed
completed_at: 2026-04-08T04:47:16.693Z
blocker_discovered: false
---

# S01: Room Lifecycle and ARI Surface

**Exposed Room lifecycle through ARI JSON-RPC: room/create, room/status, room/delete handlers, converged communication vocabulary to mesh/star/isolated, and enforced room-existence validation on session/new — all proven by 5 integration tests covering the full create→members→status→delete flow.**

## What Happened

This slice turned the Room from a metadata-only concept into a realized runtime entity managed through the ARI JSON-RPC surface.

**T01 — Communication Vocabulary Convergence.** Replaced the legacy broadcast/direct/hub CommunicationMode constants with the design-doc vocabulary: mesh/star/isolated (per D054). Updated `pkg/meta/models.go` constants, `pkg/meta/room.go` CreateRoom default, `pkg/meta/schema.sql` DDL default, and all tests across `pkg/meta/room_test.go` (8 tests) and `pkg/meta/session_test.go` (2 references). This was prerequisite work — the vocabulary had to be correct before exposing it through ARI.

**T02 — Room ARI Handlers.** Added six Room ARI types to `pkg/ari/types.go` (RoomCreateParams, RoomCommunication, RoomCreateResult, RoomStatusParams, RoomStatusResult, RoomMember, RoomDeleteParams) and implemented three JSON-RPC handlers in `pkg/ari/server.go`:
- `room/create`: validates name, maps communication mode string to constant (defaults to mesh), persists via store.CreateRoom
- `room/status`: fetches room metadata + lists sessions in the room to build a realized member list with agentName/sessionId/state
- `room/delete`: refuses deletion when non-stopped sessions exist (active-member guard), calls store.DeleteRoom on success

Also added room-existence validation to `handleSessionNew` per D051: when `room` is non-empty, the handler validates the room exists and requires `roomAgent` to be set.

**T03 — Integration Tests.** Added 5 comprehensive tests to `pkg/ari/server_test.go`:
- `TestARIRoomLifecycle`: full end-to-end — create room → create 2 sessions with room/roomAgent → room/status shows both members → remove sessions → delete room → verify not-found
- `TestARIRoomCreateDuplicate`: duplicate room name rejection
- `TestARIRoomDeleteWithActiveMembers`: active-member guard prevents deletion
- `TestARISessionNewRoomValidation`: D051 enforcement — nonexistent room rejected, missing roomAgent rejected
- `TestARIRoomCommunicationModes`: mesh/star/isolated modes plus default-to-mesh verified

All 5 new tests pass. All pre-existing ARI tests continue to pass (full suite: 9s).

## Verification

**Slice-level verification — all pass:**

1. `go build ./...` — clean compile (exit 0)
2. `go test ./pkg/meta/ -count=1 -v -run TestRoom` — 8/8 room tests pass (2.2s)
3. `go test ./pkg/ari/ -count=1 -v -run 'TestARIRoom|TestARISessionNewRoom'` — 5/5 new tests pass (2.6s)
4. `go test ./pkg/ari/ -count=1 -short` — full ARI suite passes (9.0s)

Demo claim verified: TestARIRoomLifecycle proves the exact flow — orchestrator creates a Room, creates 2 member sessions, queries room/status to see both members, stops sessions, and deletes the Room.

## Requirements Advanced

- R041 — Room lifecycle now has a realized runtime surface (room/create, room/status, room/delete) — advancing from design-only to working ARI handlers. Routing and delivery semantics remain for S02.

## Requirements Validated

None.

## New Requirements Surfaced

None.

## Requirements Invalidated or Re-scoped

None.

## Deviations

Also updated pkg/meta/session_test.go (2 references to CommunicationModeBroadcast) which was not in the original T01 plan — discovered during codebase search for old constants.

## Known Limitations

Room labels are stored but not validated or queryable by label. Communication mode is stored as a string without enum validation at the ARI layer (invalid modes would be rejected by the store but not with a clear error). Room membership is derived from session queries — no dedicated membership table — which is fine for current scale but may need indexing for large rooms.

## Follow-ups

None.

## Files Created/Modified

- `pkg/meta/models.go` — Replaced CommunicationMode constants: broadcast→mesh, direct→star, hub→isolated
- `pkg/meta/room.go` — Updated CreateRoom default from Broadcast to Mesh
- `pkg/meta/schema.sql` — Changed rooms table default from 'broadcast' to 'mesh'
- `pkg/meta/room_test.go` — Updated all 8 room tests to use new vocabulary
- `pkg/meta/session_test.go` — Updated 2 references from CommunicationModeBroadcast to CommunicationModeMesh
- `pkg/ari/types.go` — Added 7 Room ARI types: RoomCreateParams, RoomCommunication, RoomCreateResult, RoomStatusParams, RoomStatusResult, RoomMember, RoomDeleteParams
- `pkg/ari/server.go` — Added room/create, room/status, room/delete handlers and room-existence validation in session/new
- `pkg/ari/server_test.go` — Added 5 integration tests: RoomLifecycle, CreateDuplicate, DeleteWithActiveMembers, SessionNewRoomValidation, CommunicationModes
