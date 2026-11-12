package transport_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/require"

	rpcwire "github.com/zoumo/mass/mass-go/pkg/jsonrpc"
	"github.com/zoumo/mass/mass-go/pkg/transport"
)

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  json.RawMessage `json:"result"`
	Error   *rpcError       `json:"error"`
}

type rpcError struct {
	Code    int64           `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data"`
}

func newRPCServer(t *testing.T, opts transport.Options) (*transport.Server, *httptest.Server) {
	t.Helper()
	if opts.Token == "" && !opts.LocalOnly {
		opts.Token = "test-token"
	}
	server, err := transport.New(nil, opts)
	require.NoError(t, err)
	httpServer := httptest.NewServer(server)
	t.Cleanup(func() {
		server.Shutdown()
		httpServer.Close()
	})
	return server, httpServer
}

func rpcHeaders(token string, origin ...string) http.Header {
	headers := http.Header{}
	headers.Set("Authorization", "Bearer "+token)
	if len(origin) > 0 {
		headers.Set("Origin", origin[0])
	}
	return headers
}

func rpcURL(httpServer *httptest.Server) string {
	return "ws" + strings.TrimPrefix(httpServer.URL, "http") + "/rpc"
}

func dialRPC(t *testing.T, httpServer *httptest.Server, headers http.Header) *websocket.Conn {
	t.Helper()
	conn, response, err := websocket.DefaultDialer.Dial(rpcURL(httpServer), headers)
	if response != nil {
		_ = response.Body.Close()
	}
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })
	return conn
}

func sendText(t *testing.T, conn *websocket.Conn, message string) {
	t.Helper()
	require.NoError(t, conn.WriteMessage(websocket.TextMessage, []byte(message)))
}

func readRPC(t *testing.T, conn *websocket.Conn) rpcResponse {
	t.Helper()
	require.NoError(t, conn.SetReadDeadline(time.Now().Add(3*time.Second)))
	messageType, data, err := conn.ReadMessage()
	require.NoError(t, err)
	require.Equal(t, websocket.TextMessage, messageType)
	var response rpcResponse
	require.NoError(t, json.Unmarshal(data, &response))
	require.Equal(t, rpcwire.EnvelopeVersion, response.JSONRPC)
	return response
}

func requireRPCError(t *testing.T, response rpcResponse, code int64, id, reason string) {
	t.Helper()
	require.NotNil(t, response.Error)
	require.Equal(t, code, response.Error.Code)
	require.Equal(t, id, string(response.ID))
	require.Empty(t, response.Result)
	if reason != "" {
		var data rpcwire.ProtocolErrorData
		require.NoError(t, json.Unmarshal(response.Error.Data, &data))
		require.Equal(t, reason, data.Reason)
	}
}

func initializeMessage(id, version string) string {
	return `{"jsonrpc":"2.0","id":` + id + `,"method":"initialize","params":{"protocolVersion":"` + version + `"}}`
}

func TestRPCAuthenticationAndOrigin(t *testing.T) {
	_, httpServer := newRPCServer(t, transport.Options{
		Token:          "test-token",
		AllowedOrigins: []string{"https://allowed.example"},
	})

	response, err := get(t, httpServer.URL+"/rpc")
	require.NoError(t, err)
	_ = response.Body.Close()
	require.Equal(t, http.StatusUnauthorized, response.StatusCode)

	for _, headers := range []http.Header{rpcHeaders("wrong-token"), rpcHeaders("test-token", "https://denied.example")} {
		conn, handshake, dialErr := websocket.DefaultDialer.Dial(rpcURL(httpServer), headers)
		if conn != nil {
			_ = conn.Close()
		}
		if handshake != nil {
			_ = handshake.Body.Close()
		}
		require.Error(t, dialErr)
		require.NotNil(t, handshake)
		if strings.Contains(headers.Get("Origin"), "denied") {
			require.Equal(t, http.StatusForbidden, handshake.StatusCode)
		} else {
			require.Equal(t, http.StatusUnauthorized, handshake.StatusCode)
		}
	}

	conn := dialRPC(t, httpServer, rpcHeaders("test-token", "https://allowed.example"))
	sendText(t, conn, initializeMessage(`"init"`, rpcwire.Version))
	require.Nil(t, readRPC(t, conn).Error)
}

