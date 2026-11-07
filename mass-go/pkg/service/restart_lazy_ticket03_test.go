package service_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/zoumo/mass/mass-go/pkg/events"
	"github.com/zoumo/mass/mass-go/pkg/resource"
	"github.com/zoumo/mass/mass-go/pkg/runtime"
	"github.com/zoumo/mass/mass-go/pkg/service"
	"github.com/zoumo/mass/mass-go/pkg/store"
	"github.com/zoumo/mass/mass-go/pkg/store/file"
)

type ticket03Driver struct {
	pipe *events.EventPipe

	source    events.Source
	resumeErr error
	resumeTo  events.SessionState
	startGate <-chan struct{}
	started   chan struct{}
	startOnce sync.Once

	opens   atomic.Int32
	resumes atomic.Int32
	starts  atomic.Int32
	stops   atomic.Int32
	prompts atomic.Int32
	answers atomic.Int32
	cancels atomic.Int32
	closes  atomic.Int32
}

func newTicket03Driver() *ticket03Driver {
	return &ticket03Driver{pipe: events.NewEventPipe(64)}
}

func (d *ticket03Driver) Events() (events.Stream, error) { return d.pipe, nil }

func (d *ticket03Driver) Start(context.Context) (runtime.Capabilities, error) {
	d.starts.Add(1)
	if d.started != nil {
		d.startOnce.Do(func() { close(d.started) })
	}
	if d.startGate != nil {
		<-d.startGate
	}
	return runtime.Capabilities{}, nil
}

func (d *ticket03Driver) OpenSession(_ context.Context, a runtime.Address, spec runtime.SessionSpec) (runtime.DriverSession, error) {
	d.opens.Add(1)
	d.source = events.NewSource()
	return runtime.DriverSession{
		SessionID:         a.SessionName,
		Source:            d.source,
		EffectiveFeatures: runtime.ResolveSessionFeatures(spec.Features, runtime.FeatureSupport{AgentQuestions: true}),
	}, nil
}

func (d *ticket03Driver) ResumeSession(ctx context.Context, id string, a runtime.Address, spec runtime.SessionSpec) (runtime.DriverSession, error) {
	d.resumes.Add(1)
	if d.resumeErr != nil {
		return runtime.DriverSession{}, d.resumeErr
	}
	d.source = events.NewSource()
	if d.resumeTo != "" {
		state := d.resumeTo
		event, err := events.NewEvent(events.EventBase{
			EventID:     "10000000-0000-4000-8000-000000000303",
			Namespace:   a.Namespace,
			SessionName: a.SessionName,
			SessionID:   id,
			Time:        time.Now().UTC(),
		}, events.StateUpdate{State: &state}, d.source)
		if err != nil {
			return runtime.DriverSession{}, err
		}
		if err := d.pipe.Send(ctx, event); err != nil {
			return runtime.DriverSession{}, err
		}
	}
	return runtime.DriverSession{
		SessionID:         id,
		Source:            d.source,
		EffectiveFeatures: runtime.ResolveSessionFeatures(spec.Features, runtime.FeatureSupport{AgentQuestions: true}),
	}, nil
}

func (*ticket03Driver) LoadSession(context.Context, string, runtime.Address, string, string) error {
	return nil
}

func (*ticket03Driver) CancelHistoryLoad(context.Context, string, string) error { return nil }

func (d *ticket03Driver) SendPrompt(context.Context, string, runtime.Input) (runtime.InputReceipt, error) {
	d.prompts.Add(1)
	return runtime.InputReceipt{Submission: "ticket03", Acceptance: runtime.Acceptance{Sent: runtime.Yes()}}, nil
}

func (d *ticket03Driver) Cancel(context.Context, string) (runtime.CancelReceipt, error) {
	d.cancels.Add(1)
	return runtime.CancelReceipt{Result: "noActiveWork"}, nil
}

func (d *ticket03Driver) AnswerInteraction(context.Context, string, string, any) (runtime.Acceptance, error) {
	d.answers.Add(1)
	return runtime.Acceptance{Sent: runtime.Yes()}, nil
}

func (d *ticket03Driver) CloseSession(context.Context, string) error {
	d.closes.Add(1)
	return nil
}

func (d *ticket03Driver) Stop(context.Context) error {
	d.stops.Add(1)
	d.pipe.End(nil)
	return nil
}

type ticket03Factory struct {
	mu        sync.Mutex
	drivers   []*ticket03Driver
	configure func(*ticket03Driver)
	created   atomic.Int32
}

func (f *ticket03Factory) Validate(runtime.Config) error { return nil }

func (f *ticket03Factory) New(string, runtime.Config) (runtime.Driver, error) {
	f.created.Add(1)
	d := newTicket03Driver()
	if f.configure != nil {
		f.configure(d)
	}
	f.mu.Lock()
	f.drivers = append(f.drivers, d)
	f.mu.Unlock()
	return d, nil
}

func (f *ticket03Factory) only(t *testing.T) *ticket03Driver {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	require.Len(t, f.drivers, 1)
	return f.drivers[0]
}

func openTicket03StoreAt(t *testing.T, dir string) *file.Store {
	t.Helper()
	st, err := file.Open(dir, false)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, st.Close()) })
	return st
}

func openTicket03Store(t *testing.T) *file.Store {
	t.Helper()
	return openTicket03StoreAt(t, t.TempDir())
}

func createTicket03Runtime(t *testing.T, st *file.Store, name, state string) resource.Runtime {
	t.Helper()
	r, err := st.CreateRuntime(resource.Runtime{
		Metadata: resource.Metadata{Name: name},
		Spec:     resource.RuntimeSpec{Type: "fixture", Config: runtime.Config{}},
		Status:   resource.RuntimeStatus{State: state},
	})
	require.NoError(t, err)
	return r
}

func createTicket03Session(t *testing.T, st *file.Store, runtimeRef, name string, state events.SessionState) resource.Session {
	t.Helper()
	r, err := st.CreateSession(resource.Session{
		Metadata: resource.Metadata{Namespace: "default", Name: name},
		Spec:     resource.SessionSpec{RuntimeRef: runtimeRef, SessionSpec: runtime.SessionSpec{Cwd: "/synthetic"}},
		Status: resource.SessionStatus{
			SessionID:         name,
			State:             state,
			AppliedGeneration: 2,
			RuntimeGeneration: 1,
		},
	})
	require.NoError(t, err)
	return r
}

func TestTicket03PrepareReconcilesPersistedStateWithoutStartingAnything(t *testing.T) {
	dir := t.TempDir()
	st := openTicket03StoreAt(t, dir)
	runtimes := map[string]resource.Runtime{}
	for _, state := range []string{"stopped", "starting", "running", "stopping", "unknown"} {
		runtimes[state] = createTicket03Runtime(t, st, "runtime-"+state, state)
	}
	sessions := map[events.SessionState]resource.Session{}
	for _, state := range []events.SessionState{"offline", "opening", events.Idle, events.Running, events.RequiresAction, "closing", events.Unknown} {
		name := "session-" + string(state)
		sessions[state] = createTicket03Session(t, st, "runtime-stopped", name, state)
	}
	archived := createTicket03Session(t, st, "runtime-stopped", "session-archived", "offline")
	archived.Status.State = "archived"
	writeLegacySessionState(t, dir, "file", archived)
	require.NoError(t, st.Close())
	st = openTicket03StoreAt(t, dir)
	sessions["archived"] = archived

	factory := &ticket03Factory{}
	svc := service.New(st, st, runtime.NewRegistry(map[string]runtime.Factory{"fixture": factory}))
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		require.NoError(t, svc.Shutdown(ctx))
	})
	require.NoError(t, svc.Prepare(context.Background()))

	require.Zero(t, factory.created.Load(), "startup reconciliation must not construct a Driver")

	for _, state := range []string{"stopped", "starting", "running", "stopping", "unknown"} {
		got, err := svc.GetRuntime("runtime-" + state)
		require.NoError(t, err)
		require.Equal(t, runtimes[state].Metadata.UID, got.Metadata.UID)
		require.Equal(t, runtimes[state].Spec, got.Spec)
		require.Equal(t, runtimes[state].Metadata.Generation, got.Metadata.Generation)
		if state == "stopped" {
			require.Equal(t, "stopped", got.Status.State)
			require.Equal(t, runtimes[state].Metadata.ResourceVersion, got.Metadata.ResourceVersion)
		} else {
			require.Equal(t, "unknown", got.Status.State)
		}
	}

	for state, original := range sessions {
		got, err := svc.GetSession(resource.SessionRef{SessionID: original.Status.SessionID})
		require.NoError(t, err)
		require.Equal(t, original.Metadata.UID, got.Metadata.UID)
		require.Equal(t, original.Spec, got.Spec)
		require.Equal(t, original.Metadata.Generation, got.Metadata.Generation)
		switch state {
		case "offline", events.Unknown:
			require.Equal(t, state, got.Status.State)
			require.Equal(t, original.Metadata.ResourceVersion, got.Metadata.ResourceVersion)
		case "archived":
			require.Equal(t, events.SessionState("offline"), got.Status.State)
			require.NotEqual(t, original.Metadata.ResourceVersion, got.Metadata.ResourceVersion)
			require.Zero(t, got.Status.AppliedGeneration)
			require.Zero(t, got.Status.RuntimeGeneration)
			require.Empty(t, got.Status.Reason)
		default:
			require.Equal(t, events.Unknown, got.Status.State)
			require.NotEmpty(t, got.Status.Reason)
			require.Equal(t, original.Status.AppliedGeneration, got.Status.AppliedGeneration)
			require.Equal(t, original.Status.RuntimeGeneration, got.Status.RuntimeGeneration)
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	oldRuntime, originalVersion := runtimes["running"], runtimes["running"].Metadata.ResourceVersion
	require.NoError(t, svc.Prepare(ctx))
	current, err := svc.GetRuntime(oldRuntime.Metadata.Name)
	require.NoError(t, err)
	require.Equal(t, "unknown", current.Status.State)
	require.NotEqual(t, originalVersion, current.Metadata.ResourceVersion)
	require.NoError(t, svc.Prepare(ctx))
	again, err := svc.GetRuntime(oldRuntime.Metadata.Name)
	require.NoError(t, err)
	require.Equal(t, current.Metadata.ResourceVersion, again.Metadata.ResourceVersion, "repeated startup must be idempotent")
	require.Zero(t, factory.created.Load())
}

func TestTicket03CreateSessionStartsRuntimeOnDemandAndReusesIt(t *testing.T) {
	st := openTicket03Store(t)
	createTicket03Runtime(t, st, "agent", "stopped")
	factory := &ticket03Factory{}
	svc := service.New(st, st, runtime.NewRegistry(map[string]runtime.Factory{"fixture": factory}))
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		require.NoError(t, svc.Shutdown(ctx))
	})

	first, err := svc.CreateSession(context.Background(), resource.Session{
		Metadata: resource.Metadata{Namespace: "default", Name: "first"},
		Spec:     resource.SessionSpec{RuntimeRef: "agent", SessionSpec: runtime.SessionSpec{Cwd: "/synthetic"}},
	})
	require.NoError(t, err)
	require.Equal(t, "first", first.Status.SessionID)
	require.EqualValues(t, 1, factory.created.Load())
	driver := factory.only(t)
	require.EqualValues(t, 1, driver.starts.Load())
	require.EqualValues(t, 1, driver.opens.Load())

	runtimeResource, err := svc.GetRuntime("agent")
	require.NoError(t, err)
	require.Equal(t, "running", runtimeResource.Status.State)

	second, err := svc.CreateSession(context.Background(), resource.Session{
		Metadata: resource.Metadata{Namespace: "default", Name: "second"},
		Spec:     resource.SessionSpec{RuntimeRef: "agent", SessionSpec: runtime.SessionSpec{Cwd: "/synthetic"}},
	})
	require.NoError(t, err)
	require.Equal(t, "second", second.Status.SessionID)
	require.EqualValues(t, 1, factory.created.Load())
	require.EqualValues(t, 1, driver.starts.Load())
	require.EqualValues(t, 2, driver.opens.Load())
}

func TestTicket03ConcurrentCreateAndExplicitStartShareOneRuntimeBinding(t *testing.T) {
	st := openTicket03Store(t)
	createTicket03Runtime(t, st, "agent", "stopped")
	gate := make(chan struct{})
	entered := make(chan struct{})
	factory := &ticket03Factory{configure: func(d *ticket03Driver) {
		d.startGate = gate
		d.started = entered
	}}
	svc := service.New(st, st, runtime.NewRegistry(map[string]runtime.Factory{"fixture": factory}))
	t.Cleanup(func() {
		select {
		case <-gate:
		default:
			close(gate)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		require.NoError(t, svc.Shutdown(ctx))
	})

	type result struct{ err error }
	started := make(chan result, 1)
	created := make(chan result, 1)
	go func() {
		_, err := svc.StartRuntime(context.Background(), "agent")
		started <- result{err: err}
	}()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("explicit start did not reach the Driver")
	}
	go func() {
		_, err := svc.CreateSession(context.Background(), resource.Session{
			Metadata: resource.Metadata{Namespace: "default", Name: "during-start"},
			Spec:     resource.SessionSpec{RuntimeRef: "agent", SessionSpec: runtime.SessionSpec{Cwd: "/synthetic"}},
		})
		created <- result{err: err}
	}()
	close(gate)
	require.NoError(t, (<-started).err)
	require.NoError(t, (<-created).err)

	require.EqualValues(t, 1, factory.created.Load(), "one runtime must have exactly one Driver")
	driver := factory.only(t)
	require.EqualValues(t, 1, driver.starts.Load())
	require.EqualValues(t, 1, driver.opens.Load())
}

func TestTicket03UnknownResumeStartsRuntimeAndUsesObservedState(t *testing.T) {
	st := openTicket03Store(t)
	createTicket03Runtime(t, st, "agent", "stopped")
	original := createTicket03Session(t, st, "agent", "restored", events.Unknown)
	factory := &ticket03Factory{configure: func(d *ticket03Driver) {
		d.resumeTo = events.Idle
	}}
	svc := service.New(st, st, runtime.NewRegistry(map[string]runtime.Factory{"fixture": factory}))
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		require.NoError(t, svc.Shutdown(ctx))
	})

	resumed, err := svc.ResumeSession(context.Background(), resource.SessionRef{SessionID: "restored"})
	require.NoError(t, err)
	require.Equal(t, original.Status.SessionID, resumed.Status.SessionID)
	require.EqualValues(t, 1, factory.created.Load())
	driver := factory.only(t)
	require.EqualValues(t, 1, driver.starts.Load())
	require.EqualValues(t, 1, driver.resumes.Load())
	require.Zero(t, driver.opens.Load())
	require.Zero(t, driver.prompts.Load())
	require.Zero(t, driver.answers.Load())

	require.Eventually(t, func() bool {
		r, err := svc.GetSession(resource.SessionRef{SessionID: "restored"})
		return err == nil && r.Status.State == events.Idle
	}, time.Second, time.Millisecond)
	runtimeResource, err := svc.GetRuntime("agent")
	require.NoError(t, err)
	require.Equal(t, "running", runtimeResource.Status.State)
}

func TestTicket03UnknownResumeFailureKeepsUnknown(t *testing.T) {
	st := openTicket03Store(t)
	createTicket03Runtime(t, st, "agent", "stopped")
	original := createTicket03Session(t, st, "agent", "restored", events.Unknown)
	factory := &ticket03Factory{configure: func(d *ticket03Driver) {
		d.resumeErr = errors.Join(runtime.ErrResourcesReleased, errors.New("native resume rejected"))
	}}
	svc := service.New(st, st, runtime.NewRegistry(map[string]runtime.Factory{"fixture": factory}))
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		require.NoError(t, svc.Shutdown(ctx))
	})

	_, err := svc.ResumeSession(context.Background(), resource.SessionRef{SessionID: "restored"})
	require.Error(t, err)
	current, err := svc.GetSession(resource.SessionRef{SessionID: "restored"})
	require.NoError(t, err)
	require.Equal(t, events.Unknown, current.Status.State)
	require.Equal(t, original.Spec, current.Spec)
	require.Equal(t, original.Metadata.UID, current.Metadata.UID)

	driver := factory.only(t)
	require.EqualValues(t, 1, driver.starts.Load())
	require.EqualValues(t, 1, driver.resumes.Load())
	require.Zero(t, driver.opens.Load())
	require.Zero(t, driver.prompts.Load())
	require.Zero(t, driver.answers.Load())
	runtimeResource, err := svc.GetRuntime("agent")
	require.NoError(t, err)
	require.Equal(t, "running", runtimeResource.Status.State)
}

func TestTicket03ReadOperationsDoNotStartRuntime(t *testing.T) {
	st := openTicket03Store(t)
	createTicket03Runtime(t, st, "agent", "stopped")
	original := createTicket03Session(t, st, "agent", "offline", "offline")
	factory := &ticket03Factory{}
	svc := service.New(st, st, runtime.NewRegistry(map[string]runtime.Factory{"fixture": factory}))
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		require.NoError(t, svc.Shutdown(ctx))
	})
	ref := resource.SessionRef{SessionID: original.Status.SessionID}

	_, err := svc.GetSession(ref)
	require.NoError(t, err)
	_, err = svc.ListSessions(resource.SessionQuery{})
	require.NoError(t, err)
	_, err = svc.ListHistoryEvents(ref, store.HistoryQuery{})
	require.NoError(t, err)
	subscription, err := svc.SubscribeEvents(ref)
	require.NoError(t, err)
	require.NoError(t, subscription.Close())

	current, err := svc.GetSession(ref)
	require.NoError(t, err)
	require.Equal(t, events.SessionState("offline"), current.Status.State)
	runtimeResource, err := svc.GetRuntime("agent")
	require.NoError(t, err)
	require.Equal(t, "stopped", runtimeResource.Status.State)
	require.Zero(t, factory.created.Load())
}

func requireTicket03ResumeError(t *testing.T, err error, state events.SessionState) {
	t.Helper()
	require.Error(t, err)
	var public *runtime.OperationError
	require.ErrorAs(t, err, &public)
	require.Equal(t, "conflict", public.Code)
	require.Equal(t, "session_resume_required", public.Reason)
	require.Contains(t, strings.ToLower(public.Message), string(state))
	require.Contains(t, strings.ToLower(public.Message), "resume")
}

func TestTicket03PromptFailsFastOnDetachedUnknownAndNonExecutableStates(t *testing.T) {
	st := openTicket03Store(t)
	createTicket03Runtime(t, st, "agent", "stopped")
	for _, state := range []events.SessionState{"offline", events.Unknown, "opening", "closing"} {
		createTicket03Session(t, st, "agent", "prompt-"+string(state), state)
	}
	factory := &ticket03Factory{}
	svc := service.New(st, st, runtime.NewRegistry(map[string]runtime.Factory{"fixture": factory}))
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		require.NoError(t, svc.Shutdown(ctx))
	})

	for _, state := range []events.SessionState{"offline", events.Unknown, "opening", "closing"} {
		t.Run(string(state), func(t *testing.T) {
			_, err := svc.SendPrompt(context.Background(), resource.SessionRef{SessionID: "prompt-" + string(state)}, runtime.Input{Content: []events.ContentBlock{events.Text("must not dispatch")}})
			requireTicket03ResumeError(t, err, state)
		})
	}
	require.Zero(t, factory.created.Load(), "prompt must not start Runtime")
}

func TestTicket03AnswerAndCancelFailFastWithoutEffectiveRun(t *testing.T) {
	st := openTicket03Store(t)
	createTicket03Runtime(t, st, "agent", "stopped")
	createTicket03Session(t, st, "agent", "detached", "offline")
	factory := &ticket03Factory{}
	svc := service.New(st, st, runtime.NewRegistry(map[string]runtime.Factory{"fixture": factory}))
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		require.NoError(t, svc.Shutdown(ctx))
	})
	ref := resource.SessionRef{SessionID: "detached"}

	_, err := svc.AnswerInteraction(context.Background(), ref, "old-interaction", map[string]any{"outcome": "selected", "optionId": "allow-once"})
	requireTicket03ResumeError(t, err, "offline")
	_, err = svc.Cancel(context.Background(), ref)
	requireTicket03ResumeError(t, err, "offline")
	require.Zero(t, factory.created.Load())
}

func TestTicket03WorkDispatchesOnlyWithEffectiveRunAndExecutableState(t *testing.T) {
	st := openTicket03Store(t)
	createTicket03Runtime(t, st, "agent", "stopped")
	factory := &ticket03Factory{}
	svc := service.New(st, st, runtime.NewRegistry(map[string]runtime.Factory{"fixture": factory}))
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		require.NoError(t, svc.Shutdown(ctx))
	})

	for _, state := range []events.SessionState{events.Idle, events.Running, events.RequiresAction} {
		created, err := svc.CreateSession(context.Background(), resource.Session{
			Metadata: resource.Metadata{Namespace: "default", Name: "executable-" + strings.ReplaceAll(string(state), "_", "-")},
			Spec:     resource.SessionSpec{RuntimeRef: "agent", SessionSpec: runtime.SessionSpec{Cwd: "/synthetic"}},
		})
		require.NoError(t, err)
		created.Status.State = state
		_, err = st.UpdateSession(created)
		require.NoError(t, err)
		_, err = svc.SendPrompt(context.Background(), resource.SessionRef{SessionID: created.Status.SessionID}, runtime.Input{Content: []events.ContentBlock{events.Text("dispatch")}})
		require.NoError(t, err)
	}
	driver := factory.only(t)
	require.EqualValues(t, 3, driver.prompts.Load())
	require.Zero(t, driver.resumes.Load())

	created, err := svc.CreateSession(context.Background(), resource.Session{
		Metadata: resource.Metadata{Namespace: "default", Name: "actionable"},
		Spec:     resource.SessionSpec{RuntimeRef: "agent", SessionSpec: runtime.SessionSpec{Cwd: "/synthetic"}},
	})
	require.NoError(t, err)
	created.Status.State = events.RequiresAction
	_, err = st.UpdateSession(created)
	require.NoError(t, err)
	_, err = svc.AnswerInteraction(context.Background(), resource.SessionRef{SessionID: created.Status.SessionID}, "question", map[string]any{"outcome": "selected", "optionId": "allow-once"})
	require.NoError(t, err)

	created, err = svc.GetSession(resource.SessionRef{SessionID: created.Status.SessionID})
	require.NoError(t, err)
	created.Status.State = events.Running
	_, err = st.UpdateSession(created)
	require.NoError(t, err)
	_, err = svc.Cancel(context.Background(), resource.SessionRef{SessionID: created.Status.SessionID})
	require.NoError(t, err)
	require.EqualValues(t, 1, driver.answers.Load())
	require.EqualValues(t, 1, driver.cancels.Load())
}

func TestTicket03FreshLiveUnknownAdmitsWorkBeforeObservedState(t *testing.T) {
	st := openTicket03Store(t)
	createTicket03Runtime(t, st, "agent", "stopped")
	factory := &ticket03Factory{}
	svc := service.New(st, st, runtime.NewRegistry(map[string]runtime.Factory{"fixture": factory}))
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		require.NoError(t, svc.Shutdown(ctx))
	})

	created, err := svc.CreateSession(context.Background(), resource.Session{
		Metadata: resource.Metadata{Namespace: "default", Name: "fresh"},
		Spec:     resource.SessionSpec{RuntimeRef: "agent", SessionSpec: runtime.SessionSpec{Cwd: "/synthetic"}},
	})
	require.NoError(t, err)
	require.Equal(t, events.Unknown, created.Status.State)

	_, err = svc.SendPrompt(context.Background(), resource.SessionRef{SessionID: created.Status.SessionID}, runtime.Input{Content: []events.ContentBlock{events.Text("dispatch")}})
	require.NoError(t, err)
	require.EqualValues(t, 1, factory.only(t).prompts.Load())
	require.EqualValues(t, 1, factory.only(t).opens.Load(), "prompt must not reopen the Session")
	require.Zero(t, factory.only(t).resumes.Load(), "prompt must not resume the Session")
}

func TestTicket03ObservedUnknownRejectsWorkWithoutDispatchSideEffects(t *testing.T) {
	st := openTicket03Store(t)
	createTicket03Runtime(t, st, "agent", "stopped")
	factory := &ticket03Factory{}
	svc := service.New(st, st, runtime.NewRegistry(map[string]runtime.Factory{"fixture": factory}))
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		require.NoError(t, svc.Shutdown(ctx))
	})

	created, err := svc.CreateSession(context.Background(), resource.Session{
		Metadata: resource.Metadata{Namespace: "default", Name: "lost"},
		Spec:     resource.SessionSpec{RuntimeRef: "agent", SessionSpec: runtime.SessionSpec{Cwd: "/synthetic"}},
	})
	require.NoError(t, err)
	driver := factory.only(t)
	subscription, err := svc.SubscribeEvents(resource.SessionRef{SessionID: created.Status.SessionID})
	require.NoError(t, err)
	defer subscription.Close()

	event, err := events.NewEvent(events.EventBase{
		EventID:     "10000000-0000-4000-8000-000000000304",
		Namespace:   created.Metadata.Namespace,
		SessionName: created.Metadata.Name,
		SessionID:   created.Status.SessionID,
		Time:        time.Now().UTC(),
	}, events.StateUpdate{}, driver.source)
	require.NoError(t, err)
	require.NoError(t, driver.pipe.Send(context.Background(), event))
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_, err = nextSubscription(subscription, ctx)
	require.NoError(t, err)

	_, err = svc.SendPrompt(context.Background(), resource.SessionRef{SessionID: created.Status.SessionID}, runtime.Input{Content: []events.ContentBlock{events.Text("must not dispatch")}})
	requireTicket03ResumeError(t, err, events.Unknown)
	require.Zero(t, driver.prompts.Load(), "an observed unknown Session must not reach the Driver")
	require.Zero(t, driver.resumes.Load(), "prompt must not implicitly resume the Session")
	require.EqualValues(t, 1, driver.opens.Load(), "prompt must not open another Session")
	require.EqualValues(t, 1, factory.created.Load(), "prompt must reuse the bound Runtime")

	_, err = svc.Cancel(context.Background(), resource.SessionRef{SessionID: created.Status.SessionID})
	require.NoError(t, err)
	require.EqualValues(t, 1, driver.cancels.Load())
}

func TestTicket03DetachedUnknownRejectsWorkWithoutDispatchSideEffects(t *testing.T) {
	st := openTicket03Store(t)
	createTicket03Runtime(t, st, "agent", "stopped")
	factory := &ticket03Factory{}
	svc := service.New(st, st, runtime.NewRegistry(map[string]runtime.Factory{"fixture": factory}))
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		require.NoError(t, svc.Shutdown(ctx))
	})

	_, err := svc.StartRuntime(context.Background(), "agent")
	require.NoError(t, err)
	driver := factory.only(t)
	require.EqualValues(t, 1, driver.starts.Load())

	detached := createTicket03Session(t, st, "agent", "detached-unknown", events.Unknown)
	_, err = svc.SendPrompt(context.Background(), resource.SessionRef{SessionID: detached.Status.SessionID}, runtime.Input{Content: []events.ContentBlock{events.Text("must not dispatch")}})
	requireTicket03ResumeError(t, err, events.Unknown)
	require.Zero(t, driver.prompts.Load(), "a Session without a current binding must not reach the Driver")
	require.Zero(t, driver.resumes.Load(), "prompt must not implicitly resume the Session")
	require.Zero(t, driver.opens.Load(), "prompt must not implicitly open the Session")
	require.EqualValues(t, 1, factory.created.Load(), "prompt must reuse the running Runtime")
}

func TestTicket03ResumeRejectsNonResumableStateBeforeStartingRuntime(t *testing.T) {
	st := openTicket03Store(t)
	createTicket03Runtime(t, st, "agent", "stopped")
	createTicket03Session(t, st, "agent", "still-idle", events.Idle)
	factory := &ticket03Factory{}
	svc := service.New(st, st, runtime.NewRegistry(map[string]runtime.Factory{"fixture": factory}))
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		require.NoError(t, svc.Shutdown(ctx))
	})

	_, err := svc.ResumeSession(context.Background(), resource.SessionRef{SessionID: "still-idle"})
	require.ErrorIs(t, err, runtime.ErrFailedPrecondition)
	require.Zero(t, factory.created.Load(), "resume must reject before starting the Runtime")
}

func TestTicket03DuplicateCreateDoesNotStartRuntime(t *testing.T) {
	st := openTicket03Store(t)
	createTicket03Runtime(t, st, "agent", "stopped")
	createTicket03Session(t, st, "agent", "existing", "offline")
	factory := &ticket03Factory{}
	svc := service.New(st, st, runtime.NewRegistry(map[string]runtime.Factory{"fixture": factory}))
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		require.NoError(t, svc.Shutdown(ctx))
	})

	_, err := svc.CreateSession(context.Background(), resource.Session{
		Metadata: resource.Metadata{Namespace: "default", Name: "existing"},
		Spec:     resource.SessionSpec{RuntimeRef: "agent", SessionSpec: runtime.SessionSpec{Cwd: "/synthetic"}},
	})
	require.ErrorIs(t, err, store.ErrConflict)
	require.Zero(t, factory.created.Load(), "duplicate create must not start the Runtime")
}
