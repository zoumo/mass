package file_test

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/zoumo/mass/massd/pkg/events"
	"github.com/zoumo/mass/massd/pkg/metastore/file"
	"github.com/zoumo/mass/massd/pkg/resource"
)

func openLayout(t *testing.T) (*file.Store, string) {
	t.Helper()
	dir := t.TempDir()
	st, err := file.Open(dir, false)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, st.Close()) })
	return st, dir
}

func session(namespace, name, id string) resource.Session {
	return resource.Session{
		Metadata: resource.Metadata{Namespace: namespace, Name: name},
		Spec:     resource.SessionSpec{RuntimeRef: "codex"},
		Status:   resource.SessionStatus{SessionID: id, State: events.Unknown},
	}
}

func TestJSONLIndexRebuildAndStatusUpdates(t *testing.T) {
	st, dir := openLayout(t)
	first, err := st.CreateSession(session("z", "demo", "native-z"))
	require.NoError(t, err)
	_, err = st.CreateSession(session("a", "demo", "native-a"))
	require.NoError(t, err)
	index := filepath.Join(dir, "session_index.jsonl")
	before, err := os.Stat(index)
	require.NoError(t, err)
	original, err := os.ReadFile(index)
	require.NoError(t, err)
	first.Status.State = events.SessionState("offline")
	first, err = st.UpdateSession(first)
	require.NoError(t, err)
	after, err := os.Stat(index)
	require.NoError(t, err)
	require.True(t, os.SameFile(before, after), "status updates must not replace the index")
	require.Equal(t, before.ModTime(), after.ModTime(), "status updates must not rewrite the index")
	b, err := os.ReadFile(index)
	require.NoError(t, err)
	require.Equal(t, original, b)
	lines := bytes.Split(bytes.TrimSuffix(b, []byte("\n")), []byte("\n"))
	require.Len(t, lines, 2)
	var row map[string]any
	require.NoError(t, json.Unmarshal(lines[1], &row))
	require.Equal(t, map[string]any{"namespace": "z", "name": "demo", "sessionId": "native-z", "runtimeRef": "codex"}, row)
	require.NoError(t, st.DeleteSession(resource.SessionRef{SessionID: "native-z"}, first.Metadata.ResourceVersion))
	b, err = os.ReadFile(index)
	require.NoError(t, err)
	require.Equal(t, 1, bytes.Count(b, []byte("\n")))
	require.NotContains(t, string(b), "native-z")
	require.NoError(t, st.Close())
	for _, missing := range []bool{true, false} {
		if missing {
			require.NoError(t, os.Remove(index))
		} else {
			require.NoError(t, os.WriteFile(index, []byte("corrupt index"), 0o600))
		}
		reader, err := file.Open(dir, true)
		require.NoError(t, err, "derived index is never authoritative")
		require.NoError(t, reader.Close())
		if missing {
			_, err := os.Stat(index)
			require.True(t, os.IsNotExist(err), "read-only Open must not rebuild")
		} else {
			b, err = os.ReadFile(index)
			require.NoError(t, err)
			require.Equal(t, "corrupt index", string(b))
		}
		writer, err := file.Open(dir, false)
		require.NoError(t, err)
		require.NoError(t, writer.Close())
		b, err = os.ReadFile(index)
		require.NoError(t, err)
		require.Equal(t, 1, bytes.Count(b, []byte("\n")))
		require.NoError(t, json.Unmarshal(bytes.TrimSpace(b), &row))
		require.Equal(t, "native-a", row["sessionId"])
	}
}

func TestInterruptedMultiFileCommitRequiresRecovery(t *testing.T) {
	st, dir := openLayout(t)
	path := filepath.Join(dir, "sessions", "default", "demo")
	require.NoError(t, os.MkdirAll(filepath.Join(path, "state.json"), 0o700))
	_, err := st.CreateSession(session("default", "demo", "native"))
	require.ErrorContains(t, err, "outcome_unknown")
	_, err = st.ListSessions(resource.SessionQuery{})
	require.ErrorContains(t, err, "outcome_unknown")
	_, err = file.Open(dir, true)
	require.ErrorContains(t, err, "writable reopen required")
	require.NoError(t, st.Close())
	require.FileExists(t, filepath.Join(dir, ".pending.json"))
	require.NoError(t, os.Remove(filepath.Join(path, "state.json")))
	reopened, err := file.Open(dir, false)
	require.NoError(t, err)
	defer reopened.Close()
	r, err := reopened.GetSession(resource.SessionRef{Namespace: "default", Name: "demo"})
	require.NoError(t, err)
	require.Equal(t, "native", r.Status.SessionID)
	_, err = os.Stat(filepath.Join(dir, ".pending.json"))
	require.True(t, os.IsNotExist(err))
}

func TestRejectUnsafePathsAndDirectoryIdentityMismatch(t *testing.T) {
	st, dir := openLayout(t)
	for _, bad := range []string{"..", ".", "a/b", "a\\b", "\x00", ""} {
		_, err := st.CreateSession(session(bad, "demo", "native"))
		require.Error(t, err)
		_, err = st.CreateSession(session("default", bad, "native"))
		require.Error(t, err)
	}
	_, err := st.CreateSession(session("default", "demo", "native"))
	require.NoError(t, err)
	require.NoError(t, st.Close())
	ns := filepath.Join(dir, "sessions", "default")
	require.NoError(t, os.Rename(filepath.Join(ns, "demo"), filepath.Join(ns, "renamed")))
	_, err = file.Open(dir, true)
	require.ErrorContains(t, err, "directory identity mismatch")
	require.NoError(t, os.Rename(filepath.Join(ns, "renamed"), filepath.Join(ns, "demo")))
	outside := t.TempDir()
	require.NoError(t, os.Symlink(outside, filepath.Join(dir, "sessions", "linked")))
	_, err = file.Open(dir, false)
	require.ErrorContains(t, err, "unexpected storage entry")
	entries, err := os.ReadDir(outside)
	require.NoError(t, err)
	require.Empty(t, entries)
}
