package service_test

import (
	"context"
	"errors"
	"github.com/stretchr/testify/suite"
	"github.com/zoumo/mass/mass-go/pkg/events"
	"github.com/zoumo/mass/mass-go/pkg/service"
	"github.com/zoumo/mass/mass-go/pkg/store"
	"github.com/zoumo/mass/mass-go/pkg/store/file"
	"github.com/zoumo/mass/mass-go/pkg/transport"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/zoumo/mass/mass-go/pkg/resource"
	"github.com/zoumo/mass/mass-go/pkg/runtime"
)

// The public RuntimeService must retain stop ownership after its caller leaves.
func (s *RecoverySuite) TestStopCallerLeavesButOriginalOwnerCompletes() {
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { _, err := s.svc.StopRuntime(ctx, "agent"); done <- err }()
	var owner context.Context
	select {
	case owner = <-s.d.closeEntered:
	case <-time.After(time.Second):
		s.FailNow("stop did not dispatch close")
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
	s.NoError(owner.Err(), "original stop must outlive caller cancellation")
	s.release()
	s.Eventually(func() bool {
		r, err := s.svc.GetRuntime("agent")
		return err == nil && r.Status.State == "stopped"
	}, time.Second, time.Millisecond)
	r, err := s.svc.GetSession(resource.SessionRef{SessionID: "slow"})
	s.Require().NoError(err)
	s.Equal("slow", r.Status.SessionID)
	s.EqualValues("offline", r.Status.State)
	s.Zero(r.Status.AppliedGeneration)
}

type stopDriver struct {
	workDriver
	closes   atomic.Int32
	stops    atomic.Int32
	failStop atomic.Bool
	onClose  func(context.Context) error
	onStop   func(context.Context) error
	config   runtime.Config
}

func (d *stopDriver) ResumeSession(_ context.Context, id string, _ runtime.Address, _ runtime.SessionSpec) (runtime.DriverSession, error) {
	d.source = events.NewSource()
	return runtime.DriverSession{SessionID: id, Source: d.source}, nil
}
func (d *stopDriver) CloseSession(ctx context.Context, _ string) error {
	d.closes.Add(1)
	if d.onClose != nil {
		return d.onClose(ctx)
	}
	return nil
}
func (d *stopDriver) Stop(ctx context.Context) error {
	if d.onStop != nil {
		if err := d.onStop(ctx); err != nil {
			return err
		}
	}
	d.stops.Add(1)
	if d.failStop.Load() {
		return errors.New("stop release unknown")
	}
	d.pipe.End(nil)
	return nil
}

type stopFactory struct{ drivers []*stopDriver }

func (f *stopFactory) Validate(runtime.Config) error { return nil }
func (f *stopFactory) New(_ string, config runtime.Config) (runtime.Driver, error) {
	d := &stopDriver{workDriver: workDriver{pipe: events.NewEventPipe(32), entered: make(chan context.Context, 16), release: make(chan struct{})}}
	d.config = config
	f.drivers = append(f.drivers, d)
	return d, nil
}

type RuntimeStopSuite struct {
	suite.Suite
	svc     *service.Service
	factory *stopFactory
	d       *stopDriver
	ref     resource.SessionRef
	history *recoveryHistory
}

func TestRuntimeStopSuite(t *testing.T) { suite.Run(t, new(RuntimeStopSuite)) }
func (s *RuntimeStopSuite) SetupTest() {
	st, err := file.Open(s.T().TempDir(), false)
	s.Require().NoError(err)
	s.T().Cleanup(func() { s.NoError(st.Close()) })
	s.factory = &stopFactory{}
	s.history = &recoveryHistory{HistoryStore: st, entered: make(chan struct{}), release: make(chan struct{})}
	s.svc = service.New(st, s.history, runtime.NewRegistry(map[string]runtime.Factory{"fixture": s.factory}))
	s.T().Cleanup(func() {
		for _, d := range s.factory.drivers {
			d.failStop.Store(false)
			select {
			case <-d.release:
			default:
				close(d.release)
			}
		}
		c, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		s.NoError(s.svc.Shutdown(c))
	})
	_, err = s.svc.CreateRuntime(resource.Runtime{Metadata: resource.Metadata{Name: "agent"}, Spec: resource.RuntimeSpec{Type: "fixture", Config: runtime.Config{}}})
	s.Require().NoError(err)
	_, err = s.svc.StartRuntime(context.Background(), "agent")
	s.Require().NoError(err)
	s.d = s.factory.drivers[0]
	r, err := s.svc.CreateSession(context.Background(), resource.Session{Metadata: resource.Metadata{Namespace: "one", Name: "demo"}, Spec: resource.SessionSpec{RuntimeRef: "agent", SessionSpec: runtime.SessionSpec{Cwd: "/remote"}}})
	s.Require().NoError(err)
	s.ref = resource.SessionRef{SessionID: r.Status.SessionID}
}
func (s *RuntimeStopSuite) TestConfirmedSessionReleaseIsNotRedispatchedAfterDriverFailure() {
	s.d.failStop.Store(true)
	_, err := s.svc.StopRuntime(context.Background(), "agent")
	s.Require().Error(err)
	r, err := s.svc.GetRuntime("agent")
	s.Require().NoError(err)
	s.Equal("stopping", r.Status.State)
	s.NotZero(r.Status.AppliedGeneration)
	s.Error(s.svc.DeleteRuntime("agent"))
	_, err = s.svc.StartRuntime(context.Background(), "agent")
	s.Error(err)
	s.d.failStop.Store(false)
	r, err = s.svc.StopRuntime(context.Background(), "agent")
	s.Require().NoError(err)
	s.Equal("stopped", r.Status.State)
	s.Equal(int32(1), s.d.closes.Load(), "confirmed native release must not be sent again")
	s.Equal(int32(2), s.d.stops.Load(), "only unknown Driver stop is explicitly retried")
}

func (s *RuntimeConfigSuite) TestRuntimeLifecycleValidatesNamesBeforeStoreAccess() {
	_, err := s.svc.StartRuntime(context.Background(), "../agent")
	s.ErrorIs(err, runtime.ErrInvalidArgument)
	_, err = s.svc.StopRuntime(context.Background(), "../agent")
	s.ErrorIs(err, runtime.ErrInvalidArgument)
	s.ErrorIs(s.svc.DeleteRuntime("../agent"), runtime.ErrInvalidArgument)
}

func (s *RuntimeStopSuite) TestCancelCannotEnterSessionAlreadyBeingReleased() {
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	defer unblock()
	s.d.onClose = func(context.Context) error { close(entered); <-release; return nil }
	done := make(chan error, 1)
	go func() { _, err := s.svc.StopRuntime(context.Background(), "agent"); done <- err }()
	select {
	case <-entered:
	case <-time.After(time.Second):
		s.FailNow("close not entered")
	}
	c, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	_, err := s.svc.Cancel(c, s.ref)
	s.ErrorIs(err, runtime.ErrFailedPrecondition, "release must atomically seal cancel dispatch too")
	unblock()
	s.NoError(<-done)
}
func (s *SessionWorkSuite) TestStopWaitsForAllOriginalDispatchesAndTheirFacts() {
	for _, kind := range []string{"input", "answer", "cancel"} {
		c, cancel := context.WithCancel(context.Background())
		done := make(chan error, 1)
		go func() {
			var err error
			switch kind {
			case "input":
				_, err = s.svc.SendPrompt(c, s.ref, runtime.Input{Content: []events.ContentBlock{events.Text("hello")}})
			case "answer":
				_, err = s.svc.AnswerInteraction(c, s.ref, "original", nil)
			default:
				_, err = s.svc.Cancel(c, s.ref)
			}
			done <- err
		}()
		select {
		case <-s.driver.entered:
		case <-time.After(time.Second):
			s.FailNow("dispatch not entered")
		}
		cancel()
		s.Error(<-done)
	}
	e, err := events.NewEvent(events.EventBase{EventID: "01234567-89ab-4cde-8fab-0123456789ab", Namespace: "one", SessionName: "demo", SessionID: "original", ItemID: "accepted", Time: time.Now().UTC()}, events.AgentMessage{MessageID: "accepted", Content: []events.ContentBlock{events.Text("complete fact")}}, s.driver.source)
	s.Require().NoError(err)
	var once sync.Once
	s.driver.onFinish = func() error {
		var err error
		once.Do(func() { err = s.driver.pipe.Send(context.Background(), e) })
		return err
	}
	c, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	_, err = s.svc.StopRuntime(c, "agent")
	var public *runtime.OperationError
	s.Require().ErrorAs(err, &public)
	s.Equal("outcome_unknown", public.Code)
	close(s.driver.release)
	_, err = s.svc.StopRuntime(context.Background(), "agent")
	s.Require().NoError(err)
	page, err := s.svc.ListHistoryEvents(s.ref, store.HistoryQuery{})
	s.Require().NoError(err)
	s.Require().Len(page.Events, 1)
	s.Equal(e.EventID, page.Events[0].EventID)
}
func (s *RuntimeStopSuite) TestConcurrentStopWaitersShareOneDriverRelease() {
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	defer unblock()
	s.d.onStop = func(context.Context) error { close(entered); <-release; return nil }
	done := make(chan error, 8)
	for range 8 {
		go func() { _, err := s.svc.StopRuntime(context.Background(), "agent"); done <- err }()
	}
	select {
	case <-entered:
	case <-time.After(time.Second):
		s.FailNow("driver release not entered")
	}
	unblock()
	for range 8 {
		s.NoError(<-done)
	}
	s.Equal(int32(1), s.d.stops.Load())
	s.Equal(int32(1), s.d.closes.Load())
}
func (s *RuntimeStopSuite) TestRestartUsesNewConfigurationAndRetainsDirectory() {
	original, err := s.svc.GetSession(s.ref)
	s.Require().NoError(err)
	updated, err := s.svc.UpdateRuntime("agent", resource.RuntimeSpec{Type: "fixture", Config: runtime.Config{"next": true}})
	s.Require().NoError(err)
	running, err := s.svc.StartRuntime(context.Background(), "agent")
	s.Require().NoError(err)
	s.Equal(uint64(1), running.Status.AppliedGeneration)
	s.Require().Len(s.factory.drivers, 1)
	_, err = s.svc.StopRuntime(context.Background(), "agent")
	s.Require().NoError(err)
	s.ErrorIs(s.svc.DeleteRuntime("agent"), runtime.ErrFailedPrecondition)
	current, err := s.svc.GetSession(s.ref)
	s.Require().NoError(err)
	s.Equal(original.Metadata.UID, current.Metadata.UID)
	s.Equal(original.Metadata.CreatedAt, current.Metadata.CreatedAt)
	s.Equal(original.Metadata.Generation, current.Metadata.Generation)
	s.Equal(original.Spec, current.Spec)
	s.EqualValues("offline", current.Status.State)
	running, err = s.svc.StartRuntime(context.Background(), "agent")
	s.Require().NoError(err)
	s.Equal(updated.Metadata.Generation, running.Status.AppliedGeneration)
	s.Require().Len(s.factory.drivers, 2)
	s.Equal(true, s.factory.drivers[1].config["next"])
	s.Empty(s.d.config)
}
func (s *RuntimeStopSuite) TestFailureLeavesOtherRuntimeIndependent() {
	s.d.failStop.Store(true)
	_, err := s.svc.StopRuntime(context.Background(), "agent")
	s.Require().Error(err)
	_, err = s.svc.CreateRuntime(resource.Runtime{Metadata: resource.Metadata{Name: "healthy"}, Spec: resource.RuntimeSpec{Type: "fixture", Config: runtime.Config{}}})
	s.Require().NoError(err)
	r, err := s.svc.StartRuntime(context.Background(), "healthy")
	s.Require().NoError(err)
	s.Equal("running", r.Status.State)
	s.ErrorIs(s.svc.DeleteRuntime("healthy"), runtime.ErrFailedPrecondition)
	r, err = s.svc.StopRuntime(context.Background(), "healthy")
	s.Require().NoError(err)
	s.Equal("stopped", r.Status.State)
	s.NoError(s.svc.DeleteRuntime("healthy"))
	r, err = s.svc.GetRuntime("agent")
	s.Require().NoError(err)
	s.Equal("stopping", r.Status.State)
}
func (s *RuntimeStopSuite) TestHTTPStopRestartAndSafeDelete() {
	h, err := transport.New(s.svc, transport.Options{Token: "test-token"})
	s.Require().NoError(err)
	defer h.Shutdown()
	server := httptest.NewServer(h)
	defer server.Close()
	call := func(method, path, body string) (int, string) {
		req, err := http.NewRequest(method, server.URL+"/apis/v1"+path, strings.NewReader(body))
		s.Require().NoError(err)
		req.Header.Set("Authorization", "Bearer test-token")
		if body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		res, err := http.DefaultClient.Do(req)
		s.Require().NoError(err)
		defer res.Body.Close()
		b, err := io.ReadAll(res.Body)
		s.Require().NoError(err)
		s.Equal("no-store", res.Header.Get("Cache-Control"))
		return res.StatusCode, string(b)
	}
	status, body := call("DELETE", "/runtimes/agent", "")
	s.Equal(409, status, body)
	for _, invalid := range []string{"null", "[]", "true", `{"unknown":true}`} {
		status, body = call("POST", "/runtimes/agent/stop", invalid)
		s.Equal(400, status, body)
	}
	status, body = call("POST", "/runtimes/agent/stop", "{}")
	s.Equal(200, status, body)
	s.Contains(body, `"state":"stopped"`)
	status, body = call("DELETE", "/runtimes/agent", "")
	s.Equal(409, status, body)
	status, body = call("POST", "/runtimes/agent/start", "")
	s.Equal(200, status, body)
	s.Contains(body, `"state":"running"`)
	status, body = call("POST", "/runtimes", `{"metadata":{"name":"unused"},"spec":{"type":"fixture","config":{}}}`)
	s.Require().Equal(201, status, body)
	status, body = call("DELETE", "/runtimes/unused", "")
	s.Equal(204, status, body)
	s.Empty(body)
	status, body = call("GET", "/runtimes/unused", "")
	s.Equal(404, status, body)
}

func (s *RuntimeStopSuite) TestStopRetainsOverflowArrivingDuringDriverRelease() {
	s.assertStopRetainsOverflow(false)
}
func (s *RuntimeStopSuite) TestStopRetainsRetiredSourceOverflowAfterRecovery() {
	s.assertStopRetainsOverflow(true)
}
func (s *RuntimeStopSuite) assertStopRetainsOverflow(recovered bool) {
	source := s.d.source
	if recovered {
		_, err := s.svc.ArchiveSession(context.Background(), s.ref)
		s.Require().NoError(err)
		_, err = s.svc.ResumeSession(context.Background(), s.ref)
		s.Require().NoError(err)
	}
	state := events.Idle
	message, err := events.NewEvent(events.EventBase{EventID: "01234567-89ab-4cde-8fab-0123456789ab", Namespace: "one", SessionName: "demo", SessionID: "original", ItemID: "final", Time: time.Now().UTC()}, events.AgentMessage{MessageID: "final", Content: []events.ContentBlock{events.Text("final complete fact")}}, source)
	s.Require().NoError(err)
	update, err := events.NewEvent(events.EventBase{EventID: "01234567-89ab-4cde-8fab-0123456789ac", Namespace: "one", SessionName: "demo", SessionID: "original", Time: time.Now().UTC()}, events.StateUpdate{State: &state}, source)
	s.Require().NoError(err)
	var once sync.Once
	release := func() { once.Do(func() { close(s.history.release) }) }
	defer release()
	s.d.onStop = func(context.Context) error {
		s.history.blocked.Store(true)
		if err := s.d.pipe.Send(context.Background(), message); err != nil {
			return err
		}
		<-s.history.entered
		for range 129 {
			if err := s.d.pipe.Send(context.Background(), update); err != nil {
				return err
			}
		}
		return nil
	}
	c, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	_, err = s.svc.StopRuntime(c, "agent")
	s.Require().Error(err)
	release()
	_, err = s.svc.StopRuntime(context.Background(), "agent")
	s.Require().Error(err)
	r, err := s.svc.GetRuntime("agent")
	s.Require().NoError(err)
	s.Equal("stopping", r.Status.State, "driver release alone cannot discard retained facts")
	s.Require().NoError(s.svc.ReconcileDelivery(context.Background(), "agent"))
	_, err = s.svc.StopRuntime(context.Background(), "agent")
	s.Require().NoError(err)
	page, err := s.svc.ListHistoryEvents(s.ref, store.HistoryQuery{})
	s.Require().NoError(err)
	s.Require().Len(page.Events, 1)
	s.Equal(message.EventID, page.Events[0].EventID)
	s.Equal(int32(1), s.d.stops.Load(), "confirmed Driver release must survive delivery settlement")
}
