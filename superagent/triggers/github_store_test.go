package triggers

import (
	"bytes"
	"encoding/json"
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
	stream, err := store.stream(repo.Repository, "issues")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	observations := []githubObservation{
		{Key: "2", Kind: "edited", Data: json.RawMessage(`{"number":2,"state":"open"}`), UpdatedAt: now, Title: "Analyzed first"},
		{Key: "1", Kind: "opened", Data: json.RawMessage(`{"number":1,"state":"open"}`), UpdatedAt: now, Title: "Analyzed second"},
	}
	if queued, err := store.commitScan(c, repo, "issues", stream, observations, now, now.Add(time.Minute), time.Time{}, true); err != nil || queued != 2 {
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
