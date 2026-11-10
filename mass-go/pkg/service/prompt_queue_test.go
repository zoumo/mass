package service_test

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"github.com/zoumo/agent-sdk-go/agent"

	"github.com/zoumo/mass/mass-go/pkg/events"
	"github.com/zoumo/mass/mass-go/pkg/resource"
	"github.com/zoumo/mass/mass-go/pkg/runtime"
	"github.com/zoumo/mass/mass-go/pkg/service"
	"github.com/zoumo/mass/mass-go/pkg/store/file"
)

type queuedAttempt struct {
	receipt runtime.InputReceipt
	err     error
	block   bool
}

type promptQueueDriver struct {
	pipe *events.EventPipe

	mu        sync.Mutex
	source    events.Source
	sessionID string
	attempts  []queuedAttempt
	calls     []string
	opens     int
	resumes   int
	closes    int
	cancels   int
	admitCh   chan struct{}
	admitAck  chan struct{}

	callCh     chan string
	validateCh chan struct{}
	release    chan struct{}
	releaseS   sync.Once
}

func (d *promptQueueDriver) Events() (events.Stream, error) { return d.pipe, nil }
func (*promptQueueDriver) Start(context.Context) (runtime.Capabilities, error) {
	return runtime.Capabilities{}, nil
}

func (d *promptQueueDriver) ValidateInitialInput(input runtime.Input) error {
	if err := runtime.ValidateTextInput(input); err != nil {
		return err
	}
	if d.validateCh != nil {
		d.validateCh <- struct{}{}
	}
	if d.admitCh != nil {
		d.admitCh <- struct{}{}
		<-d.admitAck
	}
	return nil
}

func (d *promptQueueDriver) OpenSession(_ context.Context, address runtime.Address, _ runtime.SessionSpec) (runtime.DriverSession, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.opens++
	d.sessionID = address.SessionName + "-native"
	d.source = events.NewSource()
	return runtime.DriverSession{SessionID: d.sessionID, Source: d.source}, nil
}

func (*promptQueueDriver) OpenSessionWithInput(context.Context, runtime.Address, runtime.SessionSpec, runtime.Input) (runtime.DriverSession, error) {
	panic("OpenSessionWithInput must not bypass Service admission")
}

func (d *promptQueueDriver) ResumeSession(_ context.Context, id string, _ runtime.Address, _ runtime.SessionSpec) (runtime.DriverSession, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.resumes++
	d.sessionID = id
	d.source = events.NewSource()
	return runtime.DriverSession{SessionID: id, Source: d.source}, nil
}

func (*promptQueueDriver) ResumeSessionWithInput(context.Context, string, runtime.Address, runtime.SessionSpec, runtime.Input) (runtime.DriverSession, error) {
	panic("ResumeSessionWithInput must not bypass Service admission")
}

func (*promptQueueDriver) LoadSession(context.Context, string, runtime.Address, runtime.SessionSpec, string) error {
	return nil
}

func (*promptQueueDriver) CancelHistoryLoad(context.Context, string, string) error { return nil }

func (d *promptQueueDriver) SendPrompt(ctx context.Context, _ string, input runtime.Input) (runtime.InputReceipt, error) {
	text := queuedPromptText(input)
	d.mu.Lock()
	attempt := queuedAttempt{receipt: runtime.InputReceipt{Submission: "applied", Result: agent.InputAppliedNewWork, Acceptance: runtime.Acceptance{Sent: runtime.Yes()}}}
	if len(d.attempts) != 0 {
		attempt = d.attempts[0]
		d.attempts = d.attempts[1:]
	}
	d.calls = append(d.calls, text)
	d.mu.Unlock()
	d.callCh <- text
	if attempt.block {
		select {
		case <-d.release:
		case <-ctx.Done():
			return runtime.InputReceipt{}, ctx.Err()
		}
	}
	return attempt.receipt, attempt.err
}

func (d *promptQueueDriver) Cancel(context.Context, string) (runtime.CancelReceipt, error) {
	d.mu.Lock()
	d.cancels++
	d.mu.Unlock()
	return runtime.CancelReceipt{Result: "noActiveWork"}, nil
}

func (*promptQueueDriver) AnswerInteraction(context.Context, string, string, any) (runtime.Acceptance, error) {
	return runtime.Acceptance{}, nil
}

func (d *promptQueueDriver) CloseSession(context.Context, string) error {
	d.mu.Lock()
	d.closes++
	d.mu.Unlock()
	return nil
}

func (d *promptQueueDriver) Stop(context.Context) error {
	d.pipe.End(nil)
	return nil
}

func (d *promptQueueDriver) setAttempts(attempts ...queuedAttempt) {
	d.mu.Lock()
	d.attempts = append([]queuedAttempt(nil), attempts...)
	d.mu.Unlock()
}

func (d *promptQueueDriver) releaseAll() {
	d.releaseS.Do(func() { close(d.release) })
}

func (d *promptQueueDriver) counts() (calls, opens, resumes, closes int) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return len(d.calls), d.opens, d.resumes, d.closes
}

func (d *promptQueueDriver) sourceNow() events.Source {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.source
}

