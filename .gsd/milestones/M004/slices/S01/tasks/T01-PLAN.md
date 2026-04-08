---
estimated_steps: 17
estimated_files: 4
skills_used: []
---

# T01: Converge communication mode vocabulary to mesh/star/isolated

Update CommunicationMode constants in pkg/meta/models.go from broadcast/direct/hub to mesh/star/isolated per D054. Update the schema.sql default from 'broadcast' to 'mesh'. Update CreateRoom default. Update all tests in pkg/meta/room_test.go to use the new vocabulary.

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

## Inputs

- ``pkg/meta/models.go` — existing CommunicationMode constants to replace`
- ``pkg/meta/room.go` — CreateRoom default to update`
- ``pkg/meta/schema.sql` — rooms table DDL with old default`
- ``pkg/meta/room_test.go` — tests using old vocabulary`

## Expected Output

- ``pkg/meta/models.go` — mesh/star/isolated constants`
- ``pkg/meta/room.go` — CreateRoom defaults to mesh`
- ``pkg/meta/schema.sql` — rooms table default 'mesh'`
- ``pkg/meta/room_test.go` — tests using new vocabulary`

## Verification

go build ./... && go test ./pkg/meta/ -count=1 -v -run TestRoom
