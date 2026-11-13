package integration_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zoumo/mass/mass-go/pkg/events"
	"github.com/zoumo/mass/mass-go/pkg/eventstore"
	eventfile "github.com/zoumo/mass/mass-go/pkg/eventstore/file"
	"github.com/zoumo/mass/mass-go/pkg/metastore"
	metafile "github.com/zoumo/mass/mass-go/pkg/metastore/file"
	"github.com/zoumo/mass/mass-go/pkg/prototype"
	"github.com/zoumo/mass/mass-go/pkg/resource"
	"github.com/zoumo/mass/mass-go/pkg/runtime"
	"github.com/zoumo/mass/mass-go/pkg/runtime/claude"
	"github.com/zoumo/mass/mass-go/pkg/service"
	"github.com/zoumo/mass/mass-go/pkg/store"
	"github.com/zoumo/mass/mass-go/pkg/store/file"
	"github.com/zoumo/mass/mass-go/pkg/transport"
)

const claudeLoadID = "11111111-1111-4111-8111-111111111111"

var (
	ticket07Build    sync.Once
	ticket07Binary   string
	ticket07BuildErr error
)

type ticket07Setup struct {
	path, config       string
	env                map[string]any
	history            func(eventstore.EventStore) eventstore.EventStore
	factory            func(runtime.Factory) runtime.Factory
	allowShutdownError bool
	fileStoreDir       string
}

func ticket07HTTP(t *testing.T, mode string, rows []map[string]any, options ...func(*ticket07Setup)) (*service.Service, string, string, string) {
	t.Helper()
	ticket07Build.Do(func() {
		ticket07Binary = filepath.Join(filepath.Dir(claudeMock), "claude-load-peer")
		out, err := exec.CommandContext(ctx(t), "go", "build", "-o", ticket07Binary, "./testdata/claude-load-peer").CombinedOutput()
		if err != nil {
			ticket07BuildErr = fmt.Errorf("build peer: %w: %s", err, out)
		}
	})
	require.NoError(t, ticket07BuildErr)
	bin := ticket07Binary
	cwd, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	config := t.TempDir()
	key := regexp.MustCompile(`[^a-zA-Z0-9]`).ReplaceAllString(cwd, "-")
	dir := filepath.Join(config, "projects", key)
	require.NoError(t, os.MkdirAll(dir, 0o700))
	path := filepath.Join(dir, claudeLoadID+".jsonl")
	f, err := os.Create(path)
	require.NoError(t, err)
	for _, row := range rows {
		row["sessionId"] = claudeLoadID
		row["cwd"] = cwd
		require.NoError(t, json.NewEncoder(f).Encode(row))
	}
	require.NoError(t, f.Close())
	evidence := filepath.Join(t.TempDir(), "peer.jsonl")
	setup := &ticket07Setup{path: path, config: config, env: map[string]any{"CLAUDE_CONFIG_DIR": config, "LOAD_ID": claudeLoadID, "LOAD_MODE": mode, "LOAD_EVIDENCE": evidence}}
	for _, o := range options {
		o(setup)
	}
	var meta metastore.MetaStore
	var history eventstore.EventStore
	if setup.fileStoreDir != "" {
		// Use the product's independent stores, retaining file authority for both.
		metadata, err := metafile.Open(setup.fileStoreDir, false)
		require.NoError(t, err)
		t.Cleanup(func() { require.NoError(t, metadata.Close()) })
		records, err := eventfile.Open(setup.fileStoreDir, false)
		require.NoError(t, err)
		t.Cleanup(func() { require.NoError(t, records.Close()) })
		meta, history = metadata, records
	} else {
		st, err := file.Open(t.TempDir(), false)
		require.NoError(t, err)
		t.Cleanup(func() { require.NoError(t, st.Close()) })
		meta, history = st, st
	}
	if setup.history != nil {
		history = setup.history(history)
	}
	var factory runtime.Factory = claude.Factory{RecoveryDir: t.TempDir()}
	if setup.factory != nil {
		factory = setup.factory(factory)
	}
	svc := service.New(meta, history, runtime.NewRegistry(map[string]runtime.Factory{"claude": factory}))
	_, err = svc.CreateRuntime(resource.Runtime{Metadata: resource.Metadata{Name: "agent"}, Spec: resource.RuntimeSpec{Type: "claude", Config: runtime.Config{"executable": bin, "envPolicy": "replace", "env": setup.env}}})
	require.NoError(t, err)
	_, err = svc.StartRuntime(ctx(t), "agent")
	require.NoError(t, err)
	h, err := transport.New(svc, withTestWriteTimeout(transport.Options{LocalOnly: true}))
	require.NoError(t, err)
	server := httptest.NewServer(h)
	t.Cleanup(func() {
		server.Close()
		h.Shutdown()
		c, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		err := svc.Shutdown(c)
		if !setup.allowShutdownError {
			require.NoError(t, err)
		}
	})
	return svc, server.URL, cwd, evidence
}

func ticket07Rows() []map[string]any {
	return []map[string]any{
		{"type": "user", "uuid": "first", "parentUuid": nil, "message": map[string]any{"role": "user", "content": "first retained"}},
		{"type": "assistant", "uuid": "last", "parentUuid": "first", "message": map[string]any{"id": "answer", "role": "assistant", "content": []any{map[string]any{"type": "text", "text": "last retained"}}}},
	}
}

