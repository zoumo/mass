package catalog_test

import (
	"context"
	"encoding/json"
	"time"

	"github.com/zoumo/mass/mass-go/internal/testutil"
	rt "github.com/zoumo/mass/mass-go/pkg/runtime"
	"github.com/zoumo/mass/mass-go/pkg/runtime/catalog"
)

// One raw control frame is one provisional fact, even though the SDK also
// invokes a permission callback. The opened case catches double accounting;
// overflow must retain its specific failure and owned-process cleanup evidence.
func (s *lifecycleSuite) TestProvisionalPermissionHasOneBudgetOwner() {
	const raw = `{"type":"control_request","request_id":"early-permission","request":{"subtype":"can_use_tool","tool_name":"Read","tool_use_id":"tool-1","input":{}}}`
	// Allow finite space for the SDK startup observation, whose serialized
	// epoch/identity size varies. The fitting budget cannot hold two copies of
	// the 148-byte raw permission plus that observation (at least 213 bytes).
	const startupAllowance = 256
	for _, tc := range []struct {
		name   string
		bytes  int
		opened bool
	}{{"one_frame_fits", startupAllowance + len(raw), true}, {"one_frame_exceeds", startupAllowance, false}} {
		s.Run(tc.name, func() {
			frames := claudeFrames("budget-root", false)[:3]
			frames = append(frames, "server control "+raw,
				`delay=100 server protocol {"type":"system","subtype":"init","session_id":"budget-root"}`,
				`client control {"type":"control_response","response":{"subtype":"success","request_id":"early-permission","response":{"behavior":"deny","message":""}}}`,
				`server protocol {"type":"result","subtype":"success","session_id":"budget-root","is_error":false}`)
			config := s.singleScenario("claude", frames)
			config.CreationLimits = rt.CreationLimits{Facts: 2, Bytes: tc.bytes, Timeout: 2 * time.Second}
			wrapper, exited := testutil.TrackProcess(s.T(), config.Executable)
			config.Executable = wrapper
			_, worker := s.register("claude", config)
			input := "hello"
			op, err := worker.CreateSession(context.Background(), rt.SessionOptions{InitialInput: &input})
			s.Require().NoError(err)
			if tc.opened {
				s.Require().Eventually(func() bool {
					var err error
					op, err = worker.GetOperation(op.OperationRef)
					return err == nil && (op.Phase == "opened" || op.Phase == "outcome_unknown")
				}, 6*time.Second, time.Millisecond)
				s.Require().Equal("opened", op.Phase, "%+v", op)
				s.Require().NotNil(op.Key)
				session, err := worker.GetSession(*op.Key)
				s.Require().NoError(err)
				ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
				defer cancel()
				event := s.nextInteraction(session, ctx)
				receipt, err := session.Respond(ctx, event.Interaction.Ref, rt.Response{Decision: "deny_once"})
				s.Require().NoError(err)
				s.Equal("written", receipt.Outcome)
				for {
					e, err := session.Next(ctx)
					s.Require().NoError(err)
					if e.Kind == rt.TurnCompleted {
						break
					}
				}
			} else {
				op = s.waitOperation(worker, op.OperationRef, "outcome_unknown")
				s.Equal("incomplete_stream", op.Failure)
				s.Nil(op.Key)
				s.Empty(worker.ListSessions())
				s.Contains(op.CleanupState, "local_resources_released")
			}
			s.NoError(worker.Stop())
			exited()
		})
	}
}

func (s *lifecycleSuite) TestCompleteClaudeInvalidContentFailsStream() {
	for _, tc := range []struct{ name, message string }{
		{"missing", `{"type":"assistant","message":{"id":"invalid","role":"assistant"}}`},
		{"null", `{"type":"assistant","message":{"id":"invalid","role":"assistant","content":null}}`},
		{"bare_tool_object", `{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"call","content":{"unexpected":"object"}}]}}`},
	} {
		s.Run(tc.name, func() {
			frames := claudeFrames("boundary-root", false)[:4]
			frames = append(frames,
				`server protocol {"type":"result","subtype":"success","session_id":"boundary-root","is_error":false}`,
				`client protocol {"type":"user","message":{"role":"user","content":"boundary"},"parent_tool_use_id":null,"session_id":""}`,
				"server protocol "+tc.message)
			config := s.singleScenario("claude", frames)
			wrapper, exited := testutil.TrackProcess(s.T(), config.Executable)
			config.Executable = wrapper
			registry := catalog.New()
			_, err := registry.Register(rt.Registration{Name: "worker", Type: "claude", Config: config})
			s.Require().NoError(err)
			worker, err := registry.Get("worker")
			s.Require().NoError(err)
			s.Require().NoError(worker.Start(context.Background()))
			// SDK decode failure is also returned from Close. Keep this error
			// visible instead of claiming a successful cleanup receipt.
			s.T().Cleanup(func() {
				if err := worker.Stop(); err != nil {
					s.ErrorContains(err, "claude runtime close failed")
				}
			})
			ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
			defer cancel()
			input := "hello"
			session := testutil.OpenSession(s.T(), worker, ctx, rt.SessionOptions{InitialInput: &input})
			for {
				event, err := session.Next(ctx)
				s.Require().NoError(err)
				if event.Kind == rt.TurnCompleted {
					break
				}
			}
			_, err = session.Submit(ctx, rt.TextInput("boundary"))
			s.Require().NoError(err)
			for {
				event, err := session.Next(ctx)
				if err != nil {
					s.ErrorIs(err, rt.ErrIncomplete)
					s.NotErrorIs(err, context.DeadlineExceeded)
					var safe *rt.SafeError
					s.Require().ErrorAs(err, &safe)
					s.Equal("transport_error", safe.Category)
					break
				}
				s.Nil(event.Message, "rejected content cannot become a complete empty message")
				s.Nil(event.Tool, "rejected content cannot become a complete tool result")
			}
			s.ErrorContains(worker.Stop(), "claude runtime close failed")
			exited()
		})
	}
}

// Rejection is not evidence for the lifecycle of an external connection.
func (s *catalogSuite) TestRemoteTransportIsUnavailable() {
	for _, kind := range []string{"codex", "claude"} {
		s.Run(kind, func() {
			registry := catalog.New()
			_, err := registry.Register(rt.Registration{Name: "external", Type: kind,
				Config: rt.Config{Executable: "ws://127.0.0.1:1", Env: []string{}}})
			s.ErrorIs(err, rt.ErrInvalidInput)
			s.Empty(registry.List())
			for _, description := range registry.Types() {
				for _, field := range description.ConfigFields {
					s.NotEqual("url", field.Name)
				}
				data, err := json.Marshal(description)
				s.Require().NoError(err)
				s.NotContains(string(data), "external_server")
			}
		})
	}
}
