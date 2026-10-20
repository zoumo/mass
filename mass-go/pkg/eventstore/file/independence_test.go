package file_test

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/zoumo/mass/mass-go/pkg/events"
	"github.com/zoumo/mass/mass-go/pkg/eventstore/file"
	"github.com/zoumo/mass/mass-go/pkg/metastore"
	"github.com/zoumo/mass/mass-go/pkg/metastore/backend"
	"github.com/zoumo/mass/mass-go/pkg/resource"
	"github.com/zoumo/mass/mass-go/pkg/store"
)

func TestIndependentHistoryRebuildAndIdentity(t *testing.T) {
	dir := t.TempDir()
	st, err := file.Open(dir, false)
	require.NoError(t, err)
	defer st.Close()
	empty, err := st.ListHistory("empty", store.HistoryQuery{})
	require.NoError(t, err)
	require.Empty(t, empty.Events)
	require.Nil(t, empty.SyncedEventID)
	fact := func(id, text string) events.SessionEvent {
		e, err := events.NewEvent(events.EventBase{EventID: uuid.NewString(), Namespace: "default", SessionName: "demo", SessionID: id, ItemID: uuid.NewString(), Time: time.Now().UTC()}, events.AgentMessage{MessageID: uuid.NewString(), Content: []events.ContentBlock{events.Text(text)}}, events.NewSource())
		require.NoError(t, err)
		return e
	}
	one, two := fact("old", "one"), fact("old", "two")
	_, err = st.Append("old", []events.SessionEvent{one, two})
	require.NoError(t, err)
	first, err := st.ListHistory("old", store.HistoryQuery{Limit: 1})
	require.NoError(t, err)
	require.NotEmpty(t, first.Next)
	require.NoError(t, st.Close())
	require.NoError(t, os.Remove(filepath.Join(dir, "history.sqlite")))
	// Metadata paths are unavailable: reconstruction must use event materials only.
	require.NoError(t, os.Mkdir(filepath.Join(dir, "metadata-authority.sqlite"), 0o700))
	st, err = file.Open(dir, false)
	require.NoError(t, err)
	defer st.Close()
	next, err := st.ListHistory("old", store.HistoryQuery{After: first.Next, Limit: 1})
	require.NoError(t, err)
	want, err := json.Marshal([]events.SessionEvent{two})
	require.NoError(t, err)
	got, err := json.Marshal(next.Events)
	require.NoError(t, err)
	require.JSONEq(t, string(want), string(got))
	require.Equal(t, two.EventID, *next.SyncedEventID)
	_, err = st.Append("new", []events.SessionEvent{fact("new", "new identity")})
	require.NoError(t, err)
	old, err := st.ListHistory("old", store.HistoryQuery{})
	require.NoError(t, err)
	want, err = json.Marshal([]events.SessionEvent{one, two})
	require.NoError(t, err)
	got, err = json.Marshal(old.Events)
	require.NoError(t, err)
	require.JSONEq(t, string(want), string(got))
	_, err = st.ListHistory("new", store.HistoryQuery{After: first.Next})
	require.Error(t, err)
}

func TestInterruptedEmptyLogCreationCanBeRetried(t *testing.T) {
	dir := t.TempDir()
	st, err := file.Open(dir, false)
	require.NoError(t, err)
	require.NoError(t, st.Close())
	// Simulate interruption between durable directory creation and log creation.
	path := filepath.Join(dir, "sessions", "default", "demo", "eventlogs", base64.RawURLEncoding.EncodeToString([]byte("native")))
	require.NoError(t, os.MkdirAll(path, 0o700))
	st, err = file.Open(dir, false)
	require.NoError(t, err)
	defer st.Close()
	fact, err := events.NewEvent(events.EventBase{EventID: uuid.NewString(), Namespace: "default", SessionName: "demo", SessionID: "native", ItemID: "one", Time: time.Now().UTC()}, events.AgentMessage{MessageID: "one", Content: []events.ContentBlock{events.Text("retry creation")}}, events.NewSource())
	require.NoError(t, err)
	_, err = st.Append("native", []events.SessionEvent{fact})
	require.NoError(t, err)
	page, err := st.ListHistory("native", store.HistoryQuery{})
	require.NoError(t, err)
	require.Len(t, page.Events, 1)
	require.Equal(t, fact.EventID, page.Events[0].EventID)
}

func TestEventRebuildCannotResetMetadataAuthority(t *testing.T) {
	dir := t.TempDir()
	meta, err := backend.Open(dir, metastore.Config{}, false)
	require.NoError(t, err)
	defer meta.Close()
	rt, err := meta.CreateRuntime(resource.Runtime{Metadata: resource.Metadata{Name: "runtime"}, Spec: resource.RuntimeSpec{Type: "fixture"}})
	require.NoError(t, err)
	st, err := file.Open(dir, false)
	require.NoError(t, err)
	defer st.Close()
	fact := event(t, "native", "one", events.AgentMessage{MessageID: "one", Content: []events.ContentBlock{events.Text("independent")}})
	_, err = st.Append("native", []events.SessionEvent{fact})
	require.NoError(t, err)
	require.NoError(t, st.Close())
	require.NoError(t, os.WriteFile(filepath.Join(dir, "history.sqlite"), []byte("corrupt projection"), 0o600))
	rt.Spec.Config = map[string]any{"marker": "authority"}
	rt, err = meta.UpdateRuntime(rt)
	require.NoError(t, err)
	before := readBytes(t, filepath.Join(dir, "metadata-authority.sqlite"))
	restored, err := file.Open(dir, false)
	require.NoError(t, err)
	defer restored.Close()
	require.Equal(t, before, readBytes(t, filepath.Join(dir, "metadata-authority.sqlite")))
	got, err := meta.GetRuntime("runtime")
	require.NoError(t, err)
	require.Equal(t, rt, got)
	require.NoError(t, meta.Close())
	_, err = restored.Append("native", []events.SessionEvent{fact})
	require.NoError(t, err)
	progress, err := restored.GetSyncProgress("native")
	require.NoError(t, err)
	require.Equal(t, fact.EventID, *progress)
	require.NoError(t, restored.Close())
	reopened, err := backend.Open(dir, metastore.Config{}, false)
	require.NoError(t, err)
	defer reopened.Close()
	got, err = reopened.GetRuntime("runtime")
	require.NoError(t, err)
	require.Equal(t, rt, got)
}
