package transport_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http/httptest"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"github.com/zoumo/agent-sdk-go/agent"

	"github.com/zoumo/mass/mass-go/pkg/client"
	"github.com/zoumo/mass/mass-go/pkg/events"
	"github.com/zoumo/mass/mass-go/pkg/resource"
	"github.com/zoumo/mass/mass-go/pkg/runtime"
	"github.com/zoumo/mass/mass-go/pkg/runtime/agentrelay"
	"github.com/zoumo/mass/mass-go/pkg/service"
	"github.com/zoumo/mass/mass-go/pkg/store"
	"github.com/zoumo/mass/mass-go/pkg/store/file"
	"github.com/zoumo/mass/mass-go/pkg/transport"
)

type notificationSession struct {
	agent.Session
	queue  chan agent.Event
	stop   chan struct{}
	end    sync.Once
	closed atomic.Bool
}

func (*notificationSession) ID() string { return "native-notifications" }
func (s *notificationSession) Events() (agent.EventStream, error) {
	return s, nil
}

func (s *notificationSession) Next(ctx context.Context) (agent.Event, error) {
	select {
	case event := <-s.queue:
		return event, nil
	case <-s.stop:
		return agent.Event{}, io.EOF
	case <-ctx.Done():
		return agent.Event{}, ctx.Err()
	}
}

func (s *notificationSession) Close(context.Context) error {
	if s.closed.CompareAndSwap(false, true) {
		s.end.Do(func() { close(s.stop) })
	}
	return nil
}

type notificationDriver struct {
	runtime.Driver
	pipe   *events.EventPipe
	mu     sync.Mutex
	native *notificationSession
	relay  *agentrelay.Relay
}

func (d *notificationDriver) Events() (events.Stream, error) { return d.pipe, nil }
func (*notificationDriver) Start(context.Context) (runtime.Capabilities, error) {
	return runtime.Capabilities{"textInput": true}, nil
}

func (d *notificationDriver) OpenSession(ctx context.Context, address runtime.Address, _ runtime.SessionSpec) (runtime.DriverSession, error) {
	native := &notificationSession{queue: make(chan agent.Event, 32), stop: make(chan struct{})}
	source := events.NewSource()
	relay, err := agentrelay.Start(context.WithoutCancel(ctx), agentrelay.Config{
		Session: native, Address: address, Source: source, ExpectedEOF: native.closed.Load,
		Sink: d.pipe.Send,
	})
	if err != nil {
		return runtime.DriverSession{}, err
	}
	d.mu.Lock()
	d.native, d.relay = native, relay
	d.mu.Unlock()
	return runtime.DriverSession{
		SessionID: native.ID(), Source: source, Observation: relay,
		Capabilities: runtime.Capabilities{"textInput": true},
	}, nil
}

func (d *notificationDriver) ResumeSession(ctx context.Context, _ string, address runtime.Address, spec runtime.SessionSpec) (runtime.DriverSession, error) {
	return d.OpenSession(ctx, address, spec)
}

