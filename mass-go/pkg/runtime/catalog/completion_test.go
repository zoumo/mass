package catalog_test

import (
	"context"
	"encoding/json"
	"github.com/zoumo/mass/mass-go/internal/testutil"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	rt "github.com/zoumo/mass/mass-go/pkg/runtime"
	"github.com/zoumo/mass/mass-go/pkg/runtime/catalog"
)

type runtimeEvents interface {
	Next(context.Context) (rt.Event, error)
}

func (s *lifecycleSuite) lifecycleNext(stream runtimeEvents, ctx context.Context, kind string) rt.Event {
	s.T().Helper()
	for {
		event, err := stream.Next(ctx)
		s.Require().NoError(err)
		wire, err := json.Marshal(event)
		s.Require().NoError(err)
		var decoded rt.Event
		s.Require().NoError(json.Unmarshal(wire, &decoded))
		s.Equal(event, decoded)
		if string(event.Kind) == kind {
			return event
		}
	}
}

func (s *lifecycleSuite) TestRuntimeLifecycleAndCapabilityAvailability() {
	for _, kind := range []string{"codex", "claude"} {
		s.Run(kind, func() {
			ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
			defer cancel()
			config, pids := s.recoveryConfig(kind, "native-A")
			worker := s.registeredRecovery(config, kind, &bindingMemory{})
			stream, ok := worker.(runtimeEvents)
			s.Require().True(ok, "V01 requires public Runtime lifecycle event consumption")
			s.capability(worker.Status(), "create_session", "supported", "unavailable", "runtime_not_ready")
			s.Require().NoError(worker.Start(ctx))
			ready := s.lifecycleNext(stream, ctx, "runtime.status_changed")
			for {
				var payload map[string]any
				data, _ := json.Marshal(ready)
				s.Require().NoError(json.Unmarshal(data, &payload))
				if payload["payload"].(map[string]any)["phase"] == "ready" {
					break
				}
				ready = s.lifecycleNext(stream, ctx, "runtime.status_changed")
			}
			s.Equal("runtime", ready.Scope)
			s.Empty(ready.SessionID)
			s.capability(worker.Status(), "create_session", "supported", "available", "")
			s.capability(worker.Status(), "remote_transport", "unsupported", "unavailable", "transport_not_implemented")
			op, err := worker.CreateSession(ctx, rt.SessionOptions{})
			s.Require().NoError(err)
			if kind == "claude" {
				pending := s.lifecycleNext(stream, ctx, "operation.state_changed")
				s.Equal(op.OperationRef, pending.OperationRef)
				s.Empty(pending.SessionID)
				_, err = worker.ContinueCreation(ctx, op.OperationRef, "hello")
				s.Require().NoError(err)
			}
			op = s.waitOperation(worker, op.OperationRef, "opened")
			opened := s.lifecycleNext(stream, ctx, "session.opened")
			s.Require().NotNil(opened.Session)
			s.Equal(*op.Key, opened.Session.Key)
			s.Equal(op.OperationRef, opened.OperationRef)
			session, err := worker.GetSession(*op.Key)
			s.Require().NoError(err)
			s.capability(session.Snapshot(), "native_release", "unsupported", "unavailable", "native_release_unproven")
			s.Require().NoError(worker.Stop())
			s.assertExited(pids[0])
			s.capability(session.Snapshot(), "text_input", "supported", "unavailable", "attachment_closed")
			s.capability(session.Snapshot(), "resume_session", "supported", "unavailable", "runtime_not_ready")
			closed := s.lifecycleNext(stream, ctx, "session.state_changed")
			for closed.Session.Attachment != "closed" {
				closed = s.lifecycleNext(stream, ctx, "session.state_changed")
			}
			s.Equal(session.Key(), closed.Session.Key)
			s.Require().NoError(worker.Start(ctx))
			resumed, err := worker.ResumeSession(ctx, session.Key())
			s.Require().NoError(err)
			event := s.lifecycleNext(stream, ctx, "session.resumed")
			s.Equal(session.Key(), event.Session.Key)
			s.NotEqual(opened.Session.ConnectionEpoch, event.Session.ConnectionEpoch)
			s.capability(resumed.Snapshot(), "text_input", "supported", "available", "")
			_, err = resumed.Submit(ctx, rt.TextInput("after resume"))
			s.Require().NoError(err)
			running := s.lifecycleNext(stream, ctx, "session.state_changed")
			for running.Session.Foreground.State != "running" {
				running = s.lifecycleNext(stream, ctx, "session.state_changed")
			}
			s.capability(running.Session, "text_input", "supported", "unavailable", "foreground_unresolved")
			s.readReply(ctx, resumed)
			s.Require().NoError(worker.Stop())
			s.assertExited(pids[1])
		})
	}
}

