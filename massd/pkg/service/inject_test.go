package service_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"github.com/zoumo/agent-sdk-go/agent"

	"github.com/zoumo/mass/internal/massdtest"
	"github.com/zoumo/mass/massd/pkg/events"
	"github.com/zoumo/mass/massd/pkg/resource"
	"github.com/zoumo/mass/massd/pkg/runtime"
	"github.com/zoumo/mass/massd/pkg/service"
)

type injectWorkDriver struct {
	*workDriver
	receipt runtime.InjectReceipt
	err     error
}

func (d *injectWorkDriver) Inject(ctx context.Context, _ string, _ runtime.Input) (runtime.InjectReceipt, error) {
	d.calls.Add(1)
	d.entered <- ctx
	select {
	case <-d.release:
	case <-ctx.Done():
		return runtime.InjectReceipt{}, ctx.Err()
	}
	return d.receipt, d.err
}

type injectWorkFactory struct{ d runtime.Driver }

func (injectWorkFactory) Validate(runtime.Config) error                        { return nil }
func (f injectWorkFactory) New(string, runtime.Config) (runtime.Driver, error) { return f.d, nil }
func injectService(t *testing.T, d runtime.Driver, options ...service.Options) (*service.Service, resource.SessionRef) {
	t.Helper()
	st, err := massdtest.OpenFileStores(t.TempDir(), false)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, st.Close()) })
	registry := runtime.NewRegistry(map[string]runtime.Factory{"fixture": injectWorkFactory{d}})
	svc := service.New(st.Meta, st.Events, registry)
	if len(options) > 0 {
		svc = service.NewWithOptions(st.Meta, st.Events, registry, options[0])
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		require.NoError(t, svc.Shutdown(ctx))
	})
	_, err = svc.CreateRuntime(resource.Runtime{Metadata: resource.Metadata{Name: "agent"}, Spec: resource.RuntimeSpec{Type: "fixture", Config: runtime.Config{}}})
	require.NoError(t, err)
	r, err := svc.CreateSession(t.Context(), resource.Session{Metadata: resource.Metadata{Namespace: "one", Name: "context"}, Spec: resource.SessionSpec{RuntimeRef: "agent", SessionSpec: runtime.SessionSpec{Cwd: "/remote"}}})
	require.NoError(t, err)
	return svc, resource.SessionRef{SessionID: r.Status.SessionID}
}

func newInjectWorkDriver() *injectWorkDriver {
	return &injectWorkDriver{workDriver: &workDriver{pipe: events.NewEventPipe(16), entered: make(chan context.Context, 8), release: make(chan struct{})}}
}

func TestInjectServiceEvidence(t *testing.T) {
	for _, tc := range []struct {
		name     string
		evidence runtime.Acceptance
		code     string
	}{
		{"absent", runtime.Acceptance{}, ""},
		{"not_applied", runtime.Acceptance{Sent: runtime.Bool(false), NativeAccepted: runtime.Bool(false)}, ""},
		{"native_rejected", runtime.Acceptance{Sent: runtime.Bool(true), NativeAccepted: runtime.Bool(false)}, "native_rejected"},
		{"unknown", runtime.Acceptance{}, "outcome_unknown"},
		{"empty_non_unknown_error", runtime.Acceptance{}, "native_rejected"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := newInjectWorkDriver()
			close(d.release)
			d.receipt = runtime.InjectReceipt{Acceptance: tc.evidence}
			if tc.code != "" {
				d.err = &runtime.OperationError{Code: tc.code, Reason: "synthetic", Message: "Synthetic outcome"}
			}
			svc, ref := injectService(t, d)
			input := runtime.Input{Content: []events.ContentBlock{events.Text("context")}}
			r, err := svc.Inject(t.Context(), ref, input)
			require.Equal(t, d.receipt, r)
			if tc.code != "" {
				require.ErrorIs(t, err, d.err)
			} else {
				require.NoError(t, err)
			}
			if tc.code == "outcome_unknown" {
				r, err = svc.Inject(t.Context(), ref, input)
				require.Error(t, err)
				require.Equal(t, runtime.Bool(false), r.Acceptance.Sent)
				_, err = svc.SendPrompt(t.Context(), ref, input)
				require.Error(t, err)
				require.EqualValues(t, 1, d.calls.Load())
			}
		})
	}
}

func TestInjectServiceOptionalDriver(t *testing.T) {
	d := newInjectWorkDriver()
	close(d.release)
	// Expose only the mandatory Driver methods, hiding the optional capability.
	svc, ref := injectService(t, struct{ runtime.Driver }{d})
	r, err := svc.Inject(t.Context(), ref, runtime.Input{Content: []events.ContentBlock{events.Text("context")}})
	var p *runtime.OperationError
	require.ErrorAs(t, err, &p)
	require.Equal(t, "unsupported", p.Code)
	require.Equal(t, runtime.Bool(false), r.Acceptance.Sent)
	require.Zero(t, d.calls.Load())
}