func (d *notificationDriver) CloseSession(ctx context.Context, _ string) error {
	d.mu.Lock()
	native, relay := d.native, d.relay
	d.mu.Unlock()
	if err := native.Close(ctx); err != nil {
		return err
	}
	select {
	case <-relay.Done():
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (*notificationDriver) Cancel(context.Context, string) (runtime.CancelReceipt, error) {
	return runtime.CancelReceipt{Result: "noActiveWork"}, nil
}

func (d *notificationDriver) Stop(context.Context) error {
	d.pipe.End(nil)
	return nil
}

func (d *notificationDriver) send(event agent.Event) {
	d.mu.Lock()
	native := d.native
	d.mu.Unlock()
	native.queue <- event
}

func (d *notificationDriver) disconnect() {
	d.mu.Lock()
	native := d.native
	d.mu.Unlock()
	native.end.Do(func() { close(native.stop) })
}

func TestSDKNotificationsThroughRPCHistoryStateAndResume(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	st, err := file.Open(t.TempDir(), false)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, st.Close()) })
	driver := &notificationDriver{pipe: events.NewEventPipe(32)}
	svc := service.New(st, st, runtime.NewRegistry(map[string]runtime.Factory{"fixture": httpConfigFactoryWithDriver{driver}}))
	t.Cleanup(func() {
		cleanup, done := context.WithTimeout(context.Background(), 5*time.Second)
		defer done()
		require.NoError(t, svc.Shutdown(cleanup))
	})
	_, err = svc.CreateRuntime(resource.Runtime{
		Metadata: resource.Metadata{Name: "agent"},
		Spec:     resource.RuntimeSpec{Type: "fixture", Config: runtime.Config{}},
	})
	require.NoError(t, err)
	handler, err := transport.New(svc, withTestWriteTimeout(transport.Options{LocalOnly: true}))
	require.NoError(t, err)
	t.Cleanup(handler.Shutdown)
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	rpcClient, err := client.New(rpcURL(server), "", client.Options{
		WriteTimeout:              time.Second,
		SubscriptionQueueCapacity: 8,
	})
	require.NoError(t, err)
	defer rpcClient.Close()
	var created resource.Session
	require.NoError(t, rpcClient.CreateSession(ctx, resource.Session{
		Metadata: resource.Metadata{Namespace: "one", Name: "notifications"},
		Spec: resource.SessionSpec{RuntimeRef: "agent", SessionSpec: runtime.SessionSpec{
			Cwd: "/work", Config: &runtime.InitialSessionConfig{Model: "literal-model", Effort: "literal-effort"},
			SystemPrompt: "Persistent additional instructions.",
		}},
	}, &created))
	ref := resource.SessionRef{SessionID: created.Status.SessionID}
	observer, err := rpcClient.Subscribe(ctx, resource.SessionRef{Namespace: "one", Name: "notifications"})
	require.NoError(t, err)
	defer observer.Close()

	raw, err := os.ReadFile("../../../harness/modules/runtime/event-examples.json")
	require.NoError(t, err)
	var examples struct {
		Events []map[string]json.RawMessage `json:"events"`
	}
	require.NoError(t, json.Unmarshal(raw, &examples))
	persistent := []events.SessionEvent{}
	delivered := 0
	for _, example := range examples.Events {
		if string(example["type"]) == `"session.history_replay"` {
			continue
		}
		delete(example, "namespace")
		delete(example, "sessionName")
		example["sessionId"], err = json.Marshal(created.Status.SessionID)
		require.NoError(t, err)
		wire, err := json.Marshal(example)
		require.NoError(t, err)
		var native agent.Event
		require.NoError(t, json.Unmarshal(wire, &native))
		driver.send(native)
		got, err := observer.Next(ctx)
		require.NoError(t, err)
		require.Equal(t, native.EventID, got.EventID)
		require.Equal(t, native.SessionID, got.SessionID)
		require.Equal(t, native.Time, got.Time)
		require.Equal(t, native.ItemID, got.ItemID)
		require.Equal(t, string(native.Type()), got.Type())
		require.Equal(t, "one", got.Namespace)
		require.Equal(t, "notifications", got.SessionName)
		expected, err := agentrelay.MapEvent(runtime.Address{Namespace: got.Namespace, SessionName: got.SessionName}, events.Source{}, native)
		require.NoError(t, err)
		expectedJSON, err := json.Marshal(expected)
		require.NoError(t, err)
		actualJSON, err := json.Marshal(got)
		require.NoError(t, err)
		require.JSONEq(t, string(expectedJSON), string(actualJSON))
		if got.Persistent() {
			persistent = append(persistent, got)
			var history store.HistoryPage
			require.NoError(t, rpcClient.History(ctx, ref, store.HistoryQuery{}, &history))
			require.Equal(t, persistent, history.Events)
		}
		delivered++
	}
	require.Equal(t, 16, delivered)
	require.Len(t, persistent, 5)

	idle := agent.Event{
		EventID: uuid.NewString(), SessionID: created.Status.SessionID, Time: time.Now().UTC(),
		Payload: agent.StateUpdatePayload{State: new(agent.StateIdle)},
	}
	driver.send(idle)
	state, err := observer.Next(ctx)
	require.NoError(t, err)
	require.Equal(t, idle.EventID, state.EventID)
	require.Eventually(t, func() bool {
		var current resource.Session
		return rpcClient.GetSession(ctx, ref, &current) == nil && current.Status.State == events.Idle
	}, time.Second, time.Millisecond)
	var closed resource.Session
	require.NoError(t, rpcClient.CloseSession(ctx, ref, &closed))
	require.Equal(t, events.SessionState("offline"), closed.Status.State)
	require.Equal(t, created.Spec, closed.Spec)
	var resumed resource.Session
	require.NoError(t, rpcClient.ResumeSession(ctx, ref, &resumed))
	require.Equal(t, created.Spec, resumed.Spec)
	require.Equal(t, created.Metadata.UID, resumed.Metadata.UID)
	idle.EventID = uuid.NewString()
	driver.send(idle)
	state, err = observer.Next(ctx)
	require.NoError(t, err)
	require.Equal(t, idle.EventID, state.EventID)
	var history store.HistoryPage
	require.NoError(t, rpcClient.History(ctx, ref, store.HistoryQuery{}, &history))
	require.Equal(t, persistent, history.Events)
	driver.disconnect()
	require.Eventually(t, func() bool {
		var current resource.Session
		return rpcClient.GetSession(ctx, ref, &current) == nil &&
			current.Status.State == events.Unknown && current.Status.Reason == "session_observation_lost"
	}, time.Second, time.Millisecond)
	require.NoError(t, rpcClient.CloseSession(ctx, ref, nil))
}

type httpConfigFactoryWithDriver struct{ runtime.Driver }

func (httpConfigFactoryWithDriver) Validate(runtime.Config) error { return nil }
func (f httpConfigFactoryWithDriver) New(string, runtime.Config) (runtime.Driver, error) {
	return f.Driver, nil
}
