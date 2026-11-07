package file

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/zoumo/mass/mass-go/pkg/events"
	"github.com/zoumo/mass/mass-go/pkg/resource"
	"github.com/zoumo/mass/mass-go/pkg/store"
)

func (s *appendSuite) TestMetadataReplicaFailureRetainsRecoveryResponsibility() {
	_, err := s.st.Append("native", []events.SessionEvent{s.fact("retained history")})
	s.Require().NoError(err)
	// Abort real attached SQL cleanup after metadata changes, following file deletion.
	_, err = s.st.sql.conn.ExecContext(context.Background(), `CREATE TRIGGER history.fail_delete BEFORE DELETE ON event BEGIN SELECT RAISE(ABORT, 'injected replica failure'); END`)
	s.Require().NoError(err)
	err = s.st.DeleteSession(resource.SessionRef{SessionID: "native"}, s.r.Metadata.ResourceVersion)
	s.Require().ErrorContains(err, "outcome_unknown")
	_, err = os.Stat(filepath.Join(s.dir, ".pending.json"))
	s.Require().NoError(err, "retain metadata recovery evidence until both replicas commit")
	_, err = s.st.GetSession(resource.SessionRef{SessionID: "native"})
	s.Require().ErrorContains(err, "outcome_unknown")
	_, err = s.st.GetSyncProgress("native")
	s.Require().ErrorContains(err, "outcome_unknown")
	_, err = s.st.CreateSession(s.r)
	s.Require().ErrorContains(err, "outcome_unknown")
	s.Require().NoError(s.st.Reconcile())
	_, err = s.st.GetSession(resource.SessionRef{SessionID: "native"})
	s.Require().ErrorIs(err, store.ErrNotFound)
	_, err = s.st.ListHistory("native", store.HistoryQuery{})
	s.Require().ErrorIs(err, store.ErrNotFound)
	_, err = os.Stat(filepath.Join(s.dir, ".pending.json"))
	s.True(os.IsNotExist(err))
}

func (s *appendSuite) TestAppendReplicaFailuresSealUntilExplicitRecovery() {
	for _, stage := range []string{"event", "progress"} {
		for _, recovery := range []string{"reconcile", "reopen"} {
			s.Run(stage+"/"+recovery, func() {
				s.SetupTest()
				old, one, two := s.fact("old"), s.fact("one"), s.fact("two")
				_, err := s.st.Append("native", []events.SessionEvent{old})
				s.Require().NoError(err)
				statement := fmt.Sprintf(`CREATE TRIGGER history.fail_append BEFORE INSERT ON event WHEN NEW.event_id='%s' BEGIN SELECT RAISE(ABORT, 'injected event failure'); END`, two.EventID)
				if stage == "progress" {
					statement = `CREATE TRIGGER history.fail_append BEFORE UPDATE ON sync_progress BEGIN SELECT RAISE(ABORT, 'injected progress failure'); END`
				}
				_, err = s.st.sql.conn.ExecContext(context.Background(), statement)
				s.Require().NoError(err)
				_, err = s.st.Append("native", []events.SessionEvent{one, two})
				s.Require().ErrorContains(err, "outcome_unknown")
				path := s.st.historyPath(s.r)
				committed, err := os.ReadFile(path)
				s.Require().NoError(err)
				s.Equal(3, bytes.Count(committed, []byte{'\n'}))
				var intent appendIntent
				s.Require().NoError(readJSON(filepath.Join(s.dir, ".append.pending.json"), &intent))
				s.NotContains(string(intent.Data), old.EventID)
				for _, id := range []string{one.EventID, two.EventID} {
					s.Contains(string(intent.Data), id)
				}
				_, err = s.st.ListHistory("native", store.HistoryQuery{})
				s.Require().ErrorContains(err, "outcome_unknown")
				_, err = s.st.GetSyncProgress("native")
				s.Require().ErrorContains(err, "outcome_unknown")
				_, err = s.st.GetSession(resource.SessionRef{SessionID: "native"})
				s.Require().ErrorContains(err, "outcome_unknown")
				_, err = s.st.Append("native", []events.SessionEvent{one, two})
				s.Require().ErrorContains(err, "outcome_unknown")
				before := captureTree(s.T(), s.dir)
				reader, err := Open(s.dir, true)
				s.Nil(reader)
				s.Require().ErrorContains(err, "writable reopen required")
				s.Equal(before, captureTree(s.T(), s.dir))
				if recovery == "reconcile" {
					s.Require().NoError(s.st.Reconcile())
				} else {
					s.Require().NoError(s.st.Close())
					s.st, err = Open(s.dir, false)
					s.Require().NoError(err)
				}
				defer s.st.Close()
				page, err := s.st.ListHistory("native", store.HistoryQuery{})
				s.Require().NoError(err)
				s.assertEvents([]events.SessionEvent{old, one, two}, page.Events)
				s.Equal(two.EventID, *page.SyncedEventID)
				progress, err := s.st.Append("native", []events.SessionEvent{old, one, two})
				s.Require().NoError(err)
				s.Equal(two.EventID, *progress)
				after, err := os.ReadFile(path)
				s.Require().NoError(err)
				s.Equal(committed, after)
			})
		}
	}
}

