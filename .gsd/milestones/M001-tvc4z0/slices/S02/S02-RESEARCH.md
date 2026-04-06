# S02: Metadata Store (SQLite) — Research

**Date:** 2026-04-03

## Summary

This slice implements the SQLite-based metadata store for persisting session, workspace, and room records. The design follows the pattern established in the agentd.md design doc, which maps containerd's BoltDB metadata storage to SQLite for agentd. The metadata store is the foundation for session management (S04), process management (S05), and ARI service (S06) — all downstream components depend on this store for state persistence and restart recovery.

The current Registry in `pkg/ari/registry.go` is an in-memory map-based implementation that will be replaced by database-backed persistence. The key challenge is designing a schema that supports the Session/Workspace/Room data models from the design doc while providing transaction support for atomic operations.

## Recommendation

Use **github.com/mattn/go-sqlite3** as the SQLite driver. It's the most mature, widely-used SQLite driver for Go (trust: 10/10 from library lookup), and the CGO requirement is acceptable for a server daemon. Configure with:
- `_journal_mode=WAL` for better concurrent read/write performance
- `_foreign_keys=on` for referential integrity
- `_busy_timeout=5000` for busy timeout (5 seconds)
- `cache=shared` for shared cache mode

Implement the store in a new package `pkg/meta/` with a `Store` struct that wraps `*sql.DB`. Use the standard `database/sql` interface with prepared statements for type-safe queries.

## Implementation Landscape

### Key Files

- `pkg/ari/registry.go` — Current in-memory Registry that will be replaced. Contains WorkspaceMeta struct with Id, Name, Path, Spec, Status, RefCount, Refs. This is the model to migrate to database.
- `pkg/agentd/config.go` — Config struct has `MetaDB string` field for database path. Already parsed from config.yaml.
- `cmd/agentd/main.go` — Daemon entry point. Needs to initialize MetaStore and pass to managers.
- `docs/design/agentd/agentd.md` — Defines Session, Room, and metadata storage patterns. Maps to containerd's BoltDB approach.
- `docs/design/agentd/ari-spec.md` — ARI spec shows full data model: Session (id, runtimeClass, workspace, room, roomAgent, labels, state), Workspace (id, name, path, status, refs), Room (name, members, communication).

### Database Schema Design

Three core tables based on design doc requirements:

```sql
-- Sessions table
CREATE TABLE sessions (
    id TEXT PRIMARY KEY,
    runtime_class TEXT NOT NULL,
    workspace_id TEXT NOT NULL,
    room TEXT,
    room_agent TEXT,
    labels TEXT,  -- JSON-encoded map[string]string
    state TEXT NOT NULL DEFAULT 'created',  -- created, running, paused:warm, paused:cold, stopped
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    FOREIGN KEY (workspace_id) REFERENCES workspaces(id) ON DELETE RESTRICT
);

-- Workspaces table  
CREATE TABLE workspaces (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL,
    path TEXT NOT NULL,
    source TEXT,  -- JSON-encoded WorkspaceSpec.Source
    status TEXT NOT NULL DEFAULT 'ready',  -- ready, preparing, error
    ref_count INTEGER NOT NULL DEFAULT 0,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

-- Rooms table
CREATE TABLE rooms (
    name TEXT PRIMARY KEY,
    labels TEXT,  -- JSON-encoded map[string]string
    communication_mode TEXT NOT NULL DEFAULT 'mesh',  -- mesh, star, isolated
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

-- Workspace references table (for tracking which sessions reference which workspaces)
CREATE TABLE workspace_refs (
    workspace_id TEXT NOT NULL,
    session_id TEXT NOT NULL,
    PRIMARY KEY (workspace_id, session_id),
    FOREIGN KEY (workspace_id) REFERENCES workspaces(id) ON DELETE CASCADE,
    FOREIGN KEY (session_id) REFERENCES sessions(id) ON DELETE CASCADE
);

-- Schema version table for migrations
CREATE TABLE schema_version (
    version INTEGER PRIMARY KEY,
    applied_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);
```

**Design notes:**
- `labels` stored as JSON text (simple for v1, can normalize later if query needs arise)
- `source` stored as JSON to preserve full WorkspaceSpec.Source discriminated union
- `workspace_refs` enables efficient ref counting and debugging
- Foreign keys prevent deleting workspace with active sessions
- Schema version table enables future migrations

