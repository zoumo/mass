package service_test

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/zoumo/mass/mass-go/pkg/events"
	"github.com/zoumo/mass/mass-go/pkg/resource"
	"github.com/zoumo/mass/mass-go/pkg/runtime"
	"github.com/zoumo/mass/mass-go/pkg/service"
	"github.com/zoumo/mass/mass-go/pkg/store"
	"github.com/zoumo/mass/mass-go/pkg/store/file"
)

type manualTimer struct {
	clock    *manualClock
	deadline time.Duration
	callback func()
	stopped  bool
	fired    bool
}

func (t *manualTimer) Stop() bool {
	t.clock.mu.Lock()
	defer t.clock.mu.Unlock()
	if t.stopped || t.fired {
		return false
	}
	t.stopped = true
	delete(t.clock.timers, t)
	return true
}

type manualClock struct {
	mu     sync.Mutex
	now    time.Duration
	timers map[*manualTimer]struct{}
}

func newManualClock() *manualClock {
	return &manualClock{timers: map[*manualTimer]struct{}{}}
}

func (c *manualClock) AfterFunc(delay time.Duration, callback func()) service.Timer {
	c.mu.Lock()
	defer c.mu.Unlock()
	timer := &manualTimer{clock: c, deadline: c.now + delay, callback: callback}
	c.timers[timer] = struct{}{}
	return timer
}

func (c *manualClock) Advance(elapsed time.Duration) {
	c.mu.Lock()
	c.now += elapsed
	var due []*manualTimer
	for timer := range c.timers {
		if timer.deadline <= c.now {
			timer.fired = true
			delete(c.timers, timer)
			due = append(due, timer)
		}
	}
	c.mu.Unlock()
	for _, timer := range due {
		timer.callback()
	}
}

func (c *manualClock) Active() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.timers)
}

type idleFixture struct {
	clock  *manualClock
	driver *closeDriver
	store  *file.Store
	svc    *service.Service
	ref    resource.SessionRef
}

func newIdleFixture(t *testing.T, timeout time.Duration) *idleFixture {
	t.Helper()
	st, err := file.Open(t.TempDir(), false)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, st.Close()) })
	clock := newManualClock()
	d := &closeDriver{
		pipe:          events.NewEventPipe(64),
		promptEntered: make(chan context.Context, 4),
		promptRelease: make(chan struct{}),
		resumeEntered: make(chan struct{}, 4),
		resumeRelease: make(chan struct{}),
		closeEntered:  make(chan struct{}, 4),
		closeRelease:  make(chan struct{}),
	}
	svc := service.NewWithOptions(st, st, runtime.NewRegistry(map[string]runtime.Factory{"fixture": closeFactory{d}}), service.Options{
		SessionIdleTimeout: timeout,
		Clock:              clock,
	})
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
	return &idleFixture{clock: clock, driver: d, store: st, svc: svc, ref: resource.SessionRef{SessionID: created.Status.SessionID}}
}

func (f *idleFixture) emitState(t *testing.T, state events.SessionState) {
	t.Helper()
	f.emit(t, events.StateUpdate{State: &state})
}

func (f *idleFixture) emit(t *testing.T, payload events.Payload) {
	t.Helper()
	f.driver.mu.Lock()
	source := f.driver.source
	f.driver.mu.Unlock()
	event, err := events.NewEvent(events.EventBase{
		EventID:     uuid.NewString(),
		Namespace:   "one",
		SessionName: "demo",
		SessionID:   f.ref.SessionID,
		Time:        time.Now().UTC(),
		ItemID:      "idle-test",
	}, payload, source)
	require.NoError(t, err)
	require.NoError(t, f.driver.pipe.Send(context.Background(), event))
	require.NoError(t, f.driver.pipe.Flush(context.Background()))
}

func (f *idleFixture) waitIdle(t *testing.T) {
	t.Helper()
	f.emitState(t, events.Idle)
	require.Eventually(t, func() bool {
		r, err := f.svc.GetSession(f.ref)
		return err == nil && r.Status.State == events.Idle
	}, time.Second, time.Millisecond)
	require.Eventually(t, func() bool { return f.clock.Active() == 1 }, time.Second, time.Millisecond)
}

