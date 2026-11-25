package core

import (
	"database/sql"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestStartupQueryIndexNamesMatchSchema(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	require.NoError(t, err)
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	ctx := t.Context()

	readIndexNames := func() []string {
		t.Helper()
		rows, err := db.QueryContext(ctx, "SELECT name FROM sqlite_schema WHERE type='index'")
		require.NoError(t, err)
		defer rows.Close()

		var names []string
		for rows.Next() {
			var name string
			require.NoError(t, rows.Scan(&name))
			names = append(names, name)
		}
		require.NoError(t, rows.Err())
		return names
	}

	_, err = db.ExecContext(ctx, schema)
	require.NoError(t, err)
	expected := append(readIndexNames(), queryIndexNames...)

	_, err = db.ExecContext(ctx, queryIndexSchema)
	require.NoError(t, err)
	require.ElementsMatch(t, expected, readIndexNames(),
		"queryIndexNames must list every index installed by 0028-query-indexes.sql")
}