### Store Interface Design

```go
// pkg/meta/store.go
package meta

type Store struct {
    db *sql.DB
}

func NewStore(dbPath string) (*Store, error)
func (s *Store) Close() error

// Transaction support
func (s *Store) BeginTx(ctx context.Context) (*sql.Tx, error)

// Session operations
func (s *Store) CreateSession(ctx context.Context, sess *Session) error
func (s *Store) GetSession(ctx context.Context, id string) (*Session, error)
func (s *Store) ListSessions(ctx context.Context, filter SessionFilter) ([]*Session, error)
func (s *Store) UpdateSession(ctx context.Context, id string, opts UpdateSessionOpts) error
func (s *Store) DeleteSession(ctx context.Context, id string) error

// Workspace operations
func (s *Store) CreateWorkspace(ctx context.Context, ws *Workspace) error
func (s *Store) GetWorkspace(ctx context.Context, id string) (*Workspace, error)
func (s *Store) ListWorkspaces(ctx context.Context) ([]*Workspace, error)
func (s *Store) UpdateWorkspaceStatus(ctx context.Context, id string, status string) error
func (s *Store) DeleteWorkspace(ctx context.Context, id string) error
func (s *Store) AcquireWorkspace(ctx context.Context, workspaceID, sessionID string) error
func (s *Store) ReleaseWorkspace(ctx context.Context, workspaceID, sessionID string) (int, error)

// Room operations
func (s *Store) CreateRoom(ctx context.Context, room *Room) error
func (s *Store) GetRoom(ctx context.Context, name string) (*Room, error)
func (s *Store) ListRooms(ctx context.Context) ([]*Room, error)
func (s *Store) DeleteRoom(ctx context.Context, name string) error
```

### Build Order

1. **Create pkg/meta/store.go** — Store struct, NewStore with schema initialization, Close
2. **Create pkg/meta/models.go** — Session, Workspace, Room structs matching database schema
3. **Create pkg/meta/session.go** — Session CRUD operations
4. **Create pkg/meta/workspace.go** — Workspace CRUD operations, Acquire/Release for ref counting
5. **Create pkg/meta/room.go** — Room CRUD operations
6. **Create pkg/meta/store_test.go** — Integration tests using :memory: SQLite
7. **Update cmd/agentd/main.go** — Initialize Store from config.MetaDB, pass to future managers

### Migration Strategy

For v1, use embedded schema with version check:
1. On startup, check `schema_version` table
2. If table doesn't exist, create all tables (fresh database)
3. If version < current, run migration scripts (future enhancement)

This keeps v1 simple while providing an extension point for future schema changes.

### Testing Strategy

- Use `:memory:` SQLite with `cache=shared` for tests
- Each test gets isolated database via `file:test-<uuid>?mode=memory&cache=shared`
- Test CRUD operations for each entity type
- Test transaction rollback scenarios
- Test foreign key constraints (delete workspace with active session should fail)
- Test ref counting behavior (Acquire increments, Release decrements)

## Skills Discovered

- `johnlindquist/claude@db` — Generic database skill (62 installs), not SQLite-specific enough to warrant installation
- No SQLite-specific skill with high install count directly relevant to this implementation

## Constraints and Risks

### Constraints from Design Doc

1. **Session state is persisted**, but process state (pid, status) is NOT — that lives in agent-shim's state.json on tmpfs
2. **Workspace refs must be accurate** — cleanup fails if RefCount > 0
3. **Room membership is derived from session metadata** — not stored redundantly

### Risks

1. **Concurrent access** — SQLite handles concurrent reads well with WAL mode, but writes are serialized. For agentd's use case (metadata operations, not high-throughput), this is acceptable.
2. **Database corruption** — Use WAL mode and proper shutdown to minimize risk. Database file should be on persistent storage, not tmpfs.
3. **Migration complexity** — Start with simple embedded schema, defer complex migration framework until needed.

### Forward Intelligence from S01

S01 established:
- `pkg/agentd/config.go` parses MetaDB field from config.yaml
- `cmd/agentd/main.go` initializes components but currently has no metadata store
- The daemon entry point is ready to accept Store initialization

S02 must ensure:
- Store initialization happens early in daemon startup (before SessionManager, etc.)
- Store Close happens during graceful shutdown
- Database file parent directory exists (create if needed)