func TestRPCInitializeLifecycle(t *testing.T) {
	_, httpServer := newRPCServer(t, transport.Options{Token: "test-token", ServerVersion: "test-version"})
	conn := dialRPC(t, httpServer, rpcHeaders("test-token"))

	sendText(t, conn, `{"jsonrpc":"2.0","id":"before","method":"runtime.get","params":{"runtimeRef":"agent"}}`)
	requireRPCError(t, readRPC(t, conn), rpcwire.CodeProtocolState, `"before"`, "not_initialized")

	sendText(t, conn, `{"jsonrpc":"2.0","id":"bad","method":"initialize","params":{}}`)
	requireRPCError(t, readRPC(t, conn), -32602, `"bad"`, "")

	sendText(t, conn, `{"jsonrpc":"2.0","id":"bad-info","method":"initialize","params":{"protocolVersion":"1","clientInfo":{"name":"test"}}}`)
	requireRPCError(t, readRPC(t, conn), -32602, `"bad-info"`, "")

	sendText(t, conn, `{"jsonrpc":"2.0","id":"init","method":"initialize","params":{"protocolVersion":"1","clientInfo":{"name":"test","version":"1"}}}`)
	response := readRPC(t, conn)
	require.Nil(t, response.Error)
	require.Equal(t, `"init"`, string(response.ID))
	var initialized rpcwire.Initialized
	require.NoError(t, json.Unmarshal(response.Result, &initialized))
	require.Equal(t, rpcwire.Version, initialized.ProtocolVersion)
	require.Equal(t, rpcwire.ServerName, initialized.ServerInfo.Name)
	require.Equal(t, "test-version", initialized.ServerInfo.Version)

	sendText(t, conn, initializeMessage(`"again"`, rpcwire.Version))
	requireRPCError(t, readRPC(t, conn), rpcwire.CodeProtocolState, `"again"`, "already_initialized")

	sendText(t, conn, `{"jsonrpc":"2.0","id":"unknown","method":"runtime.missing"}`)
	requireRPCError(t, readRPC(t, conn), -32601, `"unknown"`, "")

	sendText(t, conn, `{"jsonrpc":"2.0","method":"initialize"}`)
	sendText(t, conn, `{"jsonrpc":"2.0","id":"plain","method":"unknown"}`)
	requireRPCError(t, readRPC(t, conn), -32601, `"plain"`, "")
}

func TestRPCUnsupportedVersionClosesConnection(t *testing.T) {
	_, httpServer := newRPCServer(t, transport.Options{Token: "test-token"})
	conn := dialRPC(t, httpServer, rpcHeaders("test-token"))

	sendText(t, conn, initializeMessage(`"init"`, "2"))
	requireRPCError(t, readRPC(t, conn), rpcwire.CodeProtocolState, `"init"`, "unsupported_protocol_version")
	require.NoError(t, conn.SetReadDeadline(time.Now().Add(3*time.Second)))
	_, _, err := conn.ReadMessage()
	require.Error(t, err)
}

func TestRPCStrictEnvelopeErrors(t *testing.T) {
	tests := []struct {
		name    string
		message string
		code    int64
		id      string
	}{
		{name: "malformed JSON", message: `{"jsonrpc":`, code: -32700, id: "null"},
		{name: "batch", message: `[{"jsonrpc":"2.0","id":"a","method":"initialize"}]`, code: -32600, id: "null"},
		{name: "duplicate key", message: `{"jsonrpc":"2.0","id":"a","id":"b","method":"initialize"}`, code: -32600, id: "null"},
		{name: "wrong envelope version", message: `{"jsonrpc":"1.0","id":"a","method":"initialize"}`, code: -32600, id: `"a"`},
		{name: "null id", message: `{"jsonrpc":"2.0","id":null,"method":"initialize"}`, code: -32600, id: "null"},
		{name: "negative id", message: `{"jsonrpc":"2.0","id":-1,"method":"initialize"}`, code: -32600, id: "null"},
		{name: "fraction id", message: `{"jsonrpc":"2.0","id":1.5,"method":"initialize"}`, code: -32600, id: "null"},
		{name: "unsafe id", message: `{"jsonrpc":"2.0","id":9007199254740992,"method":"initialize"}`, code: -32600, id: "null"},
		{name: "positional params", message: `{"jsonrpc":"2.0","id":"a","method":"initialize","params":[]}`, code: -32602, id: `"a"`},
		{name: "null params", message: `{"jsonrpc":"2.0","id":"a","method":"initialize","params":null}`, code: -32602, id: `"a"`},
		{name: "extra field", message: `{"jsonrpc":"2.0","id":"a","method":"initialize","extra":true}`, code: -32600, id: `"a"`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, httpServer := newRPCServer(t, transport.Options{Token: "test-token"})
			conn := dialRPC(t, httpServer, rpcHeaders("test-token"))
			sendText(t, conn, test.message)
			requireRPCError(t, readRPC(t, conn), test.code, test.id, "")
		})
	}
}

