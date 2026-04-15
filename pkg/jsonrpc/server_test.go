package jsonrpc_test

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zoumo/mass/pkg/jsonrpc"
)

// startTestServer creates a TCP listener, registers the given services, and
// starts Serve in a goroutine. Returns the address and a cleanup function.
func startTestServer(t *testing.T, srv *jsonrpc.Server) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() {
		_ = srv.Shutdown(context.Background())
		_ = ln.Close()
	})
	go func() { _ = srv.Serve(ln) }()
	return ln.Addr().String()
}

// dialTestClient connects to addr and returns a client that is closed on cleanup.
func dialTestClient(t *testing.T, addr string, opts ...jsonrpc.ClientOption) *jsonrpc.Client {
	t.Helper()
	client, err := jsonrpc.Dial(context.Background(), "tcp", addr, opts...)
	require.NoError(t, err)
	t.Cleanup(func() { _ = client.Close() })
	return client
}

func TestServer_Dispatch(t *testing.T) {
	srv := jsonrpc.NewServer(slog.Default())
	srv.RegisterService("echo", &jsonrpc.ServiceDesc{
		Methods: map[string]jsonrpc.Method{
			"ping": func(ctx context.Context, unmarshal func(any) error) (any, error) {
				var p struct{ Value string }
				if err := unmarshal(&p); err != nil {
					return nil, jsonrpc.ErrInvalidParams(err.Error())
				}
				return map[string]string{"pong": p.Value}, nil
			},
		},
	})

	addr := startTestServer(t, srv)
	client := dialTestClient(t, addr)

	var result map[string]string
	err := client.Call(context.Background(), "echo/ping", map[string]string{"Value": "hello"}, &result)
	require.NoError(t, err)
	assert.Equal(t, "hello", result["pong"])
}

func TestServer_MethodNotFound(t *testing.T) {
	srv := jsonrpc.NewServer(slog.Default())
	addr := startTestServer(t, srv)
	client := dialTestClient(t, addr)

	var result any
	err := client.Call(context.Background(), "svc/nonexistent", nil, &result)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "-32601")
}

func TestServer_InvalidParams(t *testing.T) {
	srv := jsonrpc.NewServer(slog.Default())
	srv.RegisterService("math", &jsonrpc.ServiceDesc{
		Methods: map[string]jsonrpc.Method{
			"double": func(ctx context.Context, unmarshal func(any) error) (any, error) {
				var p struct{ Value int }
				if err := unmarshal(&p); err != nil {
					return nil, jsonrpc.ErrInvalidParams(err.Error())
				}
				return p.Value * 2, nil
			},
		},
	})

	addr := startTestServer(t, srv)
	client := dialTestClient(t, addr)

	// Send a string where an int is expected — JSON unmarshal will fail.
	var result any
	err := client.Call(context.Background(), "math/double", map[string]string{"Value": "not-a-number"}, &result)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "-32602")
}

func TestServer_RPCError(t *testing.T) {
	const customCode = int64(-32099)
	srv := jsonrpc.NewServer(slog.Default())
	srv.RegisterService("app", &jsonrpc.ServiceDesc{
		Methods: map[string]jsonrpc.Method{
			"fail": func(ctx context.Context, unmarshal func(any) error) (any, error) {
				return nil, &jsonrpc.RPCError{Code: customCode, Message: "custom error"}
			},
		},
	})

	addr := startTestServer(t, srv)
	client := dialTestClient(t, addr)

	var result any
	err := client.Call(context.Background(), "app/fail", nil, &result)
	require.Error(t, err)
	assert.Contains(t, err.Error(), fmt.Sprintf("%d", customCode))
	assert.Contains(t, err.Error(), "custom error")
}

func TestServer_PlainError(t *testing.T) {
	srv := jsonrpc.NewServer(slog.Default())
	srv.RegisterService("app", &jsonrpc.ServiceDesc{
		Methods: map[string]jsonrpc.Method{
			"boom": func(ctx context.Context, unmarshal func(any) error) (any, error) {
				return nil, fmt.Errorf("something went wrong internally")
			},
		},
	})

	addr := startTestServer(t, srv)
	client := dialTestClient(t, addr)

	var result any
	err := client.Call(context.Background(), "app/boom", nil, &result)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "-32603")
}

