package catalog_test

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/zoumo/mass/mass-go/internal/testutil"
	rt "github.com/zoumo/mass/mass-go/pkg/runtime"
)

func (s *lifecycleSuite) TestSubmissionEvidenceAndAdmission() {
	for _, kind := range []string{"codex", "claude"} {
		s.Run(kind, func() {
			var frames []string
			if kind == "codex" {
				frames = append(codexInit(), codexThread(2, "native-A")...)
				frames = append(frames, codexTurn(3, "native-A")...)
			} else {
				frames = claudeFrames("native-A", false)
			}
			_, worker := s.register(kind, s.singleScenario(kind, frames))
			options := rt.SessionOptions{}
			if kind == "claude" {
				text := "hello"
				options.InitialInput = &text
			}
			session := testutil.OpenSession(s.T(), worker, context.Background(), options)
			var receipt rt.Submission
			var err error
			if kind == "codex" {
				receipt, err = session.Submit(context.Background(), rt.TextInput("hello"))
				s.Require().NoError(err)
			} else {
				receipt, err = session.GetSubmission(session.Snapshot().Foreground.OperationRef)
				s.Require().NoError(err)
			}
			s.NotEmpty(receipt.OperationRef)
			s.Equal(session.Key(), receipt.Key)
			s.Equal("admitted", receipt.Outcome)
			s.Equal(rt.Known(true), receipt.HandedToSDK)
			s.Equal("unknown", receipt.NativeRecorded.State)
			s.Equal("unknown", receipt.Completed.State)
			if kind == "codex" {
				s.Equal("different-work-id", receipt.WorkRef)
				s.Equal(rt.Known(true), receipt.NativeAcknowledged)
			} else {
				s.Empty(receipt.WorkRef)
				s.Equal("not_supported", receipt.NativeAcknowledged.State)
				s.Equal(rt.Known(true), receipt.WriteCompleted)
			}
			_, err = session.Submit(context.Background(), rt.TextInput("hello"))
			s.ErrorIs(err, rt.ErrBusy)
			s.Equal("running", session.Snapshot().Foreground.State)
			s.Equal("unknown", session.Snapshot().Background.Completeness)
			data, err := json.Marshal(receipt)
			s.Require().NoError(err)
			s.NotContains(string(data), "TurnID")
		})
	}
}

func codexFinished(id, status string) string { return codexFinishedFor("native-A", id, status) }
func codexFinishedFor(session, id, status string) string {
	return fmt.Sprintf(`s2c {"method":"turn/completed","params":{"threadId":%q,"turn":{"id":%q,"status":%q,"items":[],"error":null}}}`, session, id, status)
}
func (s *lifecycleSuite) TestCompletionBeforeAcknowledgement() {
	frames := append(codexInit(), codexThread(2, "native-A")...)
	turn := codexTurn(3, "native-A")
	frames = append(frames, turn[0], codexFinished("different-work-id", "completed"), "delay=150 "+turn[1])
	_, worker := s.register("codex", s.singleScenario("codex", frames))
	session := testutil.OpenSession(s.T(), worker, context.Background(), rt.SessionOptions{})
	receipt, err := session.Submit(context.Background(), rt.TextInput("hello"))
	s.Require().NoError(err)
	s.Equal(rt.Known(true), receipt.Completed)
	s.Equal("idle", session.Snapshot().Foreground.State)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	for {
		e, err := session.Next(ctx)
		s.Require().NoError(err)
		if e.Kind == rt.TurnCompleted {
			s.Equal(receipt.OperationRef, e.OperationRef)
			s.Equal(receipt.WorkRef, e.WorkRef)
			break
		}
	}
}

