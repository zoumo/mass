package core

import (
	"context"
	"database/sql"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"

	coreent "github.com/zoumo/mass/superagent/pkg/core/internal/ent"
)

func newCoreEntClient(db *sql.DB) *coreent.Client {
	return coreent.NewClient(coreent.Driver(entsql.OpenDB(dialect.SQLite, db)))
}

// entClientForTx binds generated operations to the same connection as raw SQL.
// The caller owns the transaction outcome and must propagate any Ent write error.
func entClientForTx(tx *sql.Tx) *coreent.Client {
	conn := entsql.NewDriver(dialect.SQLite, entsql.Conn{ExecQuerier: tx})
	return coreent.NewClient(coreent.Driver(entTxDriver{Conn: conn.Conn}))
}

// Ent can request a transaction for a multi-statement operation. The caller's
// transaction already covers those statements and owns the connection lifecycle.
type entTxDriver struct{ entsql.Conn }

func (entTxDriver) Dialect() string { return dialect.SQLite }
func (entTxDriver) Close() error    { return nil }
func (d entTxDriver) Tx(context.Context) (dialect.Tx, error) {
	return dialect.NopTx(d), nil
}
