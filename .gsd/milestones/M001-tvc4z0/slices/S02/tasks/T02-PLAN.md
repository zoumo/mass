---
estimated_steps: 1
estimated_files: 6
skills_used: []
---

# T02: Implement Session, Workspace, Room CRUD operations with tests

Implement CRUD operations for all entity types. Session operations in session.go: CreateSession, GetSession, ListSessions (with filter), UpdateSession (state/labels), DeleteSession. Workspace operations in workspace.go: CreateWorkspace, GetWorkspace, ListWorkspaces, UpdateWorkspaceStatus, DeleteWorkspace, AcquireWorkspace (increments ref count, adds to workspace_refs), ReleaseWorkspace (decrements ref count, removes from workspace_refs, returns new count). Room operations in room.go: CreateRoom, GetRoom, ListRooms, DeleteRoom. Use prepared statements for all queries. Store Labels and Source as JSON text. Comprehensive tests using :memory: SQLite: CRUD round-trips for each entity, foreign key constraint (delete workspace with session fails), ref counting behavior (Acquire increments, Release decrements, cannot delete workspace with refs), transaction rollback test, ListSessions filtering.

## Inputs

- `pkg/meta/store.go`
- `pkg/meta/models.go`

## Expected Output

- `pkg/meta/session.go`
- `pkg/meta/workspace.go`
- `pkg/meta/room.go`
- `pkg/meta/session_test.go`
- `pkg/meta/workspace_test.go`
- `pkg/meta/room_test.go`

## Verification

go test ./pkg/meta/... -v

## Observability Impact

Signals added: none (CRUD operations return errors directly). Inspection: database queries show entity state. Failure state: constraint violations return specific errors (foreign key, unique)
