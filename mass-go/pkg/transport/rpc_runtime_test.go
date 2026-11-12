package transport_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/require"

	"github.com/zoumo/mass/mass-go/pkg/events"
	"github.com/zoumo/mass/mass-go/pkg/resource"
	"github.com/zoumo/mass/mass-go/pkg/runtime"
	"github.com/zoumo/mass/mass-go/pkg/service"
	"github.com/zoumo/mass/mass-go/pkg/store/file"
	"github.com/zoumo/mass/mass-go/pkg/transport"
)

type rpcRuntimeDriver struct {
	pipe *events.EventPipe
}

func (d *rpcRuntimeDriver) Events() (events.Stream, error) { return d.pipe, nil }
func (d *rpcRuntimeDriver) Start(context.Context) (runtime.Capabilities, error) {
	return runtime.Capabilities{}, nil
}

func (*rpcRuntimeDriver) OpenSession(context.Context, runtime.Address, runtime.SessionSpec) (runtime.DriverSession, error) {
	return runtime.DriverSession{}, errors.New("unexpected open")
}

func (*rpcRuntimeDriver) ResumeSession(context.Context, string, runtime.Address, runtime.SessionSpec) (runtime.DriverSession, error) {
	return runtime.DriverSession{}, errors.New("unexpected resume")
}

func (*rpcRuntimeDriver) LoadSession(context.Context, string, runtime.Address, runtime.SessionSpec, string) error {
	return errors.New("unexpected load")
}

func (*rpcRuntimeDriver) CancelHistoryLoad(context.Context, string, string) error {
	return errors.New("unexpected cancel load")
}

func (*rpcRuntimeDriver) SendPrompt(context.Context, string, runtime.Input) (runtime.InputReceipt, error) {
	return runtime.InputReceipt{}, errors.New("unexpected prompt")
}

func (*rpcRuntimeDriver) Cancel(context.Context, string) (runtime.CancelReceipt, error) {
	return runtime.CancelReceipt{}, errors.New("unexpected cancel")
}

func (*rpcRuntimeDriver) AnswerInteraction(context.Context, string, string, any) (runtime.Acceptance, error) {
	return runtime.Acceptance{}, errors.New("unexpected answer")
}

func (*rpcRuntimeDriver) CloseSession(context.Context, string) error {
	return errors.New("unexpected close")
}

func (d *rpcRuntimeDriver) Stop(context.Context) error {
	d.pipe.End(nil)
	return nil
}

type rpcRuntimeFactory struct{ driver *rpcRuntimeDriver }

func (rpcRuntimeFactory) Validate(runtime.Config) error { return nil }
func (f rpcRuntimeFactory) New(string, runtime.Config) (runtime.Driver, error) {
	return f.driver, nil
}

func newRPCRuntimeFixture(t *testing.T, driver *rpcRuntimeDriver) *httptest.Server {
	t.Helper()
	if driver.pipe == nil {
		driver.pipe = events.NewEventPipe(16)
	}
	st, err := file.Open(t.TempDir(), false)
	require.NoError(t, err)
	svc := service.New(st, st, runtime.NewRegistry(map[string]runtime.Factory{"fixture": rpcRuntimeFactory{driver}}))
	endpoint, err := transport.New(svc, transport.Options{Token: "test-token"})
	require.NoError(t, err)
	httpServer := httptest.NewServer(endpoint)
	t.Cleanup(func() {
		endpoint.Shutdown()
		httpServer.Close()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		require.NoError(t, svc.Shutdown(ctx))
		require.NoError(t, st.Close())
	})
	return httpServer
}

func sendCall(t *testing.T, conn *websocket.Conn, id, method, params string) {
	t.Helper()
	if params == "" {
		sendText(t, conn, fmt.Sprintf(`{"jsonrpc":"2.0","id":%q,"method":%q}`, id, method))
		return
	}
	sendText(t, conn, fmt.Sprintf(`{"jsonrpc":"2.0","id":%q,"method":%q,"params":%s}`, id, method, params))
}

func TestRPCRuntimeCRUDAndLifecycle(t *testing.T) {
	httpServer := newRPCRuntimeFixture(t, &rpcRuntimeDriver{})
	conn := dialRPC(t, httpServer, rpcHeaders("test-token"))
	sendText(t, conn, initializeMessage(`"init"`, "1"))
	require.Nil(t, readRPC(t, conn).Error)

	sendCall(t, conn, "create", "runtime.create", `{
		"input":{
			"metadata":{"name":"agent"},
			"spec":{"type":"fixture","config":{"large":9007199254740993,"nested":{"ok":true}},"features":{"agentQuestions":false}}
		}
	}`)
	created := readRPC(t, conn)
	require.Nil(t, created.Error)
	require.Contains(t, string(created.Result), `"large":9007199254740993`)
	require.Contains(t, string(created.Result), `"state":"stopped"`)

	sendCall(t, conn, "get", "runtime.get", `{"runtimeRef":"agent"}`)
	require.Contains(t, string(readRPC(t, conn).Result), `"name":"agent"`)

	sendCall(t, conn, "list", "runtime.list", `{"query":{"type":"fixture","state":"stopped"}}`)
	list := readRPC(t, conn)
	require.Nil(t, list.Error)
	var runtimes []resource.Runtime
	require.NoError(t, json.Unmarshal(list.Result, &runtimes))
	require.Len(t, runtimes, 1)

	sendCall(t, conn, "update", "runtime.update", `{"runtimeRef":"agent","spec":{"type":"fixture","config":{"updated":true}}}`)
	updated := readRPC(t, conn)
	require.Contains(t, string(updated.Result), `"updated":true`)
	require.NotContains(t, string(updated.Result), `"large"`)
	require.Contains(t, string(updated.Result), `"generation":2`)

	sendCall(t, conn, "start", "runtime.start", `{"runtimeRef":"agent"}`)
	started := readRPC(t, conn)
	require.Nil(t, started.Error)
	require.Contains(t, string(started.Result), `"state":"running"`)

	sendCall(t, conn, "stop", "runtime.stop", `{"runtimeRef":"agent"}`)
	stopped := readRPC(t, conn)
	require.Nil(t, stopped.Error)
	require.Contains(t, string(stopped.Result), `"state":"stopped"`)

	sendCall(t, conn, "delete", "runtime.delete", `{"runtimeRef":"agent"}`)
	deleted := readRPC(t, conn)
	require.Nil(t, deleted.Error)
	require.Equal(t, "null", string(deleted.Result))

	sendCall(t, conn, "missing", "runtime.get", `{"runtimeRef":"agent"}`)
	requireServiceRPCError(t, readRPC(t, conn), "not_found", "resource_not_found")
}

