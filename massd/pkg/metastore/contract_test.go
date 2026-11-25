package metastore_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/stretchr/testify/suite"

	"github.com/zoumo/mass/massd"
	"github.com/zoumo/mass/massd/pkg/events"
	"github.com/zoumo/mass/massd/pkg/metastore"
	"github.com/zoumo/mass/massd/pkg/metastore/backend"
	"github.com/zoumo/mass/massd/pkg/resource"
	"github.com/zoumo/mass/massd/pkg/runtime"
	"github.com/zoumo/mass/massd/pkg/store"
	"github.com/zoumo/mass/massd/pkg/transport"
)

type MetaStoreSuite struct {
	suite.Suite
	backend string
}

func open(t *testing.T, name string) (backend.Store, string) {
	t.Helper()
	dir := t.TempDir()
	s, err := backend.Open(dir, metastore.Config{Backend: name}, false)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, s.Close()) })
	return s, dir
}

func session(ns, name, id string) resource.Session {
	return resource.Session{Metadata: resource.Metadata{Namespace: ns, Name: name}, Spec: resource.SessionSpec{RuntimeRef: "codex", SessionSpec: runtime.SessionSpec{Cwd: "/synthetic"}}, Status: resource.SessionStatus{SessionID: id, State: events.Unknown}}
}

func (metaSuite *MetaStoreSuite) TestAtomicIdentityAndWriterOwnership() {
	t := metaSuite.T()
	s, dir := open(t, metaSuite.backend)
	_, err := backend.Open(dir, metastore.Config{Backend: metaSuite.backend}, false)
	require.Error(t, err, "a second file Store cannot create an independent writer cache")
	for _, mode := range []string{"name", "native-id"} {
		metaSuite.Run(mode, func() {
			subT := metaSuite.T()
			var winners atomic.Int32
			var wg sync.WaitGroup
			for i := range 24 {
				wg.Go(func() {
					ns, name, id := "default", fmt.Sprintf("demo-%s-%d", mode, i), fmt.Sprintf("native-%s-%d", mode, i)
					if mode == "name" {
						name = "same-name"
					} else {
						id = "same-native-id"
					}
					_, err := s.CreateSession(session(ns, name, id))
					if err == nil {
						winners.Add(1)
					} else if !errors.Is(err, store.ErrConflict) {
						subT.Errorf("unexpected create error: %v", err)
					}
				})
			}
			wg.Wait()
			require.EqualValues(subT, 1, winners.Load())
		})
	}
	_, err = s.CreateSession(session("other", "other", "same-native-id"))
	require.ErrorIs(t, err, store.ErrConflict, "native identity is global across namespace/name")
	readonly, err := backend.Open(dir, metastore.Config{Backend: metaSuite.backend}, true)
	require.NoError(t, err)
	defer readonly.Close()
	_, err = readonly.CreateSession(session("other", "new", "fresh"))
	require.ErrorIs(t, err, store.ErrReadOnly)
}

func (metaSuite *MetaStoreSuite) TestResourceVersionsSnapshotsAndEncoding() {
	t := metaSuite.T()
	s, dir := open(t, metaSuite.backend)
	r, err := s.CreateRuntime(resource.Runtime{Metadata: resource.Metadata{Name: "codex"}, Spec: resource.RuntimeSpec{Type: "codex", Config: runtime.Config{"nested": map[string]any{"value": "one"}}}, Status: resource.RuntimeStatus{State: "stopped"}})
	require.NoError(t, err)
	initial := r
	r.Spec.Config["nested"].(map[string]any)["value"] = "local mutation"
	stored, err := s.GetRuntime("codex")
	require.NoError(t, err)
	require.Equal(t, "one", stored.Spec.Config["nested"].(map[string]any)["value"])
	stored.Status.State = "running"
	status, err := s.UpdateRuntime(stored)
	require.NoError(t, err)
	require.EqualValues(t, 1, status.Metadata.Generation)
	require.NotEqual(t, initial.Metadata.ResourceVersion, status.Metadata.ResourceVersion)
	stale := initial
	stale.Status.State = "unknown"
	_, err = s.UpdateRuntime(stale)
	require.ErrorIs(t, err, store.ErrConflict)
	status.Spec.Config["nested"].(map[string]any)["value"] = "two"
	next, err := s.UpdateRuntime(status)
	require.NoError(t, err)
	require.EqualValues(t, 2, next.Metadata.Generation)
	same, err := s.UpdateRuntime(next)
	require.NoError(t, err)
	require.Equal(t, next.Metadata, same.Metadata)
	invalid := same
	invalid.Metadata.Generation++
	_, err = s.UpdateRuntime(invalid)
	require.Error(t, err)
	for _, bad := range []any{make(chan int), func() {}, struct{ Hidden string }{Hidden: "cannot silently omit or coerce"}, []byte("binary")} {
		_, err = s.CreateRuntime(resource.Runtime{Metadata: resource.Metadata{Name: "bad"}, Spec: resource.RuntimeSpec{Type: "codex", Config: runtime.Config{"value": bad}}})
		require.Error(t, err)
		_, err = s.GetRuntime("bad")
		require.ErrorIs(t, err, store.ErrNotFound)
	}
	require.NoError(t, s.Close())
	reopened, err := backend.Open(dir, metastore.Config{Backend: metaSuite.backend}, true)
	require.NoError(t, err)
	defer reopened.Close()
	saved, err := reopened.GetRuntime("codex")
	require.NoError(t, err)
	require.Equal(t, next, saved)
}

