package github

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/zoumo/mass/superagent/triggers/internal/coreclient"
	"github.com/zoumo/mass/superagent/triggers/internal/triggerdb"
)

func TestGitHubSQLiteDatabaseIsolation(t *testing.T) {
	c := githubConfig{
		AccountID: "account", InstanceID: "github",
		DatabasePath: filepath.Join(t.TempDir(), "github.sqlite"),
		Repositories: []githubRepository{{Repository: "demo/project"}},
	}
	_, closeDB, err := openGitHubStore(c)
	if err != nil {
		t.Fatal(err)
	}
	opened := true
	defer func() {
		if opened {
			closeDB()
		}
	}()
	reject := func(config githubConfig, expected string) {
		t.Helper()
		_, closeUnexpected, err := openGitHubStore(config)
		if err == nil {
			closeUnexpected()
			t.Fatal("opened the wrong or already locked database")
		}
		if !strings.Contains(err.Error(), expected) {
			t.Fatalf("expected %q, got %v", expected, err)
		}
	}
	reject(c, "cannot acquire Trigger database lock")
	closeDB()
	opened = false

	another := c
	another.InstanceID = "different"
	reject(another, "belongs to a different")

	dwsPath := filepath.Join(t.TempDir(), "dws.sqlite")
	_, closeDWS, err := triggerdb.Open(dwsPath, triggerdb.Identity{
		ApplicationID: 0x44575354, Version: 1, Source: "dws", AccountID: c.AccountID, InstanceID: c.InstanceID,
	}, `CREATE TABLE dws_fixture (id INTEGER PRIMARY KEY)`)
	if err != nil {
		t.Fatal(err)
	}
	closeDWS()
	another = c
	another.DatabasePath = dwsPath
	reject(another, "invalid Trigger database type")
}

