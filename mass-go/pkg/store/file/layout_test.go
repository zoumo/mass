package file_test

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/zoumo/mass/mass-go/pkg/events"
	"github.com/zoumo/mass/mass-go/pkg/resource"
	"github.com/zoumo/mass/mass-go/pkg/runtime"
	"github.com/zoumo/mass/mass-go/pkg/store"
	"github.com/zoumo/mass/mass-go/pkg/store/file"
)

func (fileSuite *FileSuite) TestJSONLIndexRebuildAndStatusUpdates() {
	t := fileSuite.T()
	st, dir := open(t)
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

func (fileSuite *FileSuite) TestJSONLCompleteLinesAndLargeEvents() {
	t := fileSuite.T()
	st, dir := open(t)
	_, err := st.CreateSession(session("default", "demo", "native"))
	require.NoError(t, err)
	text := "line one\n" + strings.Repeat("x", 128*1024)
	record := event(t, "native", "one", events.AgentMessage{MessageID: "one", Content: []events.ContentBlock{events.Text(text)}})
	_, err = st.Append("native", []events.SessionEvent{record})
	require.NoError(t, err)
	require.NoError(t, st.Close())
	path := filepath.Join(dir, "sessions", "default", "demo", "events.jsonl")
	b, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, 1, bytes.Count(b, []byte("\n")), "content newlines must be escaped")
	reader, err := file.Open(dir, true)
	require.NoError(t, err, "large events must not hit a Scanner token limit")
	page, err := reader.ListHistory("native", store.HistoryQuery{})
	require.NoError(t, err)
	require.Len(t, page.Events, 1)
	wire, err := json.Marshal(page.Events[0])
	require.NoError(t, err)
	require.JSONEq(t, string(bytes.TrimSpace(b)), string(wire))
	require.NoError(t, reader.Close())
	for _, corrupt := range [][]byte{b[:len(b)-1], append(bytes.Clone(b), '\n'), append(bytes.Clone(b), []byte("{invalid}\n")...)} {
		require.NoError(t, os.WriteFile(path, corrupt, 0o600))
		_, err := file.Open(dir, true)
		require.Error(t, err)
		_, err = file.Open(dir, false)
		require.Error(t, err, "committed JSONL is never silently repaired")
	}
}

func (fileSuite *FileSuite) TestReadableLayoutAndIndependentFiles() {
	t := fileSuite.T()
	st, dir := open(t)
	r, err := st.CreateRuntime(resource.Runtime{Metadata: resource.Metadata{Name: "codex"}, Spec: resource.RuntimeSpec{Type: "codex", Config: runtime.Config{"executable": "/owned/codex"}}})
	require.NoError(t, err)
	first, err := st.CreateSession(session("default", "demo", "native/id"))
	require.NoError(t, err)
	_, err = st.CreateSession(session("other", "demo", "other-id"))
	require.NoError(t, err)
	path := filepath.Join(dir, "sessions", "default", "demo")
	b, err := os.ReadFile(filepath.Join(path, "state.json"))
	require.NoError(t, err)
	var saved resource.Session
	require.NoError(t, json.Unmarshal(b, &saved))
	require.Equal(t, first, saved)
	b, err = os.ReadFile(filepath.Join(dir, "runtime.jsonl"))
	require.NoError(t, err)
	var savedRuntime resource.Runtime
	require.NoError(t, json.Unmarshal(b, &savedRuntime))
	require.Equal(t, r, savedRuntime)
	require.NotContains(t, string(b), "native/id")
	other := filepath.Join(dir, "sessions", "other", "demo", "events.jsonl")
	before, err := os.Stat(other)
	require.NoError(t, err)
	record := event(t, "native/id", "one", events.AgentMessage{MessageID: "one", Content: []events.ContentBlock{events.Text("hello")}})
	_, err = st.Append("native/id", []events.SessionEvent{record})
	require.NoError(t, err)
	after, err := os.Stat(other)
	require.NoError(t, err)
	require.True(t, os.SameFile(before, after), "another Session's file must not be replaced")
	b, err = os.ReadFile(filepath.Join(path, "events.jsonl"))
	require.NoError(t, err)
	var savedEvent events.SessionEvent
	require.NoError(t, json.Unmarshal(b, &savedEvent))
	require.Equal(t, record.EventID, savedEvent.EventID)
	for _, missing := range []string{"state.json", "data", ".pending.json"} {
		_, err := os.Stat(filepath.Join(dir, missing))
		require.True(t, os.IsNotExist(err), missing)
	}
	require.NoError(t, st.DeleteSession(resource.SessionRef{SessionID: "native/id"}, first.Metadata.ResourceVersion))
	_, err = os.Stat(path)
	require.True(t, os.IsNotExist(err))
	require.NoError(t, st.Close())
	reopened, err := file.Open(dir, true)
	require.NoError(t, err)
	defer reopened.Close()
	page, err := reopened.ListSessions(resource.SessionQuery{})
	require.NoError(t, err)
	require.Len(t, page.Items, 1)
	require.Equal(t, "other", page.Items[0].Metadata.Namespace)
}

func (fileSuite *FileSuite) TestInterruptedMultiFileCommitRequiresRecovery() {
	t := fileSuite.T()
	st, dir := open(t)
	path := filepath.Join(dir, "sessions", "default", "demo")
	require.NoError(t, os.MkdirAll(filepath.Join(path, "state.json"), 0o700))
	_, err := st.CreateSession(session("default", "demo", "native"))
	require.ErrorContains(t, err, "outcome_unknown")
	_, err = st.ListSessions(resource.SessionQuery{})
	require.ErrorContains(t, err, "outcome_unknown")
	_, err = file.Open(dir, true)
	require.ErrorContains(t, err, "writable reopen required")
	require.NoError(t, st.Close())
	// The first file was already written when the second file failed.
	_, err = os.Stat(filepath.Join(path, "events.jsonl"))
	require.NoError(t, err)
	require.NoError(t, os.Remove(filepath.Join(path, "state.json")))
	reopened, err := file.Open(dir, false)
	require.NoError(t, err)
	defer reopened.Close()
	r, err := reopened.GetSession(resource.SessionRef{Namespace: "default", Name: "demo"})
	require.NoError(t, err)
	require.Equal(t, "native", r.Status.SessionID)
	page, err := reopened.ListHistory("native", store.HistoryQuery{})
	require.NoError(t, err)
	require.Empty(t, page.Events)
	_, err = os.Stat(filepath.Join(dir, ".pending.json"))
	require.True(t, os.IsNotExist(err))
}

func (fileSuite *FileSuite) TestRejectUnsafePathsAndDirectoryIdentityMismatch() {
	t := fileSuite.T()
	st, dir := open(t)
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

func TestLegacyLayoutIsNotSilentlyReplaced(t *testing.T) {
	for _, legacy := range []string{"state.json", filepath.Join("data", "state.json")} {
		t.Run(legacy, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, legacy)
			require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
			require.NoError(t, os.WriteFile(path, []byte("legacy"), 0o600))
			_, err := file.Open(dir, false)
			require.ErrorContains(t, err, "not migrated")
			b, err := os.ReadFile(path)
			require.NoError(t, err)
			require.Equal(t, "legacy", string(b))
			_, err = os.Stat(filepath.Join(dir, "runtime.jsonl"))
			require.True(t, os.IsNotExist(err))
		})
	}
}
