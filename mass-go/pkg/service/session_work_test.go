package service_test

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/suite"

	"github.com/zoumo/mass/mass-go/pkg/events"
	"github.com/zoumo/mass/mass-go/pkg/resource"
	"github.com/zoumo/mass/mass-go/pkg/runtime"
	"github.com/zoumo/mass/mass-go/pkg/service"
	"github.com/zoumo/mass/mass-go/pkg/store"
	"github.com/zoumo/mass/mass-go/pkg/store/file"
	"github.com/zoumo/mass/mass-go/pkg/transport"
)

type workDriver struct {
	runtime.Driver
	pipe     *events.EventPipe
	entered  chan context.Context
	release  chan struct{}
	calls    atomic.Int32
	source   events.Source
	onFinish func() error
}

func (d *workDriver) Events() (events.Stream, error) { return d.pipe, nil }
func (d *workDriver) Start(context.Context) (runtime.Capabilities, error) {
	return runtime.Capabilities{}, nil
}

func (d *workDriver) OpenSession(context.Context, runtime.Address, runtime.SessionSpec) (runtime.DriverSession, error) {
	d.source = events.NewSource()
	return runtime.DriverSession{SessionID: "original", Source: d.source}, nil
}
func (d *workDriver) CloseSession(context.Context, string) error { return nil }
func (d *workDriver) Stop(context.Context) error                 { d.pipe.End(nil); return nil }
func (d *workDriver) SendPrompt(ctx context.Context, id string, in runtime.Input) (runtime.InputReceipt, error) {
	d.calls.Add(1)
	d.entered <- ctx
	select {
	case <-d.release:
	case <-ctx.Done():
		return runtime.InputReceipt{}, ctx.Err()
	}
	if d.onFinish != nil {
		if err := d.onFinish(); err != nil {
			return runtime.InputReceipt{}, err
		}
	}
	no := false
	return runtime.InputReceipt{Submission: "work-1", Acceptance: runtime.Acceptance{Sent: runtime.Yes(), NativeAccepted: &no}}, nil
}

type workFactory struct{ driver *workDriver }

func (f workFactory) Validate(runtime.Config) error                      { return nil }
func (f workFactory) New(string, runtime.Config) (runtime.Driver, error) { return f.driver, nil }

type SessionWorkSuite struct {
	suite.Suite
	svc    *service.Service
	driver *workDriver
	ref    resource.SessionRef
}

func (s *SessionWorkSuite) SetupTest() {
	st, err := file.Open(s.T().TempDir(), false)
	s.Require().NoError(err)
	s.T().Cleanup(func() { s.Require().NoError(st.Close()) })
	d := &workDriver{pipe: events.NewEventPipe(16), entered: make(chan context.Context, 64), release: make(chan struct{})}
	s.driver = d
	s.svc = service.New(st, st, runtime.NewRegistry(map[string]runtime.Factory{"fixture": workFactory{d}}))
	s.T().Cleanup(func() {
		select {
		case <-d.release:
		default:
			close(d.release)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		s.Require().NoError(s.svc.Shutdown(ctx))
	})
	_, err = s.svc.CreateRuntime(resource.Runtime{Metadata: resource.Metadata{Name: "agent"}, Spec: resource.RuntimeSpec{Type: "fixture", Config: runtime.Config{}}})
	s.Require().NoError(err)
	_, err = s.svc.StartRuntime(context.Background(), "agent")
	s.Require().NoError(err)
	r, err := s.svc.CreateSession(context.Background(), resource.Session{Metadata: resource.Metadata{Namespace: "one", Name: "demo"}, Spec: resource.SessionSpec{RuntimeRef: "agent", SessionSpec: runtime.SessionSpec{Cwd: "/remote"}}})
	s.Require().NoError(err)
	s.ref = resource.SessionRef{SessionID: r.Status.SessionID}
}

func (s *SessionWorkSuite) TestAbandonedWaitKeepsAcceptedInputOwnedByService() {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := s.svc.SendPrompt(ctx, resource.SessionRef{Namespace: "one", Name: "demo"}, runtime.Input{Content: []events.ContentBlock{events.Text("hello")}})
		done <- err
	}()
	var owner context.Context
	select {
	case owner = <-s.driver.entered:
	case <-time.After(time.Second):
		s.FailNow("input was not dispatched")
	}
	cancel()
	select {
	case err := <-done:
		var public *runtime.OperationError
		s.Require().ErrorAs(err, &public)
		s.Equal("outcome_unknown", public.Code)
	case <-time.After(time.Second):
		s.FailNow("caller wait did not end")
	}
	s.Require().NoError(owner.Err(), "request cancellation must not cancel the admitted native input")
	close(s.driver.release)
	s.Equal(int32(1), s.driver.calls.Load(), "no implicit resend")
}