func TestGitHubSQLiteQueueRetainsFIFOAndPayloadAcrossRestart(t *testing.T) {
	repo := githubRepository{Repository: "demo/project", Prompt: "Original prompt"}
	c := githubConfig{
		AccountID: "account", InstanceID: "github",
		DatabasePath: filepath.Join(t.TempDir(), "github.sqlite"), Repositories: []githubRepository{repo},
	}
	store, closeDB, err := openGitHubStore(c)
	if err != nil {
		t.Fatal(err)
	}
	defer closeDB()
	now := time.Now().UTC()
	const scope = "notification/123"
	if err := triggerdb.Transaction(store.db, func(tx *sql.Tx) error {
		return saveGitHubNotice(tx, repo.Repository, now, githubNotice{ID: "123", Number: 42, UpdatedAt: now})
	}); err != nil {
		t.Fatal(err)
	}
	stream, err := store.stream(repo.Repository, scope)
	if err != nil {
		t.Fatal(err)
	}
	observations := []githubObservation{
		{Key: "2", Kind: "edited", Data: json.RawMessage(`{"number":2,"state":"open"}`), UpdatedAt: now, Title: "Analyzed first"},
		{Key: "1", Kind: "opened", Data: json.RawMessage(`{"number":1,"state":"open"}`), UpdatedAt: now, Title: "Analyzed second"},
	}
	if queued, err := store.commitReadback(c, repo, scope, stream, observations, time.Time{}, nil); err != nil || queued != 2 {
		t.Fatalf("scan queued %d items: %v", queued, err)
	}
	// Replaying stale progress must fail on the pending message identity.
	// It must also roll back a snapshot written earlier in the retry.
	retry := []githubObservation{{Key: "silent", Data: json.RawMessage(`{}`), UpdatedAt: now}, observations[0]}
	_, err = store.commitReadback(c, repo, scope, stream, retry, time.Time{}, nil)
	require.ErrorContains(t, err, "UNIQUE constraint failed: github_pending.repository, github_pending.message_id")
	items, err := store.scopeItems(repo.Repository, scope)
	require.NoError(t, err)
	require.NotContains(t, items, "silent")

	first, err := store.pendingHead(repo.Repository)
	if err != nil || !bytes.Contains(first.payload, []byte("Analyzed first")) {
		t.Fatalf("head was not first observation: %v", err)
	}
	var delivery githubDeliveryState
	delivery.DeliveryFailures = 1
	delivery.NextDeliveryAt = now.Add(time.Second)
	delivery.LastDeliveryError = "core_response_unknown"
	if err := store.saveDelivery(repo.Repository, delivery, 0); err != nil {
		t.Fatal(err)
	}
	closeDB()
	c.Repositories[0].Prompt = "Edited prompt"
	store, reopenClose, err := openGitHubStore(c)
	if err != nil {
		t.Fatal(err)
	}
	defer reopenClose()
	replayed, err := store.pendingHead(repo.Repository)
	if err != nil || replayed.sequence != first.sequence || !bytes.Equal(replayed.payload, first.payload) {
		t.Fatalf("pending head changed across restart: %v", err)
	}
	saved, err := store.delivery(repo.Repository)
	if err != nil || saved.Backlog != 2 || saved.DeliveryFailures != 1 ||
		saved.LastDeliveryError != delivery.LastDeliveryError || !saved.NextDeliveryAt.Equal(delivery.NextDeliveryAt) {
		t.Fatalf("delivery backoff changed across restart: %+v, %v", saved, err)
	}
	receipt := coreclient.Receipt{Status: "duplicate", MessageID: "mass-fixture"}
	if err := store.saveDelivery(repo.Repository, githubDeliveryState{Accepted: 1, Duplicates: 1, LastReceipt: receipt}, replayed.sequence); err != nil {
		t.Fatal(err)
	}
	second, err := store.pendingHead(repo.Repository)
	if err != nil || second.sequence <= first.sequence || !bytes.Contains(second.payload, []byte("Analyzed second")) {
		t.Fatalf("FIFO did not advance to the second observation: %v", err)
	}
	saved, err = store.delivery(repo.Repository)
	if err != nil || saved.Backlog != 1 || saved.Accepted != 1 || saved.Duplicates != 1 || saved.LastReceipt != receipt {
		t.Fatalf("acknowledgement did not persist receipt and remove head: %+v, %v", saved, err)
	}
}