func (s *lifecycleSuite) capability(value any, name, support, availability, reason string) {
	s.T().Helper()
	data, err := json.Marshal(value)
	s.Require().NoError(err)
	var record struct {
		Capabilities []struct {
			Name, Support, Availability, Reason string
			Conditions                          []string
		}
	}
	s.Require().NoError(json.Unmarshal(data, &record))
	for _, feature := range record.Capabilities {
		if feature.Name == name {
			s.Equal(support, feature.Support)
			s.Equal(availability, feature.Availability)
			s.Equal(reason, feature.Reason)
			return
		}
	}
	s.FailNow("missing capability record", name)
}

func (s *lifecycleSuite) TestCreationFailureHasOperationEventWithoutFakeSession() {
	config := s.singleScenario("claude", claudeFrames("native-A", false))
	config.CreationLimits = rt.CreationLimits{Bytes: 1, Timeout: 2 * time.Second}
	_, worker := s.register("claude", config)
	stream, ok := worker.(runtimeEvents)
	s.Require().True(ok)
	input := "hello"
	op, err := worker.CreateSession(context.Background(), rt.SessionOptions{InitialInput: &input})
	s.Require().NoError(err)
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
	defer cancel()
	event := s.lifecycleNext(stream, ctx, "operation.failed")
	s.Equal("operation", event.Scope)
	s.Equal(op.OperationRef, event.OperationRef)
	s.Empty(event.SessionID)
	s.Empty(event.ConnectionEpoch)
	s.Require().NotNil(event.Error)
	s.Equal("incomplete_stream", event.Error.Category)
	s.Empty(worker.ListSessions())
}

func (s *catalogSuite) TestCapabilityDeclarationsAreDiscoverable() {
	c := catalog.New()
	for _, kind := range c.Types() {
		data, err := json.Marshal(kind)
		s.Require().NoError(err)
		var record struct {
			Features []struct {
				Name, Support, Reason string
				Conditions            []string
			}
		}
		s.Require().NoError(json.Unmarshal(data, &record))
		s.NotEmpty(record.Features, "V01/US18 requires per-feature declared conditions")
		found := false
		for _, feature := range record.Features {
			if feature.Name == "resume_session" {
				found = true
				s.Equal("supported", feature.Support)
				s.NotEmpty(feature.Conditions)
			}
		}
		s.True(found)
	}
}

func (s *lifecycleSuite) TestLifecycleRetentionFailurePreservesPrefixAndCleanup() {
	for _, kind := range []string{"codex", "claude"} {
		s.Run(kind, func() {
			config, _ := s.config(kind)
			config.LifecycleLimits = rt.DeliveryLimits{Facts: 4, Bytes: 1 << 20}
			wrapper, exited := testutil.TrackProcess(s.T(), config.Executable)
			config.Executable = wrapper
			registry := catalog.New()
			_, err := registry.Register(rt.Registration{Name: "worker", Type: kind, Config: config})
			s.Require().NoError(err)
			worker, err := registry.Get("worker")
			s.Require().NoError(err)
			s.T().Cleanup(func() { s.ErrorIs(worker.Stop(), rt.ErrIncomplete) })
			s.Require().NoError(worker.Start(context.Background()))
			input := "hello"
			options := rt.SessionOptions{}
			if kind == "claude" {
				options.InitialInput = &input
			}
			op, err := worker.CreateSession(context.Background(), options)
			s.Require().NoError(err)
			op = s.waitOperation(worker, op.OperationRef, "opened")
			s.Equal("incomplete", worker.Status().LifecycleDelivery)
			session, err := worker.GetSession(*op.Key)
			s.Require().NoError(err)
			_, err = session.Submit(context.Background(), rt.TextInput("not admitted"))
			s.ErrorIs(err, rt.ErrBackpressure)
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			for seq := uint64(1); seq <= 4; seq++ {
				event, err := worker.Next(ctx)
				s.Require().NoError(err)
				s.Equal(seq, event.Seq)
			}
			_, err = worker.Next(ctx)
			s.ErrorIs(err, rt.ErrIncomplete)
			s.ErrorIs(err, rt.ErrBackpressure)
			s.ErrorIs(worker.Stop(), rt.ErrIncomplete)
			exited()
		})
	}
}

