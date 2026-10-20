package file_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/stretchr/testify/suite"

	"github.com/zoumo/mass/mass-go/pkg/resource"
	"github.com/zoumo/mass/mass-go/pkg/runtime"
	"github.com/zoumo/mass/mass-go/pkg/store"
	"github.com/zoumo/mass/mass-go/pkg/store/file"
)

type FileSuite struct{ suite.Suite }

func open(t *testing.T) (*file.Store, string) {
	t.Helper()
	dir := t.TempDir()
	s, err := file.Open(dir, false)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, s.Close()) })
	return s, dir
}

func session(ns, name, id string) resource.Session {
	return resource.Session{Metadata: resource.Metadata{Namespace: ns, Name: name}, Spec: resource.SessionSpec{RuntimeRef: "codex", SessionSpec: runtime.SessionSpec{Cwd: "/synthetic"}}, Status: resource.SessionStatus{SessionID: id, State: runtime.Unknown}}
}

func event(t *testing.T, id, item string, p runtime.Payload) runtime.SessionEvent {
	t.Helper()
	e, err := runtime.NewEvent(runtime.EventBase{EventID: runtime.UUID(), Namespace: "default", SessionName: "demo", SessionID: id, Time: time.Date(2026, 10, 20, 1, 2, 3, 0, time.UTC), ItemID: item, Meta: map[string]any{"nested": map[string]any{"number": json.Number("9007199254740993"), "null": nil, "list": []any{false, "x"}}}}, p, runtime.NewSource().WithWork("private-work"))
	require.NoError(t, err)
	return e
}

func (fileSuite *FileSuite) TestAtomicIdentityAndWriterOwnership() {
	t := fileSuite.T()
	s, dir := open(t)
	_, err := file.Open(dir, false)
	require.Error(t, err, "a second file Store cannot create an independent writer cache")
	for _, mode := range []string{"name", "native-id"} {
		fileSuite.Run(mode, func() {
			subT := fileSuite.T()
			var winners atomic.Int32
			var wg sync.WaitGroup
			for i := range 24 {
				wg.Go(func() {
					ns, name, id := "default", fmt.Sprintf("demo-%s-%d", mode, i), fmt.Sprintf("native-%s-%d", mode, i)
					if mode == "name" {
						name = "same-name"
					} else {
						id = "same-native-id"
					}
					_, err := s.CreateSession(session(ns, name, id))
					if err == nil {
						winners.Add(1)
					} else if !errors.Is(err, store.ErrConflict) {
						subT.Errorf("unexpected create error: %v", err)
					}
				})
			}
			wg.Wait()
			require.EqualValues(subT, 1, winners.Load())
		})
	}
	_, err = s.CreateSession(session("other", "other", "same-native-id"))
	require.ErrorIs(t, err, store.ErrConflict, "native identity is global across namespace/name")
	readonly, err := file.Open(dir, true)
	require.NoError(t, err)
	defer readonly.Close()
	_, err = readonly.CreateSession(session("other", "new", "fresh"))
	require.ErrorIs(t, err, store.ErrReadOnly)
}

