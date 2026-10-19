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
	"github.com/zoumo/mass/mass-go/pkg/resource"
	"github.com/zoumo/mass/mass-go/pkg/runtime"
	"github.com/zoumo/mass/mass-go/pkg/store"
	"github.com/zoumo/mass/mass-go/pkg/store/file"
)

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
func TestAtomicIdentityAndWriterOwnership(t *testing.T) {
	s, dir := open(t)
	_, err := file.Open(dir, false)
	require.Error(t, err, "a second file Store cannot create an independent writer cache")
	for _, mode := range []string{"name", "native-id"} {
		t.Run(mode, func(t *testing.T) {
			var winners atomic.Int32
			var wg sync.WaitGroup
			for i := 0; i < 24; i++ {
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
						t.Errorf("unexpected create error: %v", err)
					}
				})
			}
			wg.Wait()
			require.EqualValues(t, 1, winners.Load())
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
func TestResourceVersionsSnapshotsAndEncoding(t *testing.T) {
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
func TestHistoryIdentityPaginationAndRestart(t *testing.T) {
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
func TestCorruptFileFailsClosed(t *testing.T) {
	s, dir := open(t)
	require.NoError(t, s.Close())
	path := filepath.Join(dir, "state.json")
	b, err := os.ReadFile(path)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(path, append(b, []byte(" trailing garbage")...), 0600))
	_, err = file.Open(dir, true)
	require.Error(t, err)
	_, err = file.Open(dir, false)
	require.Error(t, err, "writer must not discard or repair unreadable authority silently")
}