func TestTicket07ClaudeLoadHTTPAndTwoRetainedProcessWrites(t *testing.T) {
	_, url, cwd, evidence := ticket07HTTP(t, "", ticket07Rows())
	c := newHTTPClient(t, url, "")
	ref := resource.SessionRef{SessionID: claudeLoadID}
	var accepted resource.SessionLoadAccepted
	require.NoError(t, c.LoadSession(ctx(t), resource.SessionLoad{Metadata: resource.Metadata{Namespace: "one", Name: "imported"}, RuntimeRef: "agent", Cwd: cwd, SessionID: claudeLoadID}, &accepted))
	require.Eventually(t, func() bool {
		var r resource.Session
		return c.GetSession(ctx(t), ref, &r) == nil && r.Status.HistoryLoad.Phase == "completed"
	}, 5*time.Second, time.Millisecond)
	var r resource.Session
	require.NoError(t, c.GetSession(ctx(t), ref, &r))
	require.Equal(t, events.Unknown, r.Status.State, "saved history is not current idle evidence")
	var page store.HistoryPage
	require.NoError(t, c.History(ctx(t), ref, store.HistoryQuery{Limit: 1}, &page))
	require.Len(t, page.Events, 1)
	require.Equal(t, "first:0", page.Events[0].ItemID)
	require.Equal(t, "first", page.Events[0].Replay.NativeRecordID)
	require.Empty(t, page.Events[0].Replay.NativeParentID, "root null parent must not be invented")
	var tail store.HistoryPage
	require.NoError(t, c.History(ctx(t), ref, store.HistoryQuery{After: page.Next}, &tail))
	require.Len(t, tail.Events, 1)
	require.Equal(t, "last:0", tail.Events[0].ItemID)
	require.Equal(t, "last", tail.Events[0].Replay.NativeRecordID)
	observer, err := c.Subscribe(ctx(t), ref)
	require.NoError(t, err)
	defer observer.Close()
	var submissions []string
	for _, text := range []string{"one", "two"} {
		var receipt runtime.InputReceipt
		require.NoError(t, c.SendPrompt(ctx(t), ref, runtime.Input{Content: []events.ContentBlock{events.Text(text)}}, &receipt))
		require.Equal(t, runtime.Yes(), receipt.Acceptance.Sent)
		require.Nil(t, receipt.Acceptance.NativeAccepted)
		submissions = append(submissions, receipt.Submission)
		messageObserved := false
		for {
			e, nextErr := observer.Next(ctx(t))
			require.NoError(t, nextErr)
			if e.Type() == "agent.message" {
				messageObserved = true
			}
			if p, ok := e.Payload().(events.StateUpdate); ok && p.State != nil && *p.State == events.Idle && messageObserved {
				break
			}
		}
	}
	data, err := os.ReadFile(evidence)
	require.NoError(t, err)
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	require.Len(t, lines, 3, "one resume process plus exactly two native writes")
	var records [3]map[string]any
	for i, line := range lines {
		require.NoError(t, json.Unmarshal([]byte(line), &records[i]))
	}
	require.Equal(t, records[0]["pid"], records[1]["pid"])
	require.Equal(t, records[1]["pid"], records[2]["pid"])
	require.Equal(t, "input", records[1]["event"])
	require.InDelta(t, 1, records[1]["input"], 0)
	require.Equal(t, "input", records[2]["event"])
	require.InDelta(t, 2, records[2]["input"], 0)
	require.NotEqual(t, submissions[0], submissions[1])
	require.Contains(t, records[0]["args"], "--resume="+claudeLoadID)
	var canceled resource.HistoryLoadStatus
	require.Error(t, c.CancelHistoryLoad(ctx(t), ref, accepted.LoadID, &canceled))
}

func TestTicket07ClaudeOrderedFiveKindsAndOutputPresence(t *testing.T) {
	rows := ticket07Rows()
	rows[0]["timestamp"] = "2026-10-01T01:02:03Z"
	rows[1]["message"] = map[string]any{"role": "assistant", "id": "same-message", "content": []any{
		map[string]any{"type": "thinking", "thinking": "retained summary", "signature": "raw signature"},
		map[string]any{"type": "text", "text": "before tools"},
		map[string]any{"type": "tool_use", "id": "absent", "name": "Read", "input": map[string]any{"value": 123}},
		map[string]any{"type": "text", "text": "between tools"},
		map[string]any{"type": "tool_use", "id": "null", "name": "Read", "input": nil},
		map[string]any{"type": "tool_use", "id": "empty", "name": "Read", "input": map[string]any{}},
		map[string]any{"type": "tool_use", "id": "truncated", "name": "Read", "input": map[string]any{}},
		map[string]any{"type": "tool_use", "id": "no-result", "name": "Read", "input": map[string]any{}},
	}}
	rows = append(rows, map[string]any{"type": "user", "uuid": "results", "parentUuid": "last", "message": map[string]any{"role": "user", "content": []any{
		map[string]any{"type": "tool_result", "tool_use_id": "absent"},
		map[string]any{"type": "tool_result", "tool_use_id": "null", "content": nil},
		map[string]any{"type": "tool_result", "tool_use_id": "empty", "content": ""},
		map[string]any{"type": "tool_result", "tool_use_id": "truncated", "content": "partial", "truncated": true},
	}}})
	_, url, cwd, _ := ticket07HTTP(t, "", rows)
	c := newHTTPClient(t, url, "")
	ref := resource.SessionRef{SessionID: claudeLoadID}
	var accepted resource.SessionLoadAccepted
	require.NoError(t, c.LoadSession(ctx(t), resource.SessionLoad{Metadata: resource.Metadata{Namespace: "one", Name: "rich"}, RuntimeRef: "agent", Cwd: cwd, SessionID: claudeLoadID}, &accepted))
	require.Eventually(t, func() bool {
		var r resource.Session
		return c.GetSession(ctx(t), ref, &r) == nil && r.Status.HistoryLoad.Phase == "completed"
	}, 5*time.Second, time.Millisecond)
	var page store.HistoryPage
	require.NoError(t, c.History(ctx(t), ref, store.HistoryQuery{}, &page))
	require.Len(t, page.Events, 13)
	require.Equal(t, "2026-10-01T01:02:03Z", page.Events[0].Time.Format(time.RFC3339))
	require.Equal(t, "native_record", page.Events[0].Meta["timeSource"])
	require.Equal(t, []string{"user.message", "agent.thinking", "agent.message", "agent.tool_call", "agent.message", "agent.tool_call", "agent.tool_call", "agent.tool_call", "agent.tool_call", "agent.tool_result", "agent.tool_result", "agent.tool_result", "agent.tool_result"}, eventKinds(page.Events))
	require.Equal(t, []string{"first:0", "last:0", "last:1", "absent", "last:3", "null", "empty", "truncated", "no-result", "absent", "null", "empty", "truncated"}, eventItems(page.Events))
	for _, event := range page.Events[1:9] {
		require.Equal(t, "last", event.Replay.NativeRecordID)
	}
	for _, event := range page.Events[9:] {
		require.Equal(t, "results", event.Replay.NativeRecordID)
	}
	results := map[string]events.ToolResult{}
	for _, e := range page.Events {
		require.NotEmpty(t, e.Meta["nativeRaw"])
		if p, ok := e.Payload().(events.ToolResult); ok {
			results[p.ID] = p
		}
	}
	require.Nil(t, results["absent"].RawOutput)
	require.NotNil(t, results["null"].RawOutput)
	require.Nil(t, *results["null"].RawOutput)
	require.Empty(t, *results["empty"].RawOutput)
	require.Equal(t, "truncated", results["truncated"].Output.Completeness)
	require.EqualValues(t, 7, results["truncated"].Output.Truncation.RetainedBytes)
	require.NotContains(t, results, "no-result")
}

