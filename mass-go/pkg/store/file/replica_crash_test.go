package file

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"modernc.org/sqlite"

	"github.com/zoumo/mass/mass-go/pkg/events"
	"github.com/zoumo/mass/mass-go/pkg/resource"
	"github.com/zoumo/mass/mass-go/pkg/store"
)

// This driver only observes/interrupts real disk SQLite boundaries. Successful
// behavior is asserted through Store, never by reading SQL rows.
type (
	crashDriver struct{ stage, dir string }
	crashConn   struct {
		driver.Conn
		fault crashDriver
	}
	crashTx struct {
		driver.Tx
		fault crashDriver
	}
)

func (d crashDriver) Open(name string) (driver.Conn, error) {
	c, err := (&sqlite.Driver{}).Open(name)
	if err != nil {
		return nil, err
	}
	return crashConn{c, d}, nil
}

func (c crashConn) BeginTx(ctx context.Context, opts driver.TxOptions) (driver.Tx, error) {
	if c.fault.stage == "sql-begin" {
		os.Exit(77)
	}
	tx, err := c.Conn.(driver.ConnBeginTx).BeginTx(ctx, opts)
	if err != nil {
		return nil, err
	}
	return crashTx{tx, c.fault}, nil
}

func (c crashConn) ExecContext(ctx context.Context, q string, args []driver.NamedValue) (driver.Result, error) {
	boundary := ""
	if strings.HasPrefix(q, "DELETE FROM session") {
		boundary = "metadata-delete"
	}
	if strings.HasPrefix(q, "DELETE FROM history.event") {
		boundary = "history-delete"
	}
	if strings.HasPrefix(q, "DELETE FROM history.sync_progress") {
		boundary = "progress-delete"
	}
	if strings.HasPrefix(q, "INSERT INTO history.event") {
		boundary = "event"
	}
	if strings.HasPrefix(q, "UPDATE history.sync_progress") {
		boundary = "progress"
	}
	if boundary != "" && c.fault.stage == "before-"+boundary {
		os.Exit(77)
	}
	result, err := c.Conn.(driver.ExecerContext).ExecContext(ctx, q, args)
	if err == nil && boundary != "" && c.fault.stage == "after-"+boundary {
		os.Exit(77)
	}
	return result, err
}

func (c crashConn) QueryContext(ctx context.Context, q string, args []driver.NamedValue) (driver.Rows, error) {
	return c.Conn.(driver.QueryerContext).QueryContext(ctx, q, args)
}

func (tx crashTx) Commit() error {
	if tx.fault.stage == "before-commit" {
		os.Exit(77)
	}
	if tx.fault.stage == "commit-error" {
		_ = tx.Tx.Rollback()
		return errors.New("injected commit failure")
	}
	err := tx.Tx.Commit()
	if err == nil && tx.fault.stage == "after-commit" {
		os.Exit(77)
	}
	if err == nil && tx.fault.stage == "cleanup-error" {
		return os.Chmod(tx.fault.dir, 0o500)
	}
	return err
}

func installCrashSQL(t *testing.T, st *Store, stage string) {
	t.Helper()
	require.NoError(t, st.sql.close())
	sql.Register("crash-sqlite", crashDriver{stage, st.dir})
	dsn := (&url.URL{Scheme: "file", Path: filepath.Join(st.dir, "metadata.sqlite")}).String()
	db, err := sql.Open("crash-sqlite", dsn)
	require.NoError(t, err)
	st.sql = &replicas{db: db}
	st.sql.conn, err = db.Conn(t.Context())
	require.NoError(t, err)
	_, err = st.sql.conn.ExecContext(t.Context(), "ATTACH DATABASE ? AS history", filepath.Join(st.dir, "history.sqlite"))
	require.NoError(t, err)
	_, err = st.sql.conn.ExecContext(t.Context(), "PRAGMA main.journal_mode=DELETE; PRAGMA history.journal_mode=DELETE; PRAGMA main.synchronous=FULL; PRAGMA history.synchronous=FULL")
	require.NoError(t, err)
}

