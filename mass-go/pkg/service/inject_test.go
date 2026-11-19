package service_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/zoumo/mass/mass-go/pkg/events"
	"github.com/zoumo/mass/mass-go/pkg/resource"
	"github.com/zoumo/mass/mass-go/pkg/runtime"
	"github.com/zoumo/mass/mass-go/pkg/service"
	"github.com/zoumo/mass/mass-go/pkg/store/file"
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
	st, err := file.Open(t.TempDir(), false)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, st.Close()) })
	registry := runtime.NewRegistry(map[string]runtime.Factory{"fixture": injectWorkFactory{d}})
	svc := service.New(st, st, registry)
	if len(options) > 0 {
		svc = service.NewWithOptions(st, st, registry, options[0])
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
		{"native_rejected", runtime.Acceptance{Sent: runtime.Yes(), NativeAccepted: runtime.Bool(false)}, "native_rejected"},
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
	d.receipt = runtime.InjectReceipt{Acceptance: runtime.Acceptance{Sent: runtime.Yes()}}
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
	d.receipt = runtime.InjectReceipt{Acceptance: runtime.Acceptance{Sent: runtime.Yes()}}
	clock := newManualClock()
	svc, ref := injectService(t, d, service.Options{Clock: clock, SessionIdleTimeout: 10 * time.Second})
	state := events.Idle
	idle, err := events.NewEvent(events.EventBase{EventID: uuid.NewString(), Namespace: "one", SessionName: "context", SessionID: ref.SessionID, Time: time.Now()}, events.StateUpdate{State: &state}, d.source)
	require.NoError(t, err)
	require.NoError(t, d.pipe.Send(t.Context(), idle))
	require.Eventually(t, func() bool { return clock.Active() == 1 }, time.Second, time.Millisecond)
	clock.Advance(5 * time.Second)
	_, err = svc.Inject(t.Context(), ref, runtime.Input{Content: []events.ContentBlock{events.Text("context")}})
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