func ticket07History(t *testing.T, url, cwd string) store.HistoryPage {
	t.Helper()
	_, ref := ticket07Load(t, url, cwd)
	ticket07Status(t, url, ref, "completed")
	c := newHTTPClient(t, url, "")
	var page store.HistoryPage
	require.NoError(t, c.History(ctx(t), ref, store.HistoryQuery{}, &page))
	return page
}

func TestClaudeHistoryUsesNativeBlockCoordinatesAndSeparateReplay(t *testing.T) {
	rows := []map[string]any{
		{"type": "user", "uuid": "user-record", "parentUuid": nil, "message": map[string]any{"role": "user", "content": "question"}},
		{"type": "assistant", "uuid": "assistant-record", "parentUuid": "user-record", "apiBlockIndex": 3, "message": map[string]any{"id": "response-1", "role": "assistant", "content": []any{map[string]any{"type": "text", "text": "answer"}}}},
	}
	_, url, cwd, _ := ticket07HTTP(t, "", rows)
	page := ticket07History(t, url, cwd)

	require.Len(t, page.Events, 2)
	require.Equal(t, "user-record:0", page.Events[0].ItemID)
	require.Equal(t, "user-record", page.Events[0].Replay.NativeRecordID)
	require.Empty(t, page.Events[0].Replay.NativeParentID)
	require.Equal(t, "response-1:3", page.Events[1].ItemID)
	require.Equal(t, "assistant-record", page.Events[1].Replay.NativeRecordID)
	require.Equal(t, "user-record", page.Events[1].Replay.NativeParentID)
	require.Equal(t, "response-1:3", page.Events[1].Payload().(events.AgentMessage).MessageID)
	claudeMeta := decodeClaudeMeta(t, page.Events[1])
	require.Equal(t, "response-1", claudeMeta.ResponseID)
	require.Equal(t, 3, claudeMeta.BlockIndex)
	require.Nil(t, claudeMeta.ParentToolUseID)
}

func TestClaudeHistorySplitsOneRecordByBlockAndPreservesCoordinates(t *testing.T) {
	rows := []map[string]any{
		{"type": "user", "uuid": "user-record", "parentUuid": nil, "message": map[string]any{"role": "user", "content": "question"}},
		{"type": "assistant", "uuid": "assistant-record", "parentUuid": "user-record", "apiBlockIndex": 2, "message": map[string]any{"id": "response-1", "role": "assistant", "content": []any{
			map[string]any{"type": "thinking", "thinking": "reason"},
			map[string]any{"type": "thinking", "signature": "signature-only"},
			map[string]any{"type": "redacted_thinking", "data": "redacted"},
			map[string]any{"type": "text", "text": "\n\n"},
			map[string]any{"type": "text", "text": ""},
			map[string]any{"type": "tool_use", "id": "tool-shared", "name": "Read", "input": map[string]any{"path": "README.md"}},
			map[string]any{"type": "text", "text": "after tool"},
		}}},
		{"type": "user", "uuid": "result-record", "parentUuid": "assistant-record", "message": map[string]any{"role": "user", "content": []any{
			map[string]any{"type": "tool_result", "tool_use_id": "tool-shared", "content": "retained"},
		}}},
	}
	_, url, cwd, _ := ticket07HTTP(t, "", rows)
	page := ticket07History(t, url, cwd)

	require.Len(t, page.Events, 6)
	require.Equal(t, []string{"user-record:0", "response-1:2", "response-1:5", "tool-shared", "response-1:8", "tool-shared"}, eventItems(page.Events))
	require.Equal(t, []string{"user.message", "agent.thinking", "agent.message", "agent.tool_call", "agent.message", "agent.tool_result"}, eventKinds(page.Events))
	require.Equal(t, "user-record", page.Events[0].Replay.NativeRecordID)
	for _, index := range []int{1, 2, 3, 4} {
		require.Equal(t, "assistant-record", page.Events[index].Replay.NativeRecordID)
	}
	require.Equal(t, "result-record", page.Events[5].Replay.NativeRecordID)
	require.Equal(t, []events.ContentBlock{events.Text("\n\n")}, page.Events[2].Payload().(events.AgentMessage).Content)
	require.Equal(t, []events.ContentBlock{events.Text("after tool")}, page.Events[4].Payload().(events.AgentMessage).Content)
	require.NotEqual(t, page.Events[3].EventID, page.Events[5].EventID)
	require.Empty(t, page.Events[0].Replay.NativeParentID)
	for _, index := range []int{1, 2, 3, 4} {
		require.Equal(t, "user-record", page.Events[index].Replay.NativeParentID)
	}
	require.Equal(t, "assistant-record", page.Events[5].Replay.NativeParentID)
}