func (s *lifecycleSuite) TestCancellationOrderingAndFixedTarget() {
	for _, kind := range []string{"codex", "claude"} {
		for _, terminalFirst := range []bool{false, true} {
			s.Run(fmt.Sprintf("%s/terminalFirst=%v", kind, terminalFirst), func() {
				var frames []string
				var ack, terminal, startCancel string
				if kind == "codex" {
					frames = append(codexInit(), codexThread(2, "native-A")...)
					frames = append(frames, codexTurn(3, "native-A")...)
					startCancel = `c2s {"id":4,"method":"turn/interrupt","params":{"threadId":"native-A","turnId":"different-work-id"}}`
					ack = `s2c {"id":4,"result":{}}`
					terminal = codexFinished("different-work-id", "interrupted")
				} else {
					frames = claudeFrames("native-A", false)
					startCancel = `client control {"type":"control_request","request_id":"interrupt","request":{"subtype":"interrupt"}}`
					ack = `server control {"type":"control_response","response":{"subtype":"success","request_id":"interrupt","response":{}}}`
					terminal = `server protocol {"type":"result","subtype":"error_during_execution","session_id":"native-A","is_error":true}`
				}
				frames = append(frames, startCancel)
				if terminalFirst {
					frames = append(frames, terminal, "delay=200 "+ack)
				} else {
					frames = append(frames, ack, "delay=200 "+terminal)
				}
				if kind == "codex" {
					next := codexTurn(5, "native-A")
					next[1] = strings.ReplaceAll(next[1], "different-work-id", "later-work")
					frames = append(frames, next[:2]...)
				} else {
					frames = append(frames, `client protocol {"type":"user","message":{"role":"user","content":"hello"},"parent_tool_use_id":null,"session_id":""}`)
				}
				_, worker := s.register(kind, s.singleScenario(kind, frames))
				options := rt.SessionOptions{}
				if kind == "claude" {
					text := "hello"
					options.InitialInput = &text
				}
				session := testutil.OpenSession(s.T(), worker, context.Background(), options)
				var receipt rt.Submission
				if kind == "codex" {
					var err error
					receipt, err = session.Submit(context.Background(), rt.TextInput("hello"))
					s.Require().NoError(err)
				} else {
					var err error
					receipt, err = session.GetSubmission(session.Snapshot().Foreground.OperationRef)
					s.Require().NoError(err)
				}
				preCanceled, stop := context.WithCancel(context.Background())
				stop()
				_, err := session.Cancel(preCanceled, receipt.OperationRef)
				s.ErrorIs(err, context.Canceled)
				result := make(chan rt.Cancellation, 1)
				go func() { r, _ := session.Cancel(context.Background(), receipt.OperationRef); result <- r }()
				s.Require().Eventually(func() bool { return session.Snapshot().Foreground.State == "cancelling" }, time.Second, time.Millisecond)
				if terminalFirst {
					s.Require().Eventually(func() bool { r, _ := session.GetSubmission(receipt.OperationRef); return r.Completed.State == "known" }, time.Second, time.Millisecond)
				}
				_, err = session.Submit(context.Background(), rt.TextInput("hello"))
				s.ErrorIs(err, rt.ErrBusy)
				canceled := <-result
				s.Equal(rt.Known(true), canceled.NativeAcknowledged)
				s.Equal(receipt.OperationRef, canceled.TargetOperationRef)
				if !terminalFirst {
					s.Equal("unknown", canceled.TargetFinished.State)
					s.Equal("cancelling", session.Snapshot().Foreground.State)
				}
				s.Require().Eventually(func() bool { return session.Snapshot().Foreground.State == "idle" }, time.Second, time.Millisecond)
				final, err := session.GetCancellation(canceled.OperationRef)
				s.Require().NoError(err)
				s.Equal(rt.Known(true), final.TargetFinished)
				next, err := session.Submit(context.Background(), rt.TextInput("hello"))
				s.Require().NoError(err)
				s.NotEqual(receipt.OperationRef, next.OperationRef)
				// Reusing the cancellation cannot issue another session-wide interrupt.
				_, err = session.Cancel(context.Background(), receipt.OperationRef)
				s.ErrorIs(err, rt.ErrConflict)
				s.Equal("running", session.Snapshot().Foreground.State)
				s.Equal("attached", session.Snapshot().Attachment)
				s.Equal("unknown", session.Snapshot().Background.Completeness)
			})
		}
	}
}

