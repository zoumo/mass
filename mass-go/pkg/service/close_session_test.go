package service_test

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/zoumo/mass/mass-go/pkg/events"
	"github.com/zoumo/mass/mass-go/pkg/metastore"
	"github.com/zoumo/mass/mass-go/pkg/resource"
	"github.com/zoumo/mass/mass-go/pkg/runtime"
	"github.com/zoumo/mass/mass-go/pkg/service"
	"github.com/zoumo/mass/mass-go/pkg/store"
	"github.com/zoumo/mass/mass-go/pkg/store/file"
)

type closeDriver struct {
	pipe *events.EventPipe

	mu          sync.Mutex
	source      events.Source
	closeErrors []error
	closeEvent  *events.SessionEvent

	promptEntered chan context.Context
	promptRelease chan struct{}
	promptOnce    sync.Once
	resumeEntered chan struct{}
	resumeRelease chan struct{}
	resumeOnce    sync.Once
	closeEntered  chan struct{}
	closeRelease  chan struct{}
	closeOnce     sync.Once

	prompts   atomic.Int32
	answers   atomic.Int32
	closes    atomic.Int32
	cancels   atomic.Int32
	resumes   atomic.Int32
	loads     atomic.Int32
	loadExits atomic.Int32
	stops     atomic.Int32
}

func (d *closeDriver) Events() (events.Stream, error) { return d.pipe, nil }
func (d *closeDriver) Start(context.Context) (runtime.Capabilities, error) {
	return runtime.Capabilities{}, nil
}

func (d *closeDriver) OpenSession(context.Context, runtime.Address, runtime.SessionSpec) (runtime.DriverSession, error) {
	d.mu.Lock()
	d.source = events.NewSource()
	source := d.source
	d.mu.Unlock()
	return runtime.DriverSession{SessionID: "native-close", Source: source}, nil
}

func (d *closeDriver) ResumeSession(ctx context.Context, id string, _ runtime.Address, _ runtime.SessionSpec) (runtime.DriverSession, error) {
	d.resumes.Add(1)
	if d.resumeEntered != nil {
		select {
		case d.resumeEntered <- struct{}{}:
		case <-ctx.Done():
			return runtime.DriverSession{}, ctx.Err()
		}
		select {
		case <-d.resumeRelease:
		case <-ctx.Done():
			return runtime.DriverSession{}, ctx.Err()
		}
	}
	d.mu.Lock()
	d.source = events.NewSource()
	source := d.source
	d.mu.Unlock()
	return runtime.DriverSession{SessionID: id, Source: source}, nil
}

func (d *closeDriver) LoadSession(context.Context, string, runtime.Address, string, string) error {
	d.loads.Add(1)
	return nil
}

func (d *closeDriver) CancelHistoryLoad(context.Context, string, string) error {
	d.loadExits.Add(1)
	return nil
}

func (d *closeDriver) SendPrompt(ctx context.Context, _ string, _ runtime.Input) (runtime.InputReceipt, error) {
	d.prompts.Add(1)
	if d.promptEntered != nil {
		d.promptEntered <- ctx
		select {
		case <-d.promptRelease:
		case <-ctx.Done():
			return runtime.InputReceipt{}, ctx.Err()
		}
	}
	return runtime.InputReceipt{Submission: "close-test", Acceptance: runtime.Acceptance{Sent: runtime.Yes()}}, nil
}

func (d *closeDriver) Cancel(context.Context, string) (runtime.CancelReceipt, error) {
	d.cancels.Add(1)
	return runtime.CancelReceipt{Result: "noActiveWork"}, nil
}

func (d *closeDriver) AnswerInteraction(context.Context, string, string, any) (runtime.Acceptance, error) {
	d.answers.Add(1)
	return runtime.Acceptance{Sent: runtime.Yes()}, nil
}

