---
estimated_steps: 1
estimated_files: 4
skills_used: []
---

# T01: Create pkg/meta/ foundation with Store, models, and schema initialization

Create pkg/meta/ package foundation: Store struct wrapping *sql.DB, NewStore with schema initialization using embedded SQL, Close method, BeginTx for transactions. Define Session, Workspace, Room model structs matching database schema. Session has ID, RuntimeClass, WorkspaceID, Room, RoomAgent, Labels (JSON), State, timestamps. Workspace has ID, Name, Path, Source (JSON), Status, RefCount, timestamp. Room has Name, Labels (JSON), CommunicationMode, timestamp. Schema includes sessions, workspaces, rooms, workspace_refs (for ref counting), and schema_version tables with foreign keys enabled. Use go-sqlite3 driver with _journal_mode=WAL, _foreign_keys=on, _busy_timeout=5000. Create basic smoke test verifying NewStore creates tables.

## Inputs

- `pkg/workspace/spec.go`
- `pkg/agentd/config.go`

## Expected Output

- `pkg/meta/store.go`
- `pkg/meta/models.go`
- `pkg/meta/schema.sql`
- `pkg/meta/store_test.go`

## Verification

go test ./pkg/meta/... -v -run TestNewStore

## Observability Impact

Signals added: log statements on Store initialization, schema creation, and Close. Inspection: database file at MetaDB path can be queried with sqlite3 CLI. Failure state: connection errors surface on NewStore, schema errors logged