func queuedPromptText(input runtime.Input) string {
	if len(input.Content) != 1 {
		return ""
	}
	if block := input.Content[0].Text; block != nil {
		return block.Text
	}
	return ""
}

type promptQueueFactory struct{ driver *promptQueueDriver }

func (f promptQueueFactory) Validate(runtime.Config) error { return nil }
func (f promptQueueFactory) New(string, runtime.Config) (runtime.Driver, error) {
	return f.driver, nil
}

type promptQueueFixture struct {
	driver *promptQueueDriver
	store  *file.Store
	svc    *service.Service
	ref    resource.SessionRef
}

func newPromptQueueFixture(t *testing.T) *promptQueueFixture {
	t.Helper()
	st, err := file.Open(t.TempDir(), false)
	require.NoError(t, err)
	d := &promptQueueDriver{
		pipe:       events.NewEventPipe(128),
		callCh:     make(chan string, 256),
		validateCh: make(chan struct{}, 512),
		release:    make(chan struct{}),
	}
	svc := service.New(st, st, runtime.NewRegistry(map[string]runtime.Factory{"fixture": promptQueueFactory{driver: d}}))
	t.Cleanup(func() {
		d.releaseAll()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		require.NoError(t, svc.Shutdown(ctx))
		require.NoError(t, st.Close())
	})
	_, err = svc.CreateRuntime(resource.Runtime{Metadata: resource.Metadata{Name: "agent"}, Spec: resource.RuntimeSpec{Type: "fixture", Config: runtime.Config{}}})
	require.NoError(t, err)
	_, err = svc.StartRuntime(context.Background(), "agent")
	require.NoError(t, err)
	created, err := svc.CreateSession(context.Background(), resource.Session{
		Metadata: resource.Metadata{Namespace: "prompt", Name: "queue"},
		Spec:     resource.SessionSpec{RuntimeRef: "agent", SessionSpec: runtime.SessionSpec{Cwd: "/remote"}},
	})
	require.NoError(t, err)
	return &promptQueueFixture{
		driver: d,
		store:  st,
		svc:    svc,
		ref:    resource.SessionRef{SessionID: created.Status.SessionID},
	}
}

func (f *promptQueueFixture) emitState(t *testing.T, state *events.SessionState) {
	t.Helper()
	event, err := events.NewEvent(events.EventBase{
		EventID:     uuid.NewString(),
		Namespace:   "prompt",
		SessionName: "queue",
		SessionID:   f.ref.SessionID,
		Time:        time.Now().UTC(),
	}, events.StateUpdate{State: state}, f.driver.sourceNow())
	require.NoError(t, err)
	require.NoError(t, f.driver.pipe.Send(context.Background(), event))
}

func (f *promptQueueFixture) waitState(t *testing.T, state events.SessionState) {
	t.Helper()
	require.Eventually(t, func() bool {
		current, err := f.svc.GetSession(f.ref)
		return err == nil && current.Status.State == state
	}, time.Second, time.Millisecond)
}

func (f *promptQueueFixture) idle(t *testing.T) {
	t.Helper()
	idle := events.Idle
	f.emitState(t, &idle)
	f.waitState(t, events.Idle)
}

func (f *promptQueueFixture) prompt(text string) runtime.Input {
	return runtime.Input{Content: []events.ContentBlock{events.Text(text)}}
}

func (f *promptQueueFixture) waitCall(t *testing.T, want string) {
	t.Helper()
	select {
	case got := <-f.driver.callCh:
		require.Equal(t, want, got)
	case <-time.After(time.Second):
		t.Fatalf("prompt %q was not dispatched", want)
	}
}

func (f *promptQueueFixture) waitPromptLane(t *testing.T, ref resource.SessionRef) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	var lastErr error
	for time.Now().Before(deadline) {
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan error, 1)
		go func() {
			_, err := f.svc.SendPrompt(ctx, ref, f.prompt("probe"))
			done <- err
		}()
		select {
		case <-f.driver.validateCh:
			cancel()
			require.ErrorIs(t, <-done, context.Canceled)
			return
		case lastErr = <-done:
			cancel()
			time.Sleep(time.Millisecond)
		}
	}
	t.Fatalf("prompt lane was not established: %v", lastErr)
}

func (f *promptQueueFixture) emitSessionState(t *testing.T, r resource.Session, state events.SessionState) {
	t.Helper()
	event, err := events.NewEvent(events.EventBase{
		EventID:     uuid.NewString(),
		Namespace:   r.Metadata.Namespace,
		SessionName: r.Metadata.Name,
		SessionID:   r.Status.SessionID,
		Time:        time.Now().UTC(),
	}, events.StateUpdate{State: &state}, f.driver.sourceNow())
	require.NoError(t, err)
	require.NoError(t, f.driver.pipe.Send(context.Background(), event))
	require.Eventually(t, func() bool {
		current, getErr := f.svc.GetSession(resource.SessionRef{SessionID: r.Status.SessionID})
		return getErr == nil && current.Status.State == state
	}, time.Second, time.Millisecond)
}

