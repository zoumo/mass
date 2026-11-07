package integration_test

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"

	"github.com/zoumo/mass/mass-go/pkg/events"
	"github.com/zoumo/mass/mass-go/pkg/resource"
	"github.com/zoumo/mass/mass-go/pkg/runtime"
	"github.com/zoumo/mass/mass-go/pkg/store"
)

// Runs against the actual daemon and CLI for both the default SQLite authority
// and explicit file authority. All upstream protocol facts are synthetic.
func (s *entrypointSuite) TestMetadataRestartAndEventRebuild() {
	ref, _, _ := s.open("codex", "basic")
	var receipt runtime.InputReceipt
	// No watch exists: history responsibility must not depend on subscribers.
	s.call("session.input", workflowParams{Session: ref, Input: runtime.Input{Content: []events.ContentBlock{events.Text("hello")}}}, &receipt)
	s.Require().NotEmpty(receipt.Submission)
	s.Require().Eventually(func() bool {
		var page store.HistoryPage
		s.call("session.history", workflowParams{Session: ref, History: store.HistoryQuery{Types: []string{"agent.message"}}}, &page)
		var session resource.Session
		s.call("session.get", workflowParams{Session: ref}, &session)
		return len(page.Events) == 1 && session.Status.State == events.Idle
	}, 3*time.Second, 10*time.Millisecond)
	s.call("session.close", workflowParams{Session: ref}, nil)
	s.call("runtime.stop", workflowParams{Name: "agent"}, nil)

	// A real executable records even an attempted activation. Ordinary resource,
	// history and observation requests must never run it, including after rebuild.
	probe := filepath.Join(s.data, "unexpected-agent")
	s.Require().NoError(os.WriteFile(probe, []byte("#!/bin/sh\n: > \"$0.called\"\nexit 79\n"), 0o700))
	var runtimeBefore resource.Runtime
	s.call("runtime.get", workflowParams{Name: "agent"}, &runtimeBefore)
	spec := runtimeBefore.Spec
	spec.Config["executable"] = probe
	s.call("runtime.update", workflowParams{Name: "agent", Spec: &spec}, nil)
	var before resource.Session
	s.call("session.get", workflowParams{Session: ref}, &before)
	s.call("runtime.get", workflowParams{Name: "agent"}, &runtimeBefore)
	history := s.history(ref)
	s.Require().NotEmpty(history.Events)
	var first store.HistoryPage
	s.call("session.history", workflowParams{Session: ref, History: store.HistoryQuery{Limit: 1}}, &first)
	s.Require().NotEmpty(first.Next)
	var rest store.HistoryPage
	s.call("session.history", workflowParams{Session: ref, History: store.HistoryQuery{After: first.Next}}, &rest)

	for _, rebuild := range []bool{false, true} {
		func() {
			s.restartForHistory(rebuild)
			named := resource.SessionRef{Namespace: before.Metadata.Namespace, Name: before.Metadata.Name}
			// Subscription readiness is established before the queries.
			idWatch, err := newHTTPClient(s.T(), s.url, "").Observe(ctx(s.T()), ref)
			s.Require().NoError(err)
			defer idWatch.Close()
			nameWatch, err := newHTTPClient(s.T(), s.url, "").Observe(ctx(s.T()), named)
			s.Require().NoError(err)
			defer nameWatch.Close()
			// Read during the queries; shutdown is allowed to end a stream without
			// draining its queue, so its terminal frame alone cannot prove no replay.
			idResult, nameResult := make(chan error, 1), make(chan error, 1)
			go func() { _, err := idWatch.Next(); idResult <- err }()
			go func() { _, err := nameWatch.Next(); nameResult <- err }()
			var after resource.Session
			s.call("session.get", workflowParams{Session: named}, &after)
			expected := before
			expected.Status.State = "offline"
			expected.Status.Reason = ""
			originalVersion := expected.Metadata.ResourceVersion
			expected.Metadata.ResourceVersion = ""
			reconciledVersion := after.Metadata.ResourceVersion
			after.Metadata.ResourceVersion = ""
			s.Equal(expected, after, "preserve authority, identity and spec while retaining offline")
			s.Equal(originalVersion, reconciledVersion, "restart must not rewrite an already offline Session")
			var runtimeAfter resource.Runtime
			s.call("runtime.get", workflowParams{Name: "agent"}, &runtimeAfter)
			s.Equal(runtimeBefore, runtimeAfter)
			s.Equal(history, s.history(ref))
			var remaining store.HistoryPage
			s.call("session.history", workflowParams{Session: named, History: store.HistoryQuery{After: first.Next}}, &remaining)
			s.Equal(rest, remaining, "public cursors and whole-session progress survive SQL rebuild")
			// This is bounded external absence evidence. The synchronous registered
			// Handler/Reconcile Service test separately establishes no replay.
			select {
			case err := <-idResult:
				s.FailNow("ID observation delivered or ended during offline queries", "%v", err)
			case err := <-nameResult:
				s.FailNow("name observation delivered or ended during offline queries", "%v", err)
			case <-time.After(100 * time.Millisecond):
			}
			s.stopForHistory()
			err = <-idResult
			var terminal *runtime.OperationError
			s.Require().ErrorAs(err, &terminal, "ID observation must end without historical events")
			s.Equal("unavailable", terminal.Code)
			s.Equal("server_shutdown", terminal.Reason)
			_, again := idWatch.Next()
			s.Same(err, again, "terminal cause remains stable")
			err = <-nameResult
			s.Require().ErrorAs(err, &terminal, "name observation must end without historical events")
			s.Equal("server_shutdown", terminal.Reason)
			s.Require().NoError(idWatch.Close())
			s.Require().NoError(nameWatch.Close())
			_, err = os.Stat(probe + ".called")
			s.True(os.IsNotExist(err), "read/rebuild/subscribe/shutdown must not activate an Agent")
			// The suite owns the final process and verifies its port/data cleanup.
			s.startForHistory()
		}()
	}
	if s.backend == "" {
		for _, name := range []string{"runtime.jsonl", ".store.json", filepath.Join("sessions", "demo", "session", "state.json")} {
			_, err := os.Stat(filepath.Join(s.data, name))
			s.True(os.IsNotExist(err), "SQLite must not require resource file copies: "+name)
		}
	}
	s.T().Log("PASS no-subscriber persistence, restart then SQL rebuild, exact history/cursors/metadata, ID/name offline SSE without replay or Agent activation")
}

func (s *entrypointSuite) stopForHistory() {
	s.Require().NoError(s.daemon.Process.Signal(syscall.SIGTERM))
	s.Require().NoError(s.daemon.Wait(), s.stderr.String())
}

func (s *entrypointSuite) startForHistory() {
	s.daemon = exec.CommandContext(s.daemonCtx, app, "--config", filepath.Join(s.data, "daemon-config.yaml"), "--data", s.data)
	s.stderr.Reset()
	s.daemon.Stderr = &s.stderr
	out, err := s.daemon.StdoutPipe()
	s.Require().NoError(err)
	s.Require().NoError(s.daemon.Start())
	var ready struct {
		URL string `json:"url"`
	}
	s.Require().NoError(json.NewDecoder(out).Decode(&ready))
	s.url = ready.URL
}

func (s *entrypointSuite) restartForHistory(rebuild bool) {
	s.stopForHistory()
	if rebuild {
		s.Require().NoError(os.Remove(filepath.Join(s.data, "history.sqlite")))
	}
	s.startForHistory()
}
