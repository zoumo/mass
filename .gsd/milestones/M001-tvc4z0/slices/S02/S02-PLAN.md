# S02: Metadata Store (SQLite)

**Goal:** SQLite-based metadata store persists session/workspace/room records with CRUD operations and transaction support, enabling session manager and process manager to store and retrieve state across daemon restarts
**Demo:** After this: SQLite metadata store created, CRUD operations work, schema in place

## Tasks
- [x] **T01: Created pkg/meta/ foundation with Store, models, and SQLite schema initialization using go-sqlite3 with WAL mode and foreign keys.** — Create pkg/meta/ package foundation: Store struct wrapping *sql.DB, NewStore with schema initialization using embedded SQL, Close method, BeginTx for transactions. Define Session, Workspace, Room model structs matching database schema. Session has ID, RuntimeClass, WorkspaceID, Room, RoomAgent, Labels (JSON), State, timestamps. Workspace has ID, Name, Path, Source (JSON), Status, RefCount, timestamp. Room has Name, Labels (JSON), CommunicationMode, timestamp. Schema includes sessions, workspaces, rooms, workspace_refs (for ref counting), and schema_version tables with foreign keys enabled. Use go-sqlite3 driver with _journal_mode=WAL, _foreign_keys=on, _busy_timeout=5000. Create basic smoke test verifying NewStore creates tables.
  - Estimate: 1.5h
  - Files: pkg/meta/store.go, pkg/meta/models.go, pkg/meta/schema.sql, pkg/meta/store_test.go
  - Verify: go test ./pkg/meta/... -v -run TestNewStore
- [x] **T02: Implemented Session, Workspace, Room CRUD operations with comprehensive tests** — Implement CRUD operations for all entity types. Session operations in session.go: CreateSession, GetSession, ListSessions (with filter), UpdateSession (state/labels), DeleteSession. Workspace operations in workspace.go: CreateWorkspace, GetWorkspace, ListWorkspaces, UpdateWorkspaceStatus, DeleteWorkspace, AcquireWorkspace (increments ref count, adds to workspace_refs), ReleaseWorkspace (decrements ref count, removes from workspace_refs, returns new count). Room operations in room.go: CreateRoom, GetRoom, ListRooms, DeleteRoom. Use prepared statements for all queries. Store Labels and Source as JSON text. Comprehensive tests using :memory: SQLite: CRUD round-trips for each entity, foreign key constraint (delete workspace with session fails), ref counting behavior (Acquire increments, Release decrements, cannot delete workspace with refs), transaction rollback test, ListSessions filtering.
  - Estimate: 2h
  - Files: pkg/meta/session.go, pkg/meta/workspace.go, pkg/meta/room.go, pkg/meta/session_test.go, pkg/meta/workspace_test.go, pkg/meta/room_test.go
  - Verify: go test ./pkg/meta/... -v
- [x] **T03: Wired Store initialization into agentd daemon startup and shutdown lifecycle with integration tests verifying daemon behavior with and without Store configured.** — Wire Store into agentd daemon startup and shutdown. Update cmd/agentd/main.go to: create parent directory for MetaDB path if not exists, initialize Store from cfg.MetaDB after config parsing, pass Store to future managers (placeholder for now), add Store.Close() to shutdown sequence after ARI server shutdown. Create integration test that starts agentd with minimal config including MetaDB path, verifies Store created, sends SIGTERM, verifies shutdown completes. Re-verify R001 daemon launchability with Store initialized.
  - Estimate: 45m
  - Files: cmd/agentd/main.go, pkg/meta/integration_test.go
  - Verify: go build -o bin/agentd ./cmd/agentd && go test ./pkg/meta/... -v -run TestIntegration
