package catalog_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/zoumo/mass/mass-go/internal/testutil"
	rt "github.com/zoumo/mass/mass-go/pkg/runtime"
	"github.com/zoumo/mass/mass-go/pkg/runtime/catalog"
)

func permissionFrame(kind, id, session string) string {
	if kind == "codex" {
		return fmt.Sprintf(`s2c {"id":%s,"method":"item/commandExecution/requestApproval","params":{"threadId":%q,"turnId":"different-work-id","itemId":"tool","startedAtMs":1,"command":"synthetic"}}`, id, session)
	}
	return fmt.Sprintf(`server control {"type":"control_request","request_id":%s,"request":{"subtype":"can_use_tool","tool_name":"Read","tool_use_id":"tool","input":{}}}`, id)
}
func (s *lifecycleSuite) interactionSession(kind string, extra []string, expectedWriteFailure ...bool) (rt.Runtime, rt.Session, context.Context) {
	var frames []string
	if kind == "codex" {
		frames = append(codexInit(), codexThread(2, "native-A")...)
		frames = append(frames, codexTurn(3, "native-A")...)
	} else {
		frames = claudeFrames("native-A", false)
	}
	frames = append(frames, extra...)
	config := s.singleScenario(kind, frames)
	var worker rt.Runtime
	if len(expectedWriteFailure) > 0 && expectedWriteFailure[0] {
		wrapper, exited := testutil.TrackProcess(s.T(), config.Executable)
		config.Executable = wrapper
		registry := catalog.New()
		_, err := registry.Register(rt.Registration{Name: "worker", Type: kind, Config: config})
		s.Require().NoError(err)
		worker, err = registry.Get("worker")
		s.Require().NoError(err)
		s.Require().NoError(worker.Start(context.Background()))
		s.T().Cleanup(func() {
			err := worker.Stop()
			if kind == "claude" {
				if err != nil {
					s.ErrorContains(err, "claude runtime close failed")
				}
			} else {
				s.NoError(err)
			}
			exited()
		})
	} else {
		_, worker = s.register(kind, config)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	s.T().Cleanup(cancel)
	opts := rt.SessionOptions{}
	if kind == "claude" {
		text := "hello"
		opts.InitialInput = &text
	}
	session := testutil.OpenSession(s.T(), worker, ctx, opts)
	if kind == "codex" {
		_, err := session.Submit(ctx, rt.TextInput("hello"))
		s.Require().NoError(err)
	}
	return worker, session, ctx
}
func (s *lifecycleSuite) nextInteraction(session rt.Session, ctx context.Context) rt.Event {
	for {
		e, err := session.Next(ctx)
		s.Require().NoError(err)
		if e.Kind == rt.InteractionRequested {
			return e
		}
	}
}
func (s *lifecycleSuite) TestInteractionAuthorityScopeAndSingleAttempt() {
	for _, kind := range []string{"codex", "claude"} {
		s.Run(kind, func() {
			answer := `c2s {"id":"same","result":{"decision":"accept"}}`
			if kind == "claude" {
				answer = `client control {"type":"control_response","response":{"subtype":"success","request_id":"same","response":{"behavior":"allow","updatedInput":{}}}}`
			}
			worker, session, ctx := s.interactionSession(kind, []string{permissionFrame(kind, `"same"`, "native-A"), answer})
			e := s.nextInteraction(session, ctx)
			wire, err := json.Marshal(e)
			s.Require().NoError(err)
			s.NotContains(string(wire), `"same"`)
			var replay rt.Event
			s.Require().NoError(json.Unmarshal(wire, &replay))
			s.Equal(e.Interaction, replay.Interaction)
			ref := replay.Interaction.Ref
			_, err = worker.Respond(ctx, rt.SessionKey{RuntimeName: "wrong", SessionID: "native-A"}, ref, rt.Response{Decision: "allow_once"})
			s.Error(err)
			cancelled, cancel := context.WithCancel(ctx)
			cancel()
			_, err = worker.Respond(cancelled, session.Key(), ref, rt.Response{Decision: "allow_once"})
			s.ErrorIs(err, context.Canceled)
			for _, response := range []rt.Response{{Decision: "allow_always"}, {Decision: "allow_once", Answers: map[string]rt.Answer{"invented": {}}}} {
				r, err := session.Respond(ctx, ref, response)
				s.Error(err)
				s.Equal(rt.Known(false), r.HandedToSDK)
			}
			// Public edits and copied references cannot alter the retained specification.
			e.Interaction.Decisions[0] = "allow_always"
			r, err := session.Respond(ctx, ref, rt.Response{Decision: "allow_always"})
			s.Error(err)
			s.Equal(rt.Known(false), r.HandedToSDK)
			var wg sync.WaitGroup
			results := make(chan rt.ResponseReceipt, 2)
			for range 2 {
				wg.Go(func() {
					r, _ := worker.Respond(ctx, session.Key(), ref, rt.Response{Decision: "allow_once"})
					results <- r
				})
			}
			wg.Wait()
			close(results)
			winners := 0
			for r := range results {
				if r.Outcome == "written" {
					winners++
					s.Equal(rt.Known(true), r.HandedToSDK)
					s.Equal(rt.Known(true), r.WriteCompleted)
					s.Equal("unknown", r.NativeResolution.State)
				} else {
					s.Equal(rt.Known(false), r.HandedToSDK)
				}
			}
			s.Equal(1, winners)
		})
	}
}
func (s *lifecycleSuite) TestObservedInteractionInvalidationBeforeAndAfterDequeue() {
	for _, kind := range []string{"codex", "claude"} {
		for _, before := range []bool{true, false} {
			s.Run(fmt.Sprintf("%s/before=%v", kind, before), func() {
				resolution := `s2c {"method":"serverRequest/resolved","params":{"threadId":"native-A","requestId":"same"}}`
				terminal := codexFinished("different-work-id", "completed")
				if kind == "claude" {
					resolution = `server control {"type":"control_cancel_request","request_id":"same"}`
					terminal = `server protocol {"type":"result","subtype":"success","session_id":"native-A","is_error":false}`
				}
				resolution = "delay=1000 " + resolution
				_, session, ctx := s.interactionSession(kind, []string{permissionFrame(kind, `"same"`, "native-A"), resolution, terminal})
				s.Require().Eventually(func() bool { return session.Snapshot().Interactions.Pending == 1 }, 800*time.Millisecond, time.Millisecond)
				var e rt.Event
				if !before {
					e = s.nextInteraction(session, ctx)
				}
				s.Require().Eventually(func() bool { return session.Snapshot().Foreground.State == "idle" }, 4*time.Second, time.Millisecond)
				if before {
					e = s.nextInteraction(session, ctx)
				}
				r, err := session.Respond(ctx, e.Interaction.Ref, rt.Response{Decision: "allow_once"})
				s.Error(err)
				s.Equal(rt.Known(false), r.HandedToSDK)
			})
		}
	}
}
func (s *lifecycleSuite) TestCloseAndNewEpochCannotReplayInteraction() {
	for _, kind := range []string{"codex", "claude"} {
		s.Run(kind, func() {
			pendingAnswer := `c2s {"id":"same","result":{"decision":"decline"}}`
			if kind == "claude" {
				pendingAnswer = `client control {"type":"control_response","response":{"subtype":"success","request_id":"same","response":{"behavior":"deny"}}}`
			}
			_, old, ctx := s.interactionSession(kind, []string{permissionFrame(kind, `"same"`, "native-A"), pendingAnswer})
			e := s.nextInteraction(old, ctx)
			s.Require().NoError(old.Close())
			_, fresh, freshCtx := s.interactionSession(kind, []string{permissionFrame(kind, `"same"`, "native-A"), pendingAnswer})
			newer := s.nextInteraction(fresh, freshCtx)
			s.NotEqual(e.ConnectionEpoch, newer.ConnectionEpoch)
			s.NotEqual(e.Interaction.Ref, newer.Interaction.Ref)
			for _, session := range []rt.Session{old, fresh} {
				r, err := session.Respond(freshCtx, e.Interaction.Ref, rt.Response{Decision: "allow_once"})
				s.Error(err)
				s.Equal(rt.Known(false), r.HandedToSDK)
			}
		})
	}
}
func (s *lifecycleSuite) TestResponseWriteFailureCannotAuthorizeRetry() {
	for _, kind := range []string{"codex", "claude"} {
		s.Run(kind, func() {
			text := strings.Repeat("x", 1<<20)
			question := `s2c {"id":"large","method":"item/tool/requestUserInput","params":{"threadId":"native-A","turnId":"different-work-id","itemId":"question","isBlocking":true,"questions":[{"id":"q","header":"Free text","question":"Value?","isOther":true,"isSecret":false,"options":[]}]}}`
			answer := `delay=10000 c2s {"id":"large","result":{"answers":{"q":{"answers":["` + text + `"]}}}}`
			if kind == "claude" {
				question = `server control {"type":"control_request","request_id":"large","request":{"subtype":"can_use_tool","tool_name":"AskUserQuestion","input":{"questions":[{"question":"q","options":[]}]}}}`
				answer = `delay=10000 client control {"type":"control_response","response":{"subtype":"success","request_id":"large","response":{"behavior":"allow","updatedInput":{"questions":[{"question":"q","options":[]}],"answers":{"q":"` + text + `"}}}}}`
			}
			_, session, ctx := s.interactionSession(kind, []string{question, answer}, true)
			e := s.nextInteraction(session, ctx)
			attempt, cancel := context.WithCancel(ctx)
			defer cancel()
			type result struct {
				receipt rt.ResponseReceipt
				err     error
			}
			returned := make(chan result, 1)
			go func() {
				r, err := session.Respond(attempt, e.Interaction.Ref, rt.Response{Answers: map[string]rt.Answer{"q": {Text: &text}}})
				returned <- result{r, err}
			}()
			// Conversion of original SDK input can be expensive under race. Only
			// start cancellation after the public snapshot proves reservation.
			s.Require().Eventually(func() bool { return session.Snapshot().Interactions.Consumed == 1 }, 4*time.Second, time.Millisecond)
			timer := time.AfterFunc(100*time.Millisecond, cancel)
			defer timer.Stop()
			var observed result
			select {
			case observed = <-returned:
			case <-ctx.Done():
				s.FailNow("response did not settle after cancellation")
			}
			r, err := observed.receipt, observed.err
			s.ErrorIs(err, rt.ErrOutcomeUnknown)
			s.Equal("unknown", r.Outcome)
			s.Equal(rt.Known(true), r.HandedToSDK)
			s.Equal("unknown", r.WriteCompleted.State)
			retry, err := session.Respond(ctx, e.Interaction.Ref, rt.Response{Answers: map[string]rt.Answer{"q": {Text: &text}}})
			s.Error(err)
			s.Equal(rt.Known(false), retry.HandedToSDK)
		})
	}
}

func (s *lifecycleSuite) TestTargetCompletionInvalidatesUnresolvedPermission() {
	for _, kind := range []string{"codex", "claude"} {
		s.Run(kind, func() {
			terminal := codexFinished("different-work-id", "completed")
			pending := `c2s {"id":"same","result":{"decision":"accept"}}`
			if kind == "claude" {
				terminal = `server protocol {"type":"result","subtype":"success","session_id":"native-A","is_error":false}`
				pending = `client control {"type":"control_response","response":{"subtype":"success","request_id":"same","response":{"behavior":"allow","updatedInput":{}}}}`
			}
			_, session, ctx := s.interactionSession(kind, []string{permissionFrame(kind, `"same"`, "native-A"), terminal, pending})
			s.Require().Eventually(func() bool { return session.Snapshot().Foreground.State == "idle" }, 3*time.Second, time.Millisecond)
			e := s.nextInteraction(session, ctx)
			r, err := session.Respond(ctx, e.Interaction.Ref, rt.Response{Decision: "allow_once"})
			s.Error(err)
			s.Equal(rt.Known(false), r.HandedToSDK)
		})
	}
}

func (s *lifecycleSuite) TestUnownedChildRequestHasNoRootAuthority() {
	for _, kind := range []string{"codex", "claude"} {
		s.Run(kind, func() {
			request := `s2c {"id":"child","method":"item/commandExecution/requestApproval","params":{"threadId":"native-A","turnId":"child-turn","agentId":"unowned","itemId":"tool","startedAtMs":1,"command":"synthetic"}}`
			witness := codexFinished("different-work-id", "completed")
			if kind == "claude" {
				request = `server control {"type":"control_request","request_id":"child","request":{"subtype":"can_use_tool","agent_id":"unowned","tool_name":"Read","input":{}}}`
				witness = `server protocol {"type":"result","subtype":"success","session_id":"native-A","is_error":false}`
			}
			// Native cancellation follows the diagnostic so the strict peer settles its own pending request.
			resolved := `s2c {"method":"serverRequest/resolved","params":{"threadId":"native-A","requestId":"child"}}`
			if kind == "claude" {
				resolved = `server control {"type":"control_cancel_request","request_id":"child"}`
			}
			_, session, ctx := s.interactionSession(kind, []string{request, resolved, witness})
			for {
				e, err := session.Next(ctx)
				s.Require().NoError(err)
				s.NotEqual(rt.InteractionRequested, e.Kind)
				if e.Kind == rt.TurnCompleted {
					break
				}
			}
			r, err := session.Respond(ctx, "child", rt.Response{Decision: "allow_once"})
			s.Error(err)
			s.Equal(rt.Known(false), r.HandedToSDK)
		})
	}
}

func (s *lifecycleSuite) TestClaudeIdenticalRequestIDsStayWithOriginalSession() {
	dir := s.T().TempDir()
	paths := map[string]string{}
	for _, id := range []string{"native-A", "native-B"} {
		frames := append(claudeFrames(id, false), permissionFrame("claude", `"same"`, id), `client control {"type":"control_response","response":{"subtype":"success","request_id":"same","response":{"behavior":"allow","updatedInput":{}}}}`)
		paths[id] = s.scenario("claude", frames)
	}
	wrapper := filepath.Join(dir, "mock")
	script := "#!/bin/sh\nif mkdir " + quote(filepath.Join(dir, "first")) + " 2>/dev/null; then\nexport CLAUDE_MOCK_SCENARIO=" + quote(paths["native-A"]) + "\nelse\nexport CLAUDE_MOCK_SCENARIO=" + quote(paths["native-B"]) + "\nfi\nexec " + quote(s.executables["claude"]) + " \"$@\"\n"
	s.Require().NoError(os.WriteFile(wrapper, []byte(script), 0700))
	_, worker := s.register("claude", rt.Config{Executable: wrapper, Env: []string{}})
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	text := "hello"
	a := testutil.OpenSession(s.T(), worker, ctx, rt.SessionOptions{InitialInput: &text})
	b := testutil.OpenSession(s.T(), worker, ctx, rt.SessionOptions{InitialInput: &text})
	ea := s.nextInteraction(a, ctx)
	eb := s.nextInteraction(b, ctx)
	s.NotEqual(a.Key(), b.Key())
	s.NotEqual(ea.Interaction.Ref, eb.Interaction.Ref)
	for _, tc := range []struct {
		session rt.Session
		ref     string
	}{{a, eb.Interaction.Ref}, {b, ea.Interaction.Ref}} {
		r, err := tc.session.Respond(ctx, tc.ref, rt.Response{Decision: "allow_once"})
		s.ErrorIs(err, rt.ErrInvalidInteraction)
		s.Equal(rt.Known(false), r.HandedToSDK)
	}
	for _, tc := range []struct {
		session rt.Session
		ref     string
	}{{b, eb.Interaction.Ref}, {a, ea.Interaction.Ref}} {
		_, err := tc.session.Respond(ctx, tc.ref, rt.Response{Decision: "allow_once"})
		s.Require().NoError(err)
	}
}

func (s *lifecycleSuite) TestCancelledInteractionCannotTargetLaterWork() {
	for _, kind := range []string{"codex", "claude"} {
		s.Run(kind, func() {
			var extra []string
			extra = append(extra, permissionFrame(kind, `"old"`, "native-A"))
			if kind == "codex" {
				extra = append(extra, `c2s {"id":4,"method":"turn/interrupt","params":{"threadId":"native-A","turnId":"different-work-id"}}`, `s2c {"id":4,"result":{}}`, codexFinished("different-work-id", "interrupted"))
				for _, f := range codexTurn(5, "native-A") {
					extra = append(extra, strings.ReplaceAll(f, "different-work-id", "new-work-id"))
				}
				extra = append(extra, strings.ReplaceAll(permissionFrame(kind, `"new"`, "native-A"), "different-work-id", "new-work-id"), `c2s {"id":"new","result":{"decision":"accept"}}`, `s2c {"method":"serverRequest/resolved","params":{"threadId":"native-A","requestId":"old"}}`)
			} else {
				extra = append(extra, `client control {"type":"control_request","request_id":"interrupt","request":{"subtype":"interrupt"}}`, `server control {"type":"control_response","response":{"subtype":"success","request_id":"interrupt","response":{}}}`, `server protocol {"type":"result","subtype":"success","session_id":"native-A","is_error":false}`, `client protocol {"type":"user","message":{"role":"user","content":"hello"},"parent_tool_use_id":null,"session_id":""}`, permissionFrame(kind, `"new"`, "native-A"), `client control {"type":"control_response","response":{"subtype":"success","request_id":"new","response":{"behavior":"allow","updatedInput":{}}}}`, `server control {"type":"control_cancel_request","request_id":"old"}`)
			}
			_, session, ctx := s.interactionSession(kind, extra)
			old := s.nextInteraction(session, ctx)
			_, err := session.Cancel(ctx, session.Snapshot().Foreground.OperationRef)
			s.Require().NoError(err)
			r, err := session.Respond(ctx, old.Interaction.Ref, rt.Response{Decision: "allow_once"})
			s.ErrorIs(err, rt.ErrInvalidInteraction)
			s.Equal(rt.Known(false), r.HandedToSDK)
			s.Require().Eventually(func() bool { return session.Snapshot().Foreground.State == "idle" }, 2*time.Second, time.Millisecond)
			_, err = session.Submit(ctx, rt.TextInput("hello"))
			s.Require().NoError(err)
			next := s.nextInteraction(session, ctx)
			s.NotEqual(old.Interaction.Ref, next.Interaction.Ref)
			r, err = session.Respond(ctx, old.Interaction.Ref, rt.Response{Decision: "allow_once"})
			s.ErrorIs(err, rt.ErrInvalidInteraction)
			s.Equal(rt.Known(false), r.HandedToSDK)
			_, err = session.Respond(ctx, next.Interaction.Ref, rt.Response{Decision: "allow_once"})
			s.Require().NoError(err)
		})
	}
}

func (s *lifecycleSuite) TestResponseAndCloseShareAuthorityGate() {
	for _, kind := range []string{"codex", "claude"} {
		s.Run(kind, func() {
			answer := `c2s {"id":"same","result":{"decision":"accept"}}`
			if kind == "claude" {
				answer = `client control {"type":"control_response","response":{"subtype":"success","request_id":"same","response":{"behavior":"allow","updatedInput":{}}}}`
			}
			_, session, ctx := s.interactionSession(kind, []string{permissionFrame(kind, `"same"`, "native-A"), answer})
			event := s.nextInteraction(session, ctx)
			start := make(chan struct{})
			responded := make(chan rt.ResponseReceipt, 1)
			closed := make(chan error, 1)
			go func() {
				<-start
				r, _ := session.Respond(ctx, event.Interaction.Ref, rt.Response{Decision: "allow_once"})
				responded <- r
			}()
			go func() { <-start; closed <- session.Close() }()
			close(start)
			s.Require().NoError(<-closed)
			r := <-responded
			switch r.Outcome {
			case "written":
				s.Equal(rt.Known(true), r.HandedToSDK)
				s.Equal(rt.Known(true), r.WriteCompleted)
			case "unknown":
				s.Equal(rt.Known(true), r.HandedToSDK)
				s.Equal("outcome_unknown", r.Failure.Category)
			case "rejected":
				s.Equal(rt.Known(false), r.HandedToSDK)
				s.Equal("invalid_interaction", r.Failure.Category)
			default:
				s.Fail("unexpected response result", r.Outcome)
			}
			after, err := session.Respond(ctx, event.Interaction.Ref, rt.Response{Decision: "allow_once"})
			s.ErrorIs(err, rt.ErrInvalidInteraction)
			s.Equal(rt.Known(false), after.HandedToSDK)
		})
	}
}
