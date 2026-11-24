package file_test

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"github.com/stretchr/testify/suite"

	"github.com/zoumo/mass/massd/pkg/events"
	"github.com/zoumo/mass/massd/pkg/eventstore/file"
	"github.com/zoumo/mass/massd/pkg/store"
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

func event(t *testing.T, id, item string, p events.Payload) events.SessionEvent {
	t.Helper()
	e, err := events.NewEvent(events.EventBase{EventID: uuid.NewString(), Namespace: "default", SessionName: "demo", SessionID: id, Time: time.Date(2026, 10, 20, 1, 2, 3, 0, time.UTC), ItemID: item, Meta: map[string]any{"nested": map[string]any{"number": json.Number("9007199254740993"), "null": nil, "list": []any{false, "x"}}}}, p, events.NewSource().WithWork("private-work"))
	require.NoError(t, err)
	return e
}

func TestFileSuite(t *testing.T) { suite.Run(t, new(FileSuite)) }

func (fileSuite *FileSuite) TestHistoryIdentityPaginationAndRestart() {
	t := fileSuite.T()
	s, dir := open(t)
	input := event(t, "native", "user", events.UserMessage{MessageID: "user", Content: []events.ContentBlock{events.Text("hello")}})
	call := event(t, "native", "tool", events.ToolCall{ID: "tool", Kind: events.ToolKindExecute, ToolName: "shell", Provider: events.ToolProviderAgent, RawInput: map[string]any{"n": json.Number("9007199254740993"), "nil": nil}})
	null := any(nil)
	result := event(t, "native", "tool", events.ToolResult{ID: "tool", Kind: events.ToolKindExecute, ToolName: "shell", Provider: events.ToolProviderAgent, Status: "completed", RawOutput: &null})
	answer := event(t, "native", "message", events.AgentMessage{MessageID: "message", Content: []events.ContentBlock{events.Text("full")}})
	records := []events.SessionEvent{input, call, result, answer}
	p, err := s.Append("native", records)
	require.NoError(t, err)
	require.Equal(t, &answer.EventID, p)
	// Consumer mutations cannot modify file authority or a later read.
	input.Meta["nested"].(map[string]any)["number"] = "mutation"
	p, err = s.Append("native", records[1:3])
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
	_, err = s.Append("native", []events.SessionEvent{conflict})
	require.Error(t, err)
	wrong := answer
	wrong.Namespace = "other"
	wrong.EventID = uuid.NewString()
	_, err = s.Append("native", []events.SessionEvent{wrong})
	require.Error(t, err)
	chunk := event(t, "native", "message", events.MessageChunk{MessageID: "message", Content: []events.ContentBlock{events.Text("delta")}})
	_, err = s.Append("native", []events.SessionEvent{chunk})
	require.Error(t, err)
	invalid := answer
	invalid.EventID = uuid.NewString()
	invalid.Meta = map[string]any{"unsupported": make(chan int)}
	_, err = s.Append("native", []events.SessionEvent{invalid})
	require.Error(t, err)
	end, err := s.ListHistory("native", store.HistoryQuery{})
	require.NoError(t, err)
	require.Equal(t, all, end)
	progress, err := s.GetSyncProgress("native")
	require.NoError(t, err)
	require.Equal(t, end.SyncedEventID, progress)
}

func (fileSuite *FileSuite) TestCommittedHistoryTailIsValidatedBeforeFirstPage() {
	t := fileSuite.T()
	s, dir := open(t)
	var err error
	records := make([]events.SessionEvent, 105)
	for i := range records {
		records[i] = event(t, "native", fmt.Sprintf("message-%d", i), events.AgentMessage{MessageID: fmt.Sprintf("message-%d", i), Content: []events.ContentBlock{events.Text("synthetic")}})
	}
	_, err = s.Append("native", records)
	require.NoError(t, err)
	require.NoError(t, s.Close())
	p := filepath.Join(dir, "sessions", "default", "demo", "eventlogs", base64.RawURLEncoding.EncodeToString([]byte("native")), "events.jsonl")
	b, err := os.ReadFile(p)
	require.NoError(t, err)
	lines := bytes.Split(bytes.TrimSuffix(b, []byte("\n")), []byte("\n"))
	var tail map[string]any
	require.NoError(t, json.Unmarshal(lines[104], &tail))
	tail["namespace"] = "foreign"
	lines[104], err = json.Marshal(tail)
	b = append(bytes.Join(lines, []byte("\n")), '\n')
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(p, b, 0o600))
	_, err = file.Open(dir, true)
	require.ErrorContains(t, err, "identity mismatch", "even a first-page reader must reject corrupt committed tail")
	_, err = file.Open(dir, false)
	require.ErrorContains(t, err, "identity mismatch", "recovery cannot silently repair committed data")
}