func (s *lifecycleSuite) TestRuntimeLifecycleWaitAndSequenceAreIndependent() {
	for _, kind := range []string{"codex", "claude"} {
		s.Run(kind, func() {
			config, _ := s.config(kind)
			registry := catalog.New()
			_, err := registry.Register(rt.Registration{Name: "worker", Type: kind, Config: config})
			s.Require().NoError(err)
			worker, err := registry.Get("worker")
			s.Require().NoError(err)
			s.T().Cleanup(func() { s.NoError(worker.Stop()) })
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			_, err = worker.Next(ctx)
			s.ErrorIs(err, context.Canceled)
			s.Require().NoError(worker.Start(context.Background()))
			s.Require().NoError(worker.Stop())
			var seq uint64
			for {
				event, err := worker.Next(context.Background())
				if err != nil {
					s.ErrorIs(err, io.EOF)
					break
				}
				seq++
				s.Equal(seq, event.Seq)
				s.Empty(event.SessionID)
				s.Equal("runtime", event.Scope)
				s.Require().NotNil(event.Runtime)
				event.Runtime.Capabilities[0].Name = "caller mutation"
				s.NotEqual("caller mutation", worker.Status().Capabilities[0].Name)
			}
			s.Equal(uint64(5), seq, "registered, starting, ready, stopping, stopped")
			s.Require().NoError(worker.Start(context.Background()))
			event, err := worker.Next(context.Background())
			s.Require().NoError(err)
			s.Equal(seq+1, event.Seq)
			ready, err := worker.Next(context.Background())
			s.Require().NoError(err)
			s.Equal(seq+2, ready.Seq)
			wait, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
			_, err = worker.Next(wait)
			s.ErrorIs(err, context.DeadlineExceeded)
			cancel()
			s.Equal("ready", worker.Status().Phase)
			snapshot := worker.Status()
			snapshot.Capabilities[2].Conditions[0] = "caller mutation"
			s.NotEqual("caller mutation", worker.Status().Capabilities[2].Conditions[0])
		})
	}
}