func TestIdleSessionAutoClosesWithoutStoppingRuntime(t *testing.T) {
	f := newIdleFixture(t, 5*time.Second)
	f.driver.releaseClose()
	beforeRuntime, err := f.svc.GetRuntime("agent")
	require.NoError(t, err)
	beforeSession := mustSession(t, f.svc, f.ref)
	f.waitIdle(t)

	f.clock.Advance(5 * time.Second)
	require.Eventually(t, func() bool {
		current, getErr := f.svc.GetSession(f.ref)
		return getErr == nil && current.Status.State == "offline"
	}, time.Second, time.Millisecond)

	current := mustSession(t, f.svc, f.ref)
	require.Equal(t, beforeSession.Metadata.Generation, current.Metadata.Generation)
	require.EqualValues(t, 1, f.driver.closes.Load())
	require.Zero(t, f.driver.stops.Load())
	afterRuntime, err := f.svc.GetRuntime("agent")
	require.NoError(t, err)
	require.Equal(t, "running", afterRuntime.Status.State)
	require.Equal(t, beforeRuntime.Metadata.Generation, afterRuntime.Metadata.Generation)
	require.Equal(t, beforeRuntime.Status.AppliedGeneration, afterRuntime.Status.AppliedGeneration)
}

func TestIdleSessionKnownEventResetsDeadline(t *testing.T) {
	f := newIdleFixture(t, 5*time.Second)
	f.driver.releaseClose()
	f.waitIdle(t)

	f.clock.Advance(4 * time.Second)
	f.waitIdle(t)
	f.clock.Advance(4 * time.Second)
	select {
	case <-f.driver.closeEntered:
		t.Fatal("idle timer was not reset by a known runtime event")
	case <-time.After(50 * time.Millisecond):
	}
	require.Equal(t, events.Idle, mustSession(t, f.svc, f.ref).Status.State)

	f.clock.Advance(time.Second)
	require.Eventually(t, func() bool { return f.driver.closes.Load() == 1 }, time.Second, time.Millisecond)
}

func TestIdleReadsDoNotResetDeadline(t *testing.T) {
	f := newIdleFixture(t, 5*time.Second)
	f.driver.releaseClose()
	f.waitIdle(t)
	f.clock.Advance(4 * time.Second)

	_, err := f.svc.GetSession(f.ref)
	require.NoError(t, err)
	_, err = f.svc.ListHistoryEvents(f.ref, store.HistoryQuery{})
	require.NoError(t, err)
	subscription, err := f.svc.SubscribeEvents(f.ref)
	require.NoError(t, err)
	require.NoError(t, subscription.Close())

	f.clock.Advance(time.Second)
	require.Eventually(t, func() bool { return f.driver.closes.Load() == 1 }, time.Second, time.Millisecond)
}

func TestIdleSessionNonIdleStateDoesNotAutoClose(t *testing.T) {
	for _, state := range []events.SessionState{events.Running, events.RequiresAction} {
		t.Run(string(state), func(t *testing.T) {
			f := newIdleFixture(t, time.Second)
			f.driver.releaseClose()
			f.emitState(t, state)
			require.Eventually(t, func() bool {
				current, err := f.svc.GetSession(f.ref)
				return err == nil && current.Status.State == state
			}, time.Second, time.Millisecond)
			require.Eventually(t, func() bool { return f.clock.Active() == 0 }, time.Second, time.Millisecond)

			f.clock.Advance(time.Hour)
			select {
			case <-f.driver.closeEntered:
				t.Fatalf("%s Session was automatically closed", state)
			case <-time.After(50 * time.Millisecond):
			}
			require.Zero(t, f.driver.closes.Load())
		})
	}
}

