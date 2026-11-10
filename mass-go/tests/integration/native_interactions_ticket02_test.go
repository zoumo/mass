package integration_test

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/zoumo/mass/mass-go/pkg/events"
	"github.com/zoumo/mass/mass-go/pkg/resource"
	"github.com/zoumo/mass/mass-go/pkg/runtime"
	"github.com/zoumo/mass/mass-go/pkg/transport"
)

func TestNativeInteractionAnswerCorrection(t *testing.T) {
	for _, kind := range []string{"codex", "claude"} {
		t.Run(kind, func(t *testing.T) {
			svc, reg, _ := setup(t, nil)
			session := openNative(t, svc, kind, "question", "questions")
			ref := resource.SessionRef{SessionID: session.Status.SessionID}
			sub, err := svc.SubscribeEvents(ref)
			require.NoError(t, err)
			defer sub.Close()
			_, err = svc.SendPrompt(ctx(t), ref, runtime.Input{Content: []events.ContentBlock{events.Text("hello")}})
			require.NoError(t, err)
			answered := false
			for {
				e, err := nextSubscription(sub, ctx(t))
				require.NoError(t, err)
				if p, ok := e.Payload().(events.InteractionRequested); ok {
					_, err = svc.AnswerInteraction(ctx(t), ref, string(p.Interaction), map[string]any{"action": "accept"})
					require.Error(t, err)
					_, err = svc.AnswerInteraction(ctx(t), ref, string(p.Interaction), map[string]any{"action": "accept", "content": map[string]any{"answers": map[string]any{"not-a-native-question": map[string]any{"answers": []any{"json"}}}}})
					require.Error(t, err, "unknown question identity must reject before dispatch")
					var answers map[string]any
					if kind == "claude" {
						answers = map[string]any{"Which format?": "json"}
					} else {
						answers = map[string]any{"format": map[string]any{"answers": []any{"json"}}}
					}
					driver, err := reg.Get("questions")
					require.NoError(t, err)
					canceled, cancel := context.WithCancel(context.Background())
					cancel()
					_, err = driver.AnswerInteraction(canceled, ref.SessionID, string(p.Interaction), map[string]any{"action": "accept", "content": map[string]any{"answers": answers}})
					require.Error(t, err, "confirmed native pre-write cancellation must not consume live authority")
					receipt, err := svc.AnswerInteraction(ctx(t), ref, string(p.Interaction), map[string]any{"action": "accept", "content": map[string]any{"answers": answers}})
					require.NoError(t, err, "validation must not consume live native authority")
					require.Equal(t, runtime.Yes(), receipt.Evidence.Sent)
					require.Nil(t, receipt.Evidence.NativeAccepted)
					_, err = svc.AnswerInteraction(ctx(t), ref, string(p.Interaction), map[string]any{"action": "accept", "content": map[string]any{"answers": answers}})
					require.Error(t, err)
					answered = true
				}
				if p, ok := e.Payload().(events.StateUpdate); ok && p.State != nil && *p.State == events.Idle && answered {
					break
				}
			}
		})
	}
}

func (s *entrypointSuite) TestTicket02CodexQuestionAuthority()  { s.ticket02Question("codex") }
func (s *entrypointSuite) TestTicket02ClaudeQuestionAuthority() { s.ticket02Question("claude") }

