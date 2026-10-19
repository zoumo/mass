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

// The host retains opaque binding bytes; it does not interpret adapter fields.
type bindingMemory struct {
	mu     sync.Mutex
	values map[rt.SessionKey]rt.RecoveryBinding
}

func (b *bindingMemory) Save(ctx context.Context, key rt.SessionKey, value rt.RecoveryBinding) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.values == nil {
		b.values = map[rt.SessionKey]rt.RecoveryBinding{}
	}
	value.Data = append([]byte(nil), value.Data...)
	b.values[key] = value
	return nil
}
func (b *bindingMemory) Load(ctx context.Context, key rt.SessionKey) (rt.RecoveryBinding, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	value, ok := b.values[key]
	if !ok {
		return value, rt.ErrNotFound
	}
	value.Data = append([]byte(nil), value.Data...)
	return value, nil
}
func (s *lifecycleSuite) recoveryConfig(kind, returned string, interactions ...bool) (rt.Config, []string) {
	dir := s.T().TempDir()
	wrapper := filepath.Join(dir, "mock")
	firstPID, nextPID := filepath.Join(dir, "first.pid"), filepath.Join(dir, "next.pid")
	var first, next []string
	if kind == "codex" {
		first = append(codexInit(), codexThread(2, "native-A")...)
		result := codexThread(2, returned)[1]
		next = append(codexInit(), `c2s {"id":2,"method":"thread/resume","params":{"threadId":"native-A","excludeTurns":true}}`, result)
		turn := codexTurn(3, "native-A")
		turn[0] = strings.Replace(turn[0], `"text":"hello"`, `"text":"after resume"`, 1)
		next = append(next, turn...)
	} else {
		first = claudeFrames("native-A", false)
		next = append([]string{}, claudeFrames("native-A", false)[:2]...)
		if returned != "" {
			next = append(next, fmt.Sprintf(`server protocol {"type":"system","subtype":"init","session_id":%q}`, returned))
		}
		next = append(next, strings.Replace(claudeFrames("native-A", false)[2], `"content":"hello"`, `"content":"after resume"`, 1))
		next = append(next, claudeFrames("native-A", false)[4:]...)
	}

	if len(interactions) > 0 && interactions[0] {
		pending := `c2s {"id":"same","result":{"decision":"decline"}}`
		answer := `c2s {"id":"same","result":{"decision":"accept"}}`
		if kind == "codex" {
			first = append(first, codexTurn(3, "native-A")...)
		} else {
			pending = `client control {"type":"control_response","response":{"subtype":"success","request_id":"same","response":{"behavior":"deny"}}}`
			answer = `client control {"type":"control_response","response":{"subtype":"success","request_id":"same","response":{"behavior":"allow","updatedInput":{}}}}`
		}
		first = append(first, permissionFrame(kind, `"same"`, "native-A"), pending)
		next = append(next, permissionFrame(kind, `"same"`, "native-A"), answer)
	}
	a, b := s.scenario(kind, first), s.scenario(kind, next)
	variable := strings.ToUpper(kind) + "_MOCK_SCENARIO"
	script := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> " + quote(wrapper+".argv") + "\nif mkdir " + quote(filepath.Join(dir, "first")) + " 2>/dev/null; then\nexport " + variable + "=" + quote(a) + "\nprintf '%s' \"$$\" > " + quote(firstPID) + "\nelse\nexport " + variable + "=" + quote(b) + "\nprintf '%s' \"$$\" > " + quote(nextPID) + "\nfi\nexec " + quote(s.executables[kind]) + " \"$@\"\n"
	s.Require().NoError(os.WriteFile(wrapper, []byte(script), 0700))
	return rt.Config{Executable: wrapper, Env: []string{}, CreationLimits: rt.CreationLimits{Timeout: 2 * time.Second}}, []string{firstPID, nextPID}
}
func (s *lifecycleSuite) registeredRecovery(config rt.Config, kind string, store rt.BindingStore) rt.Runtime {
	c := catalog.NewWithBindings(store)
	_, err := c.Register(rt.Registration{Name: "worker", Type: kind, Config: config})
	s.Require().NoError(err)
	worker, err := c.Get("worker")
	s.Require().NoError(err)
	s.T().Cleanup(func() { s.NoError(worker.Stop()) })
	return worker
}
func (s *lifecycleSuite) TestVerifiedColdResume() {
	for _, kind := range []string{"codex", "claude"} {
		s.Run(kind, func() {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			config, pids := s.recoveryConfig(kind, "native-A")
			store := &bindingMemory{}
			worker := s.registeredRecovery(config, kind, store)
			s.Require().NoError(worker.Start(ctx))
			options := rt.SessionOptions{}
			input := "hello"
			if kind == "claude" {
				options.InitialInput = &input
			}
			old := testutil.OpenSession(s.T(), worker, ctx, options)
			epoch := old.Snapshot().ConnectionEpoch
			key := old.Key()
			s.Require().NoError(worker.Stop())
			s.assertExited(pids[0])
			// Offline inventory survives stop even if the executable disappears.
			s.Require().NoError(os.Rename(config.Executable, config.Executable+".saved"))
			got, err := worker.GetSession(key)
			s.Require().NoError(err)
			s.Equal("closed", got.Snapshot().Attachment)
			observedAt := got.Snapshot().ObservedAt
			s.Require().False(observedAt.IsZero())
			s.Equal(observedAt, got.Snapshot().ObservedAt)
			snapshots := worker.ListSessions()
			s.Require().Len(snapshots, 1)
			s.Equal("cached", snapshots[0].Freshness)
			s.Equal("unavailable", snapshots[0].Availability)
			s.Require().NoError(os.Rename(config.Executable+".saved", config.Executable))
			fresh := s.registeredRecovery(config, kind, store)
			s.Empty(fresh.ListSessions())
			s.Require().NoError(fresh.Start(ctx))
			resumed, err := fresh.ResumeSession(ctx, key)
			s.Require().NoError(err)
			s.Equal(key, resumed.Key())
			s.NotEqual(epoch, resumed.Snapshot().ConnectionEpoch)
			s.Len(fresh.ListSessions(), 1)
			receipt, err := resumed.Submit(ctx, rt.TextInput("after resume"))
			s.Require().NoError(err)
			s.NotEmpty(receipt.OperationRef)
			s.readReply(ctx, resumed)
			data, err := json.Marshal(fresh.ListSessions())
			s.Require().NoError(err)
			s.NotContains(string(data), "binding")
			s.NotContains(string(data), config.Executable)
			if kind == "claude" {
				args, err := os.ReadFile(config.Executable + ".argv")
				s.Require().NoError(err)
				s.Equal(1, strings.Count(string(args), "--resume=native-A"))
			}
			s.Require().NoError(fresh.Stop())
			s.assertExited(pids[1])
		})
	}
}
func (s *lifecycleSuite) TestResumeRejectsMissingMismatchedAndReboundOwnership() {
	for _, kind := range []string{"codex", "claude"} {
		for _, mode := range []string{"missing_binding", "wrong_scope", "wrong_key", "mismatch", "unverifiable"} {
			if kind == "codex" && mode == "unverifiable" {
				continue
			}
			s.Run(kind+"/"+mode, func() {
				ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
				defer cancel()
				returned := "native-other"
				if mode == "unverifiable" {
					returned = ""
				}
				config, pids := s.recoveryConfig(kind, returned)
				store := &bindingMemory{}
				worker := s.registeredRecovery(config, kind, store)
				s.Require().NoError(worker.Start(ctx))
				input := "hello"
				options := rt.SessionOptions{}
				if kind == "claude" {
					options.InitialInput = &input
				}
				old := testutil.OpenSession(s.T(), worker, ctx, options)
				key := old.Key()
				s.Require().NoError(worker.Stop())
				s.assertExited(pids[0])
				if mode == "missing_binding" {
					store = &bindingMemory{}
				}
				if mode == "wrong_scope" {
					config.Env = []string{"DIFFERENT_SCOPE=yes"}
				}
				if mode == "wrong_key" {
					key.SessionID = "not-the-original"
				}
				fresh := s.registeredRecovery(config, kind, store)
				s.Require().NoError(fresh.Start(ctx))
				_, err := fresh.ResumeSession(ctx, key)
				switch mode {
				case "mismatch":
					s.ErrorIs(err, rt.ErrIdentityMismatch)
				case "wrong_scope":
					s.ErrorIs(err, rt.ErrConflict)
				default:
					s.ErrorIs(err, rt.ErrUnverifiable)
				}
				s.Empty(fresh.ListSessions())
				s.Require().NoError(fresh.Stop())
				if kind == "codex" || mode == "mismatch" || mode == "unverifiable" {
					s.assertExited(pids[1])
				} else {
					_, err := os.Stat(pids[1])
					s.True(os.IsNotExist(err))
				}
			})
		}
	}
}
func (s *lifecycleSuite) TestCloseGoalsAndIsolation() {
	for _, kind := range []string{"codex", "claude"} {
		for _, goal := range []rt.CloseGoal{rt.CloseDetach, rt.CloseRelease} {
			s.Run(kind+"/"+string(goal), func() {
				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				defer cancel()
				config, pids := s.config(kind)
				worker := s.registeredRecovery(config, kind, &bindingMemory{})
				s.Require().NoError(worker.Start(ctx))
				input := "hello"
				options := rt.SessionOptions{}
				if kind == "claude" {
					options.InitialInput = &input
				}
				a := testutil.OpenSession(s.T(), worker, ctx, options)
				b := testutil.OpenSession(s.T(), worker, ctx, options)
				if kind == "codex" {
					_, err := a.Submit(ctx, rt.TextInput("hello"))
					s.Require().NoError(err)
					s.readReply(ctx, a)
					_, err = b.Submit(ctx, rt.TextInput("hello"))
					s.Require().NoError(err)
				}
				s.readReply(ctx, b)
				receipt, err := worker.CloseSession(ctx, a.Key(), goal)
				s.Equal(rt.Known(true), receipt.LocalAttachmentClosed)
				s.Equal("unsupported", receipt.NativeRelease)
				if goal == rt.CloseRelease {
					s.ErrorIs(err, rt.ErrOutcomeUnknown)
					s.Equal("close_unknown", receipt.Outcome)
					_, err = worker.ResumeSession(ctx, a.Key())
					s.ErrorIs(err, rt.ErrConflict)
				} else {
					s.NoError(err)
					s.Equal("closed", receipt.Outcome)
				}
				if kind == "claude" {
					s.Equal(rt.Known(true), receipt.OwnedProcessReaped)
					s.assertExited(pids[0])
				} else {
					s.Equal("not_applicable", receipt.OwnedProcessReaped.State)
				}
				s.Require().Eventually(func() bool { return b.Snapshot().Foreground.State == "idle" }, time.Second, time.Millisecond)
				_, err = b.Submit(ctx, rt.TextInput("hello"))
				s.Require().NoError(err)
				s.readReply(ctx, b)
				s.Require().NoError(worker.Stop())
				for _, pid := range pids {
					s.assertExited(pid)
				}
			})
		}
	}
}