func (fileSuite *FileSuite) TestResourceVersionsSnapshotsAndEncoding() {
	t := fileSuite.T()
	s, dir := open(t)
	r, err := s.CreateRuntime(resource.Runtime{Metadata: resource.Metadata{Name: "codex"}, Spec: resource.RuntimeSpec{Type: "codex", Config: runtime.Config{"nested": map[string]any{"value": "one"}}}, Status: resource.RuntimeStatus{State: "stopped"}})
	require.NoError(t, err)
	initial := r
	r.Spec.Config["nested"].(map[string]any)["value"] = "local mutation"
	stored, err := s.GetRuntime("codex")
	require.NoError(t, err)
	require.Equal(t, "one", stored.Spec.Config["nested"].(map[string]any)["value"])
	stored.Status.State = "running"
	status, err := s.UpdateRuntime(stored)
	require.NoError(t, err)
	require.EqualValues(t, 1, status.Metadata.Generation)
	require.NotEqual(t, initial.Metadata.ResourceVersion, status.Metadata.ResourceVersion)
	stale := initial
	stale.Status.State = "unknown"
	_, err = s.UpdateRuntime(stale)
	require.ErrorIs(t, err, store.ErrConflict)
	status.Spec.Config["nested"].(map[string]any)["value"] = "two"
	next, err := s.UpdateRuntime(status)
	require.NoError(t, err)
	require.EqualValues(t, 2, next.Metadata.Generation)
	same, err := s.UpdateRuntime(next)
	require.NoError(t, err)
	require.Equal(t, next.Metadata, same.Metadata)
	invalid := same
	invalid.Metadata.Generation++
	_, err = s.UpdateRuntime(invalid)
	require.Error(t, err)
	for _, bad := range []any{make(chan int), func() {}, struct{ Hidden string }{Hidden: "cannot silently omit or coerce"}, []byte("binary")} {
		_, err = s.CreateRuntime(resource.Runtime{Metadata: resource.Metadata{Name: "bad"}, Spec: resource.RuntimeSpec{Type: "codex", Config: runtime.Config{"value": bad}}})
		require.Error(t, err)
		_, err = s.GetRuntime("bad")
		require.ErrorIs(t, err, store.ErrNotFound)
	}
	require.NoError(t, s.Close())
	reopened, err := file.Open(dir, true)
	require.NoError(t, err)
	defer reopened.Close()
	saved, err := reopened.GetRuntime("codex")
	require.NoError(t, err)
	require.Equal(t, next, saved)
}

func (fileSuite *FileSuite) TestHistoryIdentityPaginationAndRestart() {
	t := fileSuite.T()
	s, dir := open(t)
	identity, err := s.CreateSession(session("default", "demo", "native"))
	require.NoError(t, err)
	input := event(t, "native", "user", runtime.UserMessage{MessageID: "user", Content: []runtime.ContentBlock{runtime.Text("hello")}})
	call := event(t, "native", "tool", runtime.ToolCall{ID: "tool", Kind: "execute", RawInput: map[string]any{"n": json.Number("9007199254740993"), "nil": nil}})
	null := any(nil)
	result := event(t, "native", "tool", runtime.ToolResult{ID: "tool", Kind: "execute", Status: "completed", RawOutput: &null})
	answer := event(t, "native", "message", runtime.AgentMessage{MessageID: "message", Content: []runtime.ContentBlock{runtime.Text("full")}})
	events := []runtime.SessionEvent{input, call, result, answer}
	p, err := s.Append("native", events)
	require.NoError(t, err)
	require.Equal(t, &answer.EventID, p)
	// Consumer mutations cannot modify file authority or a later read.
	input.Meta["nested"].(map[string]any)["number"] = "mutation"
	p, err = s.Append("native", events[1:3])
	require.NoError(t, err)
	require.Equal(t, &answer.EventID, p)
	first, err := s.ListHistory("native", store.HistoryQuery{Types: []string{"agent.tool_call", "agent.message"}, Limit: 1})
	require.NoError(t, err)
	require.Len(t, first.Events, 1)
	require.Equal(t, call.EventID, first.Events[0].EventID)
	require.NotEmpty(t, first.Next)
	require.Equal(t, &answer.EventID, first.SyncedEventID)
	_, err = s.ListSessions(resource.SessionQuery{After: first.Next})
	require.Error(t, err, "history cursor cannot be a resource cursor")
	require.NoError(t, s.Close())
	s, err = file.Open(dir, false)
	require.NoError(t, err)
	defer s.Close()
	same, err := s.GetSession(resource.SessionRef{SessionID: "native"})
	require.NoError(t, err)
	require.Equal(t, identity, same)
	last, err := s.ListHistory("native", store.HistoryQuery{Types: []string{"agent.message", "agent.tool_call"}, After: first.Next, Limit: 8})
	require.NoError(t, err)
	require.Len(t, last.Events, 1)
	require.Equal(t, answer.EventID, last.Events[0].EventID)
	require.Empty(t, last.Next)
	_, err = s.ListHistory("native", store.HistoryQuery{Types: []string{"agent.tool_call"}, After: first.Next})
	require.Error(t, err)
	_, err = s.ListHistory("native", store.HistoryQuery{After: "made-up"})
	require.Error(t, err)
	_, err = s.ListHistory("native", store.HistoryQuery{Types: []string{}})
	require.Error(t, err)
	all, err := s.ListHistory("native", store.HistoryQuery{})
	require.NoError(t, err)
	require.Len(t, all.Events, 4)
	require.Equal(t, json.Number("9007199254740993"), all.Events[0].Meta["nested"].(map[string]any)["number"])
	require.False(t, all.Events[0].Source().SameRun(call.Source()))
	wire, err := json.Marshal(all.Events[2])
	require.NoError(t, err)
	require.Contains(t, string(wire), `"rawOutput":null`)
	conflict := answer
	conflict.Time = answer.Time.Add(time.Second)
	_, err = s.Append("native", []runtime.SessionEvent{conflict})
	require.Error(t, err)
	wrong := answer
	wrong.Namespace = "other"
	wrong.EventID = runtime.UUID()
	_, err = s.Append("native", []runtime.SessionEvent{wrong})
	require.Error(t, err)
	chunk := event(t, "native", "message", runtime.MessageChunk{MessageID: "message", Content: []runtime.ContentBlock{runtime.Text("delta")}})
	_, err = s.Append("native", []runtime.SessionEvent{chunk})
	require.Error(t, err)
	invalid := answer
	invalid.EventID = runtime.UUID()
	invalid.Meta = map[string]any{"unsupported": make(chan int)}
	_, err = s.Append("native", []runtime.SessionEvent{invalid})
	require.Error(t, err)
	end, err := s.ListHistory("native", store.HistoryQuery{})
	require.NoError(t, err)
	require.Equal(t, all, end)
	progress, err := s.GetSyncProgress("native")
	require.NoError(t, err)
	require.Equal(t, end.SyncedEventID, progress)
}

