package client_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/require"

	"github.com/zoumo/mass/mass-go/pkg/client"
	rpcwire "github.com/zoumo/mass/mass-go/pkg/jsonrpc"
	"github.com/zoumo/mass/mass-go/pkg/transport"
)

func newRPCTransport(t *testing.T) *httptest.Server {
	t.Helper()
	server, err := transport.New(nil, transport.Options{Token: "test-token", ServerVersion: "test-version"})
	require.NoError(t, err)
	httpServer := httptest.NewServer(server)
	t.Cleanup(func() {
		server.Shutdown()
		httpServer.Close()
	})
	return httpServer
}

type initialized struct {
	ProtocolVersion string `json:"protocolVersion"`
	ServerInfo      struct {
		Name    string `json:"name"`
		Version string `json:"version"`
	} `json:"serverInfo"`
}

func TestWebSocketClientInitialize(t *testing.T) {
	httpServer := newRPCTransport(t)
	wsClient := client.NewWebSocket(httpServer.URL, "test-token")
	t.Cleanup(func() { _ = wsClient.Close() })

	var result initialized
	require.NoError(t, wsClient.Initialize(t.Context(), &client.ClientInfo{Name: "test", Version: "1"}, &result))
	require.Equal(t, rpcwire.Version, result.ProtocolVersion)
	require.Equal(t, rpcwire.ServerName, result.ServerInfo.Name)
	require.Equal(t, "test-version", result.ServerInfo.Version)

	err := wsClient.Initialize(t.Context(), nil, nil)
	var rpcErr *rpcwire.RPCError
	require.ErrorAs(t, err, &rpcErr)
	require.Equal(t, rpcwire.CodeProtocolState, rpcErr.Code)
	require.Equal(t, "already_initialized", rpcErr.Reason())
}

func TestWebSocketClientLazyAndAuthentication(t *testing.T) {
	httpServer := newRPCTransport(t)
	lazy := client.NewWebSocket("http://127.0.0.1:1", "test-token")
	t.Cleanup(func() { _ = lazy.Close() })

	unauthorized := client.NewWebSocket(httpServer.URL, "wrong-token")
	t.Cleanup(func() { _ = unauthorized.Close() })
	err := unauthorized.Initialize(t.Context(), nil, nil)
	require.Error(t, err)
	require.Contains(t, err.Error(), "401")
}

func TestWebSocketClientRejectsInvalidClientInfo(t *testing.T) {
	wsClient := client.NewWebSocket("http://127.0.0.1:1", "test-token")
	t.Cleanup(func() { _ = wsClient.Close() })
	err := wsClient.Initialize(t.Context(), &client.ClientInfo{Name: "test"}, nil)
	require.Error(t, err)
}

func TestWebSocketClientCancellationAndCloseWakeWaiter(t *testing.T) {
	httpServer := newHangingWebSocketServer(t)
	wsClient := client.NewWebSocket(httpServer.URL, "test-token")
	t.Cleanup(func() { _ = wsClient.Close() })

	ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	require.ErrorIs(t, wsClient.Initialize(ctx, nil, nil), context.DeadlineExceeded)
	require.Less(t, time.Since(start), 2*time.Second)

	waiting := make(chan error, 1)
	go func() {
		waiting <- wsClient.Initialize(context.Background(), nil, nil)
	}()
	time.Sleep(100 * time.Millisecond)
	require.NoError(t, wsClient.Close())
	select {
	case err := <-waiting:
		require.Error(t, err)
	case <-time.After(2 * time.Second):
		t.Fatal("Close did not wake the pending call")
	}
	require.NoError(t, wsClient.Close())
	require.Error(t, wsClient.Initialize(t.Context(), nil, nil))
}

func TestWebSocketClientRejectsMalformedServerMessage(t *testing.T) {
	httpServer := newFakeWebSocketServer(t, func(conn *websocket.Conn) {
		if _, _, err := conn.ReadMessage(); err != nil {
			return
		}
		_ = conn.WriteMessage(websocket.TextMessage, []byte(`[{"jsonrpc":"2.0","id":"a","result":null}]`))
	})
	wsClient := client.NewWebSocket(httpServer.URL, "")
	t.Cleanup(func() { _ = wsClient.Close() })

	err := wsClient.Initialize(t.Context(), nil, nil)
	var profileErr *rpcwire.ProfileError
	require.ErrorAs(t, err, &profileErr)
	require.Equal(t, int64(-32600), profileErr.Code)
}

func TestWebSocketClientRejectsInvalidInitializeResult(t *testing.T) {
	httpServer := newFakeWebSocketServer(t, func(conn *websocket.Conn) {
		if _, _, err := conn.ReadMessage(); err != nil {
			return
		}
		_ = conn.WriteMessage(websocket.TextMessage, []byte(`{"jsonrpc":"2.0","id":"0","result":{"protocolVersion":"2","serverInfo":{"name":"mass","version":"x"}}}`))
	})
	wsClient := client.NewWebSocket(httpServer.URL, "")
	t.Cleanup(func() { _ = wsClient.Close() })

	err := wsClient.Initialize(t.Context(), nil, nil)
	require.Error(t, err)
	require.Contains(t, err.Error(), "invalid initialize result")
}

func newHangingWebSocketServer(t *testing.T) *httptest.Server {
	t.Helper()
	return newFakeWebSocketServer(t, func(conn *websocket.Conn) {
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				return
			}
		}
	})
}

func newFakeWebSocketServer(t *testing.T, handle func(*websocket.Conn)) *httptest.Server {
	t.Helper()
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/rpc") {
			http.NotFound(w, r)
			return
		}
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		handle(conn)
	}))
	t.Cleanup(server.Close)
	return server
}