type treeEntry struct {
	Data     string
	Mode     fs.FileMode
	Modified time.Time
}

// Include persistent directory membership, modes and modification times.
func captureTree(t *testing.T, dir string) map[string]treeEntry {
	t.Helper()
	result := map[string]treeEntry{}
	require.NoError(t, filepath.WalkDir(dir, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		var b []byte
		if !entry.IsDir() {
			b, err = os.ReadFile(path)
			if err != nil {
				return err
			}
		}
		relative, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		result[relative] = treeEntry{string(b), info.Mode(), info.ModTime()}
		return nil
	}))
	return result
}

func openTestDB(t *testing.T, dir, name string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", (&url.URL{Scheme: "file", Path: filepath.Join(dir, name+".sqlite")}).String())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	return db
}

func (s *appendSuite) TestDisposableReplicasRestoreOnlyAuthoritativeFacts() {
	for _, damage := range []string{"missing-metadata", "missing-history", "missing-both", "corrupt-metadata", "corrupt-history", "incompatible", "stale", "sql-only"} {
		s.Run(damage, func() {
			s.SetupTest()
			rt, err := s.st.CreateRuntime(resource.Runtime{Metadata: resource.Metadata{Name: "codex"}, Spec: resource.RuntimeSpec{Type: "codex"}})
			s.Require().NoError(err)
			old, last := s.fact("first"), s.fact("last")
			_, err = s.st.Append("native", []events.SessionEvent{old, last})
			s.Require().NoError(err)
			first, err := s.st.ListHistory("native", store.HistoryQuery{Limit: 1})
			s.Require().NoError(err)
			s.Require().NoError(s.st.Close())
			switch damage {
			case "missing-metadata", "missing-history", "missing-both":
				for _, name := range []string{"metadata", "history"} {
					if damage == "missing-both" || damage == "missing-"+name {
						s.Require().NoError(os.Remove(filepath.Join(s.dir, name+".sqlite")))
					}
				}
			case "corrupt-metadata", "corrupt-history":
				name := "metadata"
				if damage == "corrupt-history" {
					name = "history"
				}
				s.Require().NoError(os.WriteFile(filepath.Join(s.dir, name+".sqlite"), []byte("not a database"), 0o600))
			case "incompatible":
				db := openTestDB(s.T(), s.dir, "metadata")
				_, err = db.ExecContext(s.T().Context(), "DROP TABLE session; CREATE TABLE session (unsupported INTEGER); PRAGMA user_version=999")
				s.Require().NoError(err)
				s.Require().NoError(db.Close())
			case "stale":
				db := openTestDB(s.T(), s.dir, "history")
				_, err = db.ExecContext(s.T().Context(), "DELETE FROM event; UPDATE sync_progress SET event_id=NULL")
				s.Require().NoError(err)
				s.Require().NoError(db.Close())
			case "sql-only":
				ghost := s.fact("SQL is not authority")
				b, err := json.Marshal(ghost)
				s.Require().NoError(err)
				db := openTestDB(s.T(), s.dir, "history")
				_, err = db.ExecContext(s.T().Context(), "INSERT INTO event VALUES(?,?,?,?,?)", "native", 3, ghost.EventID, ghost.Type(), b)
				s.Require().NoError(err)
				_, err = db.ExecContext(s.T().Context(), "UPDATE sync_progress SET event_id=?", ghost.EventID)
				s.Require().NoError(err)
				s.Require().NoError(db.Close())
				db = openTestDB(s.T(), s.dir, "metadata")
				_, err = db.ExecContext(s.T().Context(), "UPDATE runtime SET payload='{}'; UPDATE session SET payload='{}'")
				s.Require().NoError(err)
				s.Require().NoError(db.Close())
			}
			before := captureTree(s.T(), s.dir)
			temp := s.T().TempDir()
			s.T().Setenv("TMPDIR", temp)
			reader, err := Open(s.dir, true)
			s.Require().NoError(err)
			s.T().Cleanup(func() { s.Require().NoError(reader.Close()) })
			s.Require().NotEmpty(reader.sql.temporary)
			entries, err := os.ReadDir(reader.sql.temporary)
			s.Require().NoError(err)
			s.Len(entries, 2)
			s.assertRestored(reader, rt, old, last, first.Next)
			s.Require().NoError(reader.Close())
			entries, err = os.ReadDir(temp)
			s.Require().NoError(err)
			s.Empty(entries)
			s.Equal(before, captureTree(s.T(), s.dir))
			s.st, err = Open(s.dir, false)
			s.Require().NoError(err)
			defer s.st.Close()
			s.assertRestored(s.st, rt, old, last, first.Next)
			after := captureTree(s.T(), s.dir)
			for path, value := range before {
				if filepath.Ext(path) != ".sqlite" && path != "." {
					s.Equal(value, after[path], path)
				}
			}
		})
	}
}