func (s *lifecycleSuite) TestVerifiedReattachmentRejectsOldAuthority() {
	for _, kind := range []string{"codex", "claude"} {
		s.Run(kind, func() {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			config, pids := s.recoveryConfig(kind, "native-A", true)
			worker := s.registeredRecovery(config, kind, &bindingMemory{})
			s.Require().NoError(worker.Start(ctx))
			input := "hello"
			options := rt.SessionOptions{}
			if kind == "claude" {
				options.InitialInput = &input
			}
			old := testutil.OpenSession(s.T(), worker, ctx, options)
			if kind == "codex" {
				_, err := old.Submit(ctx, rt.TextInput("hello"))
				s.Require().NoError(err)
			}
			e := s.nextInteraction(old, ctx)
			data, err := json.Marshal(e)
			s.Require().NoError(err)
			receipt, err := worker.CloseSession(ctx, old.Key(), rt.CloseDetach)
			s.Require().NoError(err)
			s.Equal("closed", receipt.Outcome)
			pending, err := old.GetSubmission(old.Snapshot().Foreground.OperationRef)
			s.Require().NoError(err)
			s.Equal("unknown", pending.Outcome)
			s.Equal("unknown", pending.Completed.State)
			s.Require().NoError(worker.Stop())
			s.assertExited(pids[0])
			s.Require().NoError(worker.Start(ctx))
			fresh, err := worker.ResumeSession(ctx, old.Key())
			s.Require().NoError(err)
			s.NotEqual(e.ConnectionEpoch, fresh.Snapshot().ConnectionEpoch)
			s.Len(worker.ListSessions(), 1)
			got, err := worker.GetSession(old.Key())
			s.Require().NoError(err)
			s.Equal(fresh, got)
			var delayed rt.Event
			s.Require().NoError(json.Unmarshal(data, &delayed))
			s.Equal(e.ConnectionEpoch, delayed.ConnectionEpoch)
			for _, session := range []rt.Session{old, fresh} {
				r, err := session.Respond(ctx, delayed.Interaction.Ref, rt.Response{Decision: "allow_once"})
				s.ErrorIs(err, rt.ErrInvalidInteraction)
				s.Equal(rt.Known(false), r.HandedToSDK)
			}
			_, err = fresh.Submit(ctx, rt.TextInput("after resume"))
			s.Require().NoError(err)
			newer := s.nextInteraction(fresh, ctx)
			s.NotEqual(e.Interaction.Ref, newer.Interaction.Ref)
			r, err := worker.Respond(ctx, fresh.Key(), newer.Interaction.Ref, rt.Response{Decision: "allow_once"})
			s.Require().NoError(err)
			s.Equal("written", r.Outcome)
			s.Require().NoError(worker.Stop())
			s.assertExited(pids[1])
		})
	}
}