func TestReplicaCommitProcessExit(t *testing.T) {
	if dir := os.Getenv("MASS_TEST_SQL_CRASH_DIR"); dir != "" {
		st, err := Open(dir, false)
		require.NoError(t, err)
		stage := os.Getenv("MASS_TEST_SQL_CRASH_STAGE")
		installCrashSQL(t, st, stage)
		var e events.SessionEvent
		data, err := os.ReadFile(filepath.Join(dir, "peer-event.json"))
		require.NoError(t, err)
		require.NoError(t, json.Unmarshal(data, &e))
		_, err = st.Append("native", []events.SessionEvent{e})
		if stage == "commit-error" || stage == "cleanup-error" {
			require.ErrorContains(t, err, "outcome_unknown")
			_, readErr := st.ListHistory("native", store.HistoryQuery{})
			require.ErrorContains(t, readErr, "outcome_unknown")
			require.NoError(t, os.Chmod(dir, 0o700))
			_, statErr := os.Stat(filepath.Join(dir, ".append.pending.json"))
			require.NoError(t, statErr)
		} else {
			require.NoError(t, err)
			require.Equal(t, "response-lost", stage)
		}
		// The remote caller never receives this acknowledgement. Cleanup already ran.
		os.Exit(77)
	}
	for _, stage := range []string{"sql-begin", "before-event", "after-event", "before-progress", "after-progress", "before-commit", "commit-error", "after-commit", "cleanup-error", "response-lost"} {
		t.Run(stage, func(t *testing.T) {
			if stage == "cleanup-error" && os.Geteuid() == 0 {
				t.Skip("directory permission fault requires non-root user")
			}
			dir := t.TempDir()
			t.Cleanup(func() { require.NoError(t, os.Chmod(dir, 0o700)) })
			st, err := Open(dir, false)
			require.NoError(t, err)
			original := st
			t.Cleanup(func() { require.NoError(t, original.Close()) })
			r, err := st.CreateSession(resource.Session{Metadata: resource.Metadata{Namespace: "default", Name: "demo"}, Spec: resource.SessionSpec{RuntimeRef: "agent"}, Status: resource.SessionStatus{SessionID: "native", State: events.Unknown}})
			require.NoError(t, err)
			makeFact := func(text string) events.SessionEvent {
				e, eErr := events.NewEvent(events.EventBase{EventID: uuid.NewString(), Namespace: "default", SessionName: "demo", SessionID: "native", ItemID: text, Time: time.Date(2026, 10, 20, 1, 0, 0, 0, time.UTC)}, events.AgentMessage{MessageID: text, Content: []events.ContentBlock{events.Text(text)}}, events.NewSource())
				require.NoError(t, eErr)
				return e
			}
			old, fact := makeFact("old"), makeFact("new")
			_, err = st.Append("native", []events.SessionEvent{old})
			require.NoError(t, err)
			prefix, err := os.ReadFile(st.historyPath(r))
			require.NoError(t, err)
			// Capture a real continuation cursor by adding a second committed fact.
			middle := makeFact("middle")
			_, err = st.Append("native", []events.SessionEvent{middle})
			require.NoError(t, err)
			page, err := st.ListHistory("native", store.HistoryQuery{Limit: 1})
			require.NoError(t, err)
			require.NotEmpty(t, page.Next)
			require.NoError(t, atomicFile(filepath.Join(dir, "peer-event.json"), fact))
			require.NoError(t, st.Close())
			binary, err := os.Executable()
			require.NoError(t, err)
			ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, binary, "-test.run=^TestReplicaCommitProcessExit$")
			cmd.Env = append(os.Environ(), "MASS_TEST_SQL_CRASH_DIR="+dir, "MASS_TEST_SQL_CRASH_STAGE="+stage)
			out, err := cmd.CombinedOutput()
			var exited *exec.ExitError
			require.ErrorAs(t, err, &exited, string(out))
			require.Equal(t, 77, exited.ExitCode(), string(out))
			st, err = Open(dir, false)
			require.NoError(t, err)
			reopened := st
			t.Cleanup(func() { require.NoError(t, reopened.Close()) })
			full, err := st.ListHistory("native", store.HistoryQuery{})
			require.NoError(t, err)
			require.Len(t, full.Events, 3)
			require.Equal(t, []string{old.EventID, middle.EventID, fact.EventID}, []string{full.Events[0].EventID, full.Events[1].EventID, full.Events[2].EventID})
			require.Equal(t, fact.EventID, *full.SyncedEventID)
			next, err := st.ListHistory("native", store.HistoryQuery{After: page.Next, Limit: 8})
			require.NoError(t, err)
			require.Len(t, next.Events, 2)
			require.Equal(t, middle.EventID, next.Events[0].EventID)
			committed, err := os.ReadFile(st.historyPath(r))
			require.NoError(t, err)
			require.True(t, strings.HasPrefix(string(committed), string(prefix)))
			_, err = st.Append("native", []events.SessionEvent{old, fact, fact})
			require.NoError(t, err)
			after, err := os.ReadFile(st.historyPath(r))
			require.NoError(t, err)
			require.Equal(t, committed, after)
			got, err := st.GetSession(resource.SessionRef{SessionID: "native"})
			require.NoError(t, err)
			require.Equal(t, r, got)
			_, err = os.Stat(filepath.Join(dir, ".append.pending.json"))
			require.True(t, os.IsNotExist(err))
		})
	}
}