func TestClaudeHistorySkipsMissingAndNullSignatureOnlyThinking(t *testing.T) {
	rows := []map[string]any{
		{"type": "assistant", "uuid": "assistant-record", "parentUuid": nil, "apiBlockIndex": 4, "message": map[string]any{"id": "response-signature", "role": "assistant", "content": []any{
			map[string]any{"type": "thinking", "signature": "missing-thinking"},
			map[string]any{"type": "thinking", "thinking": nil, "signature": "null-thinking"},
			map[string]any{"type": "text", "text": "visible answer"},
		}}},
	}
	_, url, cwd, _ := ticket07HTTP(t, "", rows)
	page := ticket07History(t, url, cwd)

	require.Len(t, page.Events, 1)
	require.Equal(t, "response-signature:6", page.Events[0].ItemID)
	require.Equal(t, []events.ContentBlock{events.Text("visible answer")}, page.Events[0].Payload().(events.AgentMessage).Content)
}

func TestClaudeHistoryFallsBackToRecordAndContentOrdinalWithoutBlockCoordinate(t *testing.T) {
	rows := []map[string]any{
		{"type": "user", "uuid": "user-record", "parentUuid": nil, "message": map[string]any{"role": "user", "content": []any{
			map[string]any{"type": "text", "text": "first question"},
			map[string]any{"type": "text", "text": "second question"},
		}}},
		{"type": "assistant", "uuid": "assistant-record", "parentUuid": "user-record", "message": map[string]any{"id": "response-without-block", "role": "assistant", "content": []any{
			map[string]any{"type": "thinking", "thinking": ""},
			map[string]any{"type": "text", "text": "first answer"},
			map[string]any{"type": "thinking", "thinking": "reason"},
			map[string]any{"type": "text", "text": ""},
		}}},
	}
	_, url, cwd, _ := ticket07HTTP(t, "", rows)
	page := ticket07History(t, url, cwd)

	require.Equal(t, []string{"user-record:0", "user-record:1", "assistant-record:1", "assistant-record:2"}, eventItems(page.Events))
	for _, event := range page.Events[:2] {
		require.Equal(t, "user-record", event.Replay.NativeRecordID)
		require.Empty(t, event.Replay.NativeParentID)
	}
	for _, event := range page.Events[2:] {
		require.Equal(t, "assistant-record", event.Replay.NativeRecordID)
		require.Equal(t, "user-record", event.Replay.NativeParentID)
	}
	require.Equal(t, []events.ContentBlock{events.Text("first answer")}, page.Events[2].Payload().(events.AgentMessage).Content)
	require.Equal(t, []events.ContentBlock{events.Text("reason")}, page.Events[3].Payload().(events.AgentThinking).Content)
}

func TestClaudeHistoryRejectsInvalidNativeBlockCoordinate(t *testing.T) {
	rows := ticket07Rows()
	rows[1]["apiBlockIndex"] = -1
	_, url, cwd, _ := ticket07HTTP(t, "", rows)
	_, ref := ticket07Load(t, url, cwd)
	status := ticket07Status(t, url, ref, "failed")

	require.False(t, status.Status.HistoryLoad.DeliveryCompleted)
	c := newHTTPClient(t, url, "")
	var page store.HistoryPage
	require.NoError(t, c.History(ctx(t), ref, store.HistoryQuery{}, &page))
	for _, event := range page.Events {
		require.NotEqual(t, "agent.message", event.Type())
	}
}

func TestClaudeHistoryMatchesLiveNativeBlockItemIdentity(t *testing.T) {
	liveService, _, _ := setup(t, prototype.AutoApprovePermissions)
	live := openNative(t, liveService, "claude", "basic", "live")
	liveRef := resource.SessionRef{SessionID: live.Status.SessionID}
	subscription, err := liveService.SubscribeEvents(liveRef)
	require.NoError(t, err)
	defer subscription.Close()
	_, err = liveService.SendPrompt(ctx(t), liveRef, runtime.Input{Content: []events.ContentBlock{events.Text("hello")}})
	require.NoError(t, err)

	liveItems := map[string]bool{}
	for {
		event, err := nextSubscription(subscription, ctx(t))
		require.NoError(t, err)
		switch event.Payload().(type) {
		case events.AgentThinking, events.AgentMessage:
			liveItems[event.ItemID] = true
		case events.StateUpdate:
			state := event.Payload().(events.StateUpdate)
			if state.State != nil && *state.State == events.Idle && len(liveItems) > 0 {
				goto liveObserved
			}
		}
	}

liveObserved:
	require.Equal(t, map[string]bool{"msg-8:0": true, "msg-8:2": true, "msg-8:4": true}, liveItems)
	rows := []map[string]any{
		{"type": "user", "uuid": "history-user", "parentUuid": nil, "message": map[string]any{"role": "user", "content": "hello"}},
		{"type": "assistant", "uuid": "history-thinking", "parentUuid": "history-user", "apiBlockIndex": 0, "message": map[string]any{"id": "msg-8", "role": "assistant", "content": []any{map[string]any{"type": "thinking", "thinking": "reason one"}}}},
		{"type": "assistant", "uuid": "history-answer", "parentUuid": "history-thinking", "apiBlockIndex": 2, "message": map[string]any{"id": "msg-8", "role": "assistant", "content": []any{map[string]any{"type": "text", "text": "new answer"}}}},
		{"type": "assistant", "uuid": "history-second", "parentUuid": "history-answer", "apiBlockIndex": 4, "message": map[string]any{"id": "msg-8", "role": "assistant", "content": []any{map[string]any{"type": "text", "text": "second block"}}}},
	}
	_, url, cwd, _ := ticket07HTTP(t, "", rows)
	page := ticket07History(t, url, cwd)

	historyItems := map[string]bool{}
	for _, event := range page.Events {
		switch event.Payload().(type) {
		case events.AgentThinking, events.AgentMessage:
			historyItems[event.ItemID] = true
		}
	}
	require.Equal(t, liveItems, historyItems)
}