func TestPromptQueueFIFOwaitsForAFreshIdleObservation(t *testing.T) {
	f := newPromptQueueFixture(t)
	f.idle(t)
	f.driver.setAttempts(
		queuedAttempt{receipt: runtime.InputReceipt{Submission: "first", Result: agent.InputAppliedNewWork}, block: true},
		queuedAttempt{receipt: runtime.InputReceipt{Submission: "second", Result: agent.InputAppliedCurrentWork}},
	)

	firstDone := make(chan error, 1)
	var firstReceipt runtime.InputReceipt
	go func() {
		var err error
		firstReceipt, err = f.svc.SendPrompt(context.Background(), f.ref, f.prompt("first"))
		firstDone <- err
	}()
	f.waitCall(t, "first")

	secondDone := make(chan error, 1)
	var secondReceipt runtime.InputReceipt
	go func() {
		var err error
		secondReceipt, err = f.svc.SendPrompt(context.Background(), f.ref, f.prompt("second"))
		secondDone <- err
	}()
	require.Never(t, func() bool {
		select {
		case <-f.driver.callCh:
			return true
		default:
			return false
		}
	}, 50*time.Millisecond, 5*time.Millisecond, "second prompt bypassed the first in-flight prompt")

	running := events.Running
	f.emitState(t, &running)
	f.waitState(t, events.Running)
	f.driver.releaseAll()
	require.NoError(t, <-firstDone)
	require.Equal(t, "first", firstReceipt.Submission)
	require.Never(t, func() bool {
		select {
		case <-f.driver.callCh:
			return true
		default:
			return false
		}
	}, 50*time.Millisecond, 5*time.Millisecond, "second prompt dispatched without a new idle event")

	f.idle(t)
	f.waitCall(t, "second")
	require.NoError(t, <-secondDone)
	require.Equal(t, "second", secondReceipt.Submission)
	require.Equal(t, agent.InputAppliedCurrentWork, secondReceipt.Result)
}

func TestPromptQueuePreservesBestEffortReceipt(t *testing.T) {
	f := newPromptQueueFixture(t)
	f.idle(t)
	sent := true
	f.driver.setAttempts(queuedAttempt{receipt: runtime.InputReceipt{
		Submission: "best-effort",
		Result:     agent.InputAppliedBestEffort,
		Acceptance: runtime.Acceptance{Sent: &sent},
	}})

	receipt, err := f.svc.SendPrompt(context.Background(), f.ref, f.prompt("best effort"))
	require.NoError(t, err)
	require.Equal(t, agent.InputAppliedBestEffort, receipt.Result)
	require.NotNil(t, receipt.Acceptance.Sent)
	require.True(t, *receipt.Acceptance.Sent)
	require.Nil(t, receipt.Acceptance.NativeAccepted)
	require.Nil(t, receipt.Acceptance.NativeRecorded)
}

func TestPromptQueueRetainsNotAppliedUntilFreshIdle(t *testing.T) {
	f := newPromptQueueFixture(t)
	f.idle(t)
	f.driver.setAttempts(
		queuedAttempt{receipt: runtime.InputReceipt{Result: agent.InputNotApplied, Acceptance: runtime.Acceptance{Sent: runtime.Bool(false)}}},
		queuedAttempt{receipt: runtime.InputReceipt{Submission: "retried-first", Result: agent.InputAppliedNewWork}},
		queuedAttempt{receipt: runtime.InputReceipt{Submission: "retried-second", Result: agent.InputAppliedNewWork}},
	)

	receipt, err := f.svc.SendPrompt(context.Background(), f.ref, f.prompt("not applied"))
	require.NoError(t, err)
	require.Equal(t, agent.InputNotApplied, receipt.Result)
	require.Empty(t, receipt.Submission)
	require.NotNil(t, receipt.Acceptance.Sent)
	require.False(t, *receipt.Acceptance.Sent)
	f.waitCall(t, "not applied")

	require.Never(t, func() bool {
		calls, _, _, _ := f.driver.counts()
		return calls > 1
	}, 50*time.Millisecond, 5*time.Millisecond, "not_applied input was retried without a fresh idle observation")

	type secondResult struct {
		receipt runtime.InputReceipt
		err     error
	}
	secondDone := make(chan secondResult, 1)
	go func() {
		next, sendErr := f.svc.SendPrompt(context.Background(), f.ref, f.prompt("next"))
		secondDone <- secondResult{receipt: next, err: sendErr}
	}()

	f.idle(t)
	f.waitCall(t, "not applied")
	require.Never(t, func() bool {
		calls, _, _, _ := f.driver.counts()
		return calls > 2
	}, 50*time.Millisecond, 5*time.Millisecond, "later input overtook the retained FIFO head")

	f.idle(t)
	f.waitCall(t, "next")
	second := <-secondDone
	require.NoError(t, second.err)
	require.Equal(t, "retried-second", second.receipt.Submission)
}

