package service_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
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
	"github.com/zoumo/mass/mass-go/pkg/transport"
)

type (
	configDispatch struct {
		change runtime.ConfigChange
		done   chan configDispatchResult
	}
	configDispatchResult struct {
		receipt runtime.ConfigReceipt
		err     error
	}
	liveConfigDriver struct {
		*closeDriver
		requests chan configDispatch
		calls    atomic.Int32
	}
)

func (d *liveConfigDriver) SetSessionConfig(ctx context.Context, _ string, change runtime.ConfigChange) (runtime.ConfigReceipt, error) {
	d.calls.Add(1)
	request := configDispatch{change: change, done: make(chan configDispatchResult, 1)}
	select {
	case d.requests <- request:
	case <-ctx.Done():
		return runtime.ConfigReceipt{}, ctx.Err()
	}
	select {
	case result := <-request.done:
		return result.receipt, result.err
	case <-ctx.Done():
		return runtime.ConfigReceipt{}, ctx.Err()
	}
}

type liveConfigFactory struct{ runtime.Driver }

func (liveConfigFactory) Validate(runtime.Config) error                        { return nil }
func (f liveConfigFactory) New(string, runtime.Config) (runtime.Driver, error) { return f.Driver, nil }
func newLiveConfigService(t *testing.T, optional bool, options ...service.Options) (*service.Service, *file.Store, *liveConfigDriver, resource.SessionRef) {
	t.Helper()
	st, err := file.Open(t.TempDir(), false)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, st.Close()) })
	driver := &liveConfigDriver{closeDriver: &closeDriver{pipe: events.NewEventPipe(64)}, requests: make(chan configDispatch, 16)}
	var selected runtime.Driver = driver
	if !optional {
		selected = driver.closeDriver
	}
	registry := runtime.NewRegistry(map[string]runtime.Factory{"fixture": liveConfigFactory{selected}})
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
	created, err := svc.CreateSession(t.Context(), resource.Session{Metadata: resource.Metadata{Namespace: "one", Name: "configured"}, Spec: resource.SessionSpec{RuntimeRef: "agent", SessionSpec: runtime.SessionSpec{Cwd: "/remote", Config: &runtime.InitialSessionConfig{Model: "initial/model", Effort: "initial/effort"}}}})
	require.NoError(t, err)
	return svc, st, driver, resource.SessionRef{SessionID: created.Status.SessionID}
}

func configRequest(t *testing.T, d *liveConfigDriver) configDispatch {
	t.Helper()
	select {
	case request := <-d.requests:
		return request
	case <-t.Context().Done():
		t.Fatal("missing Driver request")
		return configDispatch{}
	}
}

func TestLiveSessionConfigurationServiceAdmission(t *testing.T) {
	svc, st, d, ref := newLiveConfigService(t, true)
	change := runtime.ConfigChange{ID: agent.ConfigModel, Value: "literal"}
	for _, state := range []events.SessionState{"offline", "opening", "closing"} {
		r, err := svc.GetSession(ref)
		require.NoError(t, err)
		r.Status.State = state
		_, err = st.UpdateSession(r)
		require.NoError(t, err)
		receipt, err := svc.SetSessionConfig(t.Context(), ref, change)
		require.Error(t, err)
		require.Equal(t, change, receipt.Change)
	}
	require.Zero(t, d.calls.Load())
	expired, cancel := context.WithDeadline(t.Context(), time.Unix(1, 0))
	defer cancel()
	receipt, err := svc.SetSessionConfig(expired, ref, change)
	var deadline *runtime.OperationError
	require.ErrorAs(t, err, &deadline)
	require.Equal(t, "deadline_exceeded", deadline.Code)
	require.Equal(t, runtime.Bool(false), receipt.Acceptance.Sent)
	require.Zero(t, d.calls.Load())
	require.Zero(t, d.resumes.Load())
	for _, state := range []events.SessionState{events.Unknown, events.Idle, events.Running, events.RequiresAction} {
		r, err := svc.GetSession(ref)
		require.NoError(t, err)
		r.Status.State = state
		_, err = st.UpdateSession(r)
		require.NoError(t, err)
		done := make(chan error, 1)
		go func() { _, err := svc.SetSessionConfig(t.Context(), ref, change); done <- err }()
		request := configRequest(t, d)
		request.done <- configDispatchResult{receipt: runtime.ConfigReceipt{Change: change}}
		require.NoError(t, <-done)
	}
	for _, bad := range []runtime.ConfigChange{{ID: "future", Value: "x"}, {ID: agent.ConfigModel}, {ID: agent.ConfigEffort, Value: "bad\x00"}, {ID: agent.ConfigModel, Value: string([]byte{0xff})}} {
		_, err := svc.SetSessionConfig(t.Context(), ref, bad)
		require.Error(t, err)
	}
	require.EqualValues(t, 4, d.calls.Load())
	require.NoError(t, func() error { _, err := svc.CloseSession(t.Context(), ref); return err }())
	_, err = svc.SetSessionConfig(t.Context(), ref, change)
	require.Error(t, err)
	require.EqualValues(t, 4, d.calls.Load())
	require.Zero(t, d.resumes.Load())
	// A process with durable unknown but no live binding cannot auto-resume.
	r, err := svc.GetSession(ref)
	require.NoError(t, err)
	r.Status.State = events.Unknown
	_, err = st.UpdateSession(r)
	require.NoError(t, err)
	_, err = svc.SetSessionConfig(t.Context(), ref, change)
	require.Error(t, err)
	require.Zero(t, d.resumes.Load())
}

