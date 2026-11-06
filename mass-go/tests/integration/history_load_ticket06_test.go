package integration_test

import (
	"context"
	"fmt"
	"maps"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zoumo/mass/mass-go/pkg/events"
	"github.com/zoumo/mass/mass-go/pkg/resource"
	"github.com/zoumo/mass/mass-go/pkg/runtime"
	"github.com/zoumo/mass/mass-go/pkg/runtime/codex"
	"github.com/zoumo/mass/mass-go/pkg/service"
	"github.com/zoumo/mass/mass-go/pkg/store"
	"github.com/zoumo/mass/mass-go/pkg/store/file"
)

var (
	ticket06Build    sync.Once
	ticket06Binary   string
	ticket06BuildErr error
)

func ticket06Peer(t *testing.T) string {
	t.Helper()
	ticket06Build.Do(func() {
		// Only the executable is shared; TestMain owns its lifetime across cases.
		ticket06Binary = filepath.Join(root, "history-peer")
		out, err := exec.CommandContext(ctx(t), "go", "build", "-o", ticket06Binary, "./testdata/history-load-peer").CombinedOutput()
		if err != nil {
			ticket06BuildErr = fmt.Errorf("build peer: %w: %s", err, out)
		}
	})
	require.NoError(t, ticket06BuildErr)
	return ticket06Binary
}

func ticket06Service(t *testing.T, mode string, extraEnv ...map[string]any) (*service.Service, string) {
	t.Helper()
	return ticket06ServiceWithPeer(t, ticket06Peer(t), mode, extraEnv...)
}

func ticket06ServiceWithPeer(t *testing.T, bin, mode string, extraEnv ...map[string]any) (*service.Service, string) {
	t.Helper()
	cwd, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	st, err := file.Open(t.TempDir(), false)
	require.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, st.Close()) })
	svc := service.New(st, st, runtime.NewRegistry(map[string]runtime.Factory{"codex": codex.Factory{RecoveryDir: t.TempDir()}}))
	t.Cleanup(func() {
		c, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		assert.NoError(t, svc.Shutdown(c))
	})
	env := map[string]any{"LOAD_CWD": cwd, "LOAD_MODE": mode}
	for _, extra := range extraEnv {
		maps.Copy(env, extra)
	}
	_, err = svc.CreateRuntime(resource.Runtime{Metadata: resource.Metadata{Name: "agent"}, Spec: resource.RuntimeSpec{Type: "codex", Config: runtime.Config{"executable": bin, "envPolicy": "replace", "env": env}}})
	require.NoError(t, err)
	_, err = svc.StartRuntime(ctx(t), "agent")
	require.NoError(t, err)
	return svc, cwd
}

func TestTicket06CodexPagedLoadAndSameConnectionInput(t *testing.T) {
	svc, cwd := ticket06Service(t, "")
	receipt, err := svc.LoadSession(ctx(t), resource.SessionLoad{Metadata: resource.Metadata{Namespace: "one", Name: "imported"}, RuntimeRef: "agent", Cwd: cwd, SessionID: "native-thread"})
	require.NoError(t, err)
	ref := resource.SessionRef{SessionID: "native-thread"}
	require.Eventually(t, func() bool { r, e := svc.GetSession(ref); return e == nil && r.Status.HistoryLoad.Phase == "completed" }, 5*time.Second, time.Millisecond, "load %s never completed", receipt.LoadID)
	page, err := svc.ListHistoryEvents(ref, store.HistoryQuery{})
	require.NoError(t, err)
	require.Len(t, page.Events, 3)
	require.Equal(t, []string{"first", "answer", "last"}, []string{page.Events[0].ItemID, page.Events[1].ItemID, page.Events[2].ItemID})
	require.Equal(t, "turn-z", page.Events[0].Replay.NativeParentID)
	require.Equal(t, "item/+:next", page.Events[1].Meta["pagination"].(map[string]any)["itemCursor"])
	require.Equal(t, "turn/+:next", page.Events[2].Meta["pagination"].(map[string]any)["turnCursor"])
	require.Equal(t, "asc", page.Events[2].Meta["pagination"].(map[string]any)["direction"])
	_, err = svc.SendPrompt(ctx(t), ref, runtime.Input{Content: []events.ContentBlock{events.Text("continue")}})
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		p, e := svc.ListHistoryEvents(ref, store.HistoryQuery{})
		return e == nil && len(p.Events) == 4
	}, time.Second, time.Millisecond)
	_, err = svc.CancelHistoryLoad(ctx(t), ref, receipt.LoadID)
	require.Error(t, err, "stale cancellation must not close continued work")
}