func TestPromptQueueOutcomeUnknownStopsWithoutRetry(t *testing.T) {
	f := newPromptQueueFixture(t)
	f.idle(t)
	f.driver.setAttempts(queuedAttempt{err: &runtime.OperationError{
		Code:    "outcome_unknown",
		Reason:  "native_write_unknown",
		Message: "do not resend",
	}})

	_, err := f.svc.SendPrompt(context.Background(), f.ref, f.prompt("unknown"))
	var operation *runtime.OperationError
	require.ErrorAs(t, err, &operation)
	require.Equal(t, "outcome_unknown", operation.Code)

	f.idle(t)
	require.Never(t, func() bool {
		calls, _, _, _ := f.driver.counts()
		return calls > 1
	}, 50*time.Millisecond, 5*time.Millisecond, "outcome_unknown input was retried")

	_, err = f.svc.SendPrompt(context.Background(), f.ref, f.prompt("later"))
	require.Error(t, err)
	calls, _, _, _ := f.driver.counts()
	require.Equal(t, 1, calls)
}

func TestPromptQueueCancelBeforeDispatchRemovesInput(t *testing.T) {
	f := newPromptQueueFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := f.svc.SendPrompt(ctx, f.ref, f.prompt("canceled"))
		done <- err
	}()
	time.Sleep(20 * time.Millisecond)
	cancel()
	require.ErrorIs(t, <-done, context.Canceled)

	f.idle(t)
	require.Never(t, func() bool {
		calls, _, _, _ := f.driver.counts()
		return calls != 0
	}, 50*time.Millisecond, 5*time.Millisecond, "canceled undispatched input was sent")
}

func TestPromptQueueCancelAfterDispatchIsOutcomeUnknownWithoutResend(t *testing.T) {
	f := newPromptQueueFixture(t)
	f.idle(t)
	f.driver.setAttempts(queuedAttempt{receipt: runtime.InputReceipt{Submission: "owned", Result: agent.InputAppliedNewWork}, block: true})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := f.svc.SendPrompt(ctx, f.ref, f.prompt("owned"))
		done <- err
	}()
	f.waitCall(t, "owned")
	cancel()
	var operation *runtime.OperationError
	require.ErrorAs(t, <-done, &operation)
	require.Equal(t, "outcome_unknown", operation.Code)

	f.driver.releaseAll()
	f.idle(t)
	require.Never(t, func() bool {
		calls, _, _, _ := f.driver.counts()
		return calls > 1
	}, 50*time.Millisecond, 5*time.Millisecond, "post-dispatch cancellation resent input")
}

func TestPromptQueueCancelAfterDispatchDoesNotRetryNotApplied(t *testing.T) {
	f := newPromptQueueFixture(t)
	f.idle(t)
	f.driver.setAttempts(
		queuedAttempt{receipt: runtime.InputReceipt{Result: agent.InputNotApplied, Acceptance: runtime.Acceptance{Sent: runtime.Bool(false)}}, block: true},
		queuedAttempt{receipt: runtime.InputReceipt{Submission: "next", Result: agent.InputAppliedNewWork}},
	)

	ctx, cancel := context.WithCancel(context.Background())
	firstDone := make(chan error, 1)
	go func() {
		_, err := f.svc.SendPrompt(ctx, f.ref, f.prompt("abandoned"))
		firstDone <- err
	}()
	f.waitCall(t, "abandoned")
	cancel()
	var operation *runtime.OperationError
	require.ErrorAs(t, <-firstDone, &operation)
	require.Equal(t, "outcome_unknown", operation.Code)

	secondDone := make(chan error, 1)
	go func() {
		_, err := f.svc.SendPrompt(context.Background(), f.ref, f.prompt("next"))
		secondDone <- err
	}()
	f.driver.releaseAll()
	f.idle(t)
	f.waitCall(t, "next")
	require.NoError(t, <-secondDone)
	require.Never(t, func() bool {
		calls, _, _, _ := f.driver.counts()
		return calls > 2
	}, 50*time.Millisecond, 5*time.Millisecond, "canceled not_applied input was retried")
}

func TestPromptQueueRunningWaitsAndUnknownSettlesUndispatched(t *testing.T) {
	f := newPromptQueueFixture(t)
	done := make(chan error, 1)
	go func() {
		_, err := f.svc.SendPrompt(context.Background(), f.ref, f.prompt("waiting"))
		done <- err
	}()
	running := events.Running
	f.emitState(t, &running)
	f.waitState(t, events.Running)
	require.Never(t, func() bool {
		calls, _, _, _ := f.driver.counts()
		return calls != 0
	}, 50*time.Millisecond, 5*time.Millisecond, "running session dispatched queued input")

	f.emitState(t, nil)
	f.waitState(t, events.Unknown)
	require.ErrorIs(t, <-done, runtime.ErrFailedPrecondition)
	calls, _, _, _ := f.driver.counts()
	require.Zero(t, calls)
}

func TestPromptQueueCloseSettlesUndispatched(t *testing.T) {
	f := newPromptQueueFixture(t)
	done := make(chan error, 1)
	go func() {
		_, err := f.svc.SendPrompt(context.Background(), f.ref, f.prompt("blocked"))
		done <- err
	}()
	time.Sleep(20 * time.Millisecond)
	closed, err := f.svc.CloseSession(context.Background(), f.ref)
	require.NoError(t, err)
	require.Equal(t, events.SessionState("offline"), closed.Status.State)
	require.ErrorIs(t, <-done, runtime.ErrFailedPrecondition)
	calls, _, _, _ := f.driver.counts()
	require.Zero(t, calls)
}