func (metaSuite *MetaStoreSuite) TestFullDirectoryCursorsAndConditionalDelete() {
	t := metaSuite.T()
	s, dir := open(t, metaSuite.backend)
	for _, v := range []struct{ ns, name, id, state string }{{"z", "a", "native-z", "offline"}, {"a", "z", "native-a-z", "unknown"}, {"a", "a", "native-a-a", "idle"}} {
		r := session(v.ns, v.name, v.id)
		r.Status.State = events.SessionState(v.state)
		_, err := s.CreateSession(r)
		require.NoError(t, err)
	}
	all, err := s.ListSessions(resource.SessionQuery{})
	require.NoError(t, err)
	require.Len(t, all.Items, 3)
	require.Equal(t, []string{"native-a-a", "native-a-z", "native-z"}, []string{all.Items[0].Status.SessionID, all.Items[1].Status.SessionID, all.Items[2].Status.SessionID})
	first, err := s.ListSessions(resource.SessionQuery{Namespace: "a", Limit: 1})
	require.NoError(t, err)
	require.NotEmpty(t, first.Next)
	_, err = s.ListSessions(resource.SessionQuery{After: first.Next})
	require.Error(t, err, "cursor binds namespace")
	_, err = s.ListSessions(resource.SessionQuery{Limit: -1})
	require.Error(t, err)
	offline, err := s.ListSessions(resource.SessionQuery{State: "offline"})
	require.NoError(t, err)
	require.Len(t, offline.Items, 1)
	r := all.Items[0]
	// Re-read confirms failed conditional deletion had no effect.
	err = s.DeleteSession(resource.SessionRef{SessionID: r.Status.SessionID}, "wrong")
	require.ErrorIs(t, err, store.ErrConflict)
	_, err = s.GetSession(resource.SessionRef{SessionID: r.Status.SessionID})
	require.NoError(t, err)
	require.NoError(t, s.Close())
	reopened, err := backend.Open(dir, metastore.Config{Backend: metaSuite.backend}, false)
	require.NoError(t, err)
	defer reopened.Close()
	last, err := reopened.ListSessions(resource.SessionQuery{Namespace: "a", After: first.Next, Limit: 20})
	require.NoError(t, err)
	require.Len(t, last.Items, 1)
	require.Equal(t, "native-a-z", last.Items[0].Status.SessionID)
	require.Empty(t, last.Next)
	// The legacy index path is unused; directory pagination survives reopening.
	_, err = os.Stat(filepath.Join(dir, "index.sqlite"))
	require.True(t, os.IsNotExist(err))
	require.NoError(t, reopened.DeleteSession(resource.SessionRef{SessionID: r.Status.SessionID}, r.Metadata.ResourceVersion))
	_, err = reopened.GetSession(resource.SessionRef{SessionID: r.Status.SessionID})
	require.ErrorIs(t, err, store.ErrNotFound)
	runtimeRecord, err := reopened.CreateRuntime(resource.Runtime{Metadata: resource.Metadata{Name: "offline"}, Spec: resource.RuntimeSpec{Type: "codex"}, Status: resource.RuntimeStatus{State: "stopped"}})
	require.NoError(t, err)
	runtimes, err := reopened.ListRuntimes(resource.RuntimeQuery{Type: "codex", State: "stopped"})
	require.NoError(t, err)
	require.Len(t, runtimes, 1)
	require.ErrorIs(t, reopened.DeleteRuntime("offline", "stale"), store.ErrConflict)
	require.NoError(t, reopened.DeleteRuntime("offline", runtimeRecord.Metadata.ResourceVersion))
}

