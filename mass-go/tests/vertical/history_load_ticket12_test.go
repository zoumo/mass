package vertical_test

import (
	"encoding/json"
	"errors"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zoumo/mass/mass-go/pkg/events"
	"github.com/zoumo/mass/mass-go/pkg/resource"
	"github.com/zoumo/mass/mass-go/pkg/runtime"
	"github.com/zoumo/mass/mass-go/pkg/store"
	"github.com/zoumo/mass/mass-go/pkg/transport"
)

type ticket12Call struct {
	Method string         `json:"method"`
	Params map[string]any `json:"params"`
	PID    int            `json:"pid"`
}

func ticket12Calls(t *testing.T, path string) []ticket12Call {
	t.Helper()
	b, err := os.ReadFile(path)
	require.NoError(t, err)
	var calls []ticket12Call
	for line := range strings.SplitSeq(strings.TrimSpace(string(b)), "\n") {
		var call ticket12Call
		require.NoError(t, json.Unmarshal([]byte(line), &call))
		calls = append(calls, call)
	}
	return calls
}

func TestTicket12CodexColdLoadHTTPHistoryAndContinuedInput(t *testing.T) {
	ticket12ColdLoadHTTP(t, "cold")
}

func ticket12ColdLoadHTTP(t *testing.T, mode string) {
	t.Helper()
	evidence := filepath.Join(t.TempDir(), "peer.jsonl")
	svc, cwd := ticket06Service(t, mode, map[string]any{"LOAD_EVIDENCE": evidence})
	h, err := transport.New(svc, transport.Options{LocalOnly: true})
	require.NoError(t, err)
	defer h.Shutdown()
	server := httptest.NewServer(h)
	defer server.Close()
	c := newHTTPClient(t, server.URL, "")
	ref := resource.SessionRef{SessionID: "native-thread"}
	var accepted resource.SessionLoadAccepted
	require.NoError(t, c.LoadSession(ctx(t), resource.SessionLoad{Metadata: resource.Metadata{Namespace: "one", Name: "cold"}, RuntimeRef: "agent", Cwd: cwd, SessionID: ref.SessionID}, &accepted))
	var loaded resource.Session
	require.Eventually(t, func() bool {
		return c.GetSession(ctx(t), ref, &loaded) == nil && (loaded.Status.HistoryLoad.Phase == "completed" || loaded.Status.HistoryLoad.Phase == "failed")
	}, 5*time.Second, time.Millisecond)
	require.Equal(t, "completed", loaded.Status.HistoryLoad.Phase, "%+v", loaded.Status.HistoryLoad)
	require.True(t, loaded.Status.HistoryLoad.NativeVerified)
	require.True(t, loaded.Status.HistoryLoad.NativeResumed)
	require.True(t, loaded.Status.HistoryLoad.NativeCompleted)
	require.True(t, loaded.Status.HistoryLoad.DeliveryCompleted)
	require.False(t, loaded.Status.HistoryLoad.CleanupCompleted, "successful load retains the connection")
	var first, last store.HistoryPage
	require.NoError(t, c.History(ctx(t), ref, store.HistoryQuery{Limit: 2}, &first))
	require.Len(t, first.Events, 2)
	require.NotEmpty(t, first.Next)
	require.NoError(t, c.History(ctx(t), ref, store.HistoryQuery{After: first.Next}, &last))
	require.Len(t, last.Events, 1)
	require.Empty(t, last.Next)
	require.Equal(t, "first", first.Events[0].Replay.NativeRecordID)
	require.Equal(t, "turn-z", first.Events[0].Replay.NativeParentID)
	require.Equal(t, "saved first", first.Events[0].Payload().(events.UserMessage).Content[0].Text)
	require.Equal(t, "saved answer", first.Events[1].Payload().(events.AgentMessage).Content[0].Text)
	require.Equal(t, "item/+:next", first.Events[1].Meta["pagination"].(map[string]any)["itemCursor"])
	require.Equal(t, "last", last.Events[0].Replay.NativeRecordID)
	require.Equal(t, "saved last", last.Events[0].Payload().(events.AgentMessage).Content[0].Text)
	require.Equal(t, "turn/+:next", last.Events[0].Meta["pagination"].(map[string]any)["turnCursor"])
	var receipt runtime.InputReceipt
	require.NoError(t, c.SendPrompt(ctx(t), ref, runtime.Input{Content: []events.ContentBlock{events.Text("continue cold thread")}}, &receipt))
	require.Equal(t, "continued", receipt.Submission)
	var live store.HistoryPage
	require.Eventually(t, func() bool {
		return c.History(ctx(t), ref, store.HistoryQuery{}, &live) == nil && len(live.Events) == 4
	}, 5*time.Second, time.Millisecond)
	require.Nil(t, live.Events[3].Replay)
	require.Equal(t, "same resumed connection", live.Events[3].Payload().(events.AgentMessage).Content[0].Text)
	var stopped resource.Runtime
	require.NoError(t, c.StopRuntime(ctx(t), "agent", &stopped))
	require.NoError(t, c.GetRuntime(ctx(t), "agent", &stopped))
	require.Equal(t, "stopped", stopped.Status.State)
	calls := ticket12Calls(t, evidence)
	counts := map[string]int{}
	var statuses []string
	var direct []any
	for _, call := range calls {
		counts[call.Method]++
		require.Equal(t, calls[0].PID, call.PID, "one owned native process")
		if strings.HasSuffix(call.Method, "/result") {
			thread := call.Params["thread"].(map[string]any)
			statuses = append(statuses, thread["status"].(map[string]any)["type"].(string))
			if mode == "cold-materialized-direct" {
				value, present := thread["canAcceptDirectInput"]
				require.True(t, present, "explicit null is not omission")
				direct = append(direct, value)
			}
		}
		if call.Method == "turn/start" {
			require.Equal(t, "native-thread", call.Params["threadId"])
			input := call.Params["input"].([]any)
			require.Len(t, input, 1)
			require.Equal(t, "continue cold thread", input[0].(map[string]any)["text"])
		}
	}
	require.Equal(t, []string{"notLoaded", "idle", "idle"}, statuses)
	if mode == "cold-materialized-direct" {
		require.Equal(t, []any{nil, true, true}, direct, "native capability materializes only at cold resume")
	}
	require.Equal(t, 1, counts["thread/resume"])
	require.Equal(t, 2, counts["thread/read"])
	require.Equal(t, 4, counts["thread/turns/list"], "both native pages in both verification passes")
	require.Equal(t, 6, counts["thread/items/list"])
	require.Equal(t, 1, counts["turn/start"], "only the user's explicit continued input")
	require.Equal(t, 1, counts["thread/unsubscribe"])
	require.Equal(t, 1, counts["released"])
	require.Eventually(t, func() bool { return errors.Is(syscall.Kill(calls[0].PID, 0), syscall.ESRCH) }, time.Second, time.Millisecond)
	t.Log("PASS cold load native pagination, single resume/input, owned process release and runtime readback")
}