func TestPromptQueueStopSettlesAndRestartDoesNotRestore(t *testing.T) {
	f := newPromptQueueFixture(t)
	done := make(chan error, 1)
	go func() {
		_, err := f.svc.SendPrompt(context.Background(), f.ref, f.prompt("not persisted"))
		done <- err
	}()
	time.Sleep(20 * time.Millisecond)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := f.svc.StopRuntime(ctx, "agent")
	require.NoError(t, err)
	require.Error(t, <-done)
	calls, _, _, _ := f.driver.counts()
	require.Zero(t, calls)

	restarted := service.New(f.store, f.store, runtime.NewRegistry(map[string]runtime.Factory{"fixture": promptQueueFactory{driver: f.driver}}))
	defer func() { require.NoError(t, restarted.Shutdown(ctx)) }()
	_, err = restarted.SendPrompt(ctx, f.ref, f.prompt("after restart"))
	require.ErrorIs(t, err, runtime.ErrFailedPrecondition)
	calls, _, _, _ = f.driver.counts()
	require.Zero(t, calls, "in-memory queue was replayed after restart")
}

func TestPromptQueueCapacityRejectsThe129thInput(t *testing.T) {
	f := newPromptQueueFixture(t)
	admitted := make(chan struct{}, 130)
	acknowledge := make(chan struct{})
	var acknowledgeOnce sync.Once
	unblock := func() { acknowledgeOnce.Do(func() { close(acknowledge) }) }
	f.driver.admitCh = admitted
	f.driver.admitAck = acknowledge

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	t.Cleanup(unblock)

	done := make(chan error, 129)
	var calls sync.WaitGroup
	start := func(text string) {
		calls.Go(func() {
			_, err := f.svc.SendPrompt(ctx, f.ref, f.prompt(text))
			done <- err
		})
	}
	for i := range 128 {
		start(strconv.Itoa(i))
	}
	for range 128 {
		select {
		case <-admitted:
			acknowledge <- struct{}{}
		case <-time.After(time.Second):
			t.Fatal("prompt was not admitted")
		}
	}

	start("overflow")
	select {
	case <-admitted:
		acknowledge <- struct{}{}
	case <-time.After(time.Second):
		t.Fatal("capacity rejection was not admitted for validation")
	}
	var overflowErr error
	select {
	case overflowErr = <-done:
	case <-time.After(time.Second):
		t.Fatal("capacity rejection did not return")
	}
	var operation *runtime.OperationError
	require.ErrorAs(t, overflowErr, &operation)
	require.Equal(t, "resource_exhausted", operation.Code)
	require.Equal(t, "session_prompt_queue_full", operation.Reason)
	nativeCalls, _, _, _ := f.driver.counts()
	require.Zero(t, nativeCalls)

	cancel()
	unblock()
	for range 128 {
		select {
		case err := <-done:
			require.ErrorIs(t, err, context.Canceled)
		case <-time.After(time.Second):
			t.Fatal("canceled queue waiter did not return")
		}
	}
	calls.Wait()
}

func TestPromptQueueAdmissionIsAtomicWithClose(t *testing.T) {
	f := newPromptQueueFixture(t)
	admitted := make(chan struct{}, 1)
	acknowledge := make(chan struct{})
	f.driver.admitCh = admitted
	f.driver.admitAck = acknowledge
	t.Cleanup(func() {
		select {
		case <-acknowledge:
		default:
			close(acknowledge)
		}
	})

	promptDone := make(chan error, 1)
	go func() {
		_, err := f.svc.SendPrompt(context.Background(), f.ref, f.prompt("racing"))
		promptDone <- err
	}()
	select {
	case <-admitted:
	case <-time.After(time.Second):
		t.Fatal("prompt admission did not start")
	}
	closeDone := make(chan error, 1)
	go func() {
		_, err := f.svc.CloseSession(context.Background(), f.ref)
		closeDone <- err
	}()
	select {
	case err := <-closeDone:
		require.NoError(t, err)
	case <-time.After(time.Second):
		t.Fatal("close waited for input validation")
	}

	close(acknowledge)
	require.ErrorIs(t, <-promptDone, runtime.ErrFailedPrecondition)
	nativeCalls, _, _, closes := f.driver.counts()
	require.Zero(t, nativeCalls)
	require.Equal(t, 1, closes)
}