func (s *lifecycleSuite) TestCloseWithStalledConsumerAndStopPendingOpen() {
	for _, kind := range []string{"codex", "claude"} {
		for _, mode := range []string{"stalled_consumer", "pending_open", "pending_submit"} {
			if kind == "claude" && mode == "pending_submit" {
				continue
			} // Send is a write, not a native ACK wait.
			s.Run(kind+"/"+mode, func() {
				ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
				defer cancel()
				var frames []string
				if kind == "codex" {
					frames = append(codexInit(), codexThread(2, "native-A")...)
				} else {
					frames = claudeFrames("native-A", false)
				}
				if mode == "pending_open" {
					if kind == "codex" {
						frames[4] = "delay=700 " + frames[4]
					} else {
						frames[3] = "delay=700 " + frames[3]
					}
				}
				if mode == "stalled_consumer" {
					if kind == "codex" {
						frames = append(frames, codexTurn(3, "native-A")...)
					}
					for i := 0; i < 180; i++ {
						if i == 50 {
							marker := `s2c {"method":"thread/name/updated","params":{"threadId":"native-A","name":"unread-prefix"}}`
							if kind == "claude" {
								marker = `server protocol {"type":"system","subtype":"init","session_id":"native-A","title":"unread-prefix"}`
							}
							frames = append(frames, marker)
						}
						frame := fmt.Sprintf(`s2c {"method":"item/agentMessage/delta","params":{"threadId":"native-A","turnId":"old-work","itemId":"m","delta":"%d"}}`, i)
						if kind == "claude" {
							frame = fmt.Sprintf(`server protocol {"type":"assistant","session_id":"native-A","message":{"id":"m%d","role":"assistant","content":[{"type":"text","text":"x"}]}}`, i)
						}
						frames = append(frames, frame)
					}
				}
				if mode == "pending_submit" {
					turn := codexTurn(3, "native-A")
					frames = append(frames, turn[0], `s2c {"method":"thread/name/updated","params":{"threadId":"native-A","name":"submit-consumed"}}`, "delay=700 "+turn[1], turn[2])
				}
				config := s.singleScenario(kind, frames)
				wrapper, exited := testutil.TrackProcess(s.T(), config.Executable)
				config.Executable = wrapper
				worker := s.registeredRecovery(config, kind, &bindingMemory{})
				s.Require().NoError(worker.Start(ctx))
				input := "hello"
				options := rt.SessionOptions{}
				if kind == "claude" {
					options.InitialInput = &input
				}
				if mode == "pending_open" {
					op, err := worker.CreateSession(ctx, options)
					s.Require().NoError(err)
					s.Require().Eventually(func() bool { _, err := os.Stat(filepath.Join(filepath.Dir(wrapper), "pid")); return err == nil }, time.Second, time.Millisecond)
					s.Empty(worker.ListSessions())
					_, err = worker.CloseSession(ctx, rt.SessionKey{RuntimeName: "worker", SessionID: op.OperationRef}, rt.CloseDetach)
					s.ErrorIs(err, rt.ErrNotFound)
					s.Require().NoError(worker.Stop())
					op, err = worker.GetOperation(op.OperationRef)
					s.Require().NoError(err)
					s.Nil(op.Key)
					s.Contains([]string{"closed", "outcome_unknown"}, op.Phase)
				} else {
					session := testutil.OpenSession(s.T(), worker, ctx, options)
					var returned chan error
					if mode == "pending_submit" {
						returned = make(chan error, 1)
						go func() { _, err := session.Submit(ctx, rt.TextInput("hello")); returned <- err }()
						s.Require().Eventually(func() bool {
							title := session.Snapshot().Metadata.Title
							return title != nil && *title == "submit-consumed"
						}, time.Second, time.Millisecond)
					}
					if mode == "stalled_consumer" {
						if kind == "codex" {
							_, err := session.Submit(ctx, rt.TextInput("hello"))
							s.Require().NoError(err)
						}
						s.Require().Eventually(func() bool {
							title := session.Snapshot().Metadata.Title
							return title != nil && *title == "unread-prefix"
						}, 2*time.Second, time.Millisecond)
					}
					// No Next call: close cannot wait on the user draining delivery.
					start := time.Now()
					receipt, err := worker.CloseSession(ctx, session.Key(), rt.CloseDetach)
					s.Require().NoError(err)
					s.Equal("closed", receipt.Outcome)
					s.True(receipt.IncompleteFacts)
					s.Less(time.Since(start), 3*time.Second)
					if returned != nil {
						select {
						case <-returned:
						case <-ctx.Done():
							s.FailNow("pending SDK call leaked")
						}
						op, err := session.GetSubmission(session.Snapshot().Foreground.OperationRef)
						s.Require().NoError(err)
						s.Equal("unknown", op.Outcome)
						s.Equal("unknown", op.Completed.State)
					}
					s.Require().NoError(worker.Stop())
				}
				exited()
			})
		}
	}
}

