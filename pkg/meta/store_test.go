package meta_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/open-agent-d/open-agent-d/pkg/meta"
)

// tempStore creates a Store backed by a temporary file.
// The store is automatically closed and the file removed when the test ends.
func tempStore(t *testing.T) *meta.Store {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "meta.db")
	s, err := meta.NewStore(path)
	require.NoError(t, err, "NewStore should succeed")
	t.Cleanup(func() { _ = s.Close() })
	return s
}

// TestNewStore_OpenClose verifies that a store opens and closes without error.
func TestNewStore_OpenClose(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "meta.db")

	s, err := meta.NewStore(path)
	require.NoError(t, err)
	require.NotNil(t, s)

	// File should exist after Open.
	_, statErr := os.Stat(path)
	require.NoError(t, statErr, "database file should exist")

	require.NoError(t, s.Close())
}

// TestNewStore_ReopenExisting verifies that re-opening an existing database works.
func TestNewStore_ReopenExisting(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "meta.db")

	s1, err := meta.NewStore(path)
	require.NoError(t, err)
	require.NoError(t, s1.Close())

	// Re-open the same file.
	s2, err := meta.NewStore(path)
	require.NoError(t, err)
	require.NoError(t, s2.Close())
}

// TestNewStore_BucketsCreated verifies that the bucket hierarchy is created
// by exercising workspace and agent CRUD which would fail without buckets.
func TestNewStore_BucketsCreated(t *testing.T) {
	s := tempStore(t)

	// ListWorkspaces and ListAgents returning empty results proves that the
	// bucket hierarchy was created successfully.
	wss, err := s.ListWorkspaces(t.Context(), nil)
	require.NoError(t, err)
	require.Empty(t, wss)

	agents, err := s.ListAgents(t.Context(), nil)
	require.NoError(t, err)
	require.Empty(t, agents)
}

// TestNewStore_PathAttribute verifies that the Path field is set correctly.
func TestNewStore_PathAttribute(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "meta.db")

	s, err := meta.NewStore(path)
	require.NoError(t, err)
	defer s.Close()

	require.Equal(t, path, s.Path)
}

// TestNewStore_InvalidPath verifies that opening a database under a non-existent
// parent directory returns an error (bbolt cannot create parent dirs).
func TestNewStore_InvalidPath(t *testing.T) {
	path := "/nonexistent-dir/should-fail/meta.db"
	s, err := meta.NewStore(path)
	require.Error(t, err)
	require.Nil(t, s)
}