func TestIdleSessionUnknownWithoutObservationDoesNotAutoClose(t *testing.T) {
	f := newIdleFixture(t, time.Second)
	f.driver.releaseClose()
	require.Eventually(t, func() bool {
		current, err := f.svc.GetSession(f.ref)
		return err == nil && current.Status.State == events.Unknown
	}, time.Second, time.Millisecond)
	require.Zero(t, f.clock.Active())

	f.clock.Advance(time.Hour)
	select {
	case <-f.driver.closeEntered:
		t.Fatal("unknown Session was automatically closed")
	case <-time.After(50 * time.Millisecond):
	}
	require.Zero(t, f.driver.closes.Load())
}

func TestIdleExpiryRevalidatesPersistedState(t *testing.T) {
	for _, state := range []events.SessionState{"opening", events.Running, events.RequiresAction, "closing", "offline", events.Unknown} {
		t.Run(string(state), func(t *testing.T) {
			f := newIdleFixture(t, time.Second)
			f.driver.releaseClose()
			f.waitIdle(t)

			current := mustSession(t, f.svc, f.ref)
			current.Status.State = state
			_, err := f.store.UpdateSession(current)
			require.NoError(t, err)

			f.clock.Advance(time.Second)
			select {
			case <-f.driver.closeEntered:
				t.Fatalf("expired idle observation closed a Session persisted as %s", state)
			case <-time.After(50 * time.Millisecond):
			}
			require.Zero(t, f.driver.closes.Load())
		})
	}
}

func TestIdleWaitsForBlockingInteractionToClose(t *testing.T) {
	f := newIdleFixture(t, time.Second)
	f.driver.releaseClose()
	f.waitIdle(t)

	subscription, err := f.svc.SubscribeEvents(f.ref)
	require.NoError(t, err)
	defer subscription.Close()
	f.emit(t, events.InteractionRequested{Interaction: "ask", Request: events.PermissionRequest("tool", "Allow?"), Blocking: true})
	select {
	case <-subscription.Events():
	case <-time.After(time.Second):
		t.Fatal("interaction event was not processed")
	}
	require.Eventually(t, func() bool { return f.clock.Active() == 0 }, time.Second, time.Millisecond)
	f.clock.Advance(time.Hour)
	select {
	case <-f.driver.closeEntered:
		t.Fatal("Session with a waiting interaction was automatically closed")
	case <-time.After(50 * time.Millisecond):
	}

	f.emit(t, events.InteractionClosed{Interaction: "ask", Outcome: "answered"})
	require.Eventually(t, func() bool { return f.clock.Active() == 1 }, time.Second, time.Millisecond)
	f.clock.Advance(time.Second)
	require.Eventually(t, func() bool { return f.driver.closes.Load() == 1 }, time.Second, time.Millisecond)
}

func TestIdleWaitsForKnownToolWorkToFinish(t *testing.T) {
	f := newIdleFixture(t, time.Second)
	f.driver.releaseClose()
	f.waitIdle(t)

	subscription, err := f.svc.SubscribeEvents(f.ref)
	require.NoError(t, err)
	defer subscription.Close()
	f.emit(t, events.ToolChunk{ID: "tool-1", Content: []events.ContentBlock{events.Text("working")}})
	select {
	case <-subscription.Events():
	case <-time.After(time.Second):
		t.Fatal("tool chunk was not processed")
	}
	require.Eventually(t, func() bool { return f.clock.Active() == 0 }, time.Second, time.Millisecond)
	f.emit(t, events.ToolCall{ID: "tool-1", Kind: events.ToolKindExecute, ToolName: "Bash", Provider: events.ToolProviderAgent, Status: "unknown", RawInput: map[string]any{"command": "sleep"}})
	select {
	case <-subscription.Events():
	case <-time.After(time.Second):
		t.Fatal("tool event was not processed")
	}
	require.Eventually(t, func() bool { return f.clock.Active() == 0 }, time.Second, time.Millisecond)
	f.clock.Advance(time.Hour)
	select {
	case <-f.driver.closeEntered:
		t.Fatal("Session with known tool work was automatically closed")
	case <-time.After(50 * time.Millisecond):
	}

	f.emit(t, events.ToolResult{ID: "tool-1", Kind: events.ToolKindExecute, ToolName: "Bash", Provider: events.ToolProviderAgent, Status: "completed"})
	require.Eventually(t, func() bool { return f.clock.Active() == 1 }, time.Second, time.Millisecond)
	f.clock.Advance(time.Second)
	require.Eventually(t, func() bool { return f.driver.closes.Load() == 1 }, time.Second, time.Millisecond)
}