func (d *closeDriver) CloseSession(ctx context.Context, _ string) error {
	d.closes.Add(1)
	if d.closeEntered != nil {
		select {
		case d.closeEntered <- struct{}{}:
		default:
		}
	}
	if d.closeRelease != nil {
		select {
		case <-d.closeRelease:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	d.mu.Lock()
	event := d.closeEvent
	var err error
	if len(d.closeErrors) > 0 {
		err = d.closeErrors[0]
		d.closeErrors = d.closeErrors[1:]
	}
	d.mu.Unlock()
	if event != nil {
		if sendErr := d.pipe.Send(ctx, *event); sendErr != nil {
			return errors.Join(err, sendErr)
		}
		if flushErr := d.pipe.Flush(ctx); flushErr != nil {
			return errors.Join(err, flushErr)
		}
	}
	return err
}

func (d *closeDriver) Stop(context.Context) error {
	d.stops.Add(1)
	d.pipe.End(nil)
	return nil
}

func (d *closeDriver) releasePrompt() {
	d.promptOnce.Do(func() { close(d.promptRelease) })
}

func (d *closeDriver) releaseResume() {
	d.resumeOnce.Do(func() { close(d.resumeRelease) })
}

func (d *closeDriver) releaseClose() {
	d.closeOnce.Do(func() { close(d.closeRelease) })
}

type closeFactory struct{ driver *closeDriver }

func (f closeFactory) Validate(runtime.Config) error { return nil }
func (f closeFactory) New(string, runtime.Config) (runtime.Driver, error) {
	return f.driver, nil
}

type closeFixture struct {
	driver *closeDriver
	store  *file.Store
	svc    *service.Service
	ref    resource.SessionRef
}

func newCloseFixture(t *testing.T) *closeFixture {
	return newCloseFixtureWithMeta(t, nil)
}

func newCloseFixtureWithMeta(t *testing.T, wrap func(metastore.MetaStore) metastore.MetaStore) *closeFixture {
	t.Helper()
	st, err := file.Open(t.TempDir(), false)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, st.Close()) })
	d := &closeDriver{
		pipe:          events.NewEventPipe(64),
		promptEntered: make(chan context.Context, 4),
		promptRelease: make(chan struct{}),
		resumeEntered: make(chan struct{}, 4),
		resumeRelease: make(chan struct{}),
		closeEntered:  make(chan struct{}, 4),
		closeRelease:  make(chan struct{}),
	}
	var meta metastore.MetaStore = st
	if wrap != nil {
		meta = wrap(meta)
	}
	svc := service.New(meta, st, runtime.NewRegistry(map[string]runtime.Factory{"fixture": closeFactory{d}}))
	t.Cleanup(func() {
		d.releasePrompt()
		d.releaseResume()
		d.releaseClose()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		require.NoError(t, svc.Shutdown(ctx))
	})
	_, err = svc.CreateRuntime(resource.Runtime{Metadata: resource.Metadata{Name: "agent"}, Spec: resource.RuntimeSpec{Type: "fixture", Config: runtime.Config{}}})
	require.NoError(t, err)
	_, err = svc.StartRuntime(context.Background(), "agent")
	require.NoError(t, err)
	created, err := svc.CreateSession(context.Background(), resource.Session{
		Metadata: resource.Metadata{Namespace: "one", Name: "demo"},
		Spec:     resource.SessionSpec{RuntimeRef: "agent", SessionSpec: runtime.SessionSpec{Cwd: "/remote"}},
	})
	require.NoError(t, err)
	return &closeFixture{driver: d, store: st, svc: svc, ref: resource.SessionRef{SessionID: created.Status.SessionID}}
}

type gatedMeta struct {
	metastore.MetaStore
	calls   atomic.Int32
	blockAt atomic.Int32
	entered chan struct{}
	release chan struct{}
}

func (m *gatedMeta) GetSession(ref resource.SessionRef) (resource.Session, error) {
	if m.calls.Add(1) == m.blockAt.Load() {
		close(m.entered)
		<-m.release
	}
	return m.MetaStore.GetSession(ref)
}

type failHistoryCleanupMeta struct {
	metastore.MetaStore
	remaining atomic.Int32
}

func (m *failHistoryCleanupMeta) UpdateSession(r resource.Session) (resource.Session, error) {
	if r.Status.HistoryLoad != nil && r.Status.HistoryLoad.CleanupCompleted && m.remaining.Add(-1) >= 0 {
		return resource.Session{}, errors.New("injected history cleanup status failure")
	}
	return m.MetaStore.UpdateSession(r)
}

