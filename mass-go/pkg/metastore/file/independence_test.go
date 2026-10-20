package file_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/zoumo/mass/mass-go/pkg/events"
	eventfile "github.com/zoumo/mass/mass-go/pkg/eventstore/file"
	"github.com/zoumo/mass/mass-go/pkg/metastore/file"
	"github.com/zoumo/mass/mass-go/pkg/resource"
)

func TestMetadataDoesNotDependOnEvents(t *testing.T) {
	dir := t.TempDir()
	st, err := file.Open(dir, false)
	require.NoError(t, err)
	r, err := st.CreateSession(resource.Session{Metadata: resource.Metadata{Namespace: "default", Name: "demo"}, Status: resource.SessionStatus{SessionID: "native"}})
	require.NoError(t, err)
	require.NoError(t, st.Close())
	// An event replica path cannot prevent metadata recovery or CRUD.
	require.NoError(t, os.Mkdir(filepath.Join(dir, "history.sqlite"), 0o700))
	st, err = file.Open(dir, false)
	require.NoError(t, err)
	defer st.Close()
	got, err := st.GetSession(resource.SessionRef{SessionID: "native"})
	require.NoError(t, err)
	require.Equal(t, r, got)
	got.Status.State = "archived"
	_, err = st.UpdateSession(got)
	require.NoError(t, err)
}

func TestEventFirstLifecycleOnSharedRoot(t *testing.T) {
	dir := t.TempDir()
	history, err := eventfile.Open(dir, false)
	require.NoError(t, err)
	defer history.Close()
	fact, err := events.NewEvent(events.EventBase{EventID: uuid.NewString(), Namespace: "one", SessionName: "demo", SessionID: "native", ItemID: "one", Time: time.Now().UTC()}, events.AgentMessage{MessageID: "one", Content: []events.ContentBlock{events.Text("fact")}}, events.NewSource())
	require.NoError(t, err)
	_, err = history.Append("native", []events.SessionEvent{fact})
	require.NoError(t, err)
	meta, err := file.Open(dir, false)
	require.NoError(t, err)
	defer meta.Close()
	_, err = meta.CreateSession(resource.Session{Metadata: resource.Metadata{Namespace: "one", Name: "demo"}, Status: resource.SessionStatus{SessionID: "native"}})
	require.NoError(t, err)
	require.NoError(t, meta.Close())
	progress, err := history.Append("native", []events.SessionEvent{fact})
	require.NoError(t, err)
	require.Equal(t, &fact.EventID, progress)
	meta, err = file.Open(dir, false)
	require.NoError(t, err)
	defer meta.Close()
	require.NoError(t, history.Close())
	got, err := meta.GetSession(resource.SessionRef{SessionID: "native"})
	require.NoError(t, err)
	got.Status.State = "archived"
	_, err = meta.UpdateSession(got)
	require.NoError(t, err)
}