func (s *SessionWorkSuite) TestReceiptPreservesUnknownFalseAndTrue() {
	close(s.driver.release)
	receipt, err := s.svc.SendPrompt(context.Background(), s.ref, runtime.Input{Content: []events.ContentBlock{events.Text("hello")}})
	s.Require().NoError(err)
	raw, err := json.Marshal(receipt)
	s.Require().NoError(err)
	s.JSONEq(`{"submission":"work-1","acceptance":{"sent":true,"nativeAccepted":false}}`, string(raw))
}
func TestSessionWorkSuite(t *testing.T) { suite.Run(t, new(SessionWorkSuite)) }

func (s *SessionWorkSuite) TestInvalidInputRejectedBeforeDispatch() {
	close(s.driver.release)
	for _, input := range []runtime.Input{{}, {Content: []events.ContentBlock{{Type: "image"}}}, {Content: []events.ContentBlock{events.Text(string([]byte{0xff}))}}} {
		_, err := s.svc.SendPrompt(context.Background(), s.ref, input)
		s.Require().ErrorIs(err, runtime.ErrInvalidArgument)
	}
	s.Equal(int32(0), s.driver.calls.Load())
}

func (d *workDriver) AnswerInteraction(ctx context.Context, id, ref string, answer any) (runtime.Acceptance, error) {
	receipt, err := d.SendPrompt(ctx, id, runtime.Input{})
	return receipt.Acceptance, err
}

func (d *workDriver) Cancel(ctx context.Context, id string) (runtime.CancelReceipt, error) {
	_, err := d.SendPrompt(ctx, id, runtime.Input{})
	return runtime.CancelReceipt{Target: "work-1", Result: "controlAcknowledged"}, err
}

func (s *SessionWorkSuite) TestAnswerAndCancelOwnAdmittedOperationsAfterCallerLeaves() {
	for _, operation := range []string{"answer", "cancel"} {
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan error, 1)
		go func() {
			var err error
			if operation == "answer" {
				_, err = s.svc.AnswerInteraction(ctx, s.ref, "original-interaction", nil)
			} else {
				_, err = s.svc.Cancel(ctx, s.ref)
			}
			done <- err
		}()
		var owner context.Context
		select {
		case owner = <-s.driver.entered:
		case <-time.After(time.Second):
			s.FailNow("operation was not dispatched")
		}
		cancel()
		select {
		case err := <-done:
			var public *runtime.OperationError
			s.Require().ErrorAs(err, &public)
			s.Equal("outcome_unknown", public.Code)
		case <-time.After(time.Second):
			s.FailNow("caller wait did not end")
		}
		s.Require().NoError(owner.Err())
	}
	close(s.driver.release)
}

func (s *SessionWorkSuite) TestNonJSONAnswersAndInvalidReferencesRejectedBeforeDispatch() {
	close(s.driver.release)
	cycle := map[string]any{}
	cycle["self"] = cycle
	for _, answer := range []any{math.NaN(), math.Inf(1), cycle, []byte("coercion"), struct{ Value string }{"coercion"}} {
		_, err := s.svc.AnswerInteraction(context.Background(), s.ref, "interaction", answer)
		s.Require().ErrorIs(err, runtime.ErrInvalidArgument)
	}
	for _, ref := range []string{"", string([]byte{0xff})} {
		_, err := s.svc.AnswerInteraction(context.Background(), s.ref, ref, nil)
		s.Require().ErrorIs(err, runtime.ErrInvalidArgument)
	}
	s.Equal(int32(0), s.driver.calls.Load())
}

func (s *SessionWorkSuite) TestHTTPDisconnectedInputStillPublishesCommittedEvent() {
	h, err := transport.New(s.svc, transport.Options{Token: "test-token"})
	s.Require().NoError(err)
	s.T().Cleanup(h.Shutdown)
	server := httptest.NewServer(h)
	s.T().Cleanup(server.Close)
	observed, cancelObserve := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancelObserve()
	watch, err := http.NewRequestWithContext(observed, http.MethodGet, server.URL+"/apis/v1/sessions/original/events/stream", http.NoBody)
	s.Require().NoError(err)
	watch.Header.Set("Authorization", "Bearer test-token")
	response, err := http.DefaultClient.Do(watch)
	s.Require().NoError(err)
	defer response.Body.Close()
	s.Require().Equal(200, response.StatusCode)
	scanner := bufio.NewScanner(response.Body)
	for scanner.Scan() {
		if scanner.Text() == "event: subscription.ready" {
			break
		}
	}
	s.Require().NoError(scanner.Err())
	event, err := events.NewEvent(events.EventBase{EventID: "01234567-89ab-4cde-8fab-0123456789ab", Namespace: "one", SessionName: "demo", SessionID: "original", ItemID: "after-disconnect", Time: time.Now()}, events.AgentMessage{MessageID: "after-disconnect", Content: []events.ContentBlock{events.Text("completed after caller left")}}, s.driver.source)
	s.Require().NoError(err)
	s.driver.onFinish = func() error { return s.driver.pipe.Send(context.Background(), event) }
	waiting, cancelWait := context.WithCancel(context.Background())
	defer cancelWait()
	request, err := http.NewRequestWithContext(waiting, http.MethodPost, server.URL+"/apis/v1/namespaces/one/sessions/demo/inputs", strings.NewReader(`{"content":[{"type":"text","text":"hello"}]}`))
	s.Require().NoError(err)
	request.Header.Set("Authorization", "Bearer test-token")
	request.Header.Set("Content-Type", "application/json")
	done := make(chan error, 1)
	go func() {
		r, err := http.DefaultClient.Do(request)
		if r != nil {
			io.Copy(io.Discard, r.Body)
			r.Body.Close()
		}
		done <- err
	}()
	var owner context.Context
	select {
	case owner = <-s.driver.entered:
	case <-observed.Done():
		s.FailNow("request not admitted")
	}
	cancelWait()
	select {
	case err := <-done:
		s.Require().ErrorIs(err, context.Canceled)
	case <-observed.Done():
		s.FailNow("HTTP wait did not end")
	}
	s.Require().NoError(owner.Err())
	close(s.driver.release)
	found := false
	for scanner.Scan() {
		if strings.Contains(scanner.Text(), "after-disconnect") {
			found = true
			break
		}
	}
	s.Require().True(found, "original operation must publish after disconnect: %v", scanner.Err())
	page, err := s.svc.ListHistoryEvents(s.ref, store.HistoryQuery{})
	s.Require().NoError(err)
	s.Require().Len(page.Events, 1)
	s.Equal("agent.message", page.Events[0].Type())
	s.Equal(int32(1), s.driver.calls.Load())
}

