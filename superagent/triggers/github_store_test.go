package triggers

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"
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
	_, closeDWS, err := openDWSQueue(Config{
		Source: "dws", AccountID: c.AccountID, InstanceID: c.InstanceID, DatabasePath: dwsPath,
	})
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
	if err := triggerTransaction(store.db, func(tx *sql.Tx) error {
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
	saved, err := readGitHubDelivery(store.db, repo.Repository)
	if err != nil || saved.Backlog != 2 || saved.DeliveryFailures != 1 ||
		saved.LastDeliveryError != delivery.LastDeliveryError || !saved.NextDeliveryAt.Equal(delivery.NextDeliveryAt) {
		t.Fatalf("delivery backoff changed across restart: %+v, %v", saved, err)
	}
	receipt := Receipt{Status: "duplicate", MessageID: "mass-fixture"}
	if err := store.saveDelivery(repo.Repository, githubDeliveryState{Accepted: 1, Duplicates: 1, LastReceipt: receipt}, replayed.sequence); err != nil {
		t.Fatal(err)
	}
	second, err := store.pendingHead(repo.Repository)
	if err != nil || second.sequence <= first.sequence || !bytes.Contains(second.payload, []byte("Analyzed second")) {
		t.Fatalf("FIFO did not advance to the second observation: %v", err)
	}
	saved, err = readGitHubDelivery(store.db, repo.Repository)
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
	if err := triggerTransaction(store.db, func(tx *sql.Tx) error {
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