func (s *appendSuite) assertRestored(st *Store, rt resource.Runtime, old, last events.SessionEvent, cursor string) {
	got, err := st.GetRuntime("codex")
	s.Require().NoError(err)
	s.Equal(rt, got)
	session, err := st.GetSession(resource.SessionRef{SessionID: "native"})
	s.Require().NoError(err)
	s.Equal(s.r, session)
	page, err := st.ListHistory("native", store.HistoryQuery{})
	s.Require().NoError(err)
	s.assertEvents([]events.SessionEvent{old, last}, page.Events)
	s.Equal(last.EventID, *page.SyncedEventID)
	page, err = st.ListHistory("native", store.HistoryQuery{After: cursor, Limit: 7})
	s.Require().NoError(err)
	s.assertEvents([]events.SessionEvent{last}, page.Events)
	progress, err := st.GetSyncProgress("native")
	s.Require().NoError(err)
	s.Equal(last.EventID, *progress)
}

func (s *appendSuite) TestFailedFullRecoveryRetainsAppendEvidenceAndCleansReadonlyReplicas() {
	other := s.r
	other.Metadata = resource.Metadata{Namespace: "default", Name: "other"}
	other.Status.SessionID = "other-native"
	other, err := s.st.CreateSession(other)
	s.Require().NoError(err)
	_, err = s.st.sql.conn.ExecContext(context.Background(), `CREATE TRIGGER history.fail_progress BEFORE UPDATE ON sync_progress BEGIN SELECT RAISE(ABORT, 'injected failure'); END`)
	s.Require().NoError(err)
	fact := s.fact("file success before SQL failure")
	_, err = s.st.Append("native", []events.SessionEvent{fact})
	s.Require().ErrorContains(err, "outcome_unknown")
	corruptPath := s.st.historyPath(other)
	s.Require().NoError(os.WriteFile(corruptPath, []byte("invalid committed record\n"), 0o600))
	s.Require().Error(s.st.Reconcile())
	_, err = os.Stat(filepath.Join(s.dir, ".append.pending.json"))
	s.Require().NoError(err, "full file validation must finish before recovery evidence is cleared")
	s.Require().NoError(os.WriteFile(corruptPath, nil, 0o600))
	s.Require().NoError(s.st.Reconcile())
	page, err := s.st.ListHistory("native", store.HistoryQuery{})
	s.Require().NoError(err)
	s.Require().Len(page.Events, 1)
	s.Equal(fact.EventID, page.Events[0].EventID)
	s.Require().NoError(s.st.Close())
	// Invalid committed history must fail even with healthy persistent replicas.
	s.Require().NoError(os.WriteFile(corruptPath, []byte("invalid committed record\n"), 0o600))
	before := captureTree(s.T(), s.dir)
	temp := s.T().TempDir()
	s.T().Setenv("TMPDIR", temp)
	reader, err := Open(s.dir, true)
	s.Nil(reader)
	s.Require().Error(err)
	s.Equal(before, captureTree(s.T(), s.dir))
	entries, err := os.ReadDir(temp)
	s.Require().NoError(err)
	s.Empty(entries)
}