func TestTicket12CodexColdLoadRejectsUnsettledOrDriftingSource(t *testing.T) {
	var cases []ticket12RejectedCase
	for _, field := range []string{"active", "unknown", "systemError", "outsideHome", "relativePath", "id", "source"} {
		cases = append(cases, ticket12RejectedCase{"cold:pre:" + field, 0, 0})
	}
	for _, stage := range []struct {
		name     string
		retained int
	}{{"resume", 0}, {"after", 3}} {
		for _, field := range []string{"active", "unknown", "systemError", "notLoaded", "sessionId", "cwd", "modelProvider", "createdAt", "ephemeral", "source", "path", "updatedAt", "preview", "metadata", "statusMetadata"} {
			if stage.name == "resume" && field == "updatedAt" {
				// Cold resume may advance the timestamp; regression is still drift.
				field = "updatedAtRegression"
			}
			cases = append(cases, ticket12RejectedCase{"cold:" + stage.name + ":" + field, stage.retained, 1})
		}
	}
	cases = append(cases, ticket12RejectedCase{"cold:after:id", 3, 1})
	for _, tc := range cases {
		t.Run(tc.mode, func(t *testing.T) { ticket12RejectedLoad(t, tc) })
	}
}

type ticket12RejectedCase struct {
	mode              string
	retained, resumes int
}

func ticket12RejectedLoad(t *testing.T, tc ticket12RejectedCase) ([]ticket12Call, map[string]int) {
	t.Helper()
	evidence := filepath.Join(t.TempDir(), "peer.jsonl")
	svc, cwd := ticket06Service(t, tc.mode, map[string]any{"LOAD_EVIDENCE": evidence})
	ref := resource.SessionRef{SessionID: "native-thread"}
	_, err := svc.LoadSession(ctx(t), resource.SessionLoad{Metadata: resource.Metadata{Namespace: "one", Name: "rejected"}, RuntimeRef: "agent", Cwd: cwd, SessionID: ref.SessionID})
	require.NoError(t, err)
	require.EventuallyWithT(t, func(c *assert.CollectT) {
		loaded, err := svc.GetSession(ref)
		if assert.NoError(c, err) {
			assert.Equal(c, "failed", loaded.Status.HistoryLoad.Phase)
			assert.True(c, loaded.Status.HistoryLoad.CleanupCompleted, "last public status: %+v", loaded.Status.HistoryLoad)
		}
	}, 5*time.Second, time.Millisecond)
	loaded, err := svc.GetSession(ref)
	require.NoError(t, err)
	require.NotNil(t, loaded.Status.HistoryLoad.Error)
	require.False(t, loaded.Status.HistoryLoad.NativeCompleted)
	require.False(t, loaded.Status.HistoryLoad.DeliveryCompleted)
	_, err = svc.SendPrompt(ctx(t), ref, runtime.Input{Content: []events.ContentBlock{events.Text("must reject")}})
	require.Error(t, err)
	page, err := svc.ListHistoryEvents(ref, store.HistoryQuery{})
	require.NoError(t, err)
	if tc.retained > 0 {
		require.True(t, loaded.Status.HistoryLoad.NativeVerified)
		require.True(t, loaded.Status.HistoryLoad.NativeResumed)
	}
	require.Len(t, page.Events, tc.retained, "retain only facts accepted before failure")
	_, err = svc.StopRuntime(ctx(t), "agent")
	require.NoError(t, err)
	calls := ticket12Calls(t, evidence)
	counts := map[string]int{}
	for _, call := range calls {
		require.Equal(t, calls[0].PID, call.PID, "original owned peer")
		counts[call.Method]++
	}
	require.Equal(t, tc.resumes, counts["thread/resume"], "no hidden resume retry")
	require.Zero(t, counts["turn/start"], "no hidden or rejected input reaches native")
	require.Equal(t, tc.resumes, counts["thread/unsubscribe"], "release every acquired native binding")
	require.Equal(t, 1, counts["released"])
	require.Eventually(t, func() bool { return errors.Is(syscall.Kill(calls[0].PID, 0), syscall.ESRCH) }, time.Second, time.Millisecond)
	return calls, counts
}
