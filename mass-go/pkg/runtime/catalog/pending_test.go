package catalog_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/zoumo/mass/mass-go/internal/testutil"
	rt "github.com/zoumo/mass/mass-go/pkg/runtime"
	"github.com/zoumo/mass/mass-go/pkg/runtime/catalog"
)

func (s *lifecycleSuite) TestPendingRequiresExplicitInput() {
	config, _ := s.config("claude")
	registry, runtime := s.register("claude", config)
	ctx := context.Background()
	op, err := runtime.CreateSession(ctx, rt.SessionOptions{})
	s.Require().NoError(err)
	s.Equal("awaiting_input", op.Phase)
	s.True(op.InputRequired)
	s.Equal("unknown", op.Evidence.NativeIdentity.State)
	s.Nil(op.Key)
	s.Empty(runtime.ListSessions())
	for _, id := range []string{"", op.OperationRef, "different-message-id"} {
		_, err := runtime.GetSession(rt.SessionKey{RuntimeName: "worker", SessionID: id})
		s.ErrorIs(err, rt.ErrNotFound)
	}
	s.ErrorIs(registry.Unregister("worker"), rt.ErrConflict)
	_, err = runtime.ContinueCreation(ctx, op.OperationRef, "hello")
	s.Require().NoError(err)
	_, err = runtime.ContinueCreation(ctx, op.OperationRef, "hello")
	s.ErrorIs(err, rt.ErrConflict)
	opened := s.waitOperation(runtime, op.OperationRef, "opened")
	s.Require().NotNil(opened.Key)
	s.Equal("native-A", opened.Key.SessionID)
	s.Equal(rt.Known(true), opened.Evidence.NativeIdentity)
	s.Equal(rt.Known(true), opened.Evidence.InitialInputWritten)
	session, err := runtime.GetSession(*opened.Key)
	s.Require().NoError(err)
	s.readReply(ctx, session)
}

func (s *lifecycleSuite) register(kind string, config rt.Config) (*catalog.Catalog, rt.Runtime) {
	s.T().Helper()
	registry := catalog.New()
	_, err := registry.Register(rt.Registration{Name: "worker", Type: kind, Config: config})
	s.Require().NoError(err)
	worker, err := registry.Get("worker")
	s.Require().NoError(err)
	s.Require().NoError(worker.Start(context.Background()))
	s.T().Cleanup(func() { s.NoError(worker.Stop()) })
	return registry, worker
}
func (s *lifecycleSuite) waitOperation(worker rt.Runtime, ref, phase string) rt.CreationOperation {
	return testutil.WaitCreation(s.T(), worker, ref, phase)
}