func (f *closeFixture) setState(t *testing.T, state events.SessionState) {
	t.Helper()
	_, err := f.store.UpdateSession(mustSession(t, f.svc, f.ref))
	require.NoError(t, err)
	current, err := f.svc.GetSession(f.ref)
	require.NoError(t, err)
	current.Status.State = state
	_, err = f.store.UpdateSession(current)
	require.NoError(t, err)
}

func mustSession(t *testing.T, svc *service.Service, ref resource.SessionRef) resource.Session {
	t.Helper()
	r, err := svc.GetSession(ref)
	require.NoError(t, err)
	return r
}

func requireRejectedAfterClose(t *testing.T, f *closeFixture) {
	t.Helper()
	_, err := f.svc.SendPrompt(context.Background(), f.ref, runtime.Input{Content: []events.ContentBlock{events.Text("blocked")}})
	require.ErrorIs(t, err, runtime.ErrFailedPrecondition)
	_, err = f.svc.AnswerInteraction(context.Background(), f.ref, "old", nil)
	require.ErrorIs(t, err, runtime.ErrFailedPrecondition)
	_, err = f.svc.Cancel(context.Background(), f.ref)
	require.ErrorIs(t, err, runtime.ErrFailedPrecondition)
	_, err = f.svc.ResumeSession(context.Background(), f.ref)
	require.ErrorIs(t, err, runtime.ErrFailedPrecondition)
	require.Zero(t, f.driver.resumes.Load())
}

func TestCloseWaitsForAcceptedDispatchThenCallsSDKOnce(t *testing.T) {
	f := newCloseFixture(t)
	promptDone := make(chan error, 1)
	go func() {
		_, err := f.svc.SendPrompt(context.Background(), f.ref, runtime.Input{Content: []events.ContentBlock{events.Text("accepted")}})
		promptDone <- err
	}()
	select {
	case <-f.driver.promptEntered:
	case <-time.After(time.Second):
		t.Fatal("prompt was not admitted")
	}

	closeDone := make(chan error, 1)
	go func() {
		_, err := f.svc.CloseSession(context.Background(), f.ref)
		closeDone <- err
	}()
	require.Eventually(t, func() bool {
		r, err := f.svc.GetSession(f.ref)
		return err == nil && r.Status.State == "closing"
	}, time.Second, time.Millisecond)
	select {
	case <-f.driver.closeEntered:
		t.Fatal("SDK close overtook an accepted dispatch")
	case <-time.After(50 * time.Millisecond):
	}
	_, err := f.svc.SendPrompt(context.Background(), f.ref, runtime.Input{Content: []events.ContentBlock{events.Text("must reject")}})
	require.ErrorIs(t, err, runtime.ErrFailedPrecondition)

	f.driver.releasePrompt()
	require.NoError(t, <-promptDone)
	select {
	case <-f.driver.closeEntered:
	case <-time.After(time.Second):
		t.Fatal("SDK close was not called")
	}
	f.driver.releaseClose()
	require.NoError(t, <-closeDone)
	require.EqualValues(t, 1, f.driver.prompts.Load())
	require.EqualValues(t, 1, f.driver.closes.Load())
	require.Zero(t, f.driver.cancels.Load())
	current := mustSession(t, f.svc, f.ref)
	require.Equal(t, events.SessionState("offline"), current.Status.State)
	require.Empty(t, current.Status.Reason)
}

func TestCloseAcceptanceRejectsWorkBeforeLaneIsMarked(t *testing.T) {
	gate := &gatedMeta{entered: make(chan struct{}), release: make(chan struct{})}
	f := newCloseFixtureWithMeta(t, func(meta metastore.MetaStore) metastore.MetaStore {
		gate.MetaStore = meta
		return gate
	})
	f.driver.releaseClose()
	gate.calls.Store(0)
	gate.blockAt.Store(2)

	closed := make(chan error, 1)
	go func() {
		_, err := f.svc.CloseSession(context.Background(), f.ref)
		closed <- err
	}()
	select {
	case <-gate.entered:
	case <-time.After(time.Second):
		t.Fatal("close did not pass operation acceptance")
	}

	_, err := f.svc.SendPrompt(context.Background(), f.ref, runtime.Input{Content: []events.ContentBlock{events.Text("blocked")}})
	require.ErrorIs(t, err, runtime.ErrFailedPrecondition)
	_, err = f.svc.AnswerInteraction(context.Background(), f.ref, "old", nil)
	require.ErrorIs(t, err, runtime.ErrFailedPrecondition)
	_, err = f.svc.Cancel(context.Background(), f.ref)
	require.ErrorIs(t, err, runtime.ErrFailedPrecondition)
	require.Zero(t, f.driver.prompts.Load())
	require.Zero(t, f.driver.answers.Load())
	require.Zero(t, f.driver.cancels.Load())

	close(gate.release)
	require.NoError(t, <-closed)
	require.Equal(t, events.SessionState("offline"), mustSession(t, f.svc, f.ref).Status.State)
}

