package integration_test

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/require"

	"github.com/zoumo/mass/mass-go/pkg/client"
	"github.com/zoumo/mass/mass-go/pkg/resource"
	"github.com/zoumo/mass/mass-go/pkg/runtime"
	"github.com/zoumo/mass/mass-go/pkg/store"
	"github.com/zoumo/mass/mass-go/pkg/transport"
)

// workflowParams describes existing executable scenario tables; no wire envelope.
type workflowParams struct {
	Name         string
	Session      resource.SessionRef
	Runtime      *resource.Runtime
	Create       *resource.Session
	Spec         *resource.RuntimeSpec
	RuntimeQuery resource.RuntimeQuery
	SessionQuery resource.SessionQuery
	History      store.HistoryQuery
	Input        runtime.Input
	Interaction  string
	Answer       any
}

func workflowHTTP(c *client.Client, ctx context.Context, op string, p workflowParams, out any) error {
	switch op {
	case "runtime.create":
		return c.CreateRuntime(ctx, *p.Runtime, out)
	case "runtime.list":
		return c.ListRuntimes(ctx, p.RuntimeQuery, out)
	case "runtime.get":
		return c.GetRuntime(ctx, p.Name, out)
	case "runtime.update":
		return c.UpdateRuntime(ctx, p.Name, *p.Spec, out)
	case "runtime.start":
		return c.StartRuntime(ctx, p.Name, out)
	case "runtime.stop":
		return c.StopRuntime(ctx, p.Name, out)
	case "runtime.delete":
		return c.DeleteRuntime(ctx, p.Name)
	case "session.create":
		return c.CreateSession(ctx, *p.Create, out)
	case "session.list":
		return c.ListSessions(ctx, p.SessionQuery, out)
	case "session.get":
		return c.GetSession(ctx, p.Session, out)
	case "session.input":
		return c.SendPrompt(ctx, p.Session, p.Input, out)
	case "session.answer":
		return c.Answer(ctx, p.Session, p.Interaction, p.Answer, out)
	case "session.history":
		return c.History(ctx, p.Session, p.History, out)
	case "session.cancel":
		return c.Cancel(ctx, p.Session, out)
	case "session.close":
		return c.CloseSession(ctx, p.Session, out)
	case "session.resume":
		if len(p.Input.Content) > 0 {
			return c.ResumeSessionWithInput(ctx, p.Session, p.Input, out)
		}
		return c.ResumeSession(ctx, p.Session, out)
	}
	return transport.Invalid()
}

func rpcEndpoint(serverURL string) string {
	if strings.HasPrefix(serverURL, "ws://") || strings.HasPrefix(serverURL, "wss://") {
		return serverURL
	}
	endpoint := "ws" + strings.TrimPrefix(serverURL, "http")
	if !strings.HasSuffix(endpoint, "/rpc") {
		endpoint += "/rpc"
	}
	return endpoint
}

// newHTTPClient binds WebSocket JSON-RPC client cleanup to the current test.
func newHTTPClient(t *testing.T, serverURL, token string) *client.Client {
	t.Helper()
	c, err := client.New(rpcEndpoint(serverURL), token, client.Options{WriteTimeout: time.Second, SubscriptionQueueCapacity: 8})
	require.NoError(t, err)
	t.Cleanup(func() {
		if err := c.Close(); err != nil {
			t.Errorf("close RPC client: %v", err)
		}
	})
	return c
}

func withTestWriteTimeout(opts transport.Options) transport.Options {
	if opts.WriteTimeout <= 0 {
		opts.WriteTimeout = time.Second
	}
	return opts
}

type rawRPCResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  json.RawMessage `json:"result"`
	Error   *struct {
		Code    int64           `json:"code"`
		Message string          `json:"message"`
		Data    json.RawMessage `json:"data"`
	} `json:"error"`
}

func rawRPCCall(t *testing.T, endpoint, token, method string, params json.RawMessage) rawRPCResponse {
	t.Helper()
	headers := http.Header{}
	if token != "" {
		headers.Set("Authorization", "Bearer "+token)
	}
	conn, handshake, err := websocket.DefaultDialer.DialContext(ctx(t), endpoint, headers)
	if handshake != nil {
		_ = handshake.Body.Close()
	}
	require.NoError(t, err)
	defer conn.Close()
	require.NoError(t, conn.WriteJSON(map[string]any{"jsonrpc": "2.0", "id": "init", "method": "initialize", "params": map[string]string{"protocolVersion": "1"}}))
	var initialized rawRPCResponse
	require.NoError(t, conn.ReadJSON(&initialized))
	require.Nil(t, initialized.Error)
	require.NoError(t, conn.WriteJSON(map[string]any{"jsonrpc": "2.0", "id": "call", "method": method, "params": params}))
	var response rawRPCResponse
	require.NoError(t, conn.ReadJSON(&response))
	return response
}