func TestRPCRuntimeInvalidParamsAndUnknownMethod(t *testing.T) {
	httpServer := newRPCRuntimeFixture(t, &rpcRuntimeDriver{})
	conn := dialRPC(t, httpServer, rpcHeaders("test-token"))
	sendText(t, conn, initializeMessage(`"init"`, "1"))
	require.Nil(t, readRPC(t, conn).Error)

	for _, test := range []struct {
		name   string
		method string
		params string
	}{
		{name: "missing create input", method: "runtime.create", params: `{}`},
		{name: "null create input", method: "runtime.create", params: `{"input":null}`},
		{name: "missing create metadata", method: "runtime.create", params: `{"input":{"spec":{"type":"fixture","config":{}}}}`},
		{name: "read-only create metadata", method: "runtime.create", params: `{"input":{"metadata":{"name":"agent","uid":""},"spec":{"type":"fixture","config":{}}}}`},
		{name: "read-only create status", method: "runtime.create", params: `{"input":{"metadata":{"name":"agent"},"spec":{"type":"fixture","config":{}},"status":{}}}`},
		{name: "null runtime ref", method: "runtime.get", params: `{"runtimeRef":null}`},
		{name: "missing runtime ref", method: "runtime.get", params: `{}`},
		{name: "unknown ref field", method: "runtime.get", params: `{"runtimeRef":"agent","extra":true}`},
		{name: "null update spec", method: "runtime.update", params: `{"runtimeRef":"agent","spec":null}`},
		{name: "missing update spec", method: "runtime.update", params: `{"runtimeRef":"agent"}`},
		{name: "null query", method: "runtime.list", params: `{"query":null}`},
		{name: "unknown query field", method: "runtime.list", params: `{"query":{"kind":"fixture"}}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			id := "invalid-" + test.name
			sendCall(t, conn, id, test.method, test.params)
			requireRPCError(t, readRPC(t, conn), -32602, fmt.Sprintf("%q", id), "")
		})
	}

	sendText(t, conn, `{"jsonrpc":"2.0","id":"no-params","method":"runtime.get"}`)
	requireRPCError(t, readRPC(t, conn), -32602, `"no-params"`, "")

	sendCall(t, conn, "unknown", "runtime.missing", `{}`)
	requireRPCError(t, readRPC(t, conn), -32601, `"unknown"`, "")
}

func TestRPCRuntimeBusinessErrorData(t *testing.T) {
	httpServer := newRPCRuntimeFixture(t, &rpcRuntimeDriver{})
	conn := dialRPC(t, httpServer, rpcHeaders("test-token"))
	sendText(t, conn, initializeMessage(`"init"`, "1"))
	require.Nil(t, readRPC(t, conn).Error)

	sendCall(t, conn, "bad-type", "runtime.create", `{"input":{"metadata":{"name":"agent"},"spec":{"type":"missing","config":{}}}}`)
	requireServiceRPCError(t, readRPC(t, conn), "invalid_argument", "invalid_request")

	sendCall(t, conn, "create", "runtime.create", `{"input":{"metadata":{"name":"agent"},"spec":{"type":"fixture","config":{}}}}`)
	require.Nil(t, readRPC(t, conn).Error)
	sendCall(t, conn, "duplicate", "runtime.create", `{"input":{"metadata":{"name":"agent"},"spec":{"type":"fixture","config":{}}}}`)
	requireServiceRPCError(t, readRPC(t, conn), "conflict", "resource_conflict")
}

func requireServiceRPCError(t *testing.T, response rpcResponse, code, reason string) {
	t.Helper()
	require.NotNil(t, response.Error)
	require.Equal(t, int64(-32000), response.Error.Code)
	require.Empty(t, response.Result)
	var operation struct {
		Code    string `json:"code"`
		Reason  string `json:"reason"`
		Message string `json:"message"`
	}
	require.NoError(t, json.Unmarshal(response.Error.Data, &operation))
	require.Equal(t, code, operation.Code)
	require.Equal(t, reason, operation.Reason)
	require.NotEmpty(t, operation.Message)
	require.Equal(t, operation.Message, response.Error.Message)
}
