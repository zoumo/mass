package metastore_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	eventfile "github.com/zoumo/mass/mass-go/pkg/eventstore/file"
	"github.com/zoumo/mass/mass-go/pkg/metastore"
	"github.com/zoumo/mass/mass-go/pkg/metastore/backend"
	"github.com/zoumo/mass/mass-go/pkg/resource"
	"github.com/zoumo/mass/mass-go/pkg/store"
)

func TestSQLiteAuthorityCannotBeRebuiltAfterLoss(t *testing.T) {
	dir := t.TempDir()
	s, err := backend.Open(dir, metastore.Config{}, false)
	require.NoError(t, err)
	_, err = s.CreateRuntime(resource.Runtime{Metadata: resource.Metadata{Name: "saved"}})
	require.NoError(t, err)
	require.NoError(t, s.Close())
	require.NoError(t, os.Remove(filepath.Join(dir, "metadata-authority.sqlite")))
	_, err = backend.Open(dir, metastore.Config{}, false)
	require.Error(t, err, "lost authority must not be recreated as an empty database")
}

func TestSelectedAuthorityFailsClosed(t *testing.T) {
	for _, name := range []string{"sqlite", "file"} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			config := metastore.Config{Backend: name}
			s, err := backend.Open(dir, config, false)
			require.NoError(t, err)
			r, err := s.CreateSession(session("demo", "saved", "native"))
			require.NoError(t, err)
			require.NoError(t, s.Close())
			other := "file"
			if name == "file" {
				other = "sqlite"
			}
			_, err = backend.Open(dir, metastore.Config{Backend: other}, false)
			require.Error(t, err, "configuration changes require explicit migration")
			s, err = backend.Open(dir, config, false)
			require.NoError(t, err)
			got, err := s.GetSession(resource.SessionRef{SessionID: "native"})
			require.NoError(t, err)
			require.Equal(t, r, got)
			require.NoError(t, s.Close())
			path := "metadata-authority.sqlite"
			if name == "file" {
				path = ".store.json"
			}
			require.NoError(t, os.WriteFile(filepath.Join(dir, path), []byte("corrupt authority"), 0o600))
			for _, readOnly := range []bool{false, true} {
				_, err = backend.Open(dir, config, readOnly)
				require.Error(t, err)
			}
		})
	}
	for _, name := range []string{"invalid", "SQLITE"} {
		_, err := backend.Open(t.TempDir(), metastore.Config{Backend: name}, false)
		require.Error(t, err)
	}
}

func TestLegacyMetadataReplicaIsNotAuthority(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "metadata.sqlite")
	legacy := []byte("legacy derived database bytes")
	require.NoError(t, os.WriteFile(path, legacy, 0o600))
	_, err := backend.Open(dir, metastore.Config{}, false)
	require.Error(t, err)
	saved, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, legacy, saved)
	// File authority does not open, delete or require the old projection.
	s, err := backend.Open(dir, metastore.Config{Backend: "file"}, false)
	require.NoError(t, err)
	_, err = s.CreateSession(session("demo", "saved", "native"))
	require.NoError(t, err)
	require.NoError(t, s.Close())
	saved, err = os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, legacy, saved)
}

func (metaSuite *MetaStoreSuite) TestSessionVersionAndSpecIsolation() {
	t := metaSuite.T()
	st, dir := open(t, metaSuite.backend)
	created, err := st.CreateSession(session("demo", "saved", "native"))
	require.NoError(t, err)
	changed := created
	changed.Spec.Cwd = "/new"
	updated, err := st.UpdateSession(changed)
	require.NoError(t, err)
	require.EqualValues(t, 2, updated.Metadata.Generation)
	created.Status.State = "offline"
	_, err = st.UpdateSession(created)
	require.ErrorIs(t, err, store.ErrConflict)
	updated.Status.State = "offline"
	saved, err := st.UpdateSession(updated)
	require.NoError(t, err)
	require.EqualValues(t, 2, saved.Metadata.Generation)
	require.NotEqual(t, updated.Metadata.ResourceVersion, saved.Metadata.ResourceVersion)
	same, err := st.UpdateSession(saved)
	require.NoError(t, err)
	require.Equal(t, saved, same)
	same.Status.SessionID = "different"
	_, err = st.UpdateSession(same)
	require.Error(t, err)
	require.NoError(t, st.Close())
	st, err = backend.Open(dir, metastore.Config{Backend: metaSuite.backend}, false)
	require.NoError(t, err)
	defer st.Close()
	got, err := st.GetSession(resource.SessionRef{Namespace: "demo", Name: "saved"})
	require.NoError(t, err)
	require.Equal(t, saved, got)
}

func TestIndependentStoreLifecycles(t *testing.T) {
	for _, name := range []string{"sqlite", "file"} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			history, err := eventfile.Open(dir, false)
			require.NoError(t, err)
			defer history.Close()
			config := metastore.Config{Backend: name}
			meta, err := backend.Open(dir, config, false)
			require.NoError(t, err)
			defer meta.Close()
			created, err := meta.CreateSession(session("demo", "saved", "native"))
			require.NoError(t, err)
			require.NoError(t, history.Close())
			require.NoError(t, os.Remove(filepath.Join(dir, "history.sqlite")))
			got, err := meta.GetSession(resource.SessionRef{SessionID: "native"})
			require.NoError(t, err)
			require.Equal(t, created, got)
			history, err = eventfile.Open(dir, false)
			require.NoError(t, err)
			defer history.Close()
			require.NoError(t, meta.Close())
			progress, err := history.GetSyncProgress("native")
			require.NoError(t, err)
			require.Nil(t, progress)
			require.NoError(t, history.Close())
			require.NoError(t, os.Remove(filepath.Join(dir, "history.sqlite")))
			require.NoError(t, os.Mkdir(filepath.Join(dir, "history.sqlite"), 0o700))
			meta, err = backend.Open(dir, config, false)
			require.NoError(t, err)
			defer meta.Close()
			got, err = meta.GetSession(resource.SessionRef{SessionID: "native"})
			require.NoError(t, err)
			require.Equal(t, created, got)
			got.Status.State = "offline"
			_, err = meta.UpdateSession(got)
			require.NoError(t, err)
		})
	}
}