func TestPromptInitialInputUsesQueueAndPreservesSDKReceipt(t *testing.T) {
	for _, result := range []agent.InputResult{
		agent.InputAppliedCurrentWork,
		agent.InputAppliedNewWork,
		agent.InputAppliedBestEffort,
		agent.InputNotApplied,
	} {
		t.Run(string(result), func(t *testing.T) {
			f := newPromptQueueFixture(t)
			_, err := f.svc.CloseSession(context.Background(), f.ref)
			require.NoError(t, err)
			_, _, _, baselineCloses := f.driver.counts()
			submission := "initial"
			sent := true
			if result == agent.InputNotApplied {
				submission = ""
				sent = false
			}
			f.driver.setAttempts(queuedAttempt{receipt: runtime.InputReceipt{
				Submission: submission,
				Result:     result,
				Acceptance: runtime.Acceptance{Sent: runtime.Bool(sent)},
			}})

			type createdResult struct {
				value resource.SessionCreated
				err   error
			}
			created := make(chan createdResult, 1)
			go func() {
				value, err := f.svc.CreateSessionWithInput(context.Background(), resource.SessionCreateInput{
					Metadata:     resource.Metadata{Namespace: "prompt", Name: "initial"},
					Spec:         resource.SessionSpec{RuntimeRef: "agent", SessionSpec: runtime.SessionSpec{Cwd: "/remote"}},
					InitialInput: &runtime.Input{Content: []events.ContentBlock{events.Text("hello")}},
				})
				created <- createdResult{value: value, err: err}
			}()
			var current resource.Session
			require.Eventually(t, func() bool {
				var getErr error
				current, getErr = f.svc.GetSession(resource.SessionRef{Namespace: "prompt", Name: "initial"})
				return getErr == nil
			}, time.Second, time.Millisecond)
			require.Never(t, func() bool {
				calls, _, _, _ := f.driver.counts()
				return calls != 0
			}, 20*time.Millisecond, 5*time.Millisecond, "initial input bypassed the idle gate")
			event, eventErr := events.NewEvent(events.EventBase{
				EventID:     uuid.NewString(),
				Namespace:   "prompt",
				SessionName: "initial",
				SessionID:   current.Status.SessionID,
				Time:        time.Now().UTC(),
			}, events.StateUpdate{State: new(events.Idle)}, f.driver.sourceNow())
			require.NoError(t, eventErr)
			require.NoError(t, f.driver.pipe.Send(context.Background(), event))

			outcome := <-created
			require.NoError(t, outcome.err)
			require.NotNil(t, outcome.value.InitialInputReceipt)
			require.Equal(t, result, outcome.value.InitialInputReceipt.Result)
			require.Equal(t, submission, outcome.value.InitialInputReceipt.Submission)
			require.Equal(t, sent, *outcome.value.InitialInputReceipt.Acceptance.Sent)
			require.Never(t, func() bool {
				_, _, _, closes := f.driver.counts()
				return closes != baselineCloses
			}, 20*time.Millisecond, 5*time.Millisecond)
		})
	}
}

func TestPromptCreateInitialInputCancelBeforeIdleRemovesInputAndRetainsSession(t *testing.T) {
	f := newPromptQueueFixture(t)
	_, _, _, baselineCloses := f.driver.counts()
	ctx, cancel := context.WithCancel(context.Background())
	created := make(chan struct {
		value resource.SessionCreated
		err   error
	}, 1)
	go func() {
		value, err := f.svc.CreateSessionWithInput(ctx, resource.SessionCreateInput{
			Metadata:     resource.Metadata{Namespace: "prompt", Name: "canceled-create"},
			Spec:         resource.SessionSpec{RuntimeRef: "agent", SessionSpec: runtime.SessionSpec{Cwd: "/remote"}},
			InitialInput: &runtime.Input{Content: []events.ContentBlock{events.Text("hello")}},
		})
		created <- struct {
			value resource.SessionCreated
			err   error
		}{value: value, err: err}
	}()
	select {
	case <-f.driver.validateCh:
	case outcome := <-created:
		t.Fatalf("create returned before validation: %v", outcome.err)
	case <-time.After(time.Second):
		t.Fatal("create did not validate its initial input")
	}
	nameRef := resource.SessionRef{Namespace: "prompt", Name: "canceled-create"}
	f.waitPromptLane(t, nameRef)

	cancel()
	outcome := <-created
	require.ErrorIs(t, outcome.err, context.Canceled)
	require.NotEmpty(t, outcome.value.Session.Status.SessionID)
	current, err := f.svc.GetSession(nameRef)
	require.NoError(t, err)
	require.Equal(t, outcome.value.Session.Status.SessionID, current.Status.SessionID)
	calls, _, _, closes := f.driver.counts()
	require.Zero(t, calls, "canceled initial input reached the native session")
	require.Equal(t, baselineCloses, closes, "canceled initial input closed the opened session")

	f.emitSessionState(t, current, events.Idle)
	require.Never(t, func() bool {
		calls, _, _, _ := f.driver.counts()
		return calls != 0
	}, 20*time.Millisecond, time.Millisecond, "canceled initial input was dispatched after idle")
}