func (s *lifecycleSuite) TestRuntimeObservesOriginalCommandLifecycles() {
	for _, kind := range []string{"codex", "claude"} {
		s.Run(kind, func() {
			extra := []string{permissionFrame(kind, `"grant"`, "native-A")}
			if kind == "codex" {
				extra = append(extra, `c2s {"id":"grant","result":{"decision":"accept"}}`, `c2s {"id":4,"method":"turn/interrupt","params":{"threadId":"native-A","turnId":"different-work-id"}}`, `s2c {"id":4,"result":{}}`, codexFinished("different-work-id", "interrupted"))
			} else {
				extra = append(extra, `client control {"type":"control_response","response":{"subtype":"success","request_id":"grant","response":{"behavior":"allow","updatedInput":{}}}}`, `client control {"type":"control_request","request_id":"interrupt","request":{"subtype":"interrupt"}}`, `server control {"type":"control_response","response":{"subtype":"success","request_id":"interrupt","response":{}}}`, `server protocol {"type":"result","subtype":"success","session_id":"native-A","is_error":false}`)
			}
			worker, session, ctx := s.interactionSession(kind, extra)
			requested := s.nextInteraction(session, ctx)
			original := session.Snapshot().Foreground.OperationRef
			s.Require().NotEmpty(original)
			response, err := worker.Respond(ctx, session.Key(), requested.Interaction.Ref, rt.Response{Decision: "allow_once"})
			s.Require().NoError(err)
			cancelled, err := session.Cancel(ctx, original)
			s.Require().NoError(err)
			s.Require().Eventually(func() bool { return session.Snapshot().Foreground.State == "idle" }, time.Second, time.Millisecond)
			s.Require().NoError(worker.Stop())
			seen := map[string]bool{}
			for {
				event, err := worker.Next(ctx)
				if err != nil {
					s.ErrorIs(err, io.EOF)
					break
				}
				if string(event.Kind) != "operation.state_changed" {
					continue
				}
				data, err := json.Marshal(event)
				s.Require().NoError(err)
				var wire struct {
					Payload struct {
						Kind         string
						Submission   *rt.Submission
						Cancellation *rt.Cancellation
						Response     *rt.ResponseReceipt
					}
				}
				s.Require().NoError(json.Unmarshal(data, &wire))
				switch wire.Payload.Kind {
				case "submission":
					s.Require().NotNil(wire.Payload.Submission)
					s.Equal(original, event.OperationRef)
					s.Equal(session.Key(), wire.Payload.Submission.Key)
					seen["submission"] = true
				case "cancellation":
					s.Require().NotNil(wire.Payload.Cancellation)
					s.Equal(cancelled.OperationRef, event.OperationRef)
					s.Equal(original, wire.Payload.Cancellation.TargetOperationRef)
					seen["cancellation"] = true
				case "response":
					s.Require().NotNil(wire.Payload.Response)
					s.Equal(response.OperationRef, event.OperationRef)
					s.Equal(requested.Interaction.Ref, wire.Payload.Response.InteractionRef)
					seen["response"] = true
				}
			}
			for _, kind := range []string{"submission", "cancellation", "response"} {
				s.True(seen[kind], "missing Runtime operation family %s", kind)
			}
		})
	}
}

func (s *lifecycleSuite) TestLifecycleByteLimitRejectsBeforeNativeActivation() {
	for _, kind := range []string{"codex", "claude"} {
		s.Run(kind, func() {
			config, pids := s.config(kind)
			config.LifecycleLimits = rt.DeliveryLimits{Bytes: 1}
			registry := catalog.New()
			_, err := registry.Register(rt.Registration{Name: "worker", Type: kind, Config: config})
			s.Require().NoError(err)
			worker, err := registry.Get("worker")
			s.Require().NoError(err)
			s.ErrorIs(worker.Start(context.Background()), rt.ErrBackpressure)
			s.Equal("incomplete", worker.Status().LifecycleDelivery)
			_, err = worker.Next(context.Background())
			s.ErrorIs(err, rt.ErrBackpressure)
			s.ErrorIs(err, rt.ErrIncomplete)
			for _, path := range pids {
				_, err := os.Stat(path)
				s.True(os.IsNotExist(err), "byte rejection must precede native launch")
			}
			s.ErrorIs(worker.Stop(), rt.ErrIncomplete)
		})
	}
}

func (s *catalogSuite) TestLifecycleLimitsAreFinite() {
	for _, limits := range []rt.DeliveryLimits{{Facts: -1}, {Facts: 4097}, {Bytes: -1}, {Bytes: 17 << 20}} {
		_, err := catalog.New().Register(rt.Registration{Name: "worker", Type: "claude", Config: rt.Config{Executable: "/synthetic/mock", Env: []string{}, LifecycleLimits: limits}})
		s.ErrorIs(err, rt.ErrInvalidInput)
	}
}

