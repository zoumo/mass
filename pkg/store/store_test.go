package store_test

import (
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/open-agent-d/open-agent-d/pkg/store"
)

// tempStore creates a Store backed by a temporary file.
// The store is automatically closed when the test ends.
func tempStore(t *testing.T) *store.Store {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "store.db")
	s, err := store.NewStore(path, slog.Default())
	require.NoError(t, err, "NewStore should succeed")
	t.Cleanup(func() { _ = s.Close() })
	return s
}

// TestNewStore_OpenClose verifies that a store opens and closes without error.
func TestNewStore_OpenClose(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "store.db")

	s, err := store.NewStore(path, slog.Default())
	require.NoError(t, err)
	require.NotNil(t, s)

	_, statErr := os.Stat(path)
	require.NoError(t, statErr, "database file should exist")

	require.NoError(t, s.Close())
}

// TestNewStore_ReopenExisting verifies that re-opening an existing database works.
func TestNewStore_ReopenExisting(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "store.db")

	s1, err := store.NewStore(path, slog.Default())
	require.NoError(t, err)
	require.NoError(t, s1.Close())

	s2, err := store.NewStore(path, slog.Default())
	require.NoError(t, err)
	require.NoError(t, s2.Close())
}

// TestNewStore_BucketsCreated verifies that the bucket hierarchy is created.
func TestNewStore_BucketsCreated(t *testing.T) {
	s := tempStore(t)

	wss, err := s.ListWorkspaces(t.Context(), nil)
	require.NoError(t, err)
	require.Empty(t, wss)

	agents, err := s.ListAgentRuns(t.Context(), nil)
	require.NoError(t, err)
	require.Empty(t, agents)
}

// TestNewStore_PathAttribute verifies that the Path field is set correctly.
func TestNewStore_PathAttribute(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "store.db")

	s, err := store.NewStore(path, slog.Default())
	require.NoError(t, err)
	defer s.Close()

	require.Equal(t, path, s.Path)
}

// TestNewStore_InvalidPath verifies that opening under a non-existent parent dir fails.
func TestNewStore_InvalidPath(t *testing.T) {
	path := "/nonexistent-dir/should-fail/store.db"
	s, err := store.NewStore(path, slog.Default())
	require.Error(t, err)
	require.Nil(t, s)
}
