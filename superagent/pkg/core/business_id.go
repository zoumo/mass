package core

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
)

// Scope the operation ID to its creator. The request digest detects changed
// replay parameters even when the resource ID is a separate number.
func creationKey(prefix, scope, operation string) string {
	sum := sha256.Sum256([]byte(scope + "\x00" + operation))
	return prefix + "_" + hex.EncodeToString(sum[:16])
}

func creationID(key string, request any) string {
	return key + "_" + creationDigest(request)
}

func creationDigest(request any) string {
	raw, err := json.Marshal(request)
	if err != nil {
		panic(err) // Only decoded, concrete command structs are passed here.
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:16])
}

// numberedCreation returns the existing short ID when the persisted request
// matches, or an empty ID when the operation has not created a record.
func numberedCreation(ctx context.Context, tx *sql.Tx, digest, query string, args ...any) (string, error) {
	var id, savedDigest string
	err := tx.QueryRowContext(ctx, query, args...).Scan(&id, &savedDigest)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return "", nil
	case err != nil:
		return "", storageError()
	case savedDigest != digest:
		return "", problem("conflict", "operationId has different creation parameters")
	default:
		return id, nil
	}
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