func (s *lifecycleSuite) TestRejectionAndUnknownSubmission() {
	for _, reject := range []bool{true, false} {
		s.Run(fmt.Sprintf("rejected=%v", reject), func() {
			frames := append(codexInit(), codexThread(2, "native-A")...)
			turn := codexTurn(3, "native-A")
			frames = append(frames, turn[0])
			if reject {
				frames = append(frames, `s2c {"id":3,"error":{"code":-32000,"message":"synthetic-sensitive-marker"}}`)
			} else {
				frames = append(frames, "delay=250 "+turn[1])
			}
			_, worker := s.register("codex", s.singleScenario("codex", frames))
			session := testutil.OpenSession(s.T(), worker, context.Background(), rt.SessionOptions{})
			ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
			defer cancel()
			receipt, err := session.Submit(ctx, rt.TextInput("hello"))
			s.Require().Error(err)
			s.NotContains(err.Error(), "synthetic-sensitive-marker")
			s.Equal("operation", receipt.Failure.Scope)
			s.Equal(receipt.OperationRef, receipt.Failure.OperationRef)
			if reject {
				s.Equal("rejected", receipt.Outcome)
				s.Equal("native_rejected", receipt.Failure.Category)
				s.Equal("idle", session.Snapshot().Foreground.State)
			} else {
				s.Equal("unknown", receipt.Outcome)
				s.ErrorIs(err, rt.ErrOutcomeUnknown)
				time.Sleep(300 * time.Millisecond)
				final, err := session.GetSubmission(receipt.OperationRef)
				s.Require().NoError(err)
				s.Equal("unknown", final.Outcome)
				s.Equal("unknown", final.NativeAcknowledged.State)
				s.Empty(final.WorkRef)
				cancelCtx, stopCancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
				cancellation, cancelErr := session.Cancel(cancelCtx, receipt.OperationRef)
				s.ErrorIs(cancelErr, rt.ErrOutcomeUnknown)
				s.NoError(cancelCtx.Err())
				stopCancel()
				s.Empty(cancellation.WorkRef)
				s.Equal("unknown", cancellation.NativeAcknowledged.State)
				_, err = session.Submit(context.Background(), rt.TextInput("hello"))
				s.ErrorIs(err, rt.ErrBusy)
			}
		})
	}
}

func (s *lifecycleSuite) TestCancelWaitsForOriginalNativeIdentity() {
	frames := append(codexInit(), codexThread(2, "native-A")...)
	turn := codexTurn(3, "native-A")
	frames = append(frames, turn[0], "delay=200 "+turn[1],
		`c2s {"id":4,"method":"turn/interrupt","params":{"threadId":"native-A","turnId":"different-work-id"}}`,
		`s2c {"id":4,"result":{}}`, codexFinished("different-work-id", "interrupted"))
	_, worker := s.register("codex", s.singleScenario("codex", frames))
	session := testutil.OpenSession(s.T(), worker, context.Background(), rt.SessionOptions{})
	submitted := make(chan rt.Submission, 1)
	go func() { r, _ := session.Submit(context.Background(), rt.TextInput("hello")); submitted <- r }()
	s.Require().Eventually(func() bool { return session.Snapshot().Foreground.OperationRef != "" }, time.Second, time.Millisecond)
	ref := session.Snapshot().Foreground.OperationRef
	s.Empty(session.Snapshot().Foreground.WorkRef)
	inFlight, err := session.GetSubmission(ref)
	s.Require().NoError(err)
	s.Equal("unknown", inFlight.HandedToSDK.State) // Reservation is not SDK evidence.
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	canceled, err := session.Cancel(ctx, ref)
	s.Require().NoError(err)
	receipt := <-submitted
	s.Equal(ref, receipt.OperationRef)
	s.Equal(receipt.WorkRef, canceled.WorkRef)
	s.Equal(rt.Known(true), canceled.NativeAcknowledged)
	s.Require().Eventually(func() bool { return session.Snapshot().Foreground.State == "idle" }, time.Second, time.Millisecond)
}