func TestIdleCancelWithoutWorkRestartsDeadline(t *testing.T) {
	f := newIdleFixture(t, 5*time.Second)
	f.driver.releaseClose()
	f.waitIdle(t)
	f.clock.Advance(4 * time.Second)

	receipt, err := f.svc.Cancel(context.Background(), f.ref)
	require.NoError(t, err)
	require.Equal(t, "noActiveWork", receipt.Result)
	require.EqualValues(t, 1, f.driver.cancels.Load())
	require.Eventually(t, func() bool { return f.clock.Active() == 1 }, time.Second, time.Millisecond)

	f.clock.Advance(4 * time.Second)
	select {
	case <-f.driver.closeEntered:
		t.Fatal("idle deadline was not reset by accepted cancel activity")
	case <-time.After(50 * time.Millisecond):
	}
	f.clock.Advance(time.Second)
	require.Eventually(t, func() bool { return f.driver.closes.Load() == 1 }, time.Second, time.Millisecond)
}

func TestIdleAcceptedPromptInvalidatesTimer(t *testing.T) {
	f := newIdleFixture(t, 5*time.Second)
	f.driver.releaseClose()
	f.waitIdle(t)
	f.clock.Advance(4 * time.Second)

	promptDone := make(chan error, 1)
	go func() {
		_, err := f.svc.SendPrompt(context.Background(), f.ref, runtime.Input{Content: []events.ContentBlock{events.Text("accepted")}})
		promptDone <- err
	}()
	select {
	case <-f.driver.promptEntered:
	case err := <-promptDone:
		t.Fatalf("prompt was not admitted: %v", err)
	case <-time.After(time.Second):
		t.Fatal("prompt was not admitted")
	}

	f.clock.Advance(5 * time.Second)
	select {
	case <-f.driver.closeEntered:
		t.Fatal("idle close overtook an in-flight prompt")
	case <-time.After(50 * time.Millisecond):
	}
	f.driver.releasePrompt()
	require.NoError(t, <-promptDone)
	require.Zero(t, f.driver.closes.Load())

	f.emitState(t, events.Idle)
	require.Eventually(t, func() bool { return f.clock.Active() == 1 }, time.Second, time.Millisecond)
	f.clock.Advance(5 * time.Second)
	require.Eventually(t, func() bool { return f.driver.closes.Load() == 1 }, time.Second, time.Millisecond)
}

func TestIdleCloseFailureStaysClosingWithoutRetry(t *testing.T) {
	f := newIdleFixture(t, time.Second)
	f.driver.closeErrors = []error{errors.New("private close failure")}
	f.driver.releaseClose()
	f.waitIdle(t)

	f.clock.Advance(time.Second)
	require.Eventually(t, func() bool {
		current, err := f.svc.GetSession(f.ref)
		return err == nil && current.Status.State == "closing" && current.Status.Reason == "session_close_failed"
	}, time.Second, time.Millisecond)
	require.EqualValues(t, 1, f.driver.closes.Load())

	select {
	case <-f.driver.closeEntered:
	default:
	}
	f.clock.Advance(time.Hour)
	select {
	case <-f.driver.closeEntered:
		t.Fatal("failed automatic close was retried")
	case <-time.After(50 * time.Millisecond):
	}
	require.EqualValues(t, 1, f.driver.closes.Load())
	require.Equal(t, events.SessionState("closing"), mustSession(t, f.svc, f.ref).Status.State)
}

func TestIdleExpiryDoesNotDuplicateInFlightManualClose(t *testing.T) {
	f := newIdleFixture(t, time.Second)
	f.waitIdle(t)

	closed := make(chan error, 1)
	go func() {
		_, err := f.svc.CloseSession(context.Background(), f.ref)
		closed <- err
	}()
	select {
	case <-f.driver.closeEntered:
	case <-time.After(time.Second):
		t.Fatal("manual close did not enter the SDK")
	}

	f.clock.Advance(time.Hour)
	select {
	case <-f.driver.closeEntered:
		t.Fatal("idle expiry dispatched a duplicate close")
	case <-time.After(50 * time.Millisecond):
	}
	require.EqualValues(t, 1, f.driver.closes.Load())

	f.driver.releaseClose()
	require.NoError(t, <-closed)
	require.Equal(t, events.SessionState("offline"), mustSession(t, f.svc, f.ref).Status.State)
	f.clock.Advance(time.Hour)
	require.EqualValues(t, 1, f.driver.closes.Load())
}

func TestRestartDoesNotCreateIdleTimersForHistoricalSessions(t *testing.T) {
	st := openTicket03Store(t)
	createTicket03Runtime(t, st, "agent", "stopped")
	createTicket03Session(t, st, "agent", "idle-history", events.Idle)
	createTicket03Session(t, st, "agent", "running-history", events.Running)

	clock := newManualClock()
	factory := &ticket03Factory{}
	svc := service.NewWithOptions(st, st, runtime.NewRegistry(map[string]runtime.Factory{"fixture": factory}), service.Options{
		SessionIdleTimeout: time.Second,
		Clock:              clock,
	})
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		require.NoError(t, svc.Shutdown(ctx))
	})
	require.NoError(t, svc.Prepare(context.Background()))
	require.Zero(t, factory.created.Load())
	require.Zero(t, clock.Active())

	clock.Advance(24 * time.Hour)
	require.Zero(t, clock.Active())
	require.Zero(t, factory.created.Load())
	for _, name := range []string{"idle-history", "running-history"} {
		current, err := svc.GetSession(resource.SessionRef{Namespace: "default", Name: name})
		require.NoError(t, err)
		require.Equal(t, events.Unknown, current.Status.State)
	}
}

type idleReplayDriver struct {
	runtime.Driver
	pipe   *events.EventPipe
	mu     sync.Mutex
	source events.Source
	closes atomic.Int32
}

func (d *idleReplayDriver) Events() (events.Stream, error) { return d.pipe, nil }
func (*idleReplayDriver) Start(context.Context) (runtime.Capabilities, error) {
	return runtime.Capabilities{}, nil
}

func (d *idleReplayDriver) LoadSession(ctx context.Context, id string, address runtime.Address, _ runtime.SessionSpec, loadID string) error {
	source := events.NewSource()
	d.mu.Lock()
	d.source = source
	d.mu.Unlock()
	for _, payload := range []events.Payload{
		events.HistoryReplay{LoadID: loadID, Phase: "started", RetainedScope: "all_retained_main_history", ResumedSession: &events.ResumedSession{SessionID: id, Capabilities: map[string]bool{}, EffectiveFeatures: events.ResumedSessionFeatures{}}},
		events.ToolCall{ID: "old-tool", Kind: events.ToolKindExecute, ToolName: "Bash", Provider: events.ToolProviderAgent, Status: "in_progress", RawInput: map[string]any{"command": "old"}},
		events.HistoryReplay{LoadID: loadID, Phase: "native_completed", RetainedScope: "all_retained_main_history", ResumedSession: &events.ResumedSession{SessionID: id, Capabilities: map[string]bool{}, EffectiveFeatures: events.ResumedSessionFeatures{}}},
	} {
		event, err := events.NewEvent(events.EventBase{
			EventID:     uuid.NewString(),
			Namespace:   address.Namespace,
			SessionName: address.SessionName,
			SessionID:   id,
			Time:        time.Now(),
			ItemID:      "old-tool",
			Replay:      &events.ReplaySource{LoadID: loadID, NativeRecordID: "old-tool"},
		}, payload, source)
		if err != nil {
			return err
		}
		if err := d.pipe.Send(ctx, event); err != nil {
			return err
		}
	}
	return nil
}
func (*idleReplayDriver) CancelHistoryLoad(context.Context, string, string) error { return nil }
func (d *idleReplayDriver) CloseSession(context.Context, string) error {
	d.closes.Add(1)
	return nil
}