func (s *appendSuite) TestCloseDeleteAndSameNameRecreationKeepDistinctIdentity() {
	rt, err := s.st.CreateRuntime(resource.Runtime{Metadata: resource.Metadata{Name: "codex"}, Spec: resource.RuntimeSpec{Type: "codex"}})
	s.Require().NoError(err)
	fact := s.fact("close retains this fact")
	_, err = s.st.Append("native", []events.SessionEvent{fact})
	s.Require().NoError(err)
	closed := s.r
	closed.Status.State = "offline"
	closed, err = s.st.UpdateSession(closed)
	s.Require().NoError(err)
	s.Equal(s.r.Metadata.Generation, closed.Metadata.Generation)
	s.Require().NoError(s.st.DeleteRuntime("codex", rt.Metadata.ResourceVersion))
	oldRuntimeUID := rt.Metadata.UID
	rt.Metadata = resource.Metadata{Name: "codex"}
	replacement, err := s.st.CreateRuntime(rt)
	s.Require().NoError(err)
	s.NotEqual(oldRuntimeUID, replacement.Metadata.UID)
	s.Require().NoError(s.st.Reconcile())
	got, err := s.st.GetSession(resource.SessionRef{SessionID: "native"})
	s.Require().NoError(err)
	s.Equal(closed, got)
	page, err := s.st.ListHistory("native", store.HistoryQuery{})
	s.Require().NoError(err)
	s.Require().Len(page.Events, 1)
	s.Equal(fact.EventID, page.Events[0].EventID)
	s.Require().NoError(s.st.DeleteSession(resource.SessionRef{SessionID: "native"}, closed.Metadata.ResourceVersion))
	next := s.r
	next.Metadata = resource.Metadata{Namespace: "default", Name: "demo"}
	next.Status.SessionID = "new-native"
	next, err = s.st.CreateSession(next)
	s.Require().NoError(err)
	s.NotEqual(closed.Metadata.UID, next.Metadata.UID)
	for _, reopen := range []bool{false, true} {
		if reopen {
			s.Require().NoError(s.st.Reconcile())
		}
		_, err = s.st.GetSession(resource.SessionRef{SessionID: "native"})
		s.Require().ErrorIs(err, store.ErrNotFound)
		page, err = s.st.ListHistory("new-native", store.HistoryQuery{})
		s.Require().NoError(err)
		s.Empty(page.Events)
		s.Nil(page.SyncedEventID)
		progress, err := s.st.GetSyncProgress("new-native")
		s.Require().NoError(err)
		s.Nil(progress)
		got, err = s.st.GetSession(resource.SessionRef{Namespace: "default", Name: "demo"})
		s.Require().NoError(err)
		s.Equal(next, got)
	}
}