func TestInjectServiceAbandonedWaitRetainsGate(t *testing.T) {
	d := newInjectWorkDriver()
	d.receipt = runtime.InjectReceipt{Acceptance: runtime.Acceptance{Sent: runtime.Bool(true)}}
	// Cleanup releases the original operation even if an assertion fails.
	svc, ref := injectService(t, d)
	t.Cleanup(func() {
		select {
		case <-d.release:
		default:
			close(d.release)
		}
	})
	input := runtime.Input{Content: []events.ContentBlock{events.Text("context")}}
	caller, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := svc.Inject(caller, ref, input); done <- err }()
	select {
	case owner := <-d.entered:
		require.NoError(t, owner.Err())
	case <-time.After(time.Second):
		t.Fatal("input not dispatched")
	}
	cancel()
	var p *runtime.OperationError
	require.ErrorAs(t, <-done, &p)
	require.Equal(t, "outcome_unknown", p.Code)
	_, err := svc.SendPrompt(t.Context(), ref, input)
	require.ErrorAs(t, err, &p)
	require.Equal(t, "input_in_flight", p.Reason)
	close(d.release)
	require.Eventually(t, func() bool { _, err := svc.Inject(t.Context(), ref, input); return err == nil }, time.Second, time.Millisecond)
}

func TestInjectServicePromptUnknownBlocksInject(t *testing.T) {
	d := newInjectWorkDriver()
	close(d.release)
	d.onFinish = func() error {
		return &runtime.OperationError{Code: "outcome_unknown", Reason: "synthetic_prompt_unknown", Message: "Prompt outcome unknown"}
	}
	svc, ref := injectService(t, d)
	input := runtime.Input{Content: []events.ContentBlock{events.Text("context")}}
	_, err := svc.SendPrompt(t.Context(), ref, input)
	require.Error(t, err)
	receipt, err := svc.Inject(t.Context(), ref, input)
	var p *runtime.OperationError
	require.ErrorAs(t, err, &p)
	require.Equal(t, "input_outcome_unknown", p.Reason)
	require.Equal(t, runtime.Bool(false), receipt.Acceptance.Sent)
	require.EqualValues(t, 1, d.calls.Load())
}

func TestInjectServiceRearmsIdleWithoutNativeStateEvent(t *testing.T) {
	d := newInjectWorkDriver()
	close(d.release)
	d.receipt = runtime.InjectReceipt{Acceptance: runtime.Acceptance{Sent: runtime.Bool(true)}}
	clock := newManualClock()
	svc, ref := injectService(t, d, service.Options{Clock: clock, SessionIdleTimeout: 10 * time.Second})
	waitForInjectIdleTimer(t, svc, d, ref, clock)
	clock.Advance(5 * time.Second)
	_, err := svc.Inject(t.Context(), ref, runtime.Input{Content: []events.ContentBlock{events.Text("context")}})
	require.NoError(t, err)
	require.Equal(t, 1, clock.Active(), "Inject has no native idle event to rearm the timer")
	clock.Advance(5 * time.Second)
	r, err := svc.GetSession(ref)
	require.NoError(t, err)
	require.Equal(t, events.Idle, r.Status.State)
	clock.Advance(5 * time.Second)
	require.Eventually(t, func() bool {
		r, err := svc.GetSession(ref)
		return err == nil && r.Status.State == events.SessionState("offline")
	}, time.Second, time.Millisecond)
}

func TestInjectServiceUnknownStillRearmsIdle(t *testing.T) {
	d := newInjectWorkDriver()
	close(d.release)
	d.err = &runtime.OperationError{Code: "outcome_unknown", Reason: "synthetic_inject_unknown"}
	clock := newManualClock()
	svc, ref := injectService(t, d, service.Options{Clock: clock, SessionIdleTimeout: time.Second})
	waitForInjectIdleTimer(t, svc, d, ref, clock)
	_, err := svc.Inject(t.Context(), ref, runtime.Input{Content: []events.ContentBlock{events.Text("context")}})
	require.ErrorIs(t, err, d.err)
	require.Equal(t, 1, clock.Active(), "context injection does not start work")
	clock.Advance(time.Second)
	require.Eventually(t, func() bool {
		r, err := svc.GetSession(ref)
		return err == nil && r.Status.State == "offline"
	}, time.Second, time.Millisecond)
}