func TestCloseFailureFencesThenExplicitRetrySucceeds(t *testing.T) {
	f := newCloseFixture(t)
	f.setState(t, events.Running)
	f.driver.closeErrors = []error{errors.New("private provider failure")}
	f.driver.releaseClose()

	_, err := f.svc.CloseSession(context.Background(), f.ref)
	var public *runtime.OperationError
	require.ErrorAs(t, err, &public)
	require.Equal(t, "outcome_unknown", public.Code)
	require.Equal(t, "session_close_failed", public.Reason)
	require.NotContains(t, err.Error(), "private provider failure")
	current := mustSession(t, f.svc, f.ref)
	require.Equal(t, events.SessionState("closing"), current.Status.State)
	require.Equal(t, "session_close_failed", current.Status.Reason)
	require.EqualValues(t, 1, f.driver.closes.Load())
	require.Zero(t, f.driver.cancels.Load())
	requireRejectedAfterClose(t, f)

	closed, err := f.svc.CloseSession(context.Background(), f.ref)
	require.NoError(t, err)
	require.Equal(t, events.SessionState("offline"), closed.Status.State)
	require.Empty(t, closed.Status.Reason)
	require.EqualValues(t, 2, f.driver.closes.Load())

	again, err := f.svc.CloseSession(context.Background(), f.ref)
	require.NoError(t, err)
	require.Equal(t, events.SessionState("offline"), again.Status.State)
	require.EqualValues(t, 2, f.driver.closes.Load(), "offline close must not call the SDK again")
	runtimeResource, err := f.svc.GetRuntime("agent")
	require.NoError(t, err)
	require.Equal(t, "running", runtimeResource.Status.State)
	require.Zero(t, f.driver.stops.Load())
}

func TestCloseUnknownWithoutHandleCompletesOfflineCleanup(t *testing.T) {
	f := newCloseFixture(t)
	_, err := f.store.CreateSession(resource.Session{
		Metadata: resource.Metadata{Namespace: "one", Name: "detached"},
		Spec:     resource.SessionSpec{RuntimeRef: "agent"},
		Status:   resource.SessionStatus{SessionID: "detached", State: events.Unknown, AppliedGeneration: 1},
	})
	require.NoError(t, err)
	ref := resource.SessionRef{SessionID: "detached"}
	closed, err := f.svc.CloseSession(context.Background(), ref)
	require.NoError(t, err)
	require.Equal(t, events.SessionState("offline"), closed.Status.State)
	require.Zero(t, f.driver.closes.Load())

	again, err := f.svc.CloseSession(context.Background(), ref)
	require.NoError(t, err)
	require.Equal(t, events.SessionState("offline"), again.Status.State)
	require.Zero(t, f.driver.closes.Load())
	require.Zero(t, f.driver.stops.Load())
}

func TestCloseUnknownWithHandleCallsSDK(t *testing.T) {
	f := newCloseFixture(t)
	f.setState(t, events.Unknown)
	f.driver.closeErrors = []error{errors.New("close failed")}
	f.driver.releaseClose()

	_, err := f.svc.CloseSession(context.Background(), f.ref)
	require.Error(t, err)
	require.EqualValues(t, 1, f.driver.closes.Load())
	current := mustSession(t, f.svc, f.ref)
	require.Equal(t, events.SessionState("closing"), current.Status.State)
	require.Equal(t, "session_close_failed", current.Status.Reason)
}