func eventKinds(records []events.SessionEvent) []string {
	kinds := make([]string, len(records))
	for i, e := range records {
		kinds[i] = e.Type()
	}
	return kinds
}

func eventItems(records []events.SessionEvent) []string {
	items := make([]string, len(records))
	for i, event := range records {
		items[i] = event.ItemID
	}
	return items
}

func ticket07Load(t *testing.T, url, cwd string) (*resource.SessionLoadAccepted, resource.SessionRef) {
	t.Helper()
	c := newHTTPClient(t, url, "")
	var a resource.SessionLoadAccepted
	require.NoError(t, c.LoadSession(ctx(t), resource.SessionLoad{Metadata: resource.Metadata{Namespace: "one", Name: "imported"}, RuntimeRef: "agent", Cwd: cwd, SessionID: claudeLoadID}, &a))
	return &a, resource.SessionRef{SessionID: claudeLoadID}
}

func ticket07Status(t *testing.T, url string, ref resource.SessionRef, phase string) resource.Session {
	t.Helper()
	return ticket07StatusWithin(t, url, ref, phase, 5*time.Second)
}

func ticket07StatusWithin(t *testing.T, url string, ref resource.SessionRef, phase string, budget time.Duration) resource.Session {
	t.Helper()
	c := newHTTPClient(t, url, "")
	var r resource.Session
	require.EventuallyWithT(t, func(collect *assert.CollectT) {
		var current resource.Session
		if err := c.GetSession(ctx(t), ref, &current); !assert.NoError(collect, err, "last public load status: %+v", r.Status.HistoryLoad) {
			return
		}
		r = current
		if assert.NotNil(collect, r.Status.HistoryLoad, "public load status missing") {
			assert.Contains(collect, []string{phase, "failed", "canceled"}, r.Status.HistoryLoad.Phase, "last public load status: %+v", r.Status.HistoryLoad)
		}
	}, budget, time.Millisecond)
	require.Equal(t, phase, r.Status.HistoryLoad.Phase, "load status: %+v", r.Status.HistoryLoad)
	return r
}

func TestTicket07ClaudeUnlinkedNativeResultUsesSDKState(t *testing.T) {
	_, url, cwd, _ := ticket07HTTP(t, "unlinked", ticket07Rows())
	_, ref := ticket07Load(t, url, cwd)
	ticket07Status(t, url, ref, "completed")
	c := newHTTPClient(t, url, "")
	observer, err := c.Subscribe(ctx(t), ref)
	require.NoError(t, err)
	defer observer.Close()
	var receipt runtime.InputReceipt
	for range 2 {
		require.NoError(t, c.SendPrompt(ctx(t), ref, runtime.Input{Content: []events.ContentBlock{events.Text("ordinary unlinked response")}}, &receipt))
		sawMessage := false
		for {
			e, err := observer.Next(ctx(t))
			require.NoError(t, err)
			if e.Type() == "agent.message" {
				sawMessage = true
				meta := e.Meta["claude"].(map[string]any)
				require.NotContains(t, meta, "submission")
				require.NotContains(t, meta, "attribution")
				raw := meta["raw"].(map[string]any)
				require.NotContains(t, raw, "user_message_uuid")
			}
			if p, ok := e.Payload().(events.StateUpdate); ok && p.State != nil && *p.State == events.Idle {
				require.True(t, sawMessage, "SDK must preserve the native message before settlement")
				require.NotEqual(t, "work_attribution_unknown", p.StopReason, "MASS must not recreate its removed attribution state machine")
				break
			}
		}
	}
	var r resource.Session
	require.NoError(t, c.GetSession(ctx(t), ref, &r))
	require.Equal(t, events.Idle, r.Status.State)
}

func TestTicket07ClaudeInvalidSourcesAndPartialUnsupportedHistory(t *testing.T) {
	for _, kind := range []string{"duplicate", "missing-parent", "cycle", "partial-line", "cwd", "session", "unsupported", "orphan-tool", "identity", "no-config"} {
		t.Run(kind, func(t *testing.T) {
			rows := ticket07Rows()
			mode := ""
			switch kind {
			case "duplicate":
				rows[1]["uuid"] = "first"
			case "missing-parent":
				rows[1]["parentUuid"] = "not-retained"
			case "cycle":
				rows[0]["parentUuid"] = "last"
			case "unsupported":
				rows[1]["message"] = map[string]any{"role": "assistant", "content": []any{map[string]any{"type": "image", "source": map[string]any{}}}}
			case "orphan-tool":
				rows[1]["type"] = "user"
				rows[1]["message"] = map[string]any{"role": "user", "content": []any{map[string]any{"type": "tool_result", "tool_use_id": "no-call", "content": "bad"}}}
			case "identity":
				mode = "identity"
			}
			_, url, cwd, _ := ticket07HTTP(t, mode, rows, func(s *ticket07Setup) {
				if kind == "no-config" {
					delete(s.env, "CLAUDE_CONFIG_DIR")
				}
				if kind == "partial-line" {
					f, err := os.OpenFile(s.path, os.O_APPEND|os.O_WRONLY, 0)
					require.NoError(t, err)
					_, err = f.WriteString(`{"type":`)
					require.NoError(t, err)
					require.NoError(t, f.Close())
				}
				if kind == "cwd" || kind == "session" {
					b, err := os.ReadFile(s.path)
					require.NoError(t, err)
					old, newValue := cwdPlaceholder(s, kind)
					b = []byte(strings.ReplaceAll(string(b), old, newValue))
					require.NoError(t, os.WriteFile(s.path, b, 0o600))
				}
			})
			_, ref := ticket07Load(t, url, cwd)
			r := ticket07Status(t, url, ref, "failed")
			c := newHTTPClient(t, url, "")
			require.Eventually(t, func() bool { return c.GetSession(ctx(t), ref, &r) == nil && r.Status.HistoryLoad.CleanupCompleted }, 5*time.Second, time.Millisecond)
			require.False(t, r.Status.HistoryLoad.DeliveryCompleted)
			require.NotNil(t, r.Status.HistoryLoad.Error)
			var page store.HistoryPage
			require.NoError(t, c.History(ctx(t), ref, store.HistoryQuery{}, &page))
			if kind == "unsupported" || kind == "orphan-tool" {
				require.Len(t, page.Events, 1)
				require.Equal(t, "first", page.Events[0].Replay.NativeRecordID)
			} else {
				require.Empty(t, page.Events)
			}
			var input runtime.InputReceipt
			require.Error(t, c.SendPrompt(ctx(t), ref, runtime.Input{Content: []events.ContentBlock{events.Text("fenced")}}, &input))
			var a resource.SessionLoadAccepted
			require.Error(t, c.LoadSession(ctx(t), resource.SessionLoad{Metadata: resource.Metadata{Namespace: "one", Name: "replacement"}, RuntimeRef: "agent", Cwd: cwd, SessionID: claudeLoadID}, &a))
		})
	}
}