func waitForInjectIdleTimer(t *testing.T, svc *service.Service, d *injectWorkDriver, ref resource.SessionRef, clock *manualClock) {
	t.Helper()
	state := events.Idle
	idle, err := events.NewEvent(events.EventBase{EventID: uuid.NewString(), Namespace: "one", SessionName: "context", SessionID: ref.SessionID, Time: time.Now()}, events.StateUpdate{State: &state}, d.source)
	require.NoError(t, err)
	require.NoError(t, d.pipe.Send(t.Context(), idle))
	require.Eventually(t, func() bool {
		current, err := svc.GetSession(ref)
		return err == nil && current.Status.State == events.Idle && clock.Active() == 1
	}, time.Second, time.Millisecond)
}

// Pause an operation at its cancellation check following Service admission.
type workDispatchContext struct {
	context.Context
	checks  int
	pauseAt int
	paused  chan struct{}
	resume  chan struct{}
}

func (c *workDispatchContext) Err() error {
	c.checks++
	if c.checks == c.pauseAt {
		close(c.paused)
		<-c.resume
	}
	return c.Context.Err()
}

func TestInjectServiceIdleAfterOverlappingPrompt(t *testing.T) {
	d := newInjectWorkDriver()
	d.receipt = runtime.InjectReceipt{Acceptance: runtime.Acceptance{Sent: runtime.Bool(true)}}
	clock := newManualClock()
	svc, ref := injectService(t, d, service.Options{Clock: clock, SessionIdleTimeout: time.Second})
	settleInject := sync.OnceFunc(func() { close(d.release) })
	t.Cleanup(settleInject)
	waitForInjectIdleTimer(t, svc, d, ref, clock)
	input := runtime.Input{Content: []events.ContentBlock{events.Text("context")}}
	injected := make(chan error, 1)
	go func() { _, err := svc.Inject(t.Context(), ref, input); injected <- err }()
	select {
	case <-d.entered:
	case <-time.After(time.Second):
		t.Fatal("Inject did not reach the driver")
	}

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	prompt := &workDispatchContext{Context: ctx, pauseAt: 3, paused: make(chan struct{}), resume: make(chan struct{})}
	resumePrompt := sync.OnceFunc(func() { close(prompt.resume) })
	t.Cleanup(resumePrompt)
	prompted := make(chan error, 1)
	go func() { _, err := svc.SendPrompt(prompt, ref, input); prompted <- err }()
	select {
	case err := <-prompted:
		var problem *runtime.OperationError
		require.ErrorAs(t, err, &problem)
		require.Equal(t, "input_in_flight", problem.Reason)
		settleInject()
		require.NoError(t, <-injected)
	case <-prompt.paused:
		// Expose the former interleaving. Inject settled while a contender
		// still held lane admission, then cancellation dropped that reader.
		settleInject()
		require.NoError(t, <-injected)
		cancel()
		resumePrompt()
		require.ErrorIs(t, <-prompted, context.Canceled)
	case <-time.After(time.Second):
		t.Fatal("overlapping Prompt made no progress")
	}

	require.EqualValues(t, 1, d.calls.Load())
	require.Equal(t, 1, clock.Active(), "settled Inject must leave an idle timer without a new native event")
	clock.Advance(time.Second)
	require.Eventually(t, func() bool {
		r, err := svc.GetSession(ref)
		return err == nil && r.Status.State == events.SessionState("offline")
	}, time.Second, time.Millisecond)
}

func TestPromptCancellationBeforeDispatchRestoresIdleTimer(t *testing.T) {
	d := newInjectWorkDriver()
	close(d.release)
	clock := newManualClock()
	svc, ref := injectService(t, d, service.Options{Clock: clock, SessionIdleTimeout: time.Second})
	waitForInjectIdleTimer(t, svc, d, ref, clock)

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	prompt := &workDispatchContext{Context: ctx, pauseAt: 3, paused: make(chan struct{}), resume: make(chan struct{})}
	resumePrompt := sync.OnceFunc(func() { close(prompt.resume) })
	t.Cleanup(resumePrompt)
	prompted := make(chan error, 1)
	go func() {
		_, err := svc.SendPrompt(prompt, ref, runtime.Input{Content: []events.ContentBlock{events.Text("context")}})
		prompted <- err
	}()
	select {
	case <-prompt.paused:
	case <-time.After(time.Second):
		t.Fatal("Prompt did not reach the post-admission checkpoint")
	}
	require.Zero(t, clock.Active(), "admitted Prompt pauses idle close while dispatch is possible")
	cancel()
	resumePrompt()
	require.ErrorIs(t, <-prompted, context.Canceled)

	require.Zero(t, d.calls.Load())
	require.Equal(t, 1, clock.Active())
}

type idlePromptDriver struct {
	*injectWorkDriver
	promptReceipt runtime.InputReceipt
	promptErr     error
}

func (d *idlePromptDriver) SendPrompt(context.Context, string, runtime.Input) (runtime.InputReceipt, error) {
	return d.promptReceipt, d.promptErr
}