func TestPromptResumeInitialInputWaitsForIdleAndReturnsSDKReceipt(t *testing.T) {
	f := newPromptQueueFixture(t)
	_, err := f.svc.CloseSession(context.Background(), f.ref)
	require.NoError(t, err)
	f.driver.setAttempts(queuedAttempt{receipt: runtime.InputReceipt{
		Submission: "resume-initial",
		Result:     agent.InputAppliedBestEffort,
		Acceptance: runtime.Acceptance{Sent: runtime.Yes()},
	}})

	type resumedResult struct {
		value resource.SessionResumed
		err   error
	}
	resumed := make(chan resumedResult, 1)
	go func() {
		value, resumeErr := f.svc.ResumeSessionWithInput(context.Background(), f.ref, f.prompt("resume input"))
		resumed <- resumedResult{value: value, err: resumeErr}
	}()
	require.Eventually(t, func() bool {
		current, getErr := f.svc.GetSession(f.ref)
		return getErr == nil && current.Status.State == events.Unknown
	}, time.Second, time.Millisecond)
	require.Never(t, func() bool {
		calls, _, _, _ := f.driver.counts()
		return calls != 0
	}, 20*time.Millisecond, 5*time.Millisecond, "resume initial input bypassed the idle gate")

	f.idle(t)
	f.waitCall(t, "resume input")
	outcome := <-resumed
	require.NoError(t, outcome.err)
	require.NotNil(t, outcome.value.InitialInputReceipt)
	require.Equal(t, "resume-initial", outcome.value.InitialInputReceipt.Submission)
	require.Equal(t, agent.InputAppliedBestEffort, outcome.value.InitialInputReceipt.Result)
	require.True(t, *outcome.value.InitialInputReceipt.Acceptance.Sent)
	_, _, resumes, _ := f.driver.counts()
	require.Equal(t, 1, resumes)
}

func TestPromptResumeInitialInputCancelBeforeIdleRemovesInputAndRetainsSession(t *testing.T) {
	f := newPromptQueueFixture(t)
	_, err := f.svc.CloseSession(context.Background(), f.ref)
	require.NoError(t, err)
	_, _, _, baselineCloses := f.driver.counts()

	ctx, cancel := context.WithCancel(context.Background())
	resumed := make(chan struct {
		value resource.SessionResumed
		err   error
	}, 1)
	go func() {
		value, err := f.svc.ResumeSessionWithInput(ctx, f.ref, f.prompt("resume input"))
		resumed <- struct {
			value resource.SessionResumed
			err   error
		}{value: value, err: err}
	}()
	select {
	case <-f.driver.validateCh:
	case outcome := <-resumed:
		t.Fatalf("resume returned before validation: %v", outcome.err)
	case <-time.After(time.Second):
		t.Fatal("resume did not validate its initial input")
	}
	f.waitPromptLane(t, f.ref)

	cancel()
	outcome := <-resumed
	require.Error(t, outcome.err)
	if !errors.Is(outcome.err, context.Canceled) {
		var operation *runtime.OperationError
		require.ErrorAs(t, outcome.err, &operation)
		require.Equal(t, "outcome_unknown", operation.Code)
	}
	require.Equal(t, f.ref.SessionID, outcome.value.Session.Status.SessionID)
	current, err := f.svc.GetSession(f.ref)
	require.NoError(t, err)
	require.Equal(t, f.ref.SessionID, current.Status.SessionID)
	calls, _, resumes, closes := f.driver.counts()
	require.Zero(t, calls, "canceled resume input reached the native session")
	require.Equal(t, 1, resumes)
	require.Equal(t, baselineCloses, closes, "canceled resume input released the resumed session")

	f.emitSessionState(t, current, events.Idle)
	require.Never(t, func() bool {
		calls, _, _, _ := f.driver.counts()
		return calls != 0
	}, 20*time.Millisecond, time.Millisecond, "canceled resume input was dispatched after idle")
}

func TestPromptResumeInitialInputFailureRetainsResumedSession(t *testing.T) {
	f := newPromptQueueFixture(t)
	_, err := f.svc.CloseSession(context.Background(), f.ref)
	require.NoError(t, err)
	_, _, _, baselineCloses := f.driver.counts()
	f.driver.setAttempts(queuedAttempt{err: &runtime.OperationError{
		Code:    "outcome_unknown",
		Reason:  "resume_input_unknown",
		Message: "do not resend",
	}})

	type resumedResult struct {
		value resource.SessionResumed
		err   error
	}
	resumed := make(chan resumedResult, 1)
	go func() {
		value, resumeErr := f.svc.ResumeSessionWithInput(context.Background(), f.ref, f.prompt("resume input"))
		resumed <- resumedResult{value: value, err: resumeErr}
	}()
	require.Eventually(t, func() bool {
		current, getErr := f.svc.GetSession(f.ref)
		return getErr == nil && current.Status.State == events.Unknown
	}, time.Second, time.Millisecond)
	f.idle(t)
	f.waitCall(t, "resume input")

	outcome := <-resumed
	var operation *runtime.OperationError
	require.ErrorAs(t, outcome.err, &operation)
	require.Equal(t, "outcome_unknown", operation.Code)
	require.Equal(t, f.ref.SessionID, outcome.value.Session.Status.SessionID)
	require.NotContains(t, strings.ToLower(outcome.err.Error()), "not sent")
	_, _, _, closes := f.driver.counts()
	require.Equal(t, baselineCloses, closes, "successful resume was closed after initial input failure")
	current, err := f.svc.GetSession(f.ref)
	require.NoError(t, err)
	require.Equal(t, f.ref.SessionID, current.Status.SessionID)

	f.idle(t)
	require.Never(t, func() bool {
		calls, _, _, _ := f.driver.counts()
		return calls > 1
	}, 50*time.Millisecond, 5*time.Millisecond, "resume outcome_unknown input was retried")
}

