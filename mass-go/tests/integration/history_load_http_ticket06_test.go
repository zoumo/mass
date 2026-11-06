package integration_test

import (
	"bytes"
	"encoding/json"
	"net/http/httptest"
	"os/exec"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/zoumo/mass/mass-go/pkg/events"
	"github.com/zoumo/mass/mass-go/pkg/resource"
	"github.com/zoumo/mass/mass-go/pkg/runtime"
	"github.com/zoumo/mass/mass-go/pkg/store"
	"github.com/zoumo/mass/mass-go/pkg/transport"
)

func TestTicket06CLILoadHTTPHistoryAndFutureSSE(t *testing.T) {
	svc, cwd := ticket06Service(t, "")
	handler, err := transport.New(svc, transport.Options{Token: "load-token"})
	require.NoError(t, err)
	defer handler.Shutdown()
	server := httptest.NewServer(handler)
	defer server.Close()
	// This is the built CLI against the real HTTP/Service/SDK synthetic peer.
	cmd := exec.CommandContext(ctx(t), ctl, "--url", server.URL, "-o", "json", "session", "load", "imported", "-n", "one", "--runtime", "agent", "--cwd", cwd, "--session-id", "native-thread")
	cmd.Env = []string{"MASS_TOKEN=load-token"}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	require.NoError(t, err, stderr.String())
	var accepted resource.SessionLoadAccepted
	require.NoError(t, json.Unmarshal(out, &accepted), string(out))
	require.NotEmpty(t, accepted.LoadID)
	require.Equal(t, "native-thread", accepted.Session.Status.SessionID)
	c := newHTTPClient(t, server.URL, "load-token")
	ref := resource.SessionRef{Namespace: "one", Name: "imported"}
	require.Eventually(t, func() bool {
		var r resource.Session
		return c.GetSession(ctx(t), ref, &r) == nil && r.Status.HistoryLoad.Phase == "completed" && r.Status.HistoryLoad.NativeVerified && r.Status.HistoryLoad.NativeResumed && r.Status.HistoryLoad.NativeCompleted && r.Status.HistoryLoad.DeliveryCompleted && !r.Status.HistoryLoad.CleanupCompleted
	}, 5*time.Second, time.Millisecond)
	// A late observer reads committed history independently; SSE is future-only.
	var page store.HistoryPage
	require.NoError(t, c.History(ctx(t), ref, store.HistoryQuery{Limit: 2}, &page))
	require.Len(t, page.Events, 2)
	require.Equal(t, "first", page.Events[0].Replay.NativeRecordID)
	require.NotEmpty(t, page.Next)
	var tail store.HistoryPage
	require.NoError(t, c.History(ctx(t), ref, store.HistoryQuery{After: page.Next}, &tail))
	require.Len(t, tail.Events, 1)
	require.Equal(t, "last", tail.Events[0].ItemID)
	observer, err := c.Observe(ctx(t), ref)
	require.NoError(t, err)
	defer observer.Close()
	var receipt runtime.InputReceipt
	require.NoError(t, c.SendPrompt(ctx(t), ref, runtime.Input{Content: []events.ContentBlock{events.Text("continue retained native session")}}, &receipt))
	require.Equal(t, "continued", receipt.Submission)
	for {
		e, nextErr := observer.Next()
		require.NoError(t, nextErr)
		if p, ok := e.Payload().(events.AgentMessage); ok {
			require.Nil(t, e.Replay, "current content must not gain old replay authority")
			require.Equal(t, "same resumed connection", p.Content[0].Text)
			break
		}
	}
	var canceled resource.HistoryLoadStatus
	require.Error(t, c.CancelHistoryLoad(ctx(t), ref, accepted.LoadID, &canceled), "stale load cancel must not close retained live connection")
}