func (fileSuite *FileSuite) TestCorruptFileFailsClosed() {
	t := fileSuite.T()
	s, dir := open(t)
	require.NoError(t, s.Close())
	path := filepath.Join(dir, "state.json")
	b, err := os.ReadFile(path)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(path, append(b, []byte(" trailing garbage")...), 0o600))
	_, err = file.Open(dir, true)
	require.Error(t, err)
	_, err = file.Open(dir, false)
	require.Error(t, err, "writer must not discard or repair unreadable authority silently")
}

func (fileSuite *FileSuite) TestFullDirectoryCursorsAndConditionalDelete() {
	t := fileSuite.T()
	s, dir := open(t)
	for _, v := range []struct{ ns, name, id, state string }{{"z", "a", "native-z", "archived"}, {"a", "z", "native-a-z", "unknown"}, {"a", "a", "native-a-a", "idle"}} {
		r := session(v.ns, v.name, v.id)
		r.Status.State = runtime.SessionState(v.state)
		_, err := s.CreateSession(r)
		require.NoError(t, err)
	}
	all, err := s.ListSessions(resource.SessionQuery{})
	require.NoError(t, err)
	require.Len(t, all.Items, 3)
	require.Equal(t, []string{"native-a-a", "native-a-z", "native-z"}, []string{all.Items[0].Status.SessionID, all.Items[1].Status.SessionID, all.Items[2].Status.SessionID})
	first, err := s.ListSessions(resource.SessionQuery{Namespace: "a", Limit: 1})
	require.NoError(t, err)
	require.NotEmpty(t, first.Next)
	_, err = s.ListSessions(resource.SessionQuery{After: first.Next})
	require.Error(t, err, "cursor binds namespace")
	_, err = s.ListHistory("native-a-a", store.HistoryQuery{After: first.Next})
	require.Error(t, err, "resource cursor cannot be history cursor")
	_, err = s.ListSessions(resource.SessionQuery{Limit: -1})
	require.Error(t, err)
	archived, err := s.ListSessions(resource.SessionQuery{State: "archived"})
	require.NoError(t, err)
	require.Len(t, archived.Items, 1)
	r := all.Items[0]
	// Re-read confirms failed conditional deletion had no effect.
	err = s.DeleteSession(resource.SessionRef{SessionID: r.Status.SessionID}, "wrong")
	require.ErrorIs(t, err, store.ErrConflict)
	_, err = s.GetSession(resource.SessionRef{SessionID: r.Status.SessionID})
	require.NoError(t, err)
	require.NoError(t, s.Close())
	reopened, err := file.Open(dir, false)
	require.NoError(t, err)
	defer reopened.Close()
	last, err := reopened.ListSessions(resource.SessionQuery{Namespace: "a", After: first.Next, Limit: 20})
	require.NoError(t, err)
	require.Len(t, last.Items, 1)
	require.Equal(t, "native-a-z", last.Items[0].Status.SessionID)
	require.Empty(t, last.Next)
	// No index exists or is needed. Full records and cursors survive reopen.
	_, err = os.Stat(filepath.Join(dir, "index.sqlite"))
	require.True(t, os.IsNotExist(err))
	require.NoError(t, reopened.DeleteSession(resource.SessionRef{SessionID: r.Status.SessionID}, r.Metadata.ResourceVersion))
	_, err = reopened.GetSession(resource.SessionRef{SessionID: r.Status.SessionID})
	require.ErrorIs(t, err, store.ErrNotFound)
	runtimeRecord, err := reopened.CreateRuntime(resource.Runtime{Metadata: resource.Metadata{Name: "offline"}, Spec: resource.RuntimeSpec{Type: "codex"}, Status: resource.RuntimeStatus{State: "stopped"}})
	require.NoError(t, err)
	runtimes, err := reopened.ListRuntimes(resource.RuntimeQuery{Type: "codex", State: "stopped"})
	require.NoError(t, err)
	require.Len(t, runtimes, 1)
	require.ErrorIs(t, reopened.DeleteRuntime("offline", "stale"), store.ErrConflict)
	require.NoError(t, reopened.DeleteRuntime("offline", runtimeRecord.Metadata.ResourceVersion))
}

