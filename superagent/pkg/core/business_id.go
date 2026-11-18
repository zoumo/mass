package core

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
)

// Derive stable creation IDs from the binding and the caller's operation ID.
// The request digest lets the business record detect changed replay parameters.
func creationKey(prefix, scope, operation string) string {
	sum := sha256.Sum256([]byte(scope + "\x00" + operation))
	return prefix + "_" + hex.EncodeToString(sum[:16])
}

func creationID(key string, request any) string {
	raw, err := json.Marshal(request)
	if err != nil {
		panic(err) // Only decoded, concrete command structs are passed here.
	}
	sum := sha256.Sum256(raw)
	return key + "_" + hex.EncodeToString(sum[:16])
}

func existingCreation(ctx context.Context, tx *sql.Tx, table, key, id string) (bool, error) {
	var found string
	err := tx.QueryRowContext(ctx, "SELECT id FROM "+table+" WHERE id GLOB ?", key+"_*").Scan(&found)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return false, nil
	case err != nil:
		return false, storageError()
	case found != id:
		return false, problem("conflict", "operationId has different creation parameters")
	default:
		return true, nil
	}
}
