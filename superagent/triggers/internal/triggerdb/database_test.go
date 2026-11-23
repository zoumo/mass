package triggerdb

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

const queueSchema = `CREATE TABLE pending (payload TEXT NOT NULL)`

func TestDatabaseVersionGateAndUncheckpointedQueue(t *testing.T) {
	for _, tc := range []struct {
		name    string
		version int
	}{
		{"rejects previous schema without touching state", 1},
		{"reopens current schema and recovers pending row", 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			oldPath := filepath.Join(t.TempDir(), "active.sqlite")
			identity := Identity{ApplicationID: 0x44575354, Version: tc.version, Source: "dws", AccountID: "a", InstanceID: "i"}
			writer, closeWriter, err := Open(oldPath, identity, queueSchema)
			require.NoError(t, err)
			t.Cleanup(closeWriter)
			_, err = writer.ExecContext(t.Context(), `INSERT INTO pending VALUES ('queued message')`)
			require.NoError(t, err)

			// Model an unclean exit by copying the main database and its WAL
			// before the writer can checkpoint on close.
			path, initial := copyDatabaseAndWAL(t, oldPath)
			identity.Version = 2
			if tc.version != identity.Version {
				require.ErrorContains(t, CheckDestination(path, identity), "unsupported Trigger database schema version 1")
				_, _, err := Open(path, identity, queueSchema)
				require.ErrorContains(t, err, "unsupported Trigger database schema version 1")
				assertUnchangedDatabase(t, path, initial)
				return
			}
			require.NoError(t, CheckDestination(path, identity))
			assertUnchangedDatabase(t, path, initial)
			db, closeDB, err := Open(path, identity, queueSchema)
			require.NoError(t, err)
			t.Cleanup(closeDB)
			var payload string
			require.NoError(t, db.QueryRowContext(t.Context(), `SELECT payload FROM pending`).Scan(&payload))
			require.Equal(t, "queued message", payload)
		})
	}
}

func TestRejectsDatabaseWithIdentityOnlyInWAL(t *testing.T) {
	oldPath := filepath.Join(t.TempDir(), "active.sqlite")
	db, err := sql.Open("sqlite", oldPath)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	_, err = db.ExecContext(t.Context(), `PRAGMA journal_mode = WAL; PRAGMA application_id = 1146573652;
		PRAGMA user_version = 1; CREATE TABLE pending (payload TEXT NOT NULL); INSERT INTO pending VALUES ('old message');`)
	require.NoError(t, err)
	path, initial := copyDatabaseAndWAL(t, oldPath)
	identity := Identity{ApplicationID: 0x44575354, Version: 2, Source: "dws", AccountID: "a", InstanceID: "i"}
	require.ErrorContains(t, CheckDestination(path, identity), "uncheckpointed schema")
	_, _, err = Open(path, identity, queueSchema)
	require.ErrorContains(t, err, "uncheckpointed schema")
	assertUnchangedDatabase(t, path, initial)
}

func copyDatabaseAndWAL(t *testing.T, from string) (string, map[string][]byte) {
	t.Helper()
	dest := filepath.Join(t.TempDir(), "after-crash.sqlite")
	initial := make(map[string][]byte, 2)
	for _, suffix := range []string{"", "-wal"} {
		raw, err := os.ReadFile(from + suffix)
		require.NoError(t, err)
		require.NotEmpty(t, raw)
		require.NoError(t, os.WriteFile(dest+suffix, raw, 0o600))
		initial[suffix] = raw
	}
	return dest, initial
}

func assertUnchangedDatabase(t *testing.T, path string, initial map[string][]byte) {
	t.Helper()
	for suffix, expected := range initial {
		raw, err := os.ReadFile(path + suffix)
		require.NoError(t, err)
		require.Equal(t, expected, raw)
	}
	require.NoFileExists(t, path+"-shm")
}