func (s *lifecycleSuite) TestConcurrentSessionsAndBusyAdmission() {
	frames := append(codexInit(), codexThread(2, "native-A")...)
	frames = append(frames, codexThread(3, "native-B")...)
	a, b := codexTurn(4, "native-A"), codexTurn(5, "native-B")
	frames = append(frames, a[0],
		`s2c {"method":"synthetic/first-request-observed","params":{"threadId":"native-A"}}`,
		b[0], b[1],
		`c2s {"id":6,"method":"turn/interrupt","params":{"threadId":"native-B","turnId":"different-work-id"}}`,
		a[1], `s2c {"id":6,"result":{}}`)
	_, worker := s.register("codex", s.singleScenario("codex", frames))
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	first := testutil.OpenSession(s.T(), worker, ctx, rt.SessionOptions{})
	second := testutil.OpenSession(s.T(), worker, ctx, rt.SessionOptions{})
	returned := make(chan rt.Submission, 1)
	go func() { r, _ := first.Submit(ctx, rt.TextInput("hello")); returned <- r }()
	// The public diagnostic proves the strict peer consumed the first request.
	// Local reservation alone does not fix concurrent SDK request allocation order.
	for {
		e, err := first.Next(ctx)
		s.Require().NoError(err)
		if e.Diagnostic.NativeKind == "synthetic/first-request-observed" {
			break
		}
	}
	_, err := first.Submit(ctx, rt.TextInput("hello"))
	s.ErrorIs(err, rt.ErrBusy)
	receipt, err := second.Submit(ctx, rt.TextInput("hello"))
	s.Require().NoError(err)
	s.NotEmpty(receipt.WorkRef)
	select {
	case <-returned:
		s.Fail("first call returned before peer released its acknowledgement")
	default:
	}
	_, err = second.Cancel(ctx, receipt.OperationRef)
	s.Require().NoError(err)
	original := <-returned
	s.Equal(rt.Known(true), original.NativeAcknowledged)
}

