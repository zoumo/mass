package catalog_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/zoumo/mass/mass-go/internal/testutil"
	rt "github.com/zoumo/mass/mass-go/pkg/runtime"
	"github.com/zoumo/mass/mass-go/pkg/runtime/catalog"
)

func (s *lifecycleSuite) TestDeliveryBoundsRetainAcceptedPrefix() {
	for _, kind := range []string{"codex", "claude"} {
		s.Run(kind, func() {
			var frames []string
			if kind == "codex" {
				frames = append(codexInit(), codexThread(2, "bounded")...)
				frames = append(frames, codexTurn(3, "bounded")[:2]...)
				for i := 0; i < 12; i++ {
					frames = append(frames, fmt.Sprintf(`s2c {"method":"item/agentMessage/delta","params":{"threadId":"bounded","turnId":"different-work-id","itemId":"m","delta":"%d"}}`, i))
				}
			} else {
				frames = claudeFrames("bounded", false)[:4]
				for i := 0; i < 12; i++ {
					frames = append(frames, fmt.Sprintf(`server protocol {"type":"assistant","message":{"id":"m-%d","role":"assistant","content":"%d"}}`, i, i))
				}
			}
			if kind == "claude" {
				frames[4] = "delay=100 " + frames[4]
			}
			config := s.singleScenario(kind, frames)
			config.DeliveryLimits = rt.DeliveryLimits{Facts: 4, Bytes: 1 << 20}
			wrapper, exited := testutil.TrackProcess(s.T(), config.Executable)
			config.Executable = wrapper
			_, worker := s.register(kind, config)
			text := "hello"
			opts := rt.SessionOptions{}
			if kind == "claude" {
				opts.InitialInput = &text
			}
			session := testutil.OpenSession(s.T(), worker, context.Background(), opts)
			if kind == "codex" {
				_, err := session.Submit(context.Background(), rt.TextInput(text))
				s.Require().NoError(err)
			}
			s.Require().Eventually(func() bool { return session.Snapshot().Availability == "unavailable" }, 2*time.Second, time.Millisecond)
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			var seq uint64
			for {
				e, err := session.Next(ctx)
				if err != nil {
					s.ErrorIs(err, rt.ErrIncomplete)
					s.ErrorIs(err, rt.ErrBackpressure)
					break
				}
				seq++
				s.Equal(seq, e.Seq)
				s.Equal("bounded", e.SessionID)
			}
			s.Equal(uint64(4), seq)
			s.NoError(worker.Stop())
			exited()
		})
	}
}

func (s *lifecycleSuite) TestBoundedStopPolicies() {
	for _, kind := range []string{"codex", "claude"} {
		for _, policy := range []rt.StopPolicy{rt.StopAbort, rt.StopDrain} {
			s.Run(kind+"/"+string(policy), func() {
				var frames []string
				if kind == "codex" {
					frames = append(codexInit(), codexThread(2, "native-A")...)
					frames = append(frames, codexTurn(3, "native-A")...)
					frames = append(frames, codexFinishedFor("native-A", "different-work-id", "completed"))
				} else {
					frames = claudeFrames("native-A", false)
					frames = append(frames, `server protocol {"type":"result","subtype":"success","session_id":"native-A","is_error":false}`)
				}
				config := s.singleScenario(kind, frames)
				wrapper, exited := testutil.TrackProcess(s.T(), config.Executable)
				config.Executable = wrapper
				_, worker := s.register(kind, config)
				text := "hello"
				opts := rt.SessionOptions{}
				if kind == "claude" {
					opts.InitialInput = &text
				}
				session := testutil.OpenSession(s.T(), worker, context.Background(), opts)
				if kind == "codex" {
					_, err := session.Submit(context.Background(), rt.TextInput(text))
					s.Require().NoError(err)
				}
				s.Require().Eventually(func() bool { return session.Snapshot().Foreground.State == "idle" }, time.Second, time.Millisecond)
				ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
				defer cancel()
				start := time.Now()
				receipt, err := worker.StopWith(ctx, rt.StopOptions{Policy: policy})
				s.Less(time.Since(start), time.Second)
				s.True(receipt.AdmissionClosed)
				s.True(receipt.IncompleteFacts)
				if policy == rt.StopDrain {
					s.ErrorIs(err, context.DeadlineExceeded)
					s.Equal("stop_unknown", receipt.Outcome)
				} else {
					s.NoError(err)
					s.Equal("stopped", receipt.Outcome)
				}
				s.NoError(worker.Stop())
				exited()
				_, err = session.Submit(context.Background(), rt.TextInput(text))
				s.Error(err)
				if policy == rt.StopAbort {
					_, err = session.Next(context.Background())
					s.ErrorIs(err, rt.ErrIncomplete)
				}
			})
		}
	}
}