func (s *catalogSuite) TestRetiredRuntimePublishesFinalObservation() {
	registry := catalog.New()
	_, err := registry.Register(rt.Registration{Name: "retired", Type: "claude", Config: rt.Config{Executable: "/synthetic/mock", Env: []string{}}})
	s.Require().NoError(err)
	worker, err := registry.Get("retired")
	s.Require().NoError(err)
	s.Require().NoError(registry.Unregister("retired"))
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	_, err = worker.Next(ctx)
	s.Require().NoError(err)
	event, err := worker.Next(ctx)
	s.Require().NoError(err)
	s.Equal(rt.RuntimeStatusChanged, event.Kind)
	data, err := json.Marshal(event.Runtime)
	s.Require().NoError(err)
	var snapshot struct{ Retired bool }
	s.Require().NoError(json.Unmarshal(data, &snapshot))
	s.True(snapshot.Retired)
	_, err = worker.Next(ctx)
	s.ErrorIs(err, io.EOF)
	s.ErrorIs(worker.Start(context.Background()), rt.ErrConflict)
	s.NoError(worker.Stop())
	_, err = worker.Next(ctx)
	s.ErrorIs(err, io.EOF)
}

func (s *lifecycleSuite) TestFailedResumePreservesUnknownCleanupAndOwnershipFence() {
	kind := "claude"
	dir := s.T().TempDir()
	wrapper := filepath.Join(dir, "mock")
	first := s.scenario(kind, claudeFrames("native-A", false))
	resumed := s.scenario(kind, append(claudeFrames("native-A", false)[:2], `delay=500 server protocol {"type":"assistant","message":{"id":"invalid","role":"assistant","content":null}}`))
	firstPID, nextPID := filepath.Join(dir, "first.pid"), filepath.Join(dir, "next.pid")
	script := "#!/bin/sh\nprintf '%s\\n' \"$$\" >> " + quote(wrapper+".launches") + "\nif mkdir " + quote(filepath.Join(dir, "first")) + " 2>/dev/null; then\nexport CLAUDE_MOCK_SCENARIO=" + quote(first) + "\nprintf '%s' \"$$\" > " + quote(firstPID) + "\nelse\nexport CLAUDE_MOCK_SCENARIO=" + quote(resumed) + "\nprintf '%s' \"$$\" > " + quote(nextPID) + "\nfi\nexec " + quote(s.executables[kind]) + " \"$@\"\n"
	s.Require().NoError(os.WriteFile(wrapper, []byte(script), 0700))
	registry := catalog.NewWithBindings(&bindingMemory{})
	_, err := registry.Register(rt.Registration{Name: "worker", Type: kind, Config: rt.Config{Executable: wrapper, Env: []string{}, CreationLimits: rt.CreationLimits{Timeout: 2 * time.Second}}})
	s.Require().NoError(err)
	worker, err := registry.Get("worker")
	s.Require().NoError(err)
	s.T().Cleanup(func() {
		if err := worker.Stop(); err != nil {
			s.ErrorContains(err, "claude runtime close failed")
		}
	})
	s.Require().NoError(worker.Start(context.Background()))
	input := "hello"
	original := testutil.OpenSession(s.T(), worker, context.Background(), rt.SessionOptions{InitialInput: &input})
	key := original.Key()
	epoch := original.Snapshot().ConnectionEpoch
	s.Require().NoError(worker.Stop())
	s.assertExited(firstPID)
	s.Require().NoError(worker.Start(context.Background()))
	_, err = worker.ResumeSession(context.Background(), key)
	s.ErrorIs(err, rt.ErrUnverifiable)
	s.ErrorIs(err, rt.ErrCleanupIncomplete)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	for {
		event, err := worker.Next(ctx)
		s.Require().NoError(err)
		if event.Operation != nil && event.Operation.Kind == "resume" && event.Operation.State == "outcome_unknown" {
			s.Equal("unconfirmed", event.Operation.Creation.CleanupState)
			s.Nil(event.Operation.Creation.Key)
			break
		}
	}
	s.assertExited(nextPID)
	s.Require().Len(worker.ListSessions(), 1)
	s.Equal(epoch, worker.ListSessions()[0].ConnectionEpoch)
	_, err = worker.ResumeSession(context.Background(), key)
	s.ErrorIs(err, rt.ErrConflict)
	launches, err := os.ReadFile(wrapper + ".launches")
	s.Require().NoError(err)
	s.Len(strings.Fields(string(launches)), 2, "unknown cleanup cannot authorize another attachment")
	s.ErrorContains(worker.Stop(), "claude runtime close failed")
}