func TestCloseAfterSDKSuccessSettlesAcceptedFactsBeforeOffline(t *testing.T) {
	f := newCloseFixture(t)
	event, err := events.NewEvent(events.EventBase{
		EventID:   "10000000-0000-4000-8000-000000000102",
		Namespace: "one", SessionName: "demo", SessionID: "native-close",
		ItemID: "final-close-fact", Time: time.Now(),
	}, events.AgentMessage{MessageID: "final-close-fact", Content: []events.ContentBlock{events.Text("settled")}}, f.driver.source)
	require.NoError(t, err)
	f.driver.closeEvent = &event
	f.driver.releaseClose()

	closed, err := f.svc.CloseSession(context.Background(), f.ref)
	require.NoError(t, err)
	require.Equal(t, events.SessionState("offline"), closed.Status.State)
	page, err := f.svc.ListHistoryEvents(f.ref, store.HistoryQuery{})
	require.NoError(t, err)
	require.Len(t, page.Events, 1)
	require.Equal(t, event.EventID, page.Events[0].EventID)
	require.Equal(t, events.SessionState("offline"), mustSession(t, f.svc, f.ref).Status.State)
}

func TestCloseWaitsForAcceptedPublicationBeforeOffline(t *testing.T) {
	f := newCloseFixture(t)
	event, err := events.NewEvent(events.EventBase{
		EventID:   "10000000-0000-4000-8000-000000000103",
		Namespace: "one", SessionName: "demo", SessionID: "native-close",
		ItemID: "blocked-publication", Time: time.Now(),
	}, events.AgentMessage{MessageID: "blocked-publication", Content: []events.ContentBlock{events.Text("blocked")}}, f.driver.source)
	require.NoError(t, err)
	f.driver.closeEvent = &event
	f.driver.releaseClose()

	broadcast, err := f.svc.EventBroadcast(f.ref)
	require.NoError(t, err)
	entered := make(chan struct{})
	markEntered := sync.OnceFunc(func() { close(entered) })
	releasePublication := make(chan struct{})
	allowPublication := sync.OnceFunc(func() { close(releasePublication) })
	t.Cleanup(func() {
		markEntered()
		allowPublication()
	})
	require.NoError(t, broadcast.AddHandler(&observingHandler{required: true, handle: func(got events.SessionEvent) error {
		if got.EventID != event.EventID {
			return nil
		}
		markEntered()
		<-releasePublication
		return nil
	}}))

	closed := make(chan error, 1)
	go func() {
		_, err := f.svc.CloseSession(context.Background(), f.ref)
		closed <- err
	}()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("accepted publication did not enter")
	}
	select {
	case err := <-closed:
		t.Fatalf("close completed before publication settled: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	require.Equal(t, events.SessionState("closing"), mustSession(t, f.svc, f.ref).Status.State)

	allowPublication()
	require.NoError(t, <-closed)
	require.Equal(t, events.SessionState("offline"), mustSession(t, f.svc, f.ref).Status.State)
}

func TestCloseRequiredPublicationFailureRemainsClosing(t *testing.T) {
	f := newCloseFixture(t)
	event, err := events.NewEvent(events.EventBase{
		EventID:   "10000000-0000-4000-8000-000000000104",
		Namespace: "one", SessionName: "demo", SessionID: "native-close",
		ItemID: "failed-publication", Time: time.Now(),
	}, events.AgentMessage{MessageID: "failed-publication", Content: []events.ContentBlock{events.Text("stored")}}, f.driver.source)
	require.NoError(t, err)
	f.driver.closeEvent = &event
	f.driver.releaseClose()

	broadcast, err := f.svc.EventBroadcast(f.ref)
	require.NoError(t, err)
	require.NoError(t, broadcast.AddHandler(&observingHandler{required: true, handle: func(got events.SessionEvent) error {
		if got.EventID == event.EventID {
			return errors.New("required publication failed")
		}
		return nil
	}}))

	_, err = f.svc.CloseSession(context.Background(), f.ref)
	require.Error(t, err)
	current := mustSession(t, f.svc, f.ref)
	require.Equal(t, events.SessionState("closing"), current.Status.State)
	require.Equal(t, "session_close_failed", current.Status.Reason)
	require.EqualValues(t, 1, f.driver.closes.Load())
	page, err := f.svc.ListHistoryEvents(f.ref, store.HistoryQuery{})
	require.NoError(t, err)
	require.Len(t, page.Events, 1, "the accepted fact remains committed")

	_, err = f.svc.CloseSession(context.Background(), f.ref)
	require.Error(t, err, "an incomplete required delivery is not silently settled")
	require.EqualValues(t, 1, f.driver.closes.Load(), "retry must not repeat a confirmed SDK close")
}

func TestCloseWaitsForResumeHandleThenClosesIt(t *testing.T) {
	f := newCloseFixture(t)
	f.driver.releaseClose()
	_, err := f.svc.CloseSession(context.Background(), f.ref)
	require.NoError(t, err)
	require.EqualValues(t, 1, f.driver.closes.Load())

	resumed := make(chan error, 1)
	go func() {
		_, err := f.svc.ResumeSession(context.Background(), f.ref)
		resumed <- err
	}()
	select {
	case <-f.driver.resumeEntered:
	case <-time.After(time.Second):
		t.Fatal("resume was not admitted")
	}
	current := mustSession(t, f.svc, f.ref)
	require.Equal(t, events.SessionState("opening"), current.Status.State)

	closed := make(chan error, 1)
	go func() {
		_, err := f.svc.CloseSession(context.Background(), f.ref)
		closed <- err
	}()
	select {
	case err = <-closed:
		t.Fatalf("close returned before resume produced a handle: %v", err)
	case <-time.After(50 * time.Millisecond):
	}

	f.driver.releaseResume()
	require.NoError(t, <-resumed)
	require.NoError(t, <-closed)
	current = mustSession(t, f.svc, f.ref)
	require.Equal(t, events.SessionState("offline"), current.Status.State)
	require.Empty(t, current.Status.Reason)
	require.EqualValues(t, 2, f.driver.closes.Load())
}

func TestCloseDuringHistoryLoadReleasesLoadWithoutSecondNativeTarget(t *testing.T) {
	f := newCloseFixture(t)
	accepted, err := f.svc.LoadSession(context.Background(), resource.SessionLoad{
		Metadata:   resource.Metadata{Namespace: "one", Name: "loaded"},
		RuntimeRef: "agent",
		Cwd:        "/remote",
		SessionID:  "loaded",
	})
	require.NoError(t, err)
	require.Equal(t, events.SessionState("opening"), accepted.Session.Status.State)
	require.Eventually(t, func() bool {
		return f.driver.loads.Load() == 1
	}, time.Second, time.Millisecond)

	ref := resource.SessionRef{SessionID: "loaded"}
	closed, err := f.svc.CloseSession(context.Background(), ref)
	require.NoError(t, err)
	require.Equal(t, events.SessionState("offline"), closed.Status.State)
	require.EqualValues(t, 1, f.driver.loadExits.Load())
	require.Zero(t, f.driver.closes.Load())

	again, err := f.svc.CloseSession(context.Background(), ref)
	require.NoError(t, err)
	require.Equal(t, events.SessionState("offline"), again.Status.State)
	require.EqualValues(t, 1, f.driver.loadExits.Load())
}

func TestCloseRetriesHistoryCleanupStatusWithoutSDKTarget(t *testing.T) {
	statusFailures := &failHistoryCleanupMeta{}
	statusFailures.remaining.Store(2)
	f := newCloseFixtureWithMeta(t, func(meta metastore.MetaStore) metastore.MetaStore {
		statusFailures.MetaStore = meta
		return statusFailures
	})
	_, err := f.svc.LoadSession(context.Background(), resource.SessionLoad{
		Metadata:   resource.Metadata{Namespace: "one", Name: "loaded"},
		RuntimeRef: "agent",
		Cwd:        "/remote",
		SessionID:  "loaded",
	})
	require.NoError(t, err)
	ref := resource.SessionRef{SessionID: "loaded"}

	_, err = f.svc.CloseSession(context.Background(), ref)
	require.Error(t, err)
	require.Equal(t, events.SessionState("closing"), mustSession(t, f.svc, ref).Status.State)
	require.EqualValues(t, 1, f.driver.loadExits.Load())
	require.Zero(t, f.driver.closes.Load())

	closed, err := f.svc.CloseSession(context.Background(), ref)
	require.NoError(t, err)
	require.Equal(t, events.SessionState("offline"), closed.Status.State)
	require.NotNil(t, closed.Status.HistoryLoad)
	require.True(t, closed.Status.HistoryLoad.CleanupCompleted)
	require.EqualValues(t, 1, f.driver.loadExits.Load())
	require.Zero(t, f.driver.closes.Load())
}