func TestServer_Interceptor(t *testing.T) {
	var callOrder []string
	var mu sync.Mutex

	record := func(name string) {
		mu.Lock()
		callOrder = append(callOrder, name)
		mu.Unlock()
	}

	makeInterceptor := func(name string) jsonrpc.Interceptor {
		return func(ctx context.Context, unmarshal func(any) error, info *jsonrpc.UnaryServerInfo, method jsonrpc.Method) (any, error) {
			record(name + ":before")
			res, err := method(ctx, unmarshal)
			record(name + ":after")
			return res, err
		}
	}

	srv := jsonrpc.NewServer(slog.Default(),
		jsonrpc.WithInterceptor(makeInterceptor("A")),
		jsonrpc.WithInterceptor(makeInterceptor("B")),
	)
	srv.RegisterService("svc", &jsonrpc.ServiceDesc{
		Methods: map[string]jsonrpc.Method{
			"noop": func(ctx context.Context, unmarshal func(any) error) (any, error) {
				record("handler")
				return "ok", nil
			},
		},
	})

	addr := startTestServer(t, srv)
	client := dialTestClient(t, addr)

	var result string
	err := client.Call(context.Background(), "svc/noop", nil, &result)
	require.NoError(t, err)
	assert.Equal(t, "ok", result)

	mu.Lock()
	defer mu.Unlock()
	// B is outermost (last registered), so order is: B before → A before → handler → A after → B after
	assert.Equal(t, []string{"B:before", "A:before", "handler", "A:after", "B:after"}, callOrder)
}

func TestServer_PeerNotify(t *testing.T) {
	srv := jsonrpc.NewServer(slog.Default())
	srv.RegisterService("sub", &jsonrpc.ServiceDesc{
		Methods: map[string]jsonrpc.Method{
			"subscribe": func(ctx context.Context, unmarshal func(any) error) (any, error) {
				peer := jsonrpc.PeerFromContext(ctx)
				require.NotNil(t, peer)
				// Send a push notification to the client.
				err := peer.Notify(ctx, "sub/event", map[string]string{"msg": "hello"})
				return "subscribed", err
			},
		},
	})

	addr := startTestServer(t, srv)

	notifReceived := make(chan json.RawMessage, 1)
	client := dialTestClient(t, addr, jsonrpc.WithNotificationHandler(
		func(ctx context.Context, method string, params json.RawMessage) {
			if method == "sub/event" {
				notifReceived <- params
			}
		},
	))

	var result string
	err := client.Call(context.Background(), "sub/subscribe", nil, &result)
	require.NoError(t, err)
	assert.Equal(t, "subscribed", result)

	select {
	case params := <-notifReceived:
		var m map[string]string
		require.NoError(t, json.Unmarshal(params, &m))
		assert.Equal(t, "hello", m["msg"])
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for notification")
	}
}

func TestServer_PeerDisconnect(t *testing.T) {
	disconnectDetected := make(chan struct{})

	srv := jsonrpc.NewServer(slog.Default())
	srv.RegisterService("watch", &jsonrpc.ServiceDesc{
		Methods: map[string]jsonrpc.Method{
			"start": func(ctx context.Context, unmarshal func(any) error) (any, error) {
				peer := jsonrpc.PeerFromContext(ctx)
				require.NotNil(t, peer)
				go func() {
					<-peer.DisconnectNotify()
					close(disconnectDetected)
				}()
				return nil, nil
			},
		},
	})

	addr := startTestServer(t, srv)

	// Use a connection we control so we can close it manually.
	nc, err := net.Dial("tcp", addr)
	require.NoError(t, err)
	client := jsonrpc.NewClient(nc)

	var result any
	err = client.Call(context.Background(), "watch/start", nil, &result)
	require.NoError(t, err)

	// Close the client — this closes the underlying connection.
	require.NoError(t, client.Close())

	select {
	case <-disconnectDetected:
		// success
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for disconnect detection")
	}
}