// Interrupt actual startup after bounded recovery batches have reached disk.
// No Store escapes Open before the entire file view has been restored.
func TestInterruptedReplicaStartup(t *testing.T) {
	if dir := os.Getenv("MASS_TEST_REPLICA_STARTUP_DIR"); dir != "" {
		st, err := Open(dir, false)
		require.NoError(t, err)
		defer st.Close()
		require.NoError(t, os.WriteFile(os.Getenv("MASS_TEST_REPLICA_READY"), []byte("ready"), 0o600))
		return
	}
	dir := t.TempDir()
	st, err := Open(dir, false)
	require.NoError(t, err)
	owned := st
	t.Cleanup(func() { require.NoError(t, owned.Close()) })
	r, err := st.CreateSession(resource.Session{Metadata: resource.Metadata{Namespace: "default", Name: "demo"}, Spec: resource.SessionSpec{RuntimeRef: "codex"}, Status: resource.SessionStatus{SessionID: "native", State: events.Unknown}})
	require.NoError(t, err)
	const count = 2048
	var first, last events.SessionEvent
	for offset := 0; offset < count; offset += 128 {
		batch := make([]events.SessionEvent, 0, 128)
		for i := range 128 {
			e, err := events.NewEvent(events.EventBase{EventID: fmt.Sprintf("00000000-0000-4000-8000-%012d", offset+i), Namespace: "default", SessionName: "demo", SessionID: "native", ItemID: "startup-item", Time: time.Date(2026, 10, 20, 0, 0, 0, 0, time.UTC)}, events.AgentMessage{MessageID: strconv.Itoa(offset + i), Content: []events.ContentBlock{events.Text(strings.Repeat("x", 8192))}}, events.NewSource())
			require.NoError(t, err)
			batch = append(batch, e)
		}
		if offset == 0 {
			first = batch[0]
		}
		last = batch[len(batch)-1]
		_, err = st.Append("native", batch)
		require.NoError(t, err)
	}
	page, err := st.ListHistory("native", store.HistoryQuery{Limit: 1})
	require.NoError(t, err)
	cursor := page.Next
	path := st.historyPath(r)
	digest := func() [32]byte {
		f, err := os.Open(path)
		require.NoError(t, err)
		defer f.Close()
		h := sha256.New()
		_, err = io.Copy(h, f)
		require.NoError(t, err)
		var sum [32]byte
		copy(sum[:], h.Sum(nil))
		return sum
	}
	before := digest()
	require.NoError(t, st.Close())
	for _, name := range []string{"metadata.sqlite", "history.sqlite"} {
		require.NoError(t, os.Remove(filepath.Join(dir, name)))
	}
	ready := filepath.Join(t.TempDir(), "ready")
	binary, err := os.Executable()
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, binary, "-test.run=^TestInterruptedReplicaStartup$")
	cmd.Env = append(os.Environ(), "MASS_TEST_REPLICA_STARTUP_DIR="+dir, "MASS_TEST_REPLICA_READY="+ready)
	var output bytes.Buffer
	cmd.Stdout = &output
	cmd.Stderr = &output
	require.NoError(t, cmd.Start())
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		if cmd.ProcessState == nil {
			_ = cmd.Wait()
		}
	})
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	for {
		info, err := os.Stat(filepath.Join(dir, "history.sqlite"))
		if err == nil && info.Size() > 1024*1024 {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal("startup recovery did not reach disk batches")
		case <-ticker.C:
		}
	}
	require.NoError(t, cmd.Process.Kill())
	require.Error(t, cmd.Wait())
	_, err = os.Stat(ready)
	require.True(t, os.IsNotExist(err), "partial recovery cannot return a ready Store: %s", output.String())
	require.Equal(t, before, digest())
	st, err = Open(dir, false)
	require.NoError(t, err)
	reopened := st
	t.Cleanup(func() { require.NoError(t, reopened.Close()) })
	got, err := st.GetSession(resource.SessionRef{SessionID: "native"})
	require.NoError(t, err)
	require.Equal(t, r, got)
	page, err = st.ListHistory("native", store.HistoryQuery{Limit: 1})
	require.NoError(t, err)
	require.Equal(t, first.EventID, page.Events[0].EventID)
	page, err = st.ListHistory("native", store.HistoryQuery{After: cursor, Limit: count})
	require.NoError(t, err)
	require.Len(t, page.Events, count-1)
	require.Equal(t, last.EventID, page.Events[len(page.Events)-1].EventID)
	require.Equal(t, last.EventID, *page.SyncedEventID)
	progress, err := st.Append("native", []events.SessionEvent{last})
	require.NoError(t, err)
	require.Equal(t, last.EventID, *progress)
	require.Equal(t, before, digest())
}

