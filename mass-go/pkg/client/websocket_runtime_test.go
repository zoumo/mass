package client_test

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/zoumo/mass/mass-go/pkg/client"
	"github.com/zoumo/mass/mass-go/pkg/events"
	"github.com/zoumo/mass/mass-go/pkg/resource"
	"github.com/zoumo/mass/mass-go/pkg/runtime"
	"github.com/zoumo/mass/mass-go/pkg/service"
	"github.com/zoumo/mass/mass-go/pkg/store/file"
	"github.com/zoumo/mass/mass-go/pkg/transport"
)

type blockingRuntimeDriver struct {
	runtime.Driver
	pipe         *events.EventPipe
	startEntered chan struct{}
	startRelease chan struct{}
	stopEntered  chan struct{}
	stopRelease  chan struct{}
}

func (d *blockingRuntimeDriver) Events() (events.Stream, error) { return d.pipe, nil }
func (d *blockingRuntimeDriver) Start(context.Context) (runtime.Capabilities, error) {
	d.startEntered <- struct{}{}
	<-d.startRelease
	return runtime.Capabilities{}, nil
}

func (d *blockingRuntimeDriver) Stop(context.Context) error {
	d.stopEntered <- struct{}{}
	<-d.stopRelease
	d.pipe.End(nil)
	return nil
}

type blockingRuntimeFactory struct{ driver *blockingRuntimeDriver }

func (blockingRuntimeFactory) Validate(runtime.Config) error { return nil }
func (f blockingRuntimeFactory) New(string, runtime.Config) (runtime.Driver, error) {
	return f.driver, nil
}

func newTypedRuntimeServer(t *testing.T) (*httptest.Server, *blockingRuntimeDriver) {
	t.Helper()
	driver := &blockingRuntimeDriver{
		pipe:         events.NewEventPipe(16),
		startEntered: make(chan struct{}, 1),
		startRelease: make(chan struct{}),
		stopEntered:  make(chan struct{}, 1),
		stopRelease:  make(chan struct{}),
	}
	st, err := file.Open(t.TempDir(), false)
	require.NoError(t, err)
	svc := service.New(st, st, runtime.NewRegistry(map[string]runtime.Factory{"fixture": blockingRuntimeFactory{driver}}))
	endpoint, err := transport.New(svc, transport.Options{Token: "test-token"})
	require.NoError(t, err)
	httpServer := httptest.NewServer(endpoint)
	t.Cleanup(func() {
		closeChannel(driver.startRelease)
		closeChannel(driver.stopRelease)
		endpoint.Shutdown()
		httpServer.Close()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		require.NoError(t, svc.Shutdown(ctx))
		require.NoError(t, st.Close())
	})
	return httpServer, driver
}

func closeChannel(ch chan struct{}) {
	select {
	case <-ch:
	default:
		close(ch)
	}
}

func TestWebSocketClientRuntimeCRUDAndConcurrentQueries(t *testing.T) {
	httpServer, driver := newTypedRuntimeServer(t)
	wsClient := client.NewWebSocket(httpServer.URL, "test-token")
	t.Cleanup(func() { _ = wsClient.Close() })

	var created resource.Runtime
	require.NoError(t, wsClient.CreateRuntime(t.Context(), resource.Runtime{
		Metadata: resource.Metadata{Name: "agent"},
		Spec: resource.RuntimeSpec{
			Type:   "fixture",
			Config: runtime.Config{"large": json.Number("9007199254740993")},
		},
	}, &created))
	require.Equal(t, "agent", created.Metadata.Name)
	require.Equal(t, "stopped", created.Status.State)
	require.Equal(t, json.Number("9007199254740993"), created.Spec.Config["large"])

	var got resource.Runtime
	require.NoError(t, wsClient.GetRuntime(t.Context(), "agent", &got))
	require.Equal(t, created.Metadata.UID, got.Metadata.UID)

	var listed []resource.Runtime
	require.NoError(t, wsClient.ListRuntimes(t.Context(), resource.RuntimeQuery{Type: "fixture", State: "stopped"}, &listed))
	require.Len(t, listed, 1)

	var updated resource.Runtime
	require.NoError(t, wsClient.UpdateRuntime(t.Context(), "agent", resource.RuntimeSpec{
		Type:   "fixture",
		Config: runtime.Config{"updated": true},
	}, &updated))
	require.EqualValues(t, 2, updated.Metadata.Generation)
	require.Equal(t, true, updated.Spec.Config["updated"])
	require.NotContains(t, updated.Spec.Config, "large")

	started := make(chan error, 1)
	go func() {
		var out resource.Runtime
		started <- wsClient.StartRuntime(t.Context(), "agent", &out)
	}()
	awaitSignal(t, driver.startEntered, "runtime.start was not dispatched")

	queryCtx, cancelQuery := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancelQuery()
	var duringStart resource.Runtime
	require.NoError(t, wsClient.GetRuntime(queryCtx, "agent", &duringStart), "runtime.get must not wait for a slow start")
	var listedDuringStart []resource.Runtime
	require.NoError(t, wsClient.ListRuntimes(queryCtx, resource.RuntimeQuery{}, &listedDuringStart))

	close(driver.startRelease)
	require.NoError(t, <-started)
	require.NoError(t, wsClient.GetRuntime(t.Context(), "agent", &duringStart))
	require.Equal(t, "running", duringStart.Status.State)

	stopped := make(chan error, 1)
	go func() {
		var out resource.Runtime
		stopped <- wsClient.StopRuntime(t.Context(), "agent", &out)
	}()
	awaitSignal(t, driver.stopEntered, "runtime.stop was not dispatched")
	stopQueryCtx, cancelStopQuery := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancelStopQuery()
	require.NoError(t, wsClient.GetRuntime(stopQueryCtx, "agent", &duringStart), "runtime.get must not wait for a slow stop")
	close(driver.stopRelease)
	require.NoError(t, <-stopped)

	require.NoError(t, wsClient.DeleteRuntime(t.Context(), "agent"))
	err := wsClient.GetRuntime(t.Context(), "agent", &got)
	var operation *runtime.OperationError
	require.ErrorAs(t, err, &operation)
	require.Equal(t, "not_found", operation.Code)
	require.Equal(t, "resource_not_found", operation.Reason)
}

func TestWebSocketClientRuntimeRejectsInvalidNamesLocally(t *testing.T) {
	wsClient := client.NewWebSocket("http://127.0.0.1:1", "test-token")
	t.Cleanup(func() { _ = wsClient.Close() })
	for _, call := range []func() error{
		func() error { return wsClient.GetRuntime(t.Context(), ".", nil) },
		func() error { return wsClient.StartRuntime(t.Context(), "..", nil) },
		func() error { return wsClient.StopRuntime(t.Context(), "", nil) },
		func() error { return wsClient.DeleteRuntime(t.Context(), ".") },
	} {
		err := call()
		var operation *runtime.OperationError
		require.ErrorAs(t, err, &operation)
		require.Equal(t, "invalid_argument", operation.Code)
	}
}

func awaitSignal(t *testing.T, ch chan struct{}, message string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(2 * time.Second):
		t.Fatal(message)
	}
}