// The same observer interrupts transactions spanning both attached databases.
// File deletion is already authoritative; SQL must never resurrect that Session.
func TestMetadataReplicaCommitProcessExit(t *testing.T) {
	if dir := os.Getenv("MASS_TEST_METADATA_CRASH_DIR"); dir != "" {
		st, err := Open(dir, false)
		require.NoError(t, err)
		stage := os.Getenv("MASS_TEST_SQL_CRASH_STAGE")
		installCrashSQL(t, st, stage)
		r, err := st.GetSession(resource.SessionRef{SessionID: "deleted"})
		require.NoError(t, err)
		err = st.DeleteSession(resource.SessionRef{SessionID: "deleted"}, r.Metadata.ResourceVersion)
		if stage == "commit-error" || stage == "cleanup-error" {
			require.ErrorContains(t, err, "outcome_unknown")
			_, readErr := st.GetSession(resource.SessionRef{SessionID: "retained"})
			require.ErrorContains(t, readErr, "outcome_unknown")
			require.NoError(t, os.Chmod(dir, 0o700))
			_, statErr := os.Stat(filepath.Join(dir, ".pending.json"))
			require.NoError(t, statErr)
		} else {
			require.NoError(t, err)
			require.Equal(t, "response-lost", stage)
		}
		os.Exit(77)
	}
	for _, stage := range []string{"before-metadata-delete", "after-metadata-delete", "after-history-delete", "after-progress-delete", "before-commit", "commit-error", "after-commit", "cleanup-error", "response-lost"} {
		t.Run(stage, func(t *testing.T) {
			if stage == "cleanup-error" && os.Geteuid() == 0 {
				t.Skip("directory permission fault requires non-root user")
			}
			dir := t.TempDir()
			t.Cleanup(func() { require.NoError(t, os.Chmod(dir, 0o700)) })
			st, err := Open(dir, false)
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, st.Close()) })
			var retained resource.Session
			var originalFact events.SessionEvent
			for _, id := range []string{"deleted", "retained"} {
				r, e := st.CreateSession(resource.Session{Metadata: resource.Metadata{Namespace: "multi", Name: id}, Spec: resource.SessionSpec{RuntimeRef: "agent"}, Status: resource.SessionStatus{SessionID: id, State: events.Unknown}})
				require.NoError(t, e)
				fact, e := events.NewEvent(events.EventBase{EventID: uuid.NewString(), Namespace: "multi", SessionName: id, SessionID: id, ItemID: "fact", Time: time.Now().UTC()}, events.AgentMessage{MessageID: "fact", Content: []events.ContentBlock{events.Text(id)}}, events.NewSource())
				require.NoError(t, e)
				_, e = st.Append(id, []events.SessionEvent{fact})
				require.NoError(t, e)
				if id == "retained" {
					retained = r
					originalFact = fact
				}
			}
			before, err := os.ReadFile(st.historyPath(retained))
			require.NoError(t, err)
			require.NoError(t, st.Close())
			binary, err := os.Executable()
			require.NoError(t, err)
			ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, binary, "-test.run=^TestMetadataReplicaCommitProcessExit$")
			cmd.Env = append(os.Environ(), "MASS_TEST_METADATA_CRASH_DIR="+dir, "MASS_TEST_SQL_CRASH_STAGE="+stage)
			output, err := cmd.CombinedOutput()
			var exited *exec.ExitError
			require.ErrorAs(t, err, &exited, string(output))
			require.Equal(t, 77, exited.ExitCode(), string(output))
			recovered, err := Open(dir, false)
			require.NoError(t, err)
			defer recovered.Close()
			_, err = recovered.GetSession(resource.SessionRef{SessionID: "deleted"})
			require.ErrorIs(t, err, store.ErrNotFound)
			_, err = recovered.ListHistory("deleted", store.HistoryQuery{})
			require.ErrorIs(t, err, store.ErrNotFound)
			got, err := recovered.GetSession(resource.SessionRef{SessionID: "retained"})
			require.NoError(t, err)
			require.Equal(t, retained, got)
			page, err := recovered.ListHistory("retained", store.HistoryQuery{})
			require.NoError(t, err)
			require.Len(t, page.Events, 1)
			require.Equal(t, originalFact.EventID, page.Events[0].EventID)
			require.Equal(t, originalFact.EventID, *page.SyncedEventID)
			after, err := os.ReadFile(recovered.historyPath(retained))
			require.NoError(t, err)
			require.Equal(t, before, after)
			_, err = os.Stat(filepath.Join(dir, ".pending.json"))
			require.True(t, os.IsNotExist(err))
			recreated, err := recovered.CreateSession(resource.Session{Metadata: resource.Metadata{Namespace: "multi", Name: "deleted"}, Spec: resource.SessionSpec{RuntimeRef: "agent"}, Status: resource.SessionStatus{SessionID: "new-identity", State: events.Unknown}})
			require.NoError(t, err)
			require.NotEmpty(t, recreated.Metadata.UID)
			empty, err := recovered.ListHistory("new-identity", store.HistoryQuery{})
			require.NoError(t, err)
			require.Empty(t, empty.Events)
			require.Nil(t, empty.SyncedEventID)
			require.NoError(t, recovered.Reconcile())
			empty, err = recovered.ListHistory("new-identity", store.HistoryQuery{})
			require.NoError(t, err)
			require.Empty(t, empty.Events)
			require.Nil(t, empty.SyncedEventID)
		})
	}
}