func (s *appendSuite) TestReconcilePrefixValidationRetainsRecoveryEvidence() {
	other := s.r
	other.Metadata = resource.Metadata{Namespace: "default", Name: "other"}
	other.Status.SessionID = "other-native"
	other, err := s.st.CreateSession(other)
	s.Require().NoError(err)
	original := s.fact("original-marker")
	original.SessionName = "other"
	original.SessionID = "other-native"
	_, err = s.st.Append("other-native", []events.SessionEvent{original})
	s.Require().NoError(err)
	path := s.st.historyPath(other)
	before, err := os.ReadFile(path)
	s.Require().NoError(err)
	_, err = s.st.sql.conn.ExecContext(context.Background(), `CREATE TRIGGER history.fail_progress BEFORE UPDATE ON sync_progress BEGIN SELECT RAISE(ABORT, 'injected failure'); END`)
	s.Require().NoError(err)
	fact := s.fact("pending new fact")
	_, err = s.st.Append("native", []events.SessionEvent{fact})
	s.Require().ErrorContains(err, "outcome_unknown")
	changed := bytes.Replace(before, []byte("original-marker"), []byte("modified-marker"), 1)
	s.Require().NoError(os.WriteFile(path, changed, 0o600))
	s.Require().ErrorContains(s.st.Reconcile(), "committed history prefix changed")
	_, err = os.Stat(filepath.Join(s.dir, ".append.pending.json"))
	s.Require().NoError(err)
	_, err = s.st.ListHistory("native", store.HistoryQuery{})
	s.Require().ErrorContains(err, "outcome_unknown")
	s.Require().NoError(os.WriteFile(path, before, 0o600))
	s.Require().NoError(s.st.Reconcile())
	page, err := s.st.ListHistory("native", store.HistoryQuery{})
	s.Require().NoError(err)
	s.Require().Len(page.Events, 1)
	s.Equal(fact.EventID, page.Events[0].EventID)
}

// Source is intentionally private; compare the complete persisted public value.
func (s *appendSuite) assertEvents(want, got []events.SessionEvent) {
	s.T().Helper()
	expected, err := json.Marshal(want)
	s.Require().NoError(err)
	actual, err := json.Marshal(got)
	s.Require().NoError(err)
	s.JSONEq(string(expected), string(actual))
}

func (s *appendSuite) TestStatusUpdateDoesNotDeleteRetainedResourceReplica() {
	// Deletes are only necessary for deleted identities. An unrelated retained
	// resource delete failure must not break a status-only update.
	_, err := s.st.sql.conn.ExecContext(s.T().Context(), `CREATE TRIGGER fail_retained_delete BEFORE DELETE ON session BEGIN SELECT RAISE(ABORT, 'retained resource must not be deleted'); END`)
	s.Require().NoError(err)
	r := s.r
	r.Status.State = events.Idle
	updated, err := s.st.UpdateSession(r)
	s.Require().NoError(err)
	got, err := s.st.GetSession(resource.SessionRef{SessionID: "native"})
	s.Require().NoError(err)
	s.Equal(updated, got)
}