func (s *lifecycleSuite) TestLateCreationOwnership() {
	for _, kind := range []string{"codex", "claude"} {
		for _, cancelOperation := range []bool{false, true} {
			s.Run(fmt.Sprintf("%s/cancel=%v", kind, cancelOperation), func() {
				var frames []string
				if kind == "codex" {
					frames = append(codexInit(), codexThread(2, "late-native")...)
					frames[4] = "delay=200 " + frames[4]
				} else {
					frames = claudeFrames("late-native", false)
					frames[3] = "delay=200 " + frames[3]
				}
				config := s.singleScenario(kind, frames)
				wrapper, exited := testutil.TrackProcess(s.T(), config.Executable)
				config.Executable = wrapper
				_, worker := s.register(kind, config)
				ctx, cancel := context.WithCancel(context.Background())
				options := rt.SessionOptions{}
				text := "hello"
				if kind == "claude" {
					options.InitialInput = &text
				}
				op, err := worker.CreateSession(ctx, options)
				s.Require().NoError(err)
				cancel() // Ending this caller wait must not abandon or duplicate native creation.
				s.Nil(op.Key)
				if cancelOperation {
					result, err := worker.CancelOperation(context.Background(), op.OperationRef)
					s.Require().NoError(err)
					s.Equal("cleaning_up", result.Phase)
					result = s.waitOperation(worker, op.OperationRef, "closed")
					s.Nil(result.Key)
					s.Empty(worker.ListSessions())
					s.Contains(result.CleanupState, "local_resources_released")
					if kind == "claude" {
						exited()
					}
				} else {
					result := s.waitOperation(worker, op.OperationRef, "opened")
					s.Require().NotNil(result.Key)
					s.Equal("late-native", result.Key.SessionID)
					s.Len(worker.ListSessions(), 1)
					// Snapshot mutation cannot rebind the owned operation or session.
					result.Key.SessionID = "caller-modified"
					again, err := worker.GetOperation(op.OperationRef)
					s.Require().NoError(err)
					s.Equal("late-native", again.Key.SessionID)
				}
				s.NoError(worker.Stop())
				exited()
			})
		}
	}
}
func (s *lifecycleSuite) singleScenario(kind string, frames []string) rt.Config {
	path := s.scenario(kind, frames)
	key := "CODEX_MOCK_SCENARIO="
	if kind == "claude" {
		key = "CLAUDE_MOCK_SCENARIO="
	}
	return rt.Config{Executable: s.executables[kind], Env: []string{key + path}}
}
func (s *lifecycleSuite) TestReversedPendingConnections() {
	dir := s.T().TempDir()
	a := claudeFrames("native-A", false)
	first := s.scenario("claude", a)
	second := s.scenario("claude", claudeFrames("native-B", false))
	wrapper := filepath.Join(dir, "mock")
	gate := filepath.Join(dir, "release-first")
	script := "#!/bin/sh\nif mkdir " + quote(filepath.Join(dir, "first")) + " 2>/dev/null; then\nwhile [ ! -f " + quote(gate) + " ]; do /bin/sleep 0.01; done\nexport CLAUDE_MOCK_SCENARIO=" + quote(first) + "\nelse\nexport CLAUDE_MOCK_SCENARIO=" + quote(second) + "\nfi\nexec " + quote(s.executables["claude"]) + " \"$@\"\n"
	s.Require().NoError(os.WriteFile(wrapper, []byte(script), 0700))
	_, worker := s.register("claude", rt.Config{Executable: wrapper, Env: []string{}})
	opA, err := worker.CreateSession(context.Background(), rt.SessionOptions{})
	s.Require().NoError(err)
	opB, err := worker.CreateSession(context.Background(), rt.SessionOptions{})
	s.Require().NoError(err)
	_, err = worker.ContinueCreation(context.Background(), opA.OperationRef, "hello")
	s.Require().NoError(err)
	s.Require().Eventually(func() bool { _, err := os.Stat(filepath.Join(dir, "first")); return err == nil }, 6*time.Second, time.Millisecond)
	_, err = worker.ContinueCreation(context.Background(), opB.OperationRef, "hello")
	s.Require().NoError(err)
	b := s.waitOperation(worker, opB.OperationRef, "opened")
	s.Equal("native-B", b.Key.SessionID)
	aPending, err := worker.GetOperation(opA.OperationRef)
	s.Require().NoError(err)
	s.Nil(aPending.Key)
	s.Require().NoError(os.WriteFile(gate, []byte("release"), 0600))
	aDone := s.waitOperation(worker, opA.OperationRef, "opened")
	s.Equal("native-A", aDone.Key.SessionID)
	for _, op := range []rt.CreationOperation{aDone, b} {
		session, err := worker.GetSession(*op.Key)
		s.Require().NoError(err)
		s.readReply(context.Background(), session)
	}
}
func (s *lifecycleSuite) TestEarlyFactsAndImmutableRootIdentity() {
	frames := claudeFrames("root", false)
	frames = append(frames[:3], append([]string{
		`server protocol {"type":"system","subtype":"init","session_id":"child-before","agent_id":"child-agent","parent_tool_use_id":"spawn"}`,
		`server protocol {"type":"assistant","session_id":"message-group","message":{"id":"message-id","role":"assistant","content":[{"type":"text","text":"early"}]}}`,
	}, frames[3:]...)...)
	frames = append(frames,
		`server protocol {"type":"system","subtype":"init","session_id":"child-after","agent_id":"child-agent","parent_tool_use_id":"spawn"}`,
		`server protocol {"type":"result","subtype":"success","session_id":"child-result","agent_id":"child-agent","parent_tool_use_id":"spawn","is_error":false}`,
		`server protocol {"type":"system","subtype":"init","session_id":"different-root"}`)
	_, worker := s.register("claude", s.singleScenario("claude", frames))
	text := "hello"
	session := testutil.OpenSession(s.T(), worker, context.Background(), rt.SessionOptions{InitialInput: &text})
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	seenEarly, seenDifferent := false, false
	var early rt.Event
	for !seenDifferent {
		event, err := session.Next(ctx)
		s.Require().NoError(err)
		s.Equal("root", event.SessionID)
		if testutil.MessageText(event) == "early" {
			seenEarly = true
			early = event
			s.False(event.ObservedAt.IsZero())
		}
		if event.Kind == rt.SessionReady {
			s.Less(early.SourcePosition, event.SourcePosition)
			s.True(early.ObservedAt.Before(event.ObservedAt))
		}
		if event.Diagnostic.NativeKind == "identity_mismatch" {
			seenDifferent = true
		}
	}
	s.True(seenEarly)
	s.Equal("root", session.Key().SessionID)
	s.Len(worker.ListSessions(), 1)
	data, err := json.Marshal(session.Key())
	s.Require().NoError(err)
	s.JSONEq(`{"runtimeName":"worker","sessionId":"root"}`, string(data))
}