func TestRPCStringAndNumericIDs(t *testing.T) {
	for _, id := range []string{`"string-id"`, `0`, `9007199254740991`} {
		t.Run(id, func(t *testing.T) {
			_, httpServer := newRPCServer(t, transport.Options{Token: "test-token"})
			conn := dialRPC(t, httpServer, rpcHeaders("test-token"))
			sendText(t, conn, `{"jsonrpc":"2.0","id":`+id+`,"method":"initialize","params":{"protocolVersion":"1"}}`)
			response := readRPC(t, conn)
			require.Nil(t, response.Error)
			require.Equal(t, id, string(response.ID))
		})
	}
}

func TestRPCNotificationHasNoEffect(t *testing.T) {
	_, httpServer := newRPCServer(t, transport.Options{Token: "test-token"})
	conn := dialRPC(t, httpServer, rpcHeaders("test-token"))

	sendText(t, conn, `{"jsonrpc":"2.0","method":"initialize","params":{"protocolVersion":"1"}}`)
	sendText(t, conn, `{"jsonrpc":"2.0","id":"next","method":"runtime.get"}`)
	requireRPCError(t, readRPC(t, conn), rpcwire.CodeProtocolState, `"next"`, "not_initialized")
}

func TestRPCRejectsBinaryAndInvalidUTF8(t *testing.T) {
	for _, test := range []struct {
		name        string
		messageType int
		message     []byte
		closeCode   int
	}{
		{name: "binary", messageType: websocket.BinaryMessage, message: []byte(`{}`), closeCode: websocket.CloseUnsupportedData},
		{name: "invalid UTF-8", messageType: websocket.TextMessage, message: []byte{0xff}, closeCode: websocket.CloseInvalidFramePayloadData},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, httpServer := newRPCServer(t, transport.Options{Token: "test-token"})
			conn := dialRPC(t, httpServer, rpcHeaders("test-token"))
			require.NoError(t, conn.WriteMessage(test.messageType, test.message))
			require.NoError(t, conn.SetReadDeadline(time.Now().Add(3*time.Second)))
			_, _, err := conn.ReadMessage()
			var closeErr *websocket.CloseError
			require.ErrorAs(t, err, &closeErr)
			require.Equal(t, test.closeCode, closeErr.Code)
		})
	}
}

func TestRPCShutdownClosesConnectionsAndAdmission(t *testing.T) {
	server, httpServer := newRPCServer(t, transport.Options{Token: "test-token"})
	conn := dialRPC(t, httpServer, rpcHeaders("test-token"))
	sendText(t, conn, initializeMessage(`"init"`, rpcwire.Version))
	require.Nil(t, readRPC(t, conn).Error)

	server.Shutdown()
	require.NoError(t, conn.SetReadDeadline(time.Now().Add(3*time.Second)))
	_, _, err := conn.ReadMessage()
	require.Error(t, err)

	response, err := get(t, httpServer.URL+"/livez")
	require.NoError(t, err)
	_ = response.Body.Close()
	require.Equal(t, http.StatusOK, response.StatusCode)
	response, err = get(t, httpServer.URL+"/readyz")
	require.NoError(t, err)
	_ = response.Body.Close()
	require.Equal(t, http.StatusServiceUnavailable, response.StatusCode)

	handshakeConn, handshake, dialErr := websocket.DefaultDialer.Dial(rpcURL(httpServer), rpcHeaders("test-token"))
	if handshakeConn != nil {
		_ = handshakeConn.Close()
	}
	if handshake != nil {
		_ = handshake.Body.Close()
	}
	require.Error(t, dialErr)
	require.NotNil(t, handshake)
	require.Equal(t, http.StatusServiceUnavailable, handshake.StatusCode)
}

func TestRPCWriteDeadlineClosesConnection(t *testing.T) {
	_, httpServer := newRPCServer(t, transport.Options{Token: "test-token", WriteTimeout: time.Nanosecond})
	conn := dialRPC(t, httpServer, rpcHeaders("test-token"))
	sendText(t, conn, initializeMessage(`"init"`, rpcwire.Version))
	require.NoError(t, conn.SetReadDeadline(time.Now().Add(3*time.Second)))
	_, _, err := conn.ReadMessage()
	require.Error(t, err)
}

func TestRPCLocalMode(t *testing.T) {
	_, httpServer := newRPCServer(t, transport.Options{LocalOnly: true})
	conn, response, err := websocket.DefaultDialer.Dial(rpcURL(httpServer), nil)
	if response != nil {
		_ = response.Body.Close()
	}
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })
	sendText(t, conn, initializeMessage(`"local"`, rpcwire.Version))
	require.Nil(t, readRPC(t, conn).Error)
}

func get(t *testing.T, url string) (*http.Response, error) {
	t.Helper()
	request, err := http.NewRequestWithContext(t.Context(), http.MethodGet, url, http.NoBody)
	if err != nil {
		return nil, err
	}
	return http.DefaultClient.Do(request)
}