func TestMetaStoreContract(t *testing.T) {
	for _, name := range []string{"sqlite", "file"} {
		t.Run(name, func(t *testing.T) { suite.Run(t, &MetaStoreSuite{backend: name}) })
	}
}

func TestRejectIllegalPersistentSessionIDs(t *testing.T) {
	for _, name := range []string{"sqlite", "file"} {
		t.Run(name, func(t *testing.T) {
			for _, id := range []string{".", ".."} {
				t.Run(id, func(t *testing.T) {
					st, err := backend.Open(t.TempDir(), metastore.Config{Backend: name}, false)
					require.NoError(t, err)
					defer st.Close()
					_, err = st.CreateSession(session("default", "demo", id))
					require.Error(t, err)
				})
			}
		})
	}
}

func TestRejectArchivedSessionState(t *testing.T) {
	for _, name := range []string{"sqlite", "file"} {
		t.Run(name, func(t *testing.T) {
			s, dir := open(t, name)
			created, err := s.CreateSession(session("default", "saved", "native"))
			require.NoError(t, err)
			invalid := created
			invalid.Status.State = "archived"

			_, err = s.UpdateSession(invalid)
			require.ErrorIs(t, err, runtime.ErrInvalidArgument)
			_, err = s.ListSessions(resource.SessionQuery{State: invalid.Status.State})
			require.ErrorIs(t, err, runtime.ErrInvalidArgument)
			forged := session("default", "forged", "forged-id")
			forged.Status.State = invalid.Status.State
			_, err = s.CreateSession(forged)
			require.ErrorIs(t, err, runtime.ErrInvalidArgument)
			require.NoError(t, s.Close())

			writeStoredSession(t, dir, name, invalid)
			_, err = backend.Open(dir, metastore.Config{Backend: name}, false)
			require.ErrorContains(t, err, "archived is unsupported")
		})
	}
}

func TestStartupCorrectsUnfamiliarStoredSessionState(t *testing.T) {
	for _, name := range []string{"sqlite", "file"} {
		t.Run(name, func(t *testing.T) {
			s, dir := open(t, name)
			created, err := s.CreateSession(session("default", "saved", "native"))
			require.NoError(t, err)
			require.NoError(t, s.Close())

			prior := created
			prior.Status.State = "future_state"
			writeStoredSession(t, dir, name, prior)

			config := metastore.Config{Backend: name}
			instance, err := massd.Start(t.Context(), massd.Options{
				DataDir:   dir,
				MetaStore: config,
				Transport: transport.Options{WriteTimeout: massd.DefaultWriteTimeout},
			})
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, instance.Shutdown(context.Background())) })
			require.NoError(t, instance.Shutdown(t.Context()))

			reopened, err := backend.Open(dir, config, true)
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, reopened.Close()) })
			saved, err := reopened.GetSession(resource.SessionRef{SessionID: created.Status.SessionID})
			require.NoError(t, err)
			require.Equal(t, events.Unknown, saved.Status.State)
			require.Equal(t, "state_unconfirmed_after_restart", saved.Status.Reason)
			require.Equal(t, created.Spec, saved.Spec)
			require.Equal(t, created.Metadata.UID, saved.Metadata.UID)
			require.Equal(t, created.Metadata.Generation, saved.Metadata.Generation)
			require.NotEqual(t, created.Metadata.ResourceVersion, saved.Metadata.ResourceVersion)
		})
	}
}

func writeStoredSession(t *testing.T, dir, backendName string, session resource.Session) {
	t.Helper()
	payload, err := json.Marshal(session)
	require.NoError(t, err)
	switch backendName {
	case "sqlite":
		db, err := sql.Open("sqlite", filepath.Join(dir, "metadata-authority.sqlite"))
		require.NoError(t, err)
		defer func() { require.NoError(t, db.Close()) }()
		result, err := db.ExecContext(t.Context(), "UPDATE session SET state=?, payload=? WHERE session_id=?", string(session.Status.State), payload, session.Status.SessionID)
		require.NoError(t, err)
		count, err := result.RowsAffected()
		require.NoError(t, err)
		require.EqualValues(t, 1, count)
	case "file":
		path := filepath.Join(dir, "sessions", session.Metadata.Namespace, session.Metadata.Name, "state.json")
		require.NoError(t, os.WriteFile(path, payload, 0o600))
	default:
		t.Fatalf("unknown backend %q", backendName)
	}
}