func (fileSuite *FileSuite) TestJSONLCompleteLinesAndLargeEvents() {
	t := fileSuite.T()
	st, dir := open(t)
	text := "line one\n" + strings.Repeat("x", 128*1024)
	record := event(t, "native", "one", events.AgentMessage{MessageID: "one", Content: []events.ContentBlock{events.Text(text)}})
	_, err := st.Append("native", []events.SessionEvent{record})
	require.NoError(t, err)
	require.NoError(t, st.Close())
	path := logPath(dir)
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

func (fileSuite *FileSuite) TestQueryReplicaServesSavedHistoryWithoutSourceReads() {
	t := fileSuite.T()
	st, dir := open(t)
	fact := event(t, "native", "one", events.AgentMessage{MessageID: "one", Content: []events.ContentBlock{events.Text("SQL complete body")}})
	_, err := st.Append("native", []events.SessionEvent{fact})
	require.NoError(t, err)
	path := logPath(dir)
	require.NoError(t, os.Rename(path, path+".held"))
	t.Cleanup(func() { require.NoError(t, os.Rename(path+".held", path)) })
	page, err := st.ListHistory("native", store.HistoryQuery{})
	require.NoError(t, err)
	require.Len(t, page.Events, 1)
	require.Equal(t, fact.EventID, page.Events[0].EventID)
	wire, err := json.Marshal(page.Events[0])
	require.NoError(t, err)
	require.Contains(t, string(wire), "SQL complete body")
	require.Contains(t, string(wire), "9007199254740993")
	progress, err := st.GetSyncProgress("native")
	require.NoError(t, err)
	require.Equal(t, &fact.EventID, progress)
}

func (fileSuite *FileSuite) TestRestartRestoresEverySessionAcrossHistoryBatches() {
	t := fileSuite.T()
	st, dir := open(t)
	records := make([]events.SessionEvent, 260)
	for i := range records {
		records[i] = event(t, "native", fmt.Sprintf("item-%d", i), events.AgentMessage{MessageID: fmt.Sprintf("message-%d", i), Content: []events.ContentBlock{events.Text("complete payload")}})
	}
	_, err := st.Append("native", records)
	require.NoError(t, err)
	second := records[0]
	second.EventID = uuid.NewString()
	second.Namespace, second.SessionName, second.SessionID = "other", "second", "second-native"
	_, err = st.Append("second-native", []events.SessionEvent{second})
	require.NoError(t, err)
	first, err := st.ListHistory("native", store.HistoryQuery{Limit: 129})
	require.NoError(t, err)
	require.Len(t, first.Events, 129)
	require.NotEmpty(t, first.Next)
	require.NoError(t, st.Close())
	st, err = file.Open(dir, false)
	require.NoError(t, err)
	defer st.Close()
	tail, err := st.ListHistory("native", store.HistoryQuery{After: first.Next, Limit: 200})
	require.NoError(t, err)
	require.Len(t, tail.Events, 131)
	require.Empty(t, tail.Next)
	for i, fact := range tail.Events {
		require.Equal(t, records[i+129].EventID, fact.EventID)
	}
	require.Equal(t, &records[259].EventID, tail.SyncedEventID)
	page, err := st.ListHistory("second-native", store.HistoryQuery{})
	require.NoError(t, err)
	require.Len(t, page.Events, 1)
	require.Equal(t, second.EventID, page.Events[0].EventID)
	progress, err := st.GetSyncProgress("empty-native")
	require.NoError(t, err)
	require.Nil(t, progress)
	progress, err = st.Append("native", records[:1])
	require.NoError(t, err)
	require.Equal(t, &records[259].EventID, progress)
}
