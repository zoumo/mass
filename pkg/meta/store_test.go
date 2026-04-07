// Package meta provides metadata storage for OAR session/workspace/room records.
package meta

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newTestStore creates an in-memory SQLite store for testing.
// The store is automatically closed when the test completes.
func newTestStore(t *testing.T) *Store {
	t.Helper()

	// Use :memory: for in-memory database (fast, isolated per test).
	store, err := NewStore(":memory:")
	require.NoError(t, err, "NewStore with :memory: should succeed")
	require.NotNil(t, store, "Store should not be nil")

	// Automatically close on test cleanup.
	t.Cleanup(func() {
		store.Close()
	})

	return store
}

// TestNewStore verifies that NewStore creates the database and schema correctly.
// It tests:
//   - Database file creation
//   - Schema table creation (sessions, workspaces, rooms, workspace_refs, schema_version)
//   - Connection parameters (WAL mode, foreign keys)
//   - Close method works correctly
func TestNewStore(t *testing.T) {
	t.Parallel()

	// Create a temporary directory for the test database.
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "meta.db")

	// Create the store - this should create the database and schema.
	store, err := NewStore(dbPath)
	require.NoError(t, err, "NewStore should succeed")
	require.NotNil(t, store, "Store should not be nil")

	// Verify the database file was created.
	assert.FileExists(t, dbPath, "Database file should exist")

	// Verify expected tables exist by querying sqlite_master.
	expectedTables := []string{
		"schema_version",
		"rooms",
		"workspaces",
		"sessions",
		"workspace_refs",
	}

	ctx := context.Background()
	for _, table := range expectedTables {
		var exists bool
		err := store.db.QueryRowContext(ctx,
			"SELECT EXISTS(SELECT 1 FROM sqlite_master WHERE type='table' AND name=$1)",
			table,
		).Scan(&exists)

		require.NoError(t, err, "QueryRowContext should succeed for table %s", table)
		assert.True(t, exists, "Table %s should exist", table)
	}

	// Verify schema_version table has the expected version.
	var version int
	err = store.db.QueryRowContext(ctx,
		"SELECT version FROM schema_version ORDER BY version DESC LIMIT 1",
	).Scan(&version)

	require.NoError(t, err, "QueryRowContext for schema_version should succeed")
	assert.Equal(t, 2, version, "Schema version should be 2")

	// Verify WAL mode is enabled.
	var journalMode string
	err = store.db.QueryRowContext(ctx, "PRAGMA journal_mode").Scan(&journalMode)

	require.NoError(t, err, "QueryRowContext for journal_mode should succeed")
	assert.Equal(t, "wal", journalMode, "Journal mode should be WAL")

	// Verify foreign keys are enabled.
	var foreignKeys bool
	err = store.db.QueryRowContext(ctx, "PRAGMA foreign_keys").Scan(&foreignKeys)

	require.NoError(t, err, "QueryRowContext for foreign_keys should succeed")
	assert.True(t, foreignKeys, "Foreign keys should be enabled")

	// Verify indexes exist.
	expectedIndexes := []string{
		"idx_sessions_workspace_id",
		"idx_sessions_room",
		"idx_sessions_state",
		"idx_workspaces_status",
		"idx_workspaces_name",
		"idx_workspace_refs_workspace_id",
		"idx_workspace_refs_session_id",
	}

	for _, index := range expectedIndexes {
		var exists bool
		err := store.db.QueryRowContext(ctx,
			"SELECT EXISTS(SELECT 1 FROM sqlite_master WHERE type='index' AND name=$1)",
			index,
		).Scan(&exists)

		require.NoError(t, err, "QueryRowContext should succeed for index %s", index)
		assert.True(t, exists, "Index %s should exist", index)
	}

	// Verify triggers exist.
	expectedTriggers := []string{
		"trg_workspace_refs_insert",
		"trg_workspace_refs_delete",
		"trg_sessions_updated",
		"trg_workspaces_updated",
		"trg_rooms_updated",
	}

	for _, trigger := range expectedTriggers {
		var exists bool
		err := store.db.QueryRowContext(ctx,
			"SELECT EXISTS(SELECT 1 FROM sqlite_master WHERE type='trigger' AND name=$1)",
			trigger,
		).Scan(&exists)

		require.NoError(t, err, "QueryRowContext should succeed for trigger %s", trigger)
		assert.True(t, exists, "Trigger %s should exist", trigger)
	}

	// Test BeginTx works.
	tx, err := store.BeginTx(ctx, nil)
	require.NoError(t, err, "BeginTx should succeed")
	require.NotNil(t, tx, "Transaction should not be nil")

	// Rollback the transaction.
	err = tx.Rollback()
	require.NoError(t, err, "Rollback should succeed")

	// Close the store.
	err = store.Close()
	require.NoError(t, err, "Close should succeed")

	// Verify we can create a new store from the same path (schema should still exist).
	store2, err := NewStore(dbPath)
	require.NoError(t, err, "NewStore on existing database should succeed")
	require.NotNil(t, store2, "Second Store should not be nil")

	// Verify tables still exist.
	for _, table := range expectedTables {
		var exists bool
		err := store2.db.QueryRowContext(ctx,
			"SELECT EXISTS(SELECT 1 FROM sqlite_master WHERE type='table' AND name=$1)",
			table,
		).Scan(&exists)

		require.NoError(t, err, "QueryRowContext should succeed for table %s on reopen", table)
		assert.True(t, exists, "Table %s should still exist on reopen", table)
	}

	// Close the second store.
	err = store2.Close()
	require.NoError(t, err, "Second Close should succeed")
}

// TestNewStoreInvalidPath verifies that NewStore fails gracefully with an invalid path.
func TestNewStoreInvalidPath(t *testing.T) {
	t.Parallel()

	// Try to create a store in a non-existent directory.
	invalidPath := "/nonexistent/directory/meta.db"
	store, err := NewStore(invalidPath)

	require.Error(t, err, "NewStore with invalid path should fail")
	require.Nil(t, store, "Store should be nil on error")
	require.Contains(t, err.Error(), "failed to", "Error should mention failure")
}

// TestNewStoreEmptyPath verifies that NewStore handles empty path gracefully.
func TestNewStoreEmptyPath(t *testing.T) {
	t.Parallel()

	store, err := NewStore("")
	// SQLite allows empty path (creates in-memory database), so this might succeed.
	// We'll just verify no panic occurs.
	_ = err // Error or success is acceptable for empty path.
	_ = store
	if store != nil {
		store.Close()
	}
}

// TestDBMethod verifies that DB() returns the underlying database connection.
func TestDBMethod(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "meta.db")

	store, err := NewStore(dbPath)
	require.NoError(t, err, "NewStore should succeed")

	db := store.DB()
	require.NotNil(t, db, "DB() should return non-nil database connection")

	// Verify we can use the returned DB directly.
	ctx := context.Background()
	var result int
	err = db.QueryRowContext(ctx, "SELECT 1").Scan(&result)
	require.NoError(t, err, "QueryRowContext on returned DB should succeed")
	assert.Equal(t, 1, result, "SELECT 1 should return 1")

	store.Close()
}