func (d *idleReplayDriver) Stop(context.Context) error {
	d.pipe.End(nil)
	return nil
}

type idleReplayFactory struct{ driver *idleReplayDriver }

func (idleReplayFactory) Validate(runtime.Config) error { return nil }
func (f idleReplayFactory) New(string, runtime.Config) (runtime.Driver, error) {
	return f.driver, nil
}

func TestIdleHistoryReplayToolWorkDoesNotSuppressClose(t *testing.T) {
	st, err := file.Open(t.TempDir(), false)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, st.Close()) })
	clock := newManualClock()
	driver := &idleReplayDriver{pipe: events.NewEventPipe(64)}
	svc := service.NewWithOptions(st, st, runtime.NewRegistry(map[string]runtime.Factory{"fixture": idleReplayFactory{driver: driver}}), service.Options{
		SessionIdleTimeout: time.Second,
		Clock:              clock,
	})
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		require.NoError(t, svc.Shutdown(ctx))
	})
	_, err = svc.CreateRuntime(resource.Runtime{Metadata: resource.Metadata{Name: "agent"}, Spec: resource.RuntimeSpec{Type: "fixture", Config: runtime.Config{}}})
	require.NoError(t, err)
	_, err = svc.StartRuntime(context.Background(), "agent")
	require.NoError(t, err)

	loaded, err := svc.LoadSession(context.Background(), resource.SessionLoad{
		Metadata:   resource.Metadata{Namespace: "one", Name: "imported"},
		RuntimeRef: "agent",
		Cwd:        "/remote",
		SessionID:  "native-idle",
	})
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		current, getErr := svc.GetSession(resource.SessionRef{SessionID: "native-idle"})
		return getErr == nil && current.Status.HistoryLoad != nil && current.Status.HistoryLoad.Phase == "completed"
	}, time.Second, time.Millisecond)

	driver.mu.Lock()
	source := driver.source
	driver.mu.Unlock()
	state := events.Idle
	event, err := events.NewEvent(events.EventBase{
		EventID:     uuid.NewString(),
		Namespace:   "one",
		SessionName: "imported",
		SessionID:   "native-idle",
		Time:        time.Now(),
	}, events.StateUpdate{State: &state}, source)
	require.NoError(t, err)
	require.NoError(t, driver.pipe.Send(context.Background(), event))
	require.NoError(t, driver.pipe.Flush(context.Background()))
	require.Eventually(t, func() bool {
		current, getErr := svc.GetSession(resource.SessionRef{SessionID: "native-idle"})
		return getErr == nil && current.Status.State == events.Idle
	}, time.Second, time.Millisecond)
	require.Eventually(t, func() bool { return clock.Active() == 1 }, time.Second, time.Millisecond)

	clock.Advance(time.Second)
	require.Eventually(t, func() bool { return driver.closes.Load() == 1 }, time.Second, time.Millisecond)
	current, err := svc.GetSession(resource.SessionRef{SessionID: loaded.Session.Status.SessionID})
	require.NoError(t, err)
	require.Equal(t, events.SessionState("offline"), current.Status.State)
}

func TestZeroIdleTimeoutDisablesAutomaticClose(t *testing.T) {
	f := newIdleFixture(t, 0)
	f.driver.releaseClose()
	f.emitState(t, events.Idle)
	require.Eventually(t, func() bool {
		current, err := f.svc.GetSession(f.ref)
		return err == nil && current.Status.State == events.Idle
	}, time.Second, time.Millisecond)
	require.Zero(t, f.clock.Active())

	f.clock.Advance(24 * time.Hour)
	select {
	case <-f.driver.closeEntered:
		t.Fatal("zero idle timeout must disable automatic close")
	case <-time.After(50 * time.Millisecond):
	}
	require.Zero(t, f.driver.closes.Load())
}