func (fileSuite *FileSuite) TestCommittedHistoryTailIsValidatedBeforeFirstPage() {
	t := fileSuite.T()
	s, dir := open(t)
	_, err := s.CreateSession(session("default", "demo", "native"))
	require.NoError(t, err)
	events := make([]runtime.SessionEvent, 105)
	for i := range events {
		events[i] = event(t, "native", fmt.Sprintf("message-%d", i), runtime.AgentMessage{MessageID: fmt.Sprintf("message-%d", i), Content: []runtime.ContentBlock{runtime.Text("synthetic")}})
	}
	_, err = s.Append("native", events)
	require.NoError(t, err)
	require.NoError(t, s.Close())
	p := filepath.Join(dir, "state.json")
	b, err := os.ReadFile(p)
	require.NoError(t, err)
	var snapshot map[string]any
	require.NoError(t, json.Unmarshal(b, &snapshot))
	tail := snapshot["history"].(map[string]any)["native"].([]any)[104].(map[string]any)
	tail["namespace"] = "foreign"
	b, err = json.Marshal(snapshot)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(p, b, 0o600))
	_, err = file.Open(dir, true)
	require.ErrorContains(t, err, "identity mismatch", "even a first-page reader must reject corrupt committed tail")
	_, err = file.Open(dir, false)
	require.ErrorContains(t, err, "identity mismatch", "recovery cannot silently repair committed data")
}

func TestFileSuite(t *testing.T) { suite.Run(t, new(FileSuite)) }