func TestLiveSessionConfigurationPublicReceiptsAndErrors(t *testing.T) {
	svc, _, d, ref := newLiveConfigService(t, true)
	h, err := transport.New(svc, transport.Options{LocalOnly: true})
	require.NoError(t, err)
	defer h.Shutdown()
	server := httptest.NewServer(h)
	defer server.Close()
	cases := []struct {
		code       string
		status     int
		acceptance agent.Acceptance
	}{
		{"", 200, agent.Acceptance{}},
		{"", 200, agent.Acceptance{Sent: runtime.Bool(false), NativeAccepted: runtime.Bool(false), NativeRecorded: runtime.Bool(false)}},
		{"", 200, agent.Acceptance{Sent: runtime.Yes(), NativeAccepted: runtime.Yes(), NativeRecorded: runtime.Yes()}},
		{"native_rejected", 422, agent.Acceptance{Sent: runtime.Yes(), NativeAccepted: runtime.Bool(false)}},
		{"unsupported", 422, agent.Acceptance{Sent: runtime.Bool(false)}},
		{"conflict", 409, agent.Acceptance{Sent: runtime.Bool(false)}},
		{"deadline_exceeded", 504, agent.Acceptance{Sent: runtime.Bool(false)}},
		{"outcome_unknown", 500, agent.Acceptance{Sent: runtime.Yes()}},
	}
	for i, tc := range cases {
		t.Run(fmt.Sprintf("%d_%s", i, tc.code), func(t *testing.T) {
			change := runtime.ConfigChange{ID: agent.ConfigEffort, Value: " Native Effort "}
			receipt := runtime.ConfigReceipt{Change: change, Acceptance: tc.acceptance}
			var failure error
			if tc.code != "" {
				failure = &runtime.OperationError{Code: tc.code, Reason: "sdk_reason", Message: "safe"}
			}
			// Go callers retain the receipt even on failure.
			done := make(chan configDispatchResult, 1)
			go func() { r, err := svc.SetSessionConfig(t.Context(), ref, change); done <- configDispatchResult{r, err} }()
			request := configRequest(t, d)
			request.done <- configDispatchResult{receipt, failure}
			result := <-done
			require.Equal(t, receipt, result.receipt)
			require.Equal(t, failure, result.err)
			httpDone := make(chan *http.Response, 1)
			go func() {
				req, err := http.NewRequestWithContext(t.Context(), "POST", server.URL+"/apis/v1/namespaces/one/sessions/configured/config", strings.NewReader(`{"id":"effort","value":" Native Effort "}`))
				if err != nil {
					panic(err)
				}
				req.Header.Set("Content-Type", "application/json")
				res, err := http.DefaultClient.Do(req)
				if err != nil {
					panic(err)
				}
				httpDone <- res
			}()
			request = configRequest(t, d)
			request.done <- configDispatchResult{receipt, failure}
			res := <-httpDone
			defer res.Body.Close()
			raw, err := io.ReadAll(res.Body)
			require.NoError(t, err)
			require.Equal(t, tc.status, res.StatusCode, "%s", raw)
			if failure != nil {
				require.JSONEq(t, fmt.Sprintf(`{"code":%q,"reason":"sdk_reason","message":"safe"}`, tc.code), string(raw))
			} else {
				var value struct {
					Change struct {
						ID    string `json:"id"`
						Value string `json:"value"`
					} `json:"change"`
					Acceptance runtime.Acceptance `json:"acceptance"`
				}
				require.NoError(t, json.Unmarshal(raw, &value))
				require.Equal(t, string(change.ID), value.Change.ID)
				require.Equal(t, change.Value, value.Change.Value)
				require.Equal(t, tc.acceptance.Sent, value.Acceptance.Sent)
				require.Equal(t, tc.acceptance.NativeAccepted, value.Acceptance.NativeAccepted)
				require.Equal(t, tc.acceptance.NativeRecorded, value.Acceptance.NativeRecorded)
			}
		})
	}
}