func cwdPlaceholder(s *ticket07Setup, kind string) (string, string) {
	if kind == "session" {
		return claudeLoadID, "22222222-2222-4222-8222-222222222222"
	}
	b, _ := os.ReadFile(s.path)
	line := strings.Split(string(b), "\n")[0]
	var row map[string]any
	_ = json.Unmarshal([]byte(line), &row)
	return row["cwd"].(string), "/synthetic-wrong-project"
}

func ticket07Many(n int) []map[string]any {
	rows := make([]map[string]any, 0, n)
	for i := range n {
		var parent any
		if i > 0 {
			parent = fmt.Sprintf("row-%03d", i-1)
		}
		rows = append(rows, map[string]any{"type": "assistant", "uuid": fmt.Sprintf("row-%03d", i), "parentUuid": parent, "message": map[string]any{"id": "legitimately-repeated-message-id", "role": "assistant", "content": fmt.Sprintf("retained-%03d", i)}})
	}
	return rows
}

func TestTicket07ClaudeMultipleOffsetQueriesPreserveFirstLastAndHiddenParent(t *testing.T) {
	rows := ticket07Many(105)
	rows = append([]map[string]any{{"type": "system", "uuid": "hidden", "parentUuid": nil, "subtype": "compact_boundary"}}, rows...)
	rows[1]["parentUuid"] = "hidden"
	data := t.TempDir()
	_, url, cwd, _ := ticket07HTTP(t, "", rows, func(s *ticket07Setup) { s.fileStoreDir = data })
	_, ref := ticket07Load(t, url, cwd)
	// Scenario-local observation budget for 105 durable records, not a product SLA.
	ticket07StatusWithin(t, url, ref, "completed", 10*time.Second)
	c := newHTTPClient(t, url, "")
	var page store.HistoryPage
	require.NoError(t, c.History(ctx(t), ref, store.HistoryQuery{Limit: 1000}, &page))
	require.Len(t, page.Events, 105)
	require.Equal(t, "row-000", page.Events[0].Replay.NativeRecordID)
	require.Equal(t, "hidden", page.Events[0].Replay.NativeParentID)
	require.Equal(t, "row-104", page.Events[104].Replay.NativeRecordID)
	offsets := map[any]bool{}
	for _, e := range page.Events {
		offsets[decodeHistoryOffset(t, e)] = true
	}
	require.Len(t, offsets, 2)

	// Reopen through public file-store constructors to verify durable authority.
	metadata, err := metafile.Open(data, true)
	require.NoError(t, err)
	defer func() { require.NoError(t, metadata.Close()) }()
	saved, err := metadata.GetSession(ref)
	require.NoError(t, err)
	require.Equal(t, "completed", saved.Status.HistoryLoad.Phase)
	records, err := eventfile.Open(data, true)
	require.NoError(t, err)
	defer func() { require.NoError(t, records.Close()) }()
	persisted, err := records.ListHistory(claudeLoadID, store.HistoryQuery{Limit: 1000})
	require.NoError(t, err)
	require.Equal(t, page.Events, persisted.Events)
}

func decodeHistoryOffset(t *testing.T, event events.SessionEvent) int {
	t.Helper()
	raw, err := json.Marshal(event.Meta["pagination"])
	require.NoError(t, err)
	var pagination struct {
		Offset int `json:"offset"`
	}
	require.NoError(t, json.Unmarshal(raw, &pagination))
	return pagination.Offset
}

// Public Driver/Stream decorators control observation timing without consuming a
// second stream or changing any event/source. Files are owned synthetic input.
type ticket07Gate struct {
	entered, release chan struct{}
	once             sync.Once
}