func TestInjectServiceDefersIdleTimerUntilOverlappingAnswerSettles(t *testing.T) {
	tests := []struct {
		name          string
		prompt        bool
		promptReceipt runtime.InputReceipt
		promptErr     error
	}{
		{name: "answer only"},
		{
			name:          "prompt not applied",
			prompt:        true,
			promptReceipt: runtime.InputReceipt{Result: agent.InputNotApplied, Acceptance: runtime.Acceptance{Sent: runtime.Bool(true)}},
		},
		{
			name:      "prompt rejected",
			prompt:    true,
			promptErr: &runtime.OperationError{Code: "native_rejected", Reason: "synthetic_rejection"},
		},
		{
			name:          "prompt confirmed unsent",
			prompt:        true,
			promptReceipt: runtime.InputReceipt{Acceptance: runtime.Acceptance{Sent: runtime.Bool(false)}},
			promptErr:     &runtime.OperationError{Code: "resource_exhausted", Reason: "synthetic_queue_full"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			testDeferredIdleRearm(t, tt.prompt, tt.promptReceipt, tt.promptErr)
		})
	}
}

func testDeferredIdleRearm(t *testing.T, prompt bool, promptReceipt runtime.InputReceipt, promptErr error) {
	t.Helper()
	d := &idlePromptDriver{injectWorkDriver: newInjectWorkDriver(), promptReceipt: promptReceipt, promptErr: promptErr}
	d.receipt = runtime.InjectReceipt{Acceptance: runtime.Acceptance{Sent: runtime.Bool(true)}}
	answerRelease := make(chan struct{})
	d.onFinish = func() error {
		<-answerRelease
		return nil
	}
	clock := newManualClock()
	svc, ref := injectService(t, d, service.Options{Clock: clock, SessionIdleTimeout: time.Second})
	settleInject := sync.OnceFunc(func() { close(d.release) })
	t.Cleanup(settleInject)
	settleAnswer := sync.OnceFunc(func() { close(answerRelease) })
	t.Cleanup(settleAnswer)
	waitForInjectIdleTimer(t, svc, d.injectWorkDriver, ref, clock)

	input := runtime.Input{Content: []events.ContentBlock{events.Text("context")}}
	injected := make(chan error, 1)
	go func() {
		_, err := svc.Inject(t.Context(), ref, input)
		injected <- err
	}()
	select {
	case <-d.entered:
	case <-time.After(time.Second):
		t.Fatal("Inject did not reach the driver")
	}
	answered := make(chan error, 1)
	go func() {
		_, err := svc.AnswerInteraction(t.Context(), ref, "question", nil)
		answered <- err
	}()
	select {
	case <-d.entered:
	case <-time.After(time.Second):
		t.Fatal("Answer did not reach the driver")
	}

	settleInject()
	require.NoError(t, <-injected)
	require.Zero(t, clock.Active(), "idle close must wait for the in-flight answer")
	if prompt {
		receipt, err := svc.SendPrompt(t.Context(), ref, input)
		require.ErrorIs(t, err, promptErr)
		require.Equal(t, promptReceipt, receipt)
		require.Zero(t, clock.Active(), "the Answer still holds admission")
	}
	settleAnswer()
	require.NoError(t, <-answered)
	require.Equal(t, 1, clock.Active(), "the final work owner must honor the pending idle rearm")
}

func TestIdleUnsentPromptPreservesEarlierUnobservedWork(t *testing.T) {
	d := &idlePromptDriver{
		injectWorkDriver: newInjectWorkDriver(),
		promptReceipt: runtime.InputReceipt{
			Submission: "first", Result: agent.InputAppliedNewWork, Acceptance: runtime.Acceptance{Sent: runtime.Bool(true)},
		},
	}
	close(d.release)
	clock := newManualClock()
	svc, ref := injectService(t, d, service.Options{Clock: clock, SessionIdleTimeout: time.Second})
	waitForInjectIdleTimer(t, svc, d.injectWorkDriver, ref, clock)

	input := runtime.Input{Content: []events.ContentBlock{events.Text("prompt")}}
	_, err := svc.SendPrompt(t.Context(), ref, input)
	require.NoError(t, err)
	require.Zero(t, clock.Active())

	d.promptReceipt = runtime.InputReceipt{
		Result: agent.InputNotApplied, Acceptance: runtime.Acceptance{Sent: runtime.Bool(false)},
	}
	_, err = svc.SendPrompt(t.Context(), ref, input)
	require.NoError(t, err)
	require.Zero(t, clock.Active(), "the second Prompt cannot settle earlier unobserved work")
	waitForInjectIdleTimer(t, svc, d.injectWorkDriver, ref, clock)
}