func (s *appendSuite) TestInvalidAppendRecoveryEvidenceIsRetained() {
	for _, damage := range []string{"json", "format", "identity", "duplicate", "record"} {
		s.Run(damage, func() {
			s.SetupTest()
			old := s.fact("committed")
			_, err := s.st.Append("native", []events.SessionEvent{old})
			s.Require().NoError(err)
			s.inject("before-write")
			_, err = s.st.Append("native", []events.SessionEvent{s.fact("pending")})
			s.Require().ErrorContains(err, "outcome_unknown")
			path := filepath.Join(s.dir, ".append.pending.json")
			var intent appendIntent
			s.Require().NoError(readJSON(path, &intent))
			switch damage {
			case "format":
				intent.Format = 999
			case "identity":
				intent.UID = "foreign"
			case "duplicate":
				intent.Data = append(intent.Data, intent.Data...)
			case "record":
				intent.Data = []byte("invalid record\n")
			}
			s.Require().NoError(atomicFile(path, intent))
			if damage == "json" {
				s.Require().NoError(os.WriteFile(path, []byte("{invalid"), 0o600))
			}
			evidence, err := os.ReadFile(path)
			s.Require().NoError(err)
			historyPath := s.st.historyPath(s.r)
			history, err := os.ReadFile(historyPath)
			s.Require().NoError(err)
			s.Require().NoError(s.st.Close())
			_, err = Open(s.dir, false)
			s.Require().Error(err)
			after, err := os.ReadFile(path)
			s.Require().NoError(err)
			s.Equal(evidence, after)
			after, err = os.ReadFile(historyPath)
			s.Require().NoError(err)
			s.Equal(history, after)
		})
	}
}

func (s *appendSuite) TestRecoveryValidatesAllCommittedHistoriesBeforeExtendingTail() {
	for _, mode := range []string{"reopen-corrupt", "reconcile-corrupt", "reconcile-changed"} {
		s.Run(mode, func() {
			s.SetupTest()
			other := s.r
			other.Metadata = resource.Metadata{Namespace: "default", Name: "other"}
			other.Status.SessionID = "other"
			other, err := s.st.CreateSession(other)
			s.Require().NoError(err)
			fact := s.fact("other-original")
			fact.SessionID = "other"
			fact.SessionName = "other"
			_, err = s.st.Append("other", []events.SessionEvent{fact})
			s.Require().NoError(err)
			originalOpen := s.inject("before-write")
			_, err = s.st.Append("native", []events.SessionEvent{s.fact("pending")})
			s.Require().ErrorContains(err, "outcome_unknown")
			path := s.st.historyPath(s.r)
			before, err := os.ReadFile(path)
			s.Require().NoError(err)
			pendingPath := filepath.Join(s.dir, ".append.pending.json")
			pending, err := os.ReadFile(pendingPath)
			s.Require().NoError(err)
			changed := []byte("invalid committed history\n")
			if mode == "reconcile-changed" {
				content, e := os.ReadFile(s.st.historyPath(other))
				s.Require().NoError(e)
				changed = bytes.Replace(content, []byte("other-original"), []byte("other-modified"), 1)
			}
			s.Require().NoError(os.WriteFile(s.st.historyPath(other), changed, 0o600))
			if mode == "reopen-corrupt" {
				s.Require().NoError(s.st.Close())
				_, err = Open(s.dir, false)
				s.Require().Error(err)
			} else {
				s.st.appendOpen = originalOpen
				s.Require().Error(s.st.Reconcile())
			}
			after, err := os.ReadFile(path)
			s.Require().NoError(err)
			s.Equal(before, after, "all committed histories must validate before any pending tail is extended")
			after, err = os.ReadFile(pendingPath)
			s.Require().NoError(err)
			s.Equal(pending, after)
		})
	}
}