func TestPromptInitialInputFailureRetainsOpenedSession(t *testing.T) {
	f := newPromptQueueFixture(t)
	_, err := f.svc.CloseSession(context.Background(), f.ref)
	require.NoError(t, err)
	_, _, _, baselineCloses := f.driver.counts()
	f.driver.setAttempts(queuedAttempt{err: &runtime.OperationError{
		Code:    "outcome_unknown",
		Reason:  "initial_input_unknown",
		Message: "do not resend",
	}})

	created := make(chan struct {
		value resource.SessionCreated
		err   error
	}, 1)
	go func() {
		value, err := f.svc.CreateSessionWithInput(context.Background(), resource.SessionCreateInput{
			Metadata:     resource.Metadata{Namespace: "prompt", Name: "initial-failure"},
			Spec:         resource.SessionSpec{RuntimeRef: "agent", SessionSpec: runtime.SessionSpec{Cwd: "/remote"}},
			InitialInput: &runtime.Input{Content: []events.ContentBlock{events.Text("hello")}},
		})
		created <- struct {
			value resource.SessionCreated
			err   error
		}{value: value, err: err}
	}()
	var current resource.Session
	require.Eventually(t, func() bool {
		var getErr error
		current, getErr = f.svc.GetSession(resource.SessionRef{Namespace: "prompt", Name: "initial-failure"})
		return getErr == nil
	}, time.Second, time.Millisecond)
	event, eventErr := events.NewEvent(events.EventBase{
		EventID:     uuid.NewString(),
		Namespace:   "prompt",
		SessionName: "initial-failure",
		SessionID:   current.Status.SessionID,
		Time:        time.Now().UTC(),
	}, events.StateUpdate{State: new(events.Idle)}, f.driver.sourceNow())
	require.NoError(t, eventErr)
	require.NoError(t, f.driver.pipe.Send(context.Background(), event))

	outcome := <-created
	require.Error(t, outcome.err)
	require.NotNil(t, outcome.value.Session.Status.SessionID)
	require.NotContains(t, strings.ToLower(outcome.err.Error()), "not sent")
	_, _, _, closes := f.driver.counts()
	require.Equal(t, baselineCloses, closes, "successful open was closed after initial input failure")
	_, err = f.svc.GetSession(resource.SessionRef{SessionID: outcome.value.Session.Status.SessionID})
	require.NoError(t, err)
}

func TestPromptInitialInputCloseTakesOverBoundSession(t *testing.T) {
	f := newPromptQueueFixture(t)
	created := make(chan error, 1)
	go func() {
		_, err := f.svc.CreateSessionWithInput(context.Background(), resource.SessionCreateInput{
			Metadata:     resource.Metadata{Namespace: "prompt", Name: "initial-close"},
			Spec:         resource.SessionSpec{RuntimeRef: "agent", SessionSpec: runtime.SessionSpec{Cwd: "/remote"}},
			InitialInput: &runtime.Input{Content: []events.ContentBlock{events.Text("hello")}},
		})
		created <- err
	}()
	require.Eventually(t, func() bool {
		_, err := f.svc.GetSession(resource.SessionRef{Namespace: "prompt", Name: "initial-close"})
		return err == nil
	}, time.Second, time.Millisecond)

	closed := make(chan error, 1)
	go func() {
		_, err := f.svc.CloseSession(context.Background(), resource.SessionRef{Namespace: "prompt", Name: "initial-close"})
		closed <- err
	}()
	select {
	case err := <-closed:
		require.NoError(t, err)
	case <-time.After(time.Second):
		t.Fatal("close waited for initial input to observe idle")
	}
	require.ErrorIs(t, <-created, runtime.ErrFailedPrecondition)
	calls, _, _, closes := f.driver.counts()
	require.Zero(t, calls)
	require.Equal(t, 1, closes)
}

func TestPromptResumeInitialInputCloseTakesOverBoundSession(t *testing.T) {
	f := newPromptQueueFixture(t)
	_, err := f.svc.CloseSession(context.Background(), f.ref)
	require.NoError(t, err)
	_, _, _, baselineCloses := f.driver.counts()

	resumed := make(chan error, 1)
	go func() {
		_, resumeErr := f.svc.ResumeSessionWithInput(context.Background(), f.ref, f.prompt("resume input"))
		resumed <- resumeErr
	}()
	require.Eventually(t, func() bool {
		current, getErr := f.svc.GetSession(f.ref)
		return getErr == nil && current.Status.State == events.Unknown
	}, time.Second, time.Millisecond)

	closed := make(chan error, 1)
	go func() {
		_, closeErr := f.svc.CloseSession(context.Background(), f.ref)
		closed <- closeErr
	}()
	select {
	case err := <-closed:
		require.NoError(t, err)
	case <-time.After(time.Second):
		t.Fatal("close waited for resume input to observe idle")
	}
	require.ErrorIs(t, <-resumed, runtime.ErrFailedPrecondition)
	calls, _, _, closes := f.driver.counts()
	require.Zero(t, calls)
	require.Equal(t, baselineCloses+1, closes)
}

var _ runtime.Driver = (*promptQueueDriver)(nil)
