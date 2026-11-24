package metadata

import (
	"database/sql"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/zoumo/mass/massd/pkg/resource"
)

func TestSQLiteCommitUnknownRequiresReopen(t *testing.T) {
	dir := t.TempDir()
	writer, err := OpenSQLite(dir, false)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, writer.Close()) })
	original, err := writer.CreateRuntime(resource.Runtime{Metadata: resource.Metadata{Name: "saved"}})
	require.NoError(t, err)

	// A separate reader can hold a shared SQLite lock until commit. The writer
	// can stage an update while the lock is held, but cannot commit it.
	reader, err := OpenSQLite(dir, true)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, reader.Close()) })
	ctx := t.Context()
	tx, err := reader.sql.conn.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	require.NoError(t, err)
	defer func() { _ = tx.Rollback() }()
	var format int
	require.NoError(t, tx.QueryRowContext(ctx, "SELECT format FROM authority").Scan(&format))
	_, err = writer.sql.conn.ExecContext(ctx, "PRAGMA busy_timeout=0")
	require.NoError(t, err)

	changed := original
	changed.Spec.Type = "codex"
	_, err = writer.UpdateRuntime(changed)
	require.ErrorContains(t, err, "outcome_unknown: metadata authority commit:")
	_, err = writer.GetRuntime("saved")
	require.ErrorContains(t, err, "outcome_unknown: reopen Store")
	require.ErrorContains(t, writer.Reconcile(), "SQLite authority requires reopen")
	require.NoError(t, tx.Rollback())
	require.NoError(t, writer.Close())

	reopened, err := OpenSQLite(dir, false)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, reopened.Close()) })
	got, err := reopened.GetRuntime("saved")
	require.NoError(t, err)
	require.Equal(t, original, got)
	saved, err := reopened.UpdateRuntime(changed)
	require.NoError(t, err)
	require.EqualValues(t, 2, saved.Metadata.Generation)
	require.NotEqual(t, original.Metadata.ResourceVersion, saved.Metadata.ResourceVersion)
}