func (s *lifecycleSuite) TestInputValidationAndWaitCancellation() {
	frames := append(codexInit(), codexThread(2, "native-A")...)
	frames = append(frames, codexTurn(3, "native-A")...)
	_, worker := s.register("codex", s.singleScenario("codex", frames))
	session := testutil.OpenSession(s.T(), worker, context.Background(), rt.SessionOptions{})
	for _, tc := range []struct {
		name  string
		input rt.InputCommand
		want  error
	}{
		{"missing", rt.InputCommand{}, rt.ErrInvalidInput},
		{"media", rt.InputCommand{Blocks: []rt.ContentBlock{{Kind: "media_ref"}}}, rt.ErrUnsupported},
		{"options", rt.InputCommand{Options: map[string]string{"steer": "true"}}, rt.ErrUnsupported},
		{"nil_text", rt.InputCommand{Blocks: []rt.ContentBlock{{Kind: "text"}}}, rt.ErrInvalidInput},
	} {
		s.Run(tc.name, func() {
			r, err := session.Submit(context.Background(), tc.input)
			s.ErrorIs(err, tc.want)
			s.Equal("rejected", r.Outcome)
			s.Empty(r.OperationRef)
		})
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := session.Next(ctx)
	s.ErrorIs(err, context.Canceled)
	_, err = session.Submit(ctx, rt.TextInput("hello"))
	s.ErrorIs(err, context.Canceled)
	s.Equal("idle", session.Snapshot().Foreground.State)
	_, err = session.Submit(context.Background(), rt.TextInput("hello"))
	s.Require().NoError(err)
	s.Equal("attached", session.Snapshot().Attachment)
}

// This case intentionally exceeds the unmodified pinned SDK's 30-second local
// CallTimeout. The late response is discarded by the SDK; no repair is claimed.
func (s *lifecycleSuite) TestSDKLocalTimeoutRetainsUnknownOutcome() {
	frames := append(codexInit(), codexThread(2, "native-A")...)
	turn := codexTurn(3, "native-A")
	frames = append(frames, turn[0], "delay=30200 "+turn[1])
	_, worker := s.register("codex", s.singleScenario("codex", frames))
	session := testutil.OpenSession(s.T(), worker, context.Background(), rt.SessionOptions{})
	ctx, cancel := context.WithTimeout(context.Background(), 35*time.Second)
	defer cancel()
	receipt, err := session.Submit(ctx, rt.TextInput("hello"))
	s.ErrorIs(err, rt.ErrOutcomeUnknown)
	s.NoError(ctx.Err())
	s.Equal("unknown", receipt.Outcome)
	time.Sleep(300 * time.Millisecond)
	final, err := session.GetSubmission(receipt.OperationRef)
	s.Require().NoError(err)
	s.Equal("unknown", final.Outcome)
	s.Equal("unknown", final.NativeAcknowledged.State)
	s.Empty(final.WorkRef)
	_, err = session.Submit(ctx, rt.TextInput("hello"))
	s.ErrorIs(err, rt.ErrBusy)
}

func (s *lifecycleSuite) TestDisconnectAfterHandoffDoesNotResend() {
	frames := append(codexInit(), codexThread(2, "native-A")...)
	frames = append(frames, codexTurn(3, "native-A")[0], "disconnect")
	_, worker := s.register("codex", s.singleScenario("codex", frames))
	session := testutil.OpenSession(s.T(), worker, context.Background(), rt.SessionOptions{})
	receipt, err := session.Submit(context.Background(), rt.TextInput("hello"))
	s.ErrorIs(err, rt.ErrOutcomeUnknown)
	s.Equal("unknown", receipt.Outcome)
	s.Equal(rt.Known(true), receipt.HandedToSDK)
	s.Empty(receipt.WorkRef)
	final, err := session.GetSubmission(receipt.OperationRef)
	s.Require().NoError(err)
	s.Equal("unknown", final.NativeAcknowledged.State)
	_, err = session.Submit(context.Background(), rt.TextInput("hello"))
	s.Error(err)
	s.Equal("unknown", session.Snapshot().Foreground.State)
}

func (s *lifecycleSuite) TestForegroundIgnoresIdleAndBackgroundObservations() {
	frames := append(codexInit(), codexThread(2, "native-A")...)
	frames = append(frames, codexTurn(3, "native-A")...)
	frames = append(frames,
		`s2c {"method":"thread/status/changed","params":{"threadId":"native-A","status":{"type":"idle"}}}`,
		`s2c {"method":"synthetic/background","params":{"threadId":"native-A","taskId":"background-1","state":"running"}}`,
		codexFinished("different-work-id", "completed"))
	_, worker := s.register("codex", s.singleScenario("codex", frames))
	session := testutil.OpenSession(s.T(), worker, context.Background(), rt.SessionOptions{})
	receipt, err := session.Submit(context.Background(), rt.TextInput("hello"))
	s.Require().NoError(err)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	seenBackground := false
	for {
		e, err := session.Next(ctx)
		s.Require().NoError(err)
		if e.Diagnostic.NativeKind == "synthetic/background" {
			seenBackground = true
			s.Equal(rt.Unknown, e.Kind)
		}
		if e.Kind == rt.TurnCompleted {
			s.Equal(receipt.OperationRef, e.OperationRef)
			break
		}
	}
	s.True(seenBackground)
	s.Equal("idle", session.Snapshot().Foreground.State)
	s.Equal("attached", session.Snapshot().Attachment)
	// Background conversion is a separate slice: an unrecognized observation is
	// unknown, never a fabricated empty/finished background inventory.
	s.Equal("unknown", session.Snapshot().Background.Completeness)
}

func (s *lifecycleSuite) TestNativeRecordingRequiresMatchedWork() {
	frames := append(codexInit(), codexThread(2, "native-A")...)
	frames = append(frames, codexTurn(3, "native-A")...)
	frames = append(frames,
		`s2c {"method":"item/completed","params":{"threadId":"native-A","turnId":"unrelated-work","completedAtMs":2,"item":{"type":"userMessage","id":"unrelated-echo","content":[{"type":"text","text":"hello","text_elements":[]}]}}}`,
		`c2s {"id":4,"method":"turn/interrupt","params":{"threadId":"native-A","turnId":"different-work-id"}}`,
		`s2c {"id":4,"result":{}}`,
		`s2c {"method":"item/completed","params":{"threadId":"native-A","turnId":"different-work-id","completedAtMs":2,"item":{"type":"userMessage","id":"matched-echo","content":[{"type":"text","text":"hello","text_elements":[]}]}}}`,
		codexFinished("different-work-id", "interrupted"))
	_, worker := s.register("codex", s.singleScenario("codex", frames))
	session := testutil.OpenSession(s.T(), worker, context.Background(), rt.SessionOptions{})
	receipt, err := session.Submit(context.Background(), rt.TextInput("hello"))
	s.Require().NoError(err)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	for {
		e, err := session.Next(ctx)
		s.Require().NoError(err)
		if e.Message != nil && e.Message.Role == "user" {
			s.Empty(e.OperationRef)
			break
		}
	}
	before, err := session.GetSubmission(receipt.OperationRef)
	s.Require().NoError(err)
	s.Equal("unknown", before.NativeRecorded.State)
	_, err = session.Cancel(ctx, receipt.OperationRef)
	s.Require().NoError(err)
	s.Require().Eventually(func() bool {
		r, _ := session.GetSubmission(receipt.OperationRef)
		return r.NativeRecorded.State == "known"
	}, time.Second, time.Millisecond)
	after, err := session.GetSubmission(receipt.OperationRef)
	s.Require().NoError(err)
	s.Equal(rt.Known(true), after.NativeRecorded)
	*after.NativeRecorded.Value = false
	again, err := session.GetSubmission(receipt.OperationRef)
	s.Require().NoError(err)
	s.Equal(rt.Known(true), again.NativeRecorded)
}

func (s *lifecycleSuite) TestUnknownSessionWideInterruptKeepsAdmissionClosed() {
	frames := claudeFrames("native-A", false)
	frames = append(frames,
		`client control {"type":"control_request","request_id":"interrupt","request":{"subtype":"interrupt"}}`,
		`server protocol {"type":"result","subtype":"success","session_id":"native-A","is_error":false}`,
		`delay=200 server control {"type":"control_response","response":{"subtype":"success","request_id":"interrupt","response":{}}}`)
	_, worker := s.register("claude", s.singleScenario("claude", frames))
	text := "hello"
	session := testutil.OpenSession(s.T(), worker, context.Background(), rt.SessionOptions{InitialInput: &text})
	ref := session.Snapshot().Foreground.OperationRef
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	receipt, err := session.Cancel(ctx, ref)
	s.ErrorIs(err, rt.ErrOutcomeUnknown)
	s.Equal(rt.Known(true), receipt.TargetFinished)
	s.Equal("unknown", receipt.NativeAcknowledged.State)
	time.Sleep(250 * time.Millisecond)
	// A discarded late ACK cannot prove that the unaddressed native interrupt is
	// settled. A later input stays protected even though its predecessor finished.
	_, err = session.Submit(context.Background(), rt.TextInput("hello"))
	s.ErrorIs(err, rt.ErrBusy)
	s.Equal("unknown", session.Snapshot().Foreground.State)
	final, err := session.GetCancellation(receipt.OperationRef)
	s.Require().NoError(err)
	s.Equal("unknown", final.NativeAcknowledged.State)
}
