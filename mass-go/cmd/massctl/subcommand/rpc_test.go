package subcommand_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/require"

	rpcwire "github.com/zoumo/mass/mass-go/pkg/jsonrpc"
	"github.com/zoumo/mass/mass-go/pkg/runtime"
)

type rpcTestCall struct {
	ID     json.RawMessage
	Method string
	Params json.RawMessage

	writeMu *sync.Mutex
	conn    *websocket.Conn
	t       *testing.T
}

func (c *rpcTestCall) respond(result any) {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	body := map[string]any{"jsonrpc": "2.0", "id": c.ID, "result": result}
	require.NoError(c.t, c.conn.WriteJSON(body))
}

func (c *rpcTestCall) fail(operation runtime.OperationError) {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	require.NoError(c.t, c.conn.WriteJSON(map[string]any{
		"jsonrpc": "2.0",
		"id":      c.ID,
		"error": map[string]any{
			"code":    rpcwire.CodeServiceError,
			"message": operation.Message,
			"data":    operation,
		},
	}))
}

func (c *rpcTestCall) notify(method string, params any) {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	require.NoError(c.t, c.conn.WriteJSON(map[string]any{
		"jsonrpc": "2.0",
		"method":  method,
		"params":  params,
	}))
}

type rpcTestRequest struct {
	ID     json.RawMessage `json:"id"`
	Method string          `json:"method"`
	Params json.RawMessage `json:"params"`
}

// newRPCService returns a WebSocket JSON-RPC test endpoint. The handler owns
// every non-initialize request and may reply, fail, notify, or intentionally
// leave the call pending.
func newRPCService(t *testing.T, token string, handler func(*rpcTestCall)) string {
	t.Helper()
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/rpc" {
			http.NotFound(w, r)
			return
		}
		if token != "" && r.Header.Get("Authorization") != "Bearer "+token {
			w.Header().Set("WWW-Authenticate", "Bearer")
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		var writeMu sync.Mutex
		for {
			var request rpcTestRequest
			if err := conn.ReadJSON(&request); err != nil {
				return
			}
			if request.Method == "initialize" {
				writeMu.Lock()
				err := conn.WriteJSON(map[string]any{
					"jsonrpc": "2.0",
					"id":      request.ID,
					"result": rpcwire.Initialized{
						ProtocolVersion: rpcwire.Version,
						ServerInfo:      rpcwire.PeerInfo{Name: rpcwire.ServerName, Version: "test"},
					},
				})
				writeMu.Unlock()
				if err != nil {
					return
				}
				continue
			}
			handler(&rpcTestCall{ID: request.ID, Method: request.Method, Params: request.Params, conn: conn, writeMu: &writeMu, t: t})
		}
	}))
	t.Cleanup(server.Close)
	return "ws" + strings.TrimPrefix(server.URL, "http") + "/rpc"
}
