package service_test

import (
	"bufio"
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/zoumo/mass/mass-go/pkg/events"
	eventfile "github.com/zoumo/mass/mass-go/pkg/eventstore/file"
	metafile "github.com/zoumo/mass/mass-go/pkg/metastore/file"
	"github.com/zoumo/mass/mass-go/pkg/resource"
	"github.com/zoumo/mass/mass-go/pkg/runtime"
	"github.com/zoumo/mass/mass-go/pkg/service"
	"github.com/zoumo/mass/mass-go/pkg/store"
	"github.com/zoumo/mass/mass-go/pkg/transport"
)

// Real file/SQLite failure through the existing synthetic Driver seam. No model
// or native Agent behavior is claimed by these HTTP/SSE assertions.
func TestSQLReplicaFailureNeverPublishesUncoordinatedFact(t *testing.T) {
	dir := t.TempDir()
	meta, err := metafile.Open(dir, false)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, meta.Close()) })
	st, err := eventfile.Open(dir, false)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, st.Close()) })
	d := &workDriver{pipe: events.NewEventPipe(16), entered: make(chan context.Context, 1), release: make(chan struct{})}
	svc := service.New(meta, st, runtime.NewRegistry(map[string]runtime.Factory{"fixture": workFactory{d}}))
	t.Cleanup(func() {
		_ = st.Reconcile()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = svc.ReconcileHistory(ctx, resource.SessionRef{SessionID: "original"})
		require.NoError(t, svc.Shutdown(ctx))
	})
	_, err = svc.CreateRuntime(resource.Runtime{Metadata: resource.Metadata{Name: "agent"}, Spec: resource.RuntimeSpec{Type: "fixture", Config: runtime.Config{}}})
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	_, err = svc.StartRuntime(ctx, "agent")
	require.NoError(t, err)
	_, err = svc.CreateSession(ctx, resource.Session{Metadata: resource.Metadata{Namespace: "one", Name: "demo"}, Spec: resource.SessionSpec{RuntimeRef: "agent", SessionSpec: runtime.SessionSpec{Cwd: "/synthetic"}}})
	require.NoError(t, err)
	handler, err := transport.New(svc, transport.Options{Token: "fixture"})
	require.NoError(t, err)
	defer handler.Shutdown()
	server := httptest.NewServer(handler)
	defer server.Close()
	get := func(path string) *http.Response {
		req, e := http.NewRequestWithContext(ctx, http.MethodGet, server.URL+path, http.NoBody)
		require.NoError(t, e)
		req.Header.Set("Authorization", "Bearer fixture")
		res, e := server.Client().Do(req)
		require.NoError(t, e)
		return res
	}
	stream := get("/apis/v1/sessions/original/events/stream")
	defer stream.Body.Close()
	reader := bufio.NewReader(stream.Body)
	ready, err := readFrame(reader)
	require.NoError(t, err)
	require.Contains(t, ready, "subscription.ready")
	// An actual SQLite trigger aborts the event/progress transaction after the
	// authoritative append. The Store must seal all potentially incomplete reads.
	dsn := (&url.URL{Scheme: "file", Path: filepath.Join(dir, "history.sqlite")}).String()
	db, err := sql.Open("sqlite", dsn)
	require.NoError(t, err)
	defer db.Close()
	_, err = db.ExecContext(ctx, `CREATE TRIGGER fail_progress BEFORE UPDATE ON sync_progress BEGIN SELECT RAISE(ABORT, 'publication failure'); END`)
	require.NoError(t, err)
	fact, err := events.NewEvent(events.EventBase{EventID: uuid.NewString(), Namespace: "one", SessionName: "demo", SessionID: "original", ItemID: "fact", Time: time.Now().UTC()}, events.AgentMessage{MessageID: "fact", Content: []events.ContentBlock{events.Text("committed before SQL failure")}}, d.source)
	require.NoError(t, err)
	require.NoError(t, d.pipe.Send(ctx, fact))

	frames := make(chan string, 1)
	go func() { frame, _ := readFrame(reader); frames <- frame }()
	require.Eventually(t, func() bool { _, e := os.Stat(filepath.Join(dir, ".events.append.pending.json")); return e == nil }, time.Second, time.Millisecond)
	select {
	case frame := <-frames:
		t.Fatalf("uncommitted publication: %s", frame)
	case <-time.After(100 * time.Millisecond):
	}
	bytes, err := os.ReadFile(filepath.Join(dir, "sessions", "one", "demo", "eventlogs", base64.RawURLEncoding.EncodeToString([]byte("original")), "events.jsonl"))
	require.NoError(t, err)
	require.Contains(t, string(bytes), fact.EventID)
	pending, err := os.ReadFile(filepath.Join(dir, ".events.append.pending.json"))
	require.NoError(t, err)
	var intent struct {
		Data []byte `json:"data"`
	}
	require.NoError(t, json.Unmarshal(pending, &intent))
	require.Contains(t, string(intent.Data), fact.EventID)
	res := get("/apis/v1/sessions/original/events")
	body, err := io.ReadAll(res.Body)
	res.Body.Close()
	require.NoError(t, err)
	require.NotEqual(t, http.StatusOK, res.StatusCode, string(body))
	// Metadata remains readable and writable while event SQL is sealed.
	before, err := meta.GetRuntime("agent")
	require.NoError(t, err)
	before.Spec.Config["updated"] = true
	updated, err := meta.UpdateRuntime(before)
	require.NoError(t, err)
	require.Greater(t, updated.Metadata.Generation, before.Metadata.Generation)
	require.NoError(t, db.Close())
	require.NoError(t, st.Reconcile())
	// The failed lane retains the original UUID. Explicit settlement is allowed
	// only after Store repair and never invokes Driver.OpenSession again.
	require.NoError(t, svc.ReconcileHistory(ctx, resource.SessionRef{SessionID: "original"}))

	select {
	case frame := <-frames:
		require.Contains(t, frame, fact.EventID)
	case <-ctx.Done():
		t.Fatal("settled event was not published")
	}
	page, err := svc.ListHistoryEvents(resource.SessionRef{SessionID: "original"}, store.HistoryQuery{})
	require.NoError(t, err)
	require.Len(t, page.Events, 1)
	require.Equal(t, fact.EventID, page.Events[0].EventID)
	require.Equal(t, fact.EventID, *page.SyncedEventID)
	after, err := os.ReadFile(filepath.Join(dir, "sessions", "one", "demo", "eventlogs", base64.RawURLEncoding.EncodeToString([]byte("original")), "events.jsonl"))
	require.NoError(t, err)
	require.Equal(t, bytes, after)
	require.NoError(t, svc.Shutdown(ctx))
	require.NoError(t, st.Close())
	require.NoError(t, meta.Close())
	reopened, err := eventfile.Open(dir, false)
	require.NoError(t, err)
	defer reopened.Close()
	// An empty registry makes any accidental Runtime activation fail. Startup and
	// HTTP history queries still return the original full fact without replay.
	restoredMeta, err := metafile.Open(dir, false)
	require.NoError(t, err)
	defer restoredMeta.Close()
	queryOnly := service.New(restoredMeta, reopened, runtime.NewRegistry(nil))
	readHandler, err := transport.New(queryOnly, transport.Options{Token: "fixture"})
	require.NoError(t, err)
	defer readHandler.Shutdown()
	readServer := httptest.NewServer(readHandler)
	defer readServer.Close()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, readServer.URL+"/apis/v1/sessions/original/events?types=agent.message&limit=1", http.NoBody)
	require.NoError(t, err)
	req.Header.Set("Authorization", "Bearer fixture")
	response, err := readServer.Client().Do(req)
	require.NoError(t, err)
	defer response.Body.Close()
	require.Equal(t, http.StatusOK, response.StatusCode)
	var restored store.HistoryPage
	require.NoError(t, json.NewDecoder(response.Body).Decode(&restored))
	require.Len(t, restored.Events, 1)
	require.Equal(t, fact.EventID, restored.Events[0].EventID)
	require.NotContains(t, string(after), "agent.message_chunk")
}
