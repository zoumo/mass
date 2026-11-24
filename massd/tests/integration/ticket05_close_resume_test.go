package integration_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/zoumo/mass/massd/pkg/events"
	rpcwire "github.com/zoumo/mass/massd/pkg/jsonrpc"
	"github.com/zoumo/mass/massd/pkg/resource"
	"github.com/zoumo/mass/massd/pkg/runtime"
)

func (s *entrypointSuite) TestTicket05CloseResumeAndRestart() {
	t := s.T()
	ref, _, _ := s.open("codex", "close-resume")
	s.ticket05Input(ref, "hello")

	var closed resource.Session
	s.call("session.close", workflowParams{Session: ref}, &closed)
	s.Equal(events.SessionState("offline"), closed.Status.State)
	s.Equal(ref.SessionID, closed.Status.SessionID)

	var runtimeAfterClose resource.Runtime
	s.call("runtime.get", workflowParams{Name: "agent"}, &runtimeAfterClose)
	s.Equal("running", runtimeAfterClose.Status.State, "close must not stop the owning Runtime")

	raw, err := exec.CommandContext(ctx(s.T()), ctl, "--url", s.url, "-o", "json", "session", "prompt", ref.SessionID, "--text", "blocked").CombinedOutput()
	require.Error(t, err, "prompt after close must fail")
	require.Contains(t, string(raw), `"reason":"session_resume_required"`)
	require.Contains(t, string(raw), "resume")

	raw, err = exec.CommandContext(ctx(s.T()), ctl, "--url", s.url, "-o", "json", "session", "archive", ref.SessionID).CombinedOutput()
	require.Error(t, err, "archive command must not exist")
	require.NotEmpty(t, raw)

	err = workflowHTTP(newHTTPClient(s.T(), s.url, ""), ctx(s.T()), "session.list", workflowParams{SessionQuery: resource.SessionQuery{State: "archived"}}, nil)
	var rpcErr *rpcwire.RPCError
	require.ErrorAs(t, err, &rpcErr)
	require.Equal(t, int64(-32602), rpcErr.Code)

	var resumed resource.Session
	s.call("session.resume", workflowParams{Session: ref}, &resumed)
	s.Equal(ref.SessionID, resumed.Status.SessionID)
	s.Equal(closed.Metadata.UID, resumed.Metadata.UID)
	s.ticket05Input(ref, "continue")
	s.call("session.close", workflowParams{Session: ref}, &closed)
	s.Equal(events.SessionState("offline"), closed.Status.State)

	probe := filepath.Join(s.data, "must-not-activate")
	require.NoError(t, os.WriteFile(probe, []byte("#!/bin/sh\n: > \"$0.called\"\nexit 79\n"), 0o700))
	runtimeAfterClose.Spec.Config["executable"] = probe
	s.call("runtime.update", workflowParams{Name: "agent", Spec: &runtimeAfterClose.Spec}, nil)

	history := s.history(ref)
	s.stopForHistory()
	s.startForHistory()

	var reopened resource.Session
	s.call("session.get", workflowParams{Session: ref}, &reopened)
	s.Equal(events.SessionState("offline"), reopened.Status.State)
	s.Equal(closed.Metadata.UID, reopened.Metadata.UID)
	s.Equal(closed.Metadata.CreatedAt, reopened.Metadata.CreatedAt)
	s.Equal(closed.Metadata.Generation, reopened.Metadata.Generation)
	s.Equal(closed.Spec, reopened.Spec)
	s.Equal(ref.SessionID, reopened.Status.SessionID)
	s.Equal(history, s.history(ref), "restart must preserve committed history and synchronization position")

	var runtimeAfterRestart resource.Runtime
	s.call("runtime.get", workflowParams{Name: "agent"}, &runtimeAfterRestart)
	s.Equal("stopped", runtimeAfterRestart.Status.State)
	_, err = os.Stat(probe + ".called")
	s.True(os.IsNotExist(err), "startup must not activate the Runtime or Session")
}

func (s *entrypointSuite) ticket05Input(ref resource.SessionRef, text string) {
	before := len(s.history(ref).Events)
	s.call("session.input", workflowParams{Session: ref, Input: runtime.Input{Content: []events.ContentBlock{events.Text(text)}}}, nil)
	s.Require().Eventually(func() bool {
		var current resource.Session
		s.call("session.get", workflowParams{Session: ref}, &current)
		return current.Status.State == events.Idle && len(s.history(ref).Events) > before
	}, 5*time.Second, 10*time.Millisecond)
}