func TestTicket06RetainsFiveContentKindsAndToolOutputPresence(t *testing.T) {
	svc, cwd := ticket06Service(t, "rich")
	ref := resource.SessionRef{SessionID: "native-thread"}
	_, err := svc.LoadSession(ctx(t), resource.SessionLoad{Metadata: resource.Metadata{Namespace: "one", Name: "rich"}, RuntimeRef: "agent", Cwd: cwd, SessionID: ref.SessionID})
	require.NoError(t, err)
	require.Eventually(t, func() bool { r, e := svc.GetSession(ref); return e == nil && r.Status.HistoryLoad.Phase == "completed" }, 5*time.Second, time.Millisecond)
	page, err := svc.ListHistoryEvents(ref, store.HistoryQuery{})
	require.NoError(t, err)
	require.Len(t, page.Events, 15)
	kinds := map[string]bool{}
	calls := map[string]events.ToolCall{}
	results := map[string]events.ToolResult{}
	for _, e := range page.Events {
		kinds[e.Type()] = true
		switch p := e.Payload().(type) {
		case events.ToolCall:
			calls[p.ID] = p
		case events.ToolResult:
			results[p.ID] = p
		}
	}
	require.Len(t, kinds, 5)
	require.Contains(t, calls, "shell")
	require.Equal(t, "ok", *results["shell"].RawOutput)
	require.Nil(t, calls["no-output"].RawOutput)
	require.NotContains(t, results, "no-output")
	require.NotNil(t, calls["null-output"].RawOutput)
	require.Nil(t, *calls["null-output"].RawOutput)
	require.Empty(t, *results["empty-output"].RawOutput)
	require.Equal(t, "truncated", results["truncated-output"].Output.Completeness)
	require.Equal(t, "partial", *results["truncated-output"].RawOutput)
	require.Contains(t, results, "mcp")
	require.Contains(t, results, "dynamic")
	require.Equal(t, "retained thought", page.Events[2].Payload().(events.AgentThinking).Content[0].Text)
	require.Equal(t, []any{"separate summary"}, page.Events[2].Meta["native"].(map[string]any)["item"].(map[string]any)["summary"])
}

func TestTicket06HistoryFaultsNeverAuthorizeInputOrRetry(t *testing.T) {
	for _, tc := range []struct {
		mode     string
		retained int
	}{{"identity", 0}, {"resume-drift", 0}, {"summary", 0}, {"valid-unsupported", 2}, {"unsupported", 2}, {"parent", 2}, {"drift", 3}, {"omitted", 3}, {"loop", 4}} {
		t.Run(tc.mode, func(t *testing.T) {
			svc, cwd := ticket06Service(t, tc.mode)
			ref := resource.SessionRef{SessionID: "native-thread"}
			input := resource.SessionLoad{Metadata: resource.Metadata{Namespace: "one", Name: "failed"}, RuntimeRef: "agent", Cwd: cwd, SessionID: ref.SessionID}
			_, err := svc.LoadSession(ctx(t), input)
			require.NoError(t, err)
			require.Eventually(t, func() bool {
				r, e := svc.GetSession(ref)
				return e == nil && r.Status.HistoryLoad.Phase == "failed" && r.Status.HistoryLoad.CleanupCompleted
			}, 5*time.Second, time.Millisecond)
			r, err := svc.GetSession(ref)
			require.NoError(t, err)
			require.False(t, r.Status.HistoryLoad.DeliveryCompleted)
			require.NotNil(t, r.Status.HistoryLoad.Error)
			page, err := svc.ListHistoryEvents(ref, store.HistoryQuery{})
			require.NoError(t, err)
			require.Len(t, page.Events, tc.retained)
			_, err = svc.SendPrompt(ctx(t), ref, runtime.Input{Content: []events.ContentBlock{events.Text("must reject")}})
			require.Error(t, err)
			input.Metadata.Name = "replacement"
			_, err = svc.LoadSession(ctx(t), input)
			require.Error(t, err, "failed load retains its global native ID reservation")
		})
	}
}

func TestTicket06PreservesRepeatedNativeIDsInSourceOrder(t *testing.T) {
	svc, cwd := ticket06Service(t, "repeat")
	ref := resource.SessionRef{SessionID: "native-thread"}
	_, err := svc.LoadSession(ctx(t), resource.SessionLoad{Metadata: resource.Metadata{Namespace: "one", Name: "repeated"}, RuntimeRef: "agent", Cwd: cwd, SessionID: ref.SessionID})
	require.NoError(t, err)
	require.Eventually(t, func() bool { r, e := svc.GetSession(ref); return e == nil && r.Status.HistoryLoad.Phase == "completed" }, 5*time.Second, time.Millisecond)
	page, err := svc.ListHistoryEvents(ref, store.HistoryQuery{})
	require.NoError(t, err)
	require.Len(t, page.Events, 3)
	require.Equal(t, []string{"first", "answer", "first"}, []string{page.Events[0].ItemID, page.Events[1].ItemID, page.Events[2].ItemID})
	require.NotEqual(t, page.Events[0].EventID, page.Events[2].EventID)
	require.Equal(t, "turn-a", page.Events[2].Replay.NativeParentID)
}