func (s *lifecycleSuite) TestProvisionalBudgetsReleaseOwnedProcess() {
	for _, tc := range []struct {
		name    string
		limits  rt.CreationLimits
		early   []string
		failure string
	}{
		{"count", rt.CreationLimits{Facts: 1}, []string{
			`server protocol {"type":"assistant","message":{"role":"assistant","content":"first"}}`,
			`server protocol {"type":"assistant","message":{"role":"assistant","content":"second"}}`,
		}, "incomplete_stream"},
		{"bytes", rt.CreationLimits{Bytes: 1}, []string{`server protocol {"type":"assistant","message":{"role":"assistant","content":"too many bytes"}}`}, "incomplete_stream"},
		{"permission_bytes", rt.CreationLimits{Bytes: 1, Timeout: 2 * time.Second}, []string{
			`server control {"type":"control_request","request_id":"early-permission","request":{"subtype":"can_use_tool","tool_name":"Read","tool_use_id":"tool-1","input":{}}}`,
			`client control {"type":"control_response","response":{"subtype":"success","request_id":"early-permission","response":{"behavior":"deny","message":""}}}`,
		}, "incomplete_stream"},
		{"deadline", rt.CreationLimits{Timeout: time.Second}, nil, "outcome_unknown"},
	} {
		s.Run(tc.name, func() {
			frames := claudeFrames("late-root", false)
			frames[3] = "delay=1500 " + frames[3]
			frames = append(frames[:3], append(tc.early, frames[3:]...)...)
			config := s.singleScenario("claude", frames)
			config.CreationLimits = tc.limits
			wrapper, exited := testutil.TrackProcess(s.T(), config.Executable)
			config.Executable = wrapper
			_, worker := s.register("claude", config)
			text := "hello"
			op, err := worker.CreateSession(context.Background(), rt.SessionOptions{InitialInput: &text})
			s.Require().NoError(err)
			result := s.waitOperation(worker, op.OperationRef, "outcome_unknown")
			s.Equal(tc.failure, result.Failure)
			s.Nil(result.Key)
			s.Empty(worker.ListSessions())
			s.Contains(result.CleanupState, "local_resources_released")
			exited()
			_, err = worker.ContinueCreation(context.Background(), op.OperationRef, "hello")
			s.ErrorIs(err, rt.ErrConflict)
		})
	}
}
func (s *lifecycleSuite) TestCancelAwaitingInputDoesNotStartProcess() {
	config, pids := s.config("claude")
	_, worker := s.register("claude", config)
	op, err := worker.CreateSession(context.Background(), rt.SessionOptions{})
	s.Require().NoError(err)
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = worker.CancelOperation(canceled, op.OperationRef)
	s.ErrorIs(err, context.Canceled)
	result, err := worker.CancelOperation(context.Background(), op.OperationRef)
	s.Require().NoError(err)
	s.Equal("closed", result.Phase)
	s.Nil(result.Key)
	s.Empty(worker.ListSessions())
	for _, pid := range pids {
		_, err := os.Stat(pid)
		s.True(os.IsNotExist(err))
	}
	_, err = worker.ContinueCreation(context.Background(), op.OperationRef, "hello")
	s.ErrorIs(err, rt.ErrConflict)
}

func (s *lifecycleSuite) TestAmbiguousNativeOpenIsNotRetried() {
	frames := append(codexInit(), codexThread(2, "too-late")...)
	frames[4] = "delay=250 " + frames[4]
	config := s.singleScenario("codex", frames)
	config.CreationLimits.Timeout = 50 * time.Millisecond
	registry, worker := s.register("codex", config)
	op, err := worker.CreateSession(context.Background(), rt.SessionOptions{})
	s.Require().NoError(err)
	result := s.waitOperation(worker, op.OperationRef, "outcome_unknown")
	s.Nil(result.Key)
	s.Empty(worker.ListSessions())
	// The pinned SDK discards late replies to locally expired calls.
	time.Sleep(300 * time.Millisecond)
	result, err = worker.GetOperation(op.OperationRef)
	s.Require().NoError(err)
	s.Equal("outcome_unknown", result.Phase)
	s.Nil(result.Key)
	s.Empty(worker.ListSessions())
	s.NoError(worker.Stop())
	s.ErrorIs(registry.Unregister("worker"), rt.ErrConflict)
	_, err = registry.UpdateConfig("worker", config, 1)
	s.ErrorIs(err, rt.ErrConflict)
}