func (s *entrypointSuite) ticket02Question(kind string) {
	ref, _, _ := s.open(kind, "ticket02-question")
	_, scan := s.observer(ref)
	s.call("session.input", workflowParams{Session: ref, Input: runtime.Input{Content: []events.ContentBlock{events.Text("hello")}}}, nil)
	var request *events.InteractionRequested
	answered, progressed := false, false
	waitingItem := messageItemID(kind, "waiting-message")
	for scan.Scan() {
		var e events.SessionEvent
		s.Require().NoError(json.Unmarshal(scan.Bytes(), &e))
		if p, ok := e.Payload().(events.InteractionRequested); ok {
			request = &p
			// Decode the raw public SSE payload to check exact extension data, not
			// float64 values introduced by a test-side generic JSON decoder.
			dec := json.NewDecoder(bytes.NewReader(scan.Bytes()))
			dec.UseNumber()
			var raw map[string]any
			s.Require().NoError(dec.Decode(&raw))
			encoded, err := json.Marshal(raw)
			s.Require().NoError(err)
			s.Contains(string(encoded), "9007199254740993")
			s.Contains(string(encoded), "futureQuestion")
		}
		if e.ItemID == waitingItem {
			progressed = true
		}
		if request != nil && progressed && !answered {
			client := newHTTPClient(s.T(), s.url, "")
			err := workflowHTTP(client, ctx(s.T()), "session.answer", workflowParams{Session: ref, Interaction: string(request.Interaction), Answer: map[string]any{"action": "accept"}}, nil)
			var public *transport.Error
			s.Require().ErrorAs(err, &public)
			s.Equal("invalid_argument", public.Code)
			var answers map[string]any
			if kind == "codex" {
				answers = map[string]any{"format": map[string]any{"answers": []any{"json", "free text"}}}
			} else {
				answers = map[string]any{"Which format?": "json, free text"}
			}
			answer := map[string]any{"action": "accept", "content": map[string]any{"answers": answers}}
			type result struct {
				receipt runtime.InteractionReceipt
				err     error
			}
			results := make(chan result, 2)
			start := make(chan struct{})
			for range 2 {
				go func() {
					<-start
					var receipt runtime.InteractionReceipt
					err := workflowHTTP(newHTTPClient(s.T(), s.url, ""), ctx(s.T()), "session.answer", workflowParams{Session: ref, Interaction: string(request.Interaction), Answer: answer}, &receipt)
					results <- result{receipt, err}
				}()
			}
			close(start)
			successes := 0
			for range 2 {
				result := <-results
				if result.err == nil {
					successes++
					s.Equal(runtime.Yes(), result.receipt.Evidence.Sent)
					s.Nil(result.receipt.Evidence.NativeAccepted)
				} else {
					s.Require().ErrorAs(result.err, &public)
					s.Equal("conflict", public.Code)
				}
			}
			s.Equal(1, successes)
			answered = true
		}
		if state, ok := e.Payload().(events.StateUpdate); ok && state.State != nil && *state.State == events.Idle && answered {
			break
		}
	}
	s.Require().NoError(scan.Err())
	s.True(progressed, "waiting for the UI must not block Receive")
	s.True(answered)
	s.call("session.close", workflowParams{Session: ref}, nil)
	s.call("runtime.stop", workflowParams{Name: "agent"}, nil)
}

func TestNativePermissionDecisions(t *testing.T) {
	for _, scenario := range []struct{ kind, mode string }{{"codex", "deny"}, {"claude", "deny"}, {"claude", "update"}, {"codex", "restricted"}} {
		t.Run(scenario.kind+"/"+scenario.mode, func(t *testing.T) {
			svc, _, _ := setup(t, nil)
			session := openNative(t, svc, scenario.kind, "ticket02-permission-"+scenario.mode, "permission")
			ref := resource.SessionRef{SessionID: session.Status.SessionID}
			sub, err := svc.SubscribeEvents(ref)
			require.NoError(t, err)
			defer sub.Close()
			_, err = svc.SendPrompt(ctx(t), ref, runtime.Input{Content: []events.ContentBlock{events.Text("hello")}})
			require.NoError(t, err)
			answered := false
			for {
				e, err := nextSubscription(sub, ctx(t))
				require.NoError(t, err)
				if p, ok := e.Payload().(events.InteractionRequested); ok {
					require.Equal(t, events.InteractionPermission, p.Request.Kind)
					require.NotEmpty(t, p.Request.ToolCallID)
					require.NotEmpty(t, p.Request.Options)
					if scenario.mode == "restricted" && p.Request.Title == "item/commandExecution/requestApproval" {
						require.Len(t, p.Request.Options, 1, "only native offered options may be exposed")
						_, err = svc.AnswerInteraction(ctx(t), ref, string(p.Interaction), map[string]any{"outcome": "selected", "optionId": "allow-once"})
						require.Error(t, err)
					}
					_, err = svc.AnswerInteraction(ctx(t), ref, string(p.Interaction), map[string]any{"outcome": "selected", "optionId": "not-offered"})
					require.Error(t, err)
					answer := map[string]any{"outcome": "selected", "optionId": "reject-once"}
					if scenario.mode == "update" {
						// The SDK PermissionAnswer accepts the selected option only.
						// Reject legacy input mutation before sending the valid answer.
						_, err = svc.AnswerInteraction(ctx(t), ref, string(p.Interaction), map[string]any{
							"outcome": "selected", "optionId": "allow-once",
							"updatedInput": map[string]any{"command": "echo corrected", "future": json.Number("9007199254740993")},
						})
						require.ErrorIs(t, err, runtime.ErrInvalidArgument)
						answer["optionId"] = "allow-once"
					}
					receipt, err := svc.AnswerInteraction(ctx(t), ref, string(p.Interaction), answer)
					require.NoError(t, err)
					require.Equal(t, runtime.Yes(), receipt.Evidence.Sent)
					require.Nil(t, receipt.Evidence.NativeAccepted)
					answered = true
				}
				if state, ok := e.Payload().(events.StateUpdate); ok && state.State != nil && *state.State == events.Idle && answered {
					break
				}
			}
		})
	}
}