func (s *catalogSuite) TestDeliveryAndConfigurationAdmission() {
	for _, tc := range []struct {
		name   string
		config rt.Config
	}{
		{"negative count", rt.Config{DeliveryLimits: rt.DeliveryLimits{Facts: -1}}},
		{"excess count", rt.Config{DeliveryLimits: rt.DeliveryLimits{Facts: 4097}}},
		{"negative bytes", rt.Config{DeliveryLimits: rt.DeliveryLimits{Bytes: -1}}},
		{"excess bytes", rt.Config{DeliveryLimits: rt.DeliveryLimits{Bytes: 17 << 20}}},
	} {
		s.Run(tc.name, func() {
			tc.config.Executable = "/synthetic"
			tc.config.Env = []string{}
			_, err := catalog.New().Register(rt.Registration{Name: "bounded", Type: "claude", Config: tc.config})
			s.ErrorIs(err, rt.ErrInvalidInput)
		})
	}
}

func (s *lifecycleSuite) TestDeliveryByteLimit() {
	for _, kind := range []string{"codex", "claude"} {
		s.Run(kind, func() {
			var frames []string
			text := strings.Repeat("x", 4096)
			if kind == "codex" {
				frames = append(codexInit(), codexThread(2, "native-A")...)
				frames = append(frames, codexTurn(3, "native-A")[:2]...)
				frames = append(frames, fmt.Sprintf(`s2c {"method":"item/agentMessage/delta","params":{"threadId":"native-A","turnId":"different-work-id","itemId":"m","delta":%q}}`, text))
			} else {
				frames = claudeFrames("native-A", false)[:4]
				frames = append(frames, fmt.Sprintf(`delay=100 server protocol {"type":"assistant","message":{"id":"m","role":"assistant","content":%q}}`, text))
			}
			config := s.singleScenario(kind, frames)
			config.DeliveryLimits = rt.DeliveryLimits{Bytes: 2048}
			wrapper, exited := testutil.TrackProcess(s.T(), config.Executable)
			config.Executable = wrapper
			_, worker := s.register(kind, config)
			opts := rt.SessionOptions{}
			input := "hello"
			if kind == "claude" {
				opts.InitialInput = &input
			}
			session := testutil.OpenSession(s.T(), worker, context.Background(), opts)
			if kind == "codex" {
				_, err := session.Submit(context.Background(), rt.TextInput(input))
				s.Require().NoError(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			var seq uint64
			for {
				e, err := session.Next(ctx)
				if err != nil {
					s.ErrorIs(err, rt.ErrBackpressure)
					s.ErrorIs(err, rt.ErrIncomplete)
					break
				}
				seq++
				s.Equal(seq, e.Seq)
				s.Nil(e.Message, "oversized fact must not be silently delivered past the bound")
			}
			s.Greater(seq, uint64(0), "accepted lifecycle prefix must survive byte failure")
			s.NoError(worker.Stop())
			exited()
		})
	}
}

func (s *lifecycleSuite) TestQueuedFactsSurviveSharedTransportTermination() {
	frames := append(codexInit(), codexThread(2, "native-A")...)
	frames = append(frames, codexThread(3, "native-B")...)
	frames = append(frames, codexTurn(4, "native-A")...)
	for i := 0; i < 20; i++ {
		for _, id := range []string{"native-A", "native-B"} {
			frames = append(frames, fmt.Sprintf(`s2c {"method":"item/agentMessage/delta","params":{"threadId":%q,"turnId":"work","itemId":"m","delta":"%d"}}`, id, i))
		}
	}
	frames = append(frames, "disconnect")
	config := s.singleScenario("codex", frames)
	_, worker := s.register("codex", config)
	a := testutil.OpenSession(s.T(), worker, context.Background(), rt.SessionOptions{})
	b := testutil.OpenSession(s.T(), worker, context.Background(), rt.SessionOptions{})
	_, err := a.Submit(context.Background(), rt.TextInput("hello"))
	s.Require().NoError(err)
	for _, session := range []rt.Session{a, b} {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		var seq uint64
		var texts []string
		for {
			e, err := session.Next(ctx)
			if err != nil {
				s.ErrorIs(err, rt.ErrIncomplete)
				var safe *rt.SafeError
				s.Require().ErrorAs(err, &safe)
				s.Equal("transport", safe.Scope)
				break
			}
			seq++
			s.Equal(seq, e.Seq)
			s.Equal(session.Key().SessionID, e.SessionID)
			if e.Message != nil {
				texts = append(texts, testutil.MessageText(e))
			}
		}
		expected := []string{}
		if session == a {
			expected = append(expected, "native-A reply")
		}
		for i := 0; i < 20; i++ {
			expected = append(expected, fmt.Sprint(i))
		}
		s.Equal(expected, texts)
	}
}

func (s *lifecycleSuite) TestStopRacesActivationWithoutGlobalIOLock() {
	for _, kind := range []string{"codex", "claude"} {
		s.Run(kind, func() {
			var config rt.Config
			if kind == "claude" {
				config = s.singleScenario(kind, claudeFrames("native-A", false))
			}
			if kind == "codex" {
				frames := codexInit()
				frames[1] = "delay=500 " + frames[1]
				config = s.singleScenario(kind, frames)
			}
			registry := catalog.New()
			_, err := registry.Register(rt.Registration{Name: "worker", Type: kind, Config: config})
			s.Require().NoError(err)
			worker, err := registry.Get("worker")
			s.Require().NoError(err)
			started := make(chan error, 1)
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			go func() { started <- worker.Start(ctx) }()
			s.Require().Eventually(func() bool { phase := worker.Status().Phase; return phase == "starting" || phase == "ready" }, time.Second, time.Millisecond)
			stopCtx, stopCancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
			defer stopCancel()
			receipt, stopErr := worker.StopWith(stopCtx, rt.StopOptions{Policy: rt.StopAbort})
			s.True(receipt.AdmissionClosed)
			if stopErr != nil {
				s.Equal("stop_unknown", receipt.Outcome)
				s.ErrorIs(stopErr, context.DeadlineExceeded)
			}
			select {
			case <-started:
			case <-ctx.Done():
				s.FailNow("activation owner did not settle")
			}
			s.NoError(worker.Stop())
			s.Equal("stopped", worker.Status().Phase)
		})
	}
}

func (s *lifecycleSuite) TestMalformedSessionDoesNotSealSiblingOrOtherRuntime() {
	frames := append(codexInit(), codexThread(2, "native-A")...)
	frames = append(frames, codexThread(3, "native-B")...)
	frames = append(frames, codexTurn(4, "native-A")[:2]...)
	frames = append(frames, `s2c {"method":"item/reasoning/textDelta","params":{"threadId":"native-A","turnId":"different-work-id","itemId":"r","delta":null}}`)
	frames = append(frames, codexTurn(5, "native-B")...)
	_, worker := s.register("codex", s.singleScenario("codex", frames))
	a := testutil.OpenSession(s.T(), worker, context.Background(), rt.SessionOptions{})
	b := testutil.OpenSession(s.T(), worker, context.Background(), rt.SessionOptions{})
	_, err := a.Submit(context.Background(), rt.TextInput("hello"))
	s.Require().NoError(err)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	seen := false
	for {
		e, err := a.Next(ctx)
		if err != nil {
			s.ErrorIs(err, rt.ErrIncomplete)
			break
		}
		if e.Error != nil {
			seen = true
			s.Equal("decode_error", e.Error.Category)
			s.Equal("session", e.Error.Scope)
		}
	}
	s.True(seen)
	_, err = b.Submit(ctx, rt.TextInput("hello"))
	s.Require().NoError(err)
	s.readReply(ctx, b)
	// Another named Runtime owns a different process and remains usable.
	config := s.singleScenario("codex", append(append(codexInit(), codexThread(2, "native-A")...), codexTurn(3, "native-A")...))
	registry := catalog.New()
	_, err = registry.Register(rt.Registration{Name: "other", Type: "codex", Config: config})
	s.Require().NoError(err)
	other, err := registry.Get("other")
	s.Require().NoError(err)
	s.Require().NoError(other.Start(ctx))
	defer other.Stop()
	c := testutil.OpenSession(s.T(), other, ctx, rt.SessionOptions{})
	_, err = c.Submit(ctx, rt.TextInput("hello"))
	s.Require().NoError(err)
	s.readReply(ctx, c)
}

func (s *lifecycleSuite) TestInterleavedPermissionAndFactPublication() {
	for _, kind := range []string{"codex", "claude"} {
		s.Run(kind, func() {
			var config rt.Config
			if kind == "codex" {
				frames := append(codexInit(), codexThread(2, "native-A")...)
				frames = append(frames, codexThread(3, "native-B")...)
				frames = append(frames, codexTurn(4, "native-A")...)
				frames = append(frames, codexTurn(5, "native-B")...)
				frames = append(frames, permissionFrame(kind, `7`, "native-A"), permissionFrame(kind, `"7"`, "native-B"), `c2s {"id":7,"result":{"decision":"decline"}}`, `c2s {"id":"7","result":{"decision":"decline"}}`, codexFinishedFor("native-A", "different-work-id", "completed"), codexFinishedFor("native-B", "different-work-id", "completed"))
				config = s.singleScenario(kind, frames)
			} else {
				paths := []string{}
				for _, id := range []string{"native-A", "native-B"} {
					frames := claudeFrames(id, false)
					frames = append(frames, permissionFrame(kind, `"7"`, id), `client control {"type":"control_response","response":{"subtype":"success","request_id":"7","response":{"behavior":"deny","message":""}}}`, fmt.Sprintf(`server protocol {"type":"result","subtype":"success","session_id":%q,"is_error":false}`, id))
					paths = append(paths, s.scenario(kind, frames))
				}
				dir := s.T().TempDir()
				wrapper := filepath.Join(dir, "mock")
				script := "#!/bin/sh\nif mkdir " + quote(filepath.Join(dir, "first")) + " 2>/dev/null; then\nexport CLAUDE_MOCK_SCENARIO=" + quote(paths[0]) + "\nelse\nexport CLAUDE_MOCK_SCENARIO=" + quote(paths[1]) + "\nfi\nexec " + quote(s.executables[kind]) + " \"$@\"\n"
				s.Require().NoError(os.WriteFile(wrapper, []byte(script), 0700))
				config = rt.Config{Executable: wrapper, Env: []string{}}
			}
			_, worker := s.register(kind, config)
			opts := rt.SessionOptions{}
			text := "hello"
			if kind == "claude" {
				opts.InitialInput = &text
			}
			a := testutil.OpenSession(s.T(), worker, context.Background(), opts)
			b := testutil.OpenSession(s.T(), worker, context.Background(), opts)
			if kind == "codex" {
				for _, session := range []rt.Session{a, b} {
					_, err := session.Submit(context.Background(), rt.TextInput(text))
					s.Require().NoError(err)
				}
			}
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			sequences := map[rt.Session]uint64{}
			check := func(session rt.Session, e rt.Event) {
				sequences[session]++
				s.Equal(sequences[session], e.Seq)
				s.Equal(session.Key().SessionID, e.SessionID)
				data, err := json.Marshal(e)
				s.Require().NoError(err)
				s.NotContains(string(data), "PermissionRequest")
			}
			for _, session := range []rt.Session{a, b} {
				for {
					e, err := session.Next(ctx)
					s.Require().NoError(err)
					check(session, e)
					if e.Kind == rt.InteractionRequested {
						_, err = worker.Respond(ctx, session.Key(), e.Interaction.Ref, rt.Response{Decision: "deny_once"})
						s.Require().NoError(err)
						break
					}
				}
			}
			for _, session := range []rt.Session{a, b} {
				for {
					e, err := session.Next(ctx)
					s.Require().NoError(err)
					check(session, e)
					if e.Kind == rt.TurnCompleted {
						break
					}
				}
			}
		})
	}
}

func (s *lifecycleSuite) TestDrainWithReaderPreservesAcceptedFacts() {
	for _, kind := range []string{"codex", "claude"} {
		s.Run(kind, func() {
			var frames []string
			if kind == "codex" {
				frames = append(codexInit(), codexThread(2, "native-A")...)
				frames = append(frames, codexTurn(3, "native-A")...)
				frames = append(frames, codexFinishedFor("native-A", "different-work-id", "completed"))
			} else {
				frames = append(claudeFrames("native-A", false), `server protocol {"type":"result","subtype":"success","session_id":"native-A","is_error":false}`)
			}
			_, worker := s.register(kind, s.singleScenario(kind, frames))
			opts := rt.SessionOptions{}
			text := "hello"
			if kind == "claude" {
				opts.InitialInput = &text
			}
			session := testutil.OpenSession(s.T(), worker, context.Background(), opts)
			if kind == "codex" {
				_, err := session.Submit(context.Background(), rt.TextInput(text))
				s.Require().NoError(err)
			}
			s.Require().Eventually(func() bool { return session.Snapshot().Foreground.State == "idle" }, time.Second, time.Millisecond)
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			done := make(chan []rt.Event, 1)
			go func() {
				var events []rt.Event
				for {
					e, err := session.Next(ctx)
					if err != nil {
						done <- events
						return
					}
					events = append(events, e)
				}
			}()
			receipt, err := worker.StopWith(ctx, rt.StopOptions{Policy: rt.StopDrain})
			s.Require().NoError(err)
			s.Equal("stopped", receipt.Outcome)
			events := <-done
			s.Require().NotEmpty(events)
			completed := false
			for i, e := range events {
				s.Equal(uint64(i+1), e.Seq)
				if e.Kind == rt.TurnCompleted {
					completed = true
				}
			}
			s.True(completed)
		})
	}
}