func (s *lifecycleSuite) TestSharedResumeQuarantinesHistoricalRequests() {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	frames := append(codexInit(), codexThread(2, "native-A")...)
	frames = append(frames, codexTurn(3, "native-A")...)
	// The resume response itself is the consumption witness for local detach.
	frames = append(frames, `c2s {"id":4,"method":"thread/resume","params":{"threadId":"native-A","excludeTurns":true}}`, codexThread(4, "native-A")[1])
	frames = append(frames, permissionFrame("codex", `"old-request"`, "native-A"))
	turn := codexTurn(5, "native-A")
	for i := range turn {
		turn[i] = strings.ReplaceAll(turn[i], "different-work-id", "new-work")
	}
	frames = append(frames, turn...)
	request := strings.ReplaceAll(permissionFrame("codex", `"new-request"`, "native-A"), "different-work-id", "new-work")
	frames = append(frames, request, `c2s {"id":"new-request","result":{"decision":"accept"}}`)
	config := s.singleScenario("codex", frames)
	wrapper, exited := testutil.TrackProcess(s.T(), config.Executable)
	config.Executable = wrapper
	worker := s.registeredRecovery(config, "codex", &bindingMemory{})
	s.Require().NoError(worker.Start(ctx))
	old := testutil.OpenSession(s.T(), worker, ctx, rt.SessionOptions{})
	_, err := old.Submit(ctx, rt.TextInput("hello"))
	s.Require().NoError(err)
	epoch := old.Snapshot().ConnectionEpoch
	_, err = worker.CloseSession(ctx, old.Key(), rt.CloseDetach)
	s.Require().NoError(err)
	fresh, err := worker.ResumeSession(ctx, old.Key())
	s.Require().NoError(err)
	s.NotEqual(epoch, fresh.Snapshot().ConnectionEpoch)
	for {
		e, err := fresh.Next(ctx)
		s.Require().NoError(err)
		s.NotEqual(rt.InteractionRequested, e.Kind)
		if e.Kind == rt.Unknown && e.Diagnostic.NativeKind == "request/previous_attachment_work" {
			s.Equal("request/previous_attachment_work", e.Diagnostic.NativeKind)
			s.Equal("different-work-id", e.WorkRef)
			s.Equal("unresolved", e.Origin.Attribution)
			s.Nil(e.Interaction)
			break
		}
	}
	s.Equal(0, fresh.Snapshot().Interactions.Pending)
	_, err = fresh.Submit(ctx, rt.TextInput("hello"))
	s.Require().NoError(err)
	e := s.nextInteraction(fresh, ctx)
	s.Equal("new-work", e.WorkRef)
	receipt, err := fresh.Respond(ctx, e.Interaction.Ref, rt.Response{Decision: "allow_once"})
	s.Require().NoError(err)
	s.Equal("written", receipt.Outcome)
	s.Require().NoError(worker.Stop())
	exited()
}

