-- OAR Metadata Store Schema
-- Version 1

-- Enable foreign keys (also set in connection string)
PRAGMA foreign_keys = ON;

-- Schema version tracking table
CREATE TABLE IF NOT EXISTS schema_version (
    version INTEGER PRIMARY KEY,
    applied_at DATETIME DEFAULT CURRENT_TIMESTAMP
);

-- Rooms table: communication rooms for multi-agent coordination
CREATE TABLE IF NOT EXISTS rooms (
    name TEXT PRIMARY KEY,
    labels TEXT DEFAULT '{}',  -- JSON map of labels
    communication_mode TEXT NOT NULL DEFAULT 'broadcast',
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
);

-- Workspaces table: workspace preparation records
CREATE TABLE IF NOT EXISTS workspaces (
    id TEXT PRIMARY KEY,  -- UUID
    name TEXT NOT NULL,
    path TEXT NOT NULL,  -- filesystem path to workspace directory
    source TEXT NOT NULL DEFAULT '{}',  -- JSON Source spec (git/emptyDir/local)
    status TEXT NOT NULL DEFAULT 'active',  -- active, inactive, deleted
    ref_count INTEGER NOT NULL DEFAULT 0,  -- number of sessions using this workspace
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
);

-- Sessions table: agent runtime session records
CREATE TABLE IF NOT EXISTS sessions (
    id TEXT PRIMARY KEY,  -- UUID
    runtime_class TEXT NOT NULL,
    workspace_id TEXT NOT NULL,  -- foreign key to workspaces.id
    room TEXT DEFAULT '',  -- room name, references rooms.name (optional)
    room_agent TEXT DEFAULT '',  -- agent name/ID within the room
    labels TEXT DEFAULT '{}',  -- JSON map of labels
    state TEXT NOT NULL DEFAULT 'running',  -- running, stopped, paused, error
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    FOREIGN KEY (workspace_id) REFERENCES workspaces(id) ON DELETE RESTRICT,
    FOREIGN KEY (room) REFERENCES rooms(name) ON DELETE SET NULL
);

-- Workspace refs table: tracks workspace-session references for ref counting
CREATE TABLE IF NOT EXISTS workspace_refs (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    workspace_id TEXT NOT NULL,
    session_id TEXT NOT NULL,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    FOREIGN KEY (workspace_id) REFERENCES workspaces(id) ON DELETE CASCADE,
    FOREIGN KEY (session_id) REFERENCES sessions(id) ON DELETE CASCADE,
    UNIQUE(workspace_id, session_id)
);

-- Indexes for common queries
CREATE INDEX IF NOT EXISTS idx_sessions_workspace_id ON sessions(workspace_id);
CREATE INDEX IF NOT EXISTS idx_sessions_room ON sessions(room);
CREATE INDEX IF NOT EXISTS idx_sessions_state ON sessions(state);
CREATE INDEX IF NOT EXISTS idx_workspaces_status ON workspaces(status);
CREATE INDEX IF NOT EXISTS idx_workspaces_name ON workspaces(name);
CREATE INDEX IF NOT EXISTS idx_workspace_refs_workspace_id ON workspace_refs(workspace_id);
CREATE INDEX IF NOT EXISTS idx_workspace_refs_session_id ON workspace_refs(session_id);

-- Trigger to update workspace ref_count on insert
CREATE TRIGGER IF NOT EXISTS trg_workspace_refs_insert
    AFTER INSERT ON workspace_refs
    BEGIN
        UPDATE workspaces 
        SET ref_count = ref_count + 1, updated_at = CURRENT_TIMESTAMP
        WHERE id = NEW.workspace_id;
    END;

-- Trigger to update workspace ref_count on delete
CREATE TRIGGER IF NOT EXISTS trg_workspace_refs_delete
    AFTER DELETE ON workspace_refs
    BEGIN
        UPDATE workspaces 
        SET ref_count = ref_count - 1, updated_at = CURRENT_TIMESTAMP
        WHERE id = OLD.workspace_id;
    END;

-- Trigger to update updated_at on sessions
CREATE TRIGGER IF NOT EXISTS trg_sessions_updated
    AFTER UPDATE ON sessions
    FOR EACH ROW WHEN OLD.updated_at = NEW.updated_at
    BEGIN
        UPDATE sessions SET updated_at = CURRENT_TIMESTAMP WHERE id = NEW.id;
    END;

-- Trigger to update updated_at on workspaces
CREATE TRIGGER IF NOT EXISTS trg_workspaces_updated
    AFTER UPDATE ON workspaces
    FOR EACH ROW WHEN OLD.updated_at = NEW.updated_at
    BEGIN
        UPDATE workspaces SET updated_at = CURRENT_TIMESTAMP WHERE id = NEW.id;
    END;

-- Trigger to update updated_at on rooms
CREATE TRIGGER IF NOT EXISTS trg_rooms_updated
    AFTER UPDATE ON rooms
    FOR EACH ROW WHEN OLD.updated_at = NEW.updated_at
    BEGIN
        UPDATE rooms SET updated_at = CURRENT_TIMESTAMP WHERE name = NEW.name;
    END;

-- Insert schema version record
INSERT OR IGNORE INTO schema_version (version) VALUES (1);