func TestLiveSessionConfigurationOptionalDriver(t *testing.T) {
	svc, _, d, ref := newLiveConfigService(t, false)
	change := runtime.ConfigChange{ID: agent.ConfigModel, Value: "model"}
	receipt, err := svc.SetSessionConfig(t.Context(), ref, change)
	var public *runtime.OperationError
	require.ErrorAs(t, err, &public)
	require.Equal(t, "unsupported", public.Code)
	require.Equal(t, change, receipt.Change)
	require.Equal(t, runtime.Bool(false), receipt.Acceptance.Sent)
	require.Zero(t, d.calls.Load())
}

func TestLiveSessionConfigurationIncompleteLoadAndObservedUnknown(t *testing.T) {
	svc, st, d, ref := newLiveConfigService(t, true)
	accepted, err := svc.LoadSession(t.Context(), resource.SessionLoad{Metadata: resource.Metadata{Namespace: "one", Name: "imported"}, RuntimeRef: "agent", Cwd: "/remote", SessionID: "loaded-native"})
	require.NoError(t, err)
	loaded := accepted.Session
	loaded.Status.State = events.Idle
	_, err = st.UpdateSession(loaded)
	require.NoError(t, err)
	change := runtime.ConfigChange{ID: agent.ConfigModel, Value: "literal"}
	_, err = svc.SetSessionConfig(t.Context(), resource.SessionRef{SessionID: loaded.Status.SessionID}, change)
	require.Error(t, err)
	require.Zero(t, d.calls.Load())
	_, err = svc.CloseSession(t.Context(), resource.SessionRef{SessionID: loaded.Status.SessionID})
	require.NoError(t, err)
	// An explicit unknown observation removes the pre-observation live exception.
	event, err := events.NewEvent(events.EventBase{EventID: uuid.NewString(), Namespace: "one", SessionName: "configured", SessionID: ref.SessionID, Time: time.Now()}, events.StateUpdate{}, d.source)
	require.NoError(t, err)
	subscription, err := svc.SubscribeEvents(ref)
	require.NoError(t, err)
	defer subscription.Close()
	require.NoError(t, d.pipe.Send(t.Context(), event))
	require.NoError(t, d.pipe.Flush(t.Context()))
	select {
	case observed := <-subscription.Events():
		require.Equal(t, event.EventID, observed.EventID)
	case <-t.Context().Done():
		t.Fatal("unknown observation missing")
	}
	_, err = svc.SetSessionConfig(t.Context(), ref, change)
	require.Error(t, err)
	require.Zero(t, d.calls.Load())
}

func TestLiveSessionConfigurationRearmsIdle(t *testing.T) {
	clock := newManualClock()
	svc, _, d, ref := newLiveConfigService(t, true, service.Options{SessionIdleTimeout: time.Minute, Clock: clock})
	require.NoError(t, d.emitIdle(t.Context(), runtime.Address{Namespace: "one", SessionName: "configured"}, ref.SessionID, d.source))
	require.Eventually(t, func() bool { return clock.Active() == 1 }, time.Second, time.Millisecond)
	result := make(chan error, 1)
	change := runtime.ConfigChange{ID: agent.ConfigModel, Value: "literal"}
	go func() { _, err := svc.SetSessionConfig(t.Context(), ref, change); result <- err }()
	request := configRequest(t, d)
	require.Zero(t, clock.Active())
	request.done <- configDispatchResult{receipt: runtime.ConfigReceipt{Change: change}}
	require.NoError(t, <-result)
	require.Equal(t, 1, clock.Active())
}