func (g *ticket07Gate) wait(ctx context.Context) error {
	g.once.Do(func() { close(g.entered) })
	select {
	case <-g.release:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

type ticket07Factory struct {
	runtime.Factory
	streamGate *ticket07Gate
	finished   chan error
}

func (f ticket07Factory) New(name string, c runtime.Config) (runtime.Driver, error) {
	d, err := f.Factory.New(name, c)
	if err != nil {
		return nil, err
	}
	return &ticket07Driver{Driver: d, gate: f.streamGate, finished: f.finished}, nil
}

type ticket07Driver struct {
	runtime.Driver
	gate     *ticket07Gate
	finished chan error
}

func (d *ticket07Driver) Events() (events.Stream, error) {
	s, err := d.Driver.Events()
	if err != nil {
		return nil, err
	}
	return &ticket07Stream{Stream: s, gate: d.gate}, nil
}

func (d *ticket07Driver) LoadSession(ctx context.Context, id string, a runtime.Address, spec runtime.SessionSpec, load string) error {
	err := d.Driver.LoadSession(ctx, id, a, spec, load)
	if d.finished != nil {
		d.finished <- err
	}
	return err
}

type ticket07Stream struct {
	events.Stream
	gate *ticket07Gate
}

func (s *ticket07Stream) Next(ctx context.Context) (events.SessionEvent, error) {
	e, err := s.Stream.Next(ctx)
	if err == nil && s.gate != nil && e.Replay != nil && e.Persistent() {
		if gateErr := s.gate.wait(ctx); gateErr != nil {
			return events.SessionEvent{}, gateErr
		}
	}
	return e, err
}

type ticket07AppendGate struct {
	eventstore.EventStore
	gate   *ticket07Gate
	record string
}

func (s *ticket07AppendGate) Append(id string, records []events.SessionEvent) (*string, error) {
	if id == claudeLoadID && len(records) > 0 && records[0].Replay != nil && (records[0].Replay.NativeRecordID == s.record || s.record == "" && records[0].Replay.NativeRecordID == "last") {
		_ = s.gate.wait(context.Background())
	}
	return s.EventStore.Append(id, records)
}

func ticket07NewGate(t *testing.T) *ticket07Gate {
	g := &ticket07Gate{entered: make(chan struct{}), release: make(chan struct{})}
	return g
}

func ticket07Entered(t *testing.T, g *ticket07Gate) {
	t.Helper()
	select {
	case <-g.entered:
	case <-ctx(t).Done():
		t.Fatal("public observation boundary not reached")
	}
}

func TestTicket07ClaudeCrossReadDriftOrPageCancellation(t *testing.T) {
	for _, mode := range []string{"drift", "cancel"} {
		t.Run(mode, func(t *testing.T) {
			gate := ticket07NewGate(t)
			var path string
			_, url, cwd, _ := ticket07HTTP(t, "", ticket07Many(80), func(s *ticket07Setup) {
				path = s.path
				s.factory = func(f runtime.Factory) runtime.Factory { return ticket07Factory{Factory: f, streamGate: gate} }
			})
			var once sync.Once
			release := func() { once.Do(func() { close(gate.release) }) }
			t.Cleanup(release)
			a, ref := ticket07Load(t, url, cwd)
			ticket07Entered(t, gate)
			c := newHTTPClient(t, url, "")
			if mode == "drift" {
				b, err := os.ReadFile(path)
				require.NoError(t, err)
				b = []byte(strings.ReplaceAll(string(b), "retained-000", "rewritten000"))
				require.NoError(t, os.WriteFile(path, b, 0o600))
			} else {
				var status resource.HistoryLoadStatus
				require.NoError(t, c.CancelHistoryLoad(ctx(t), ref, a.LoadID, &status))
			}
			release()
			phase := "failed"
			if mode == "cancel" {
				phase = "canceled"
			}
			r := ticket07Status(t, url, ref, phase)
			// Allow local durable drain, not a longer native stop or cancel SLA.
			require.EventuallyWithT(t, func(collect *assert.CollectT) {
				var current resource.Session
				if err := c.GetSession(ctx(t), ref, &current); !assert.NoError(collect, err, "last public cleanup status: %+v", r.Status.HistoryLoad) {
					return
				}
				r = current
				if assert.NotNil(collect, r.Status.HistoryLoad, "public load status missing") {
					assert.True(collect, r.Status.HistoryLoad.CleanupCompleted, "last public cleanup status: %+v", r.Status.HistoryLoad)
				}
			}, 10*time.Second, time.Millisecond)
			require.False(t, r.Status.HistoryLoad.DeliveryCompleted)
			var p store.HistoryPage
			require.NoError(t, c.History(ctx(t), ref, store.HistoryQuery{Limit: 1000}, &p))
			require.NotEmpty(t, p.Events)
			require.Equal(t, "row-000", p.Events[0].Replay.NativeRecordID)
			require.Equal(t, "retained-000", events.TextOf(p.Events[0].Payload().(events.AgentMessage).Content[0]), "committed facts are not reread or repaired")
			if mode == "cancel" {
				var healthy resource.Session
				require.NoError(t, c.CreateSession(ctx(t), resource.Session{Metadata: resource.Metadata{Namespace: "one", Name: "healthy"}, Spec: resource.SessionSpec{RuntimeRef: "agent", SessionSpec: runtime.SessionSpec{Cwd: cwd}}}, &healthy))
				href := resource.SessionRef{SessionID: healthy.Status.SessionID}
				var receipt runtime.InputReceipt
				require.NoError(t, c.SendPrompt(ctx(t), href, runtime.Input{Content: []events.ContentBlock{events.Text("healthy sibling")}}, &receipt))
				require.Eventually(t, func() bool {
					var page store.HistoryPage
					return c.History(ctx(t), href, store.HistoryQuery{}, &page) == nil && len(page.Events) > 0
				}, 5*time.Second, time.Millisecond)
			}
		})
	}
}

func TestTicket07ClaudeNativeCompletedBeforeLocalDrainRemainsCancelable(t *testing.T) {
	gate := ticket07NewGate(t)
	finished := make(chan error, 1)
	_, url, cwd, _ := ticket07HTTP(t, "", ticket07Rows(), func(s *ticket07Setup) {
		s.history = func(h eventstore.EventStore) eventstore.EventStore {
			return &ticket07AppendGate{EventStore: h, gate: gate}
		}
		s.factory = func(f runtime.Factory) runtime.Factory { return ticket07Factory{Factory: f, finished: finished} }
	})
	var once sync.Once
	release := func() { once.Do(func() { close(gate.release) }) }
	t.Cleanup(release)
	a, ref := ticket07Load(t, url, cwd)
	ticket07Entered(t, gate)
	select {
	case err := <-finished:
		require.NoError(t, err)
	case <-ctx(t).Done():
		t.Fatal("native read did not finish independently of local append")
	}
	c := newHTTPClient(t, url, "")
	var r resource.Session
	require.Eventually(t, func() bool { return c.GetSession(ctx(t), ref, &r) == nil && r.Status.HistoryLoad.NativeCompleted }, 5*time.Second, time.Millisecond)
	require.False(t, r.Status.HistoryLoad.DeliveryCompleted)
	var receipt runtime.InputReceipt
	require.Error(t, c.SendPrompt(ctx(t), ref, runtime.Input{Content: []events.ContentBlock{events.Text("not yet locally settled")}}, &receipt))
	var status resource.HistoryLoadStatus
	require.NoError(t, c.CancelHistoryLoad(ctx(t), ref, a.LoadID, &status))
	release()
	r = ticket07Status(t, url, ref, "canceled")
	require.Eventually(t, func() bool { return c.GetSession(ctx(t), ref, &r) == nil && r.Status.HistoryLoad.CleanupCompleted }, 5*time.Second, time.Millisecond)
	var page store.HistoryPage
	require.NoError(t, c.History(ctx(t), ref, store.HistoryQuery{}, &page))
	require.Len(t, page.Events, 2)
	require.False(t, r.Status.HistoryLoad.DeliveryCompleted)
}

func TestTicket07ClaudeCancelStartupAndOwnedInit(t *testing.T) {
	for _, mode := range []string{"startup", "init"} {
		t.Run(mode, func(t *testing.T) {
			gate := t.TempDir()
			finished := make(chan error, 1)
			_, url, cwd, evidence := ticket07HTTP(t, mode, ticket07Rows(), func(s *ticket07Setup) {
				s.env["LOAD_GATE"] = gate
				s.allowShutdownError = mode == "startup"
				s.factory = func(f runtime.Factory) runtime.Factory { return ticket07Factory{Factory: f, finished: finished} }
			})
			a, ref := ticket07Load(t, url, cwd)
			require.Eventually(t, func() bool { _, err := os.Stat(filepath.Join(gate, "ready")); return err == nil }, 5*time.Second, time.Millisecond)
			c := newHTTPClient(t, url, "")
			var status resource.HistoryLoadStatus
			require.NoError(t, c.CancelHistoryLoad(ctx(t), ref, a.LoadID, &status))
			select {
			case err := <-finished:
				require.Error(t, err)
			case <-ctx(t).Done():
				t.Fatal("cancel did not settle original startup read")
			}
			r := ticket07Status(t, url, ref, "canceled")
			require.False(t, r.Status.HistoryLoad.NativeVerified)
			require.False(t, r.Status.HistoryLoad.DeliveryCompleted)
			require.Eventually(t, func() bool {
				b, err := os.ReadFile(evidence)
				return err == nil && strings.Contains(string(b), `"released"`)
			}, 5*time.Second, time.Millisecond)
			if mode == "init" {
				require.Eventually(t, func() bool { return c.GetSession(ctx(t), ref, &r) == nil && r.Status.HistoryLoad.CleanupCompleted }, 5*time.Second, time.Millisecond)
			} else {
				require.NoError(t, c.GetSession(ctx(t), ref, &r))
				require.False(t, r.Status.HistoryLoad.CleanupCompleted, "an Open error supplies no owned Session handle/release proof; retain original responsibility")
			}
			var receipt runtime.InputReceipt
			require.Error(t, c.SendPrompt(ctx(t), ref, runtime.Input{Content: []events.ContentBlock{events.Text("not verified")}}, &receipt))
		})
	}
}

func TestTicket07ClaudeOverload205CancellationSettlesAndPeerSurvives(t *testing.T) {
	gate := ticket07NewGate(t)
	_, url, cwd, _ := ticket07HTTP(t, "", ticket07Many(205), func(s *ticket07Setup) {
		s.history = func(h eventstore.EventStore) eventstore.EventStore {
			return &ticket07AppendGate{EventStore: h, gate: gate, record: "row-000"}
		}
	})
	var once sync.Once
	release := func() { once.Do(func() { close(gate.release) }) }
	t.Cleanup(release)
	a, ref := ticket07Load(t, url, cwd)
	ticket07Entered(t, gate)
	r := ticket07Status(t, url, ref, "failed")
	require.Equal(t, "session_delivery_overloaded", r.Status.HistoryLoad.Error.Reason)
	require.False(t, r.Status.HistoryLoad.CleanupCompleted)
	require.False(t, r.Status.HistoryLoad.DeliveryCompleted)
	c := newHTTPClient(t, url, "")
	var status resource.HistoryLoadStatus
	cancelStarted := time.Now()
	require.NoError(t, c.CancelHistoryLoad(ctx(t), ref, a.LoadID, &status), "cancel must not wait for the blocked append")
	t.Logf("cancel returned with blocked append in %s; cleanup confirmed=%t", time.Since(cancelStarted), status.CleanupCompleted)
	drainStarted := time.Now()
	release()
	require.Eventually(t, func() bool { return c.GetSession(ctx(t), ref, &r) == nil && r.Status.HistoryLoad.CleanupCompleted }, 20*time.Second, 10*time.Millisecond, "original overload responsibility must settle after releasing the public sink")
	require.Equal(t, "session_delivery_overloaded", r.Status.HistoryLoad.Error.Reason)
	var page store.HistoryPage
	require.NoError(t, c.History(ctx(t), ref, store.HistoryQuery{Limit: 1000}, &page))
	require.NotEmpty(t, page.Events)
	require.LessOrEqual(t, len(page.Events), 205)
	t.Logf("cleanup confirmed after release in %s; retained facts=%d/205; overload remains explicit failure", time.Since(drainStarted), len(page.Events))
	var healthy resource.Session
	require.NoError(t, c.CreateSession(ctx(t), resource.Session{Metadata: resource.Metadata{Namespace: "one", Name: "healthy"}, Spec: resource.SessionSpec{RuntimeRef: "agent", SessionSpec: runtime.SessionSpec{Cwd: cwd}}}, &healthy))
	var receipt runtime.InputReceipt
	href := resource.SessionRef{SessionID: healthy.Status.SessionID}
	require.NoError(t, c.SendPrompt(ctx(t), href, runtime.Input{Content: []events.ContentBlock{events.Text("healthy after overloaded replay")}}, &receipt))
	require.Eventually(t, func() bool {
		var p store.HistoryPage
		return c.History(ctx(t), href, store.HistoryQuery{}, &p) == nil && len(p.Events) > 0
	}, 5*time.Second, time.Millisecond)
}