func (s *SessionWorkSuite) TestExpiredCallerDoesNotDispatchWork() {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := s.svc.SendPrompt(ctx, s.ref, runtime.Input{Content: []events.ContentBlock{events.Text("hello")}})
	s.Require().ErrorIs(err, context.Canceled)
	_, err = s.svc.AnswerInteraction(ctx, s.ref, "interaction", nil)
	s.Require().ErrorIs(err, context.Canceled)
	_, err = s.svc.Cancel(ctx, s.ref)
	s.Require().ErrorIs(err, context.Canceled)
	s.Zero(s.driver.calls.Load())
}

func (s *SessionWorkSuite) TestCancelAcknowledgmentDoesNotTerminateObservedWork() {
	observation, err := s.svc.SubscribeEvents(s.ref)
	s.Require().NoError(err)
	defer observation.Close()
	state := events.Running
	event, err := events.NewEvent(events.EventBase{EventID: "01234567-89ab-4cde-8fab-0123456789ac", Namespace: "one", SessionName: "demo", SessionID: "original", Time: time.Now()}, events.StateUpdate{State: &state}, s.driver.source.WithWork("native-work"))
	s.Require().NoError(err)
	s.Require().NoError(s.driver.pipe.Send(context.Background(), event))
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_, err = observation.Next(ctx)
	s.Require().NoError(err)
	close(s.driver.release)
	receipt, err := s.svc.Cancel(ctx, s.ref)
	s.Require().NoError(err)
	s.Equal("controlAcknowledged", receipt.Result)
	current, err := s.svc.GetSession(s.ref)
	s.Require().NoError(err)
	s.Equal(events.Running, current.Status.State)
}

func (s *SessionWorkSuite) TestOfflineWorkIsRejectedWithoutDriverDispatch() {
	_, err := s.svc.ArchiveSession(context.Background(), s.ref)
	s.Require().NoError(err)
	_, err = s.svc.SendPrompt(context.Background(), s.ref, runtime.Input{Content: []events.ContentBlock{events.Text("hello")}})
	s.Require().ErrorIs(err, runtime.ErrFailedPrecondition)
	_, err = s.svc.AnswerInteraction(context.Background(), s.ref, "old-interaction", nil)
	s.Require().ErrorIs(err, runtime.ErrFailedPrecondition)
	_, err = s.svc.Cancel(context.Background(), s.ref)
	s.Require().ErrorIs(err, runtime.ErrFailedPrecondition)
	s.Zero(s.driver.calls.Load())
}

func (s *SessionWorkSuite) TestArchiveWaitsForOriginalAnswerAndCancelDispatch() {
	for _, operation := range []string{"answer", "cancel"} {
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan error, 1)
		go func() {
			var err error
			if operation == "answer" {
				_, err = s.svc.AnswerInteraction(ctx, s.ref, "old", nil)
			} else {
				_, err = s.svc.Cancel(ctx, s.ref)
			}
			done <- err
		}()
		select {
		case <-s.driver.entered:
		case <-time.After(time.Second):
			s.FailNow("work not admitted")
		}
		cancel()
		s.Require().Error(<-done)
	}
	archived := make(chan error, 1)
	go func() { _, err := s.svc.ArchiveSession(context.Background(), s.ref); archived <- err }()
	select {
	case err := <-archived:
		s.Fail("archive completed with old dispatch outstanding", "%v", err)
		close(s.driver.release)
		return
	case <-time.After(100 * time.Millisecond):
	}
	close(s.driver.release)
	s.Require().NoError(<-archived)
}