func (s *lifecycleSuite) TestUnknownReleasePersistsInHostBinding() {
	for _, kind := range []string{"codex", "claude"} {
		s.Run(kind, func() {
			ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
			defer cancel()
			config, pids := s.recoveryConfig(kind, "native-A")
			store := &bindingMemory{}
			worker := s.registeredRecovery(config, kind, store)
			s.Require().NoError(worker.Start(ctx))
			input := "hello"
			options := rt.SessionOptions{}
			if kind == "claude" {
				options.InitialInput = &input
			}
			session := testutil.OpenSession(s.T(), worker, ctx, options)
			receipt, err := worker.CloseSession(ctx, session.Key(), rt.CloseRelease)
			s.ErrorIs(err, rt.ErrOutcomeUnknown)
			s.Equal("close_unknown", receipt.Outcome)
			s.Require().NoError(worker.Stop())
			s.assertExited(pids[0])
			fresh := s.registeredRecovery(config, kind, store)
			s.Require().NoError(fresh.Start(ctx))
			_, err = fresh.ResumeSession(ctx, session.Key())
			s.ErrorIs(err, rt.ErrConflict)
			s.Empty(fresh.ListSessions())
			s.Require().NoError(fresh.Stop())
			if kind == "codex" {
				s.assertExited(pids[1])
			} else {
				_, err = os.Stat(pids[1])
				s.True(os.IsNotExist(err))
			}
		})
	}
}