func TestGitHubSQLiteReadbackRollbackKeepsNotificationRetryable(t *testing.T) {
	repo := githubRepository{Repository: "demo/project"}
	c := githubConfig{AccountID: "account", InstanceID: "github", DatabasePath: filepath.Join(t.TempDir(), "github.sqlite")}
	store, closeDB, err := openGitHubStore(c)
	if err != nil {
		t.Fatal(err)
	}
	defer closeDB()
	now := time.Now().UTC()
	const scope = "notification/123"
	if err := triggerdb.Transaction(store.db, func(tx *sql.Tx) error {
		return saveGitHubNotice(tx, repo.Repository, now, githubNotice{ID: "123", Number: 42, UpdatedAt: now, Pull: true})
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(t.Context(), `CREATE TRIGGER fail_github_readback BEFORE INSERT ON github_snapshots
		WHEN NEW.scope = 'tracked/42' BEGIN SELECT RAISE(ABORT, 'test readback failure'); END`); err != nil {
		t.Fatal(err)
	}
	stream := githubStreamState{EnabledAt: now, LastSuccessAt: now}
	pull := githubPullRecord{githubObjectRecord: githubObjectRecord{ID: 34, State: "open"}, Number: 42, UpdatedAt: now}
	observations := []githubObservation{{Key: "object", Kind: "opened", Data: json.RawMessage(`{}`), UpdatedAt: now, At: now}}
	if _, err := store.commitReadback(c, repo, scope, stream, observations, time.Time{}, &pull); err == nil {
		t.Fatal("readback committed despite an interrupted transaction")
	}
	saved, err := store.stream(repo.Repository, scope)
	if err != nil || !saved.Active || !saved.LastSuccessAt.IsZero() || saved.ItemCount != 1 {
		t.Fatalf("failed readback changed notification progress: %+v, %v", saved, err)
	}
	if _, err := store.pendingHead(repo.Repository); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("failed readback changed delivery queue: %v", err)
	}
	if _, err := store.stream(repo.Repository, "tracked/42"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("failed readback changed tracking schedule: %v", err)
	}
	if _, err := store.db.ExecContext(t.Context(), "DROP TRIGGER fail_github_readback"); err != nil {
		t.Fatal(err)
	}
	if queued, err := store.commitReadback(c, repo, scope, stream, observations, time.Time{}, &pull); err != nil || queued != 1 {
		t.Fatalf("readback did not recover: queued %d, %v", queued, err)
	}
	saved, err = store.stream(repo.Repository, scope)
	if err != nil || saved.Active || saved.ItemCount != 2 || !saved.LastSuccessAt.Equal(now) {
		t.Fatalf("successful readback did not advance progress: %+v, %v", saved, err)
	}
}

func TestGitHubSQLiteCompositeCheckpoints(t *testing.T) {
	c := githubConfig{AccountID: "account", InstanceID: "github", DatabasePath: filepath.Join(t.TempDir(), "github.sqlite")}
	store, closeDB, err := openGitHubStore(c)
	require.NoError(t, err)
	defer closeDB()

	now := time.Now().UTC()
	ctx := t.Context()
	for _, item := range []struct{ repository, id string }{
		{"demo/first", "123"}, {"demo/second", "123"}, {"demo/first", "456"},
	} {
		require.NoError(t, triggerdb.Transaction(store.db, func(tx *sql.Tx) error {
			notice := githubNotice{ID: item.id, Number: 42, UpdatedAt: now}
			return saveGitHubNotice(tx, item.repository, now, notice)
		}))
		scope := githubNoticePrefix + item.id
		state, err := store.stream(item.repository, scope)
		require.NoError(t, err)
		require.Equal(t, 1, state.ItemCount)

		repo := githubRepository{Repository: item.repository}
		baseline := []githubObservation{{Key: "object", Data: json.RawMessage(`{}`), UpdatedAt: now}}
		for range 2 {
			queued, err := store.commitReadback(c, repo, scope, state, baseline, time.Time{}, nil)
			require.NoError(t, err)
			require.Zero(t, queued)
		}
		state, err = store.stream(item.repository, scope)
		require.NoError(t, err)
		require.Equal(t, 2, state.ItemCount)
		_, err = store.pendingHead(item.repository)
		require.ErrorIs(t, err, sql.ErrNoRows)
	}

	_, err = store.db.ExecContext(ctx, `INSERT INTO github_streams (repository, scope, kind, active, enabled_at)
		VALUES ('demo/first', 'notification/123', 'discussion', 1, 0)`)
	require.ErrorContains(t, err, "UNIQUE constraint failed: github_streams.repository, github_streams.scope")
	_, err = store.db.ExecContext(ctx, `INSERT INTO github_snapshots (repository, scope, object_key, version, updated_at, data)
		VALUES ('demo/first', 'notification/123', 'object', 0, 0, '{}')`)
	require.ErrorContains(t, err, "UNIQUE constraint failed: github_snapshots.repository, github_snapshots.scope, github_snapshots.object_key")
	_, err = store.db.ExecContext(ctx, `DELETE FROM github_snapshots WHERE repository = 'demo/first'
		AND scope = 'notification/123' AND object_key = 'object'`)
	require.NoError(t, err)

	state, err := store.stream("demo/first", "notification/123")
	require.NoError(t, err)
	require.Equal(t, 1, state.ItemCount)
	for _, item := range []struct{ repository, scope string }{
		{"demo/second", "notification/123"}, {"demo/first", "notification/456"},
	} {
		state, err := store.stream(item.repository, item.scope)
		require.NoError(t, err)
		require.Equal(t, 2, state.ItemCount)
	}
}