func (s *lifecycleSuite) TestTransportEOFDoesNotProveNativeRelease() {
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	frames := append(codexInit(), codexThread(2, "native-A")...)
	turn := codexTurn(3, "native-A")
	frames = append(frames, turn[0], turn[1], "disconnect")
	config := s.singleScenario("codex", frames)
	wrapper, exited := testutil.TrackProcess(s.T(), config.Executable)
	config.Executable = wrapper
	worker := s.registeredRecovery(config, "codex", &bindingMemory{})
	s.Require().NoError(worker.Start(ctx))
	session := testutil.OpenSession(s.T(), worker, ctx, rt.SessionOptions{})
	submitted, err := session.Submit(ctx, rt.TextInput("hello"))
	s.Require().NoError(err)
	s.Equal(rt.Known(true), submitted.NativeAcknowledged)
	for {
		_, err := session.Next(ctx)
		if err != nil {
			s.NotErrorIs(err, context.DeadlineExceeded)
			break
		}
	}
	receipt, err := worker.CloseSession(ctx, session.Key(), rt.CloseRelease)
	s.ErrorIs(err, rt.ErrOutcomeUnknown)
	s.Equal("close_unknown", receipt.Outcome)
	s.Equal("unsupported", receipt.NativeRelease)
	s.Equal(rt.Known(true), receipt.LocalAttachmentClosed)
	s.Require().NoError(worker.Stop())
	exited()
}
