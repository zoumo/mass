package client

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	rpc2 "github.com/sourcegraph/jsonrpc2"

	rpcwire "github.com/zoumo/mass/mass-go/pkg/jsonrpc"
	"github.com/zoumo/mass/mass-go/pkg/runtime"
)

const rpcHandshakeTimeout = 5 * time.Second

// ClientInfo identifies this client during WebSocket initialize.
type ClientInfo struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

// WebSocketClient is the temporary JSON-RPC client; ticket 06 renames it to Client.
type WebSocketClient struct {
	serverURL string
	token     string

	initMu sync.Mutex

	mu          sync.Mutex
	conn        *rpc2.Conn
	stream      *rpcwire.Stream
	cancel      context.CancelFunc
	initialized *rpc2.Conn
	closed      bool
}

// NewWebSocket creates a lazy JSON-RPC client without opening a connection.
func NewWebSocket(serverURL, token string) *WebSocketClient {
	return &WebSocketClient{serverURL: serverURL, token: token}
}

// Initialize performs the complete side-effect-free MASS protocol handshake.
func (c *WebSocketClient) Initialize(ctx context.Context, clientInfo *ClientInfo, out any) error {
	if clientInfo != nil && (clientInfo.Name == "" || clientInfo.Version == "") {
		return invalid()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	c.initMu.Lock()
	defer c.initMu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	conn, stream, err := c.connection(ctx)
	if err != nil {
		return err
	}
	return c.initializeConnection(ctx, conn, stream, clientInfo, out)
}

func (c *WebSocketClient) initializeConnection(ctx context.Context, conn *rpc2.Conn, stream *rpcwire.Stream, clientInfo *ClientInfo, out any) error {
	params := rpcwire.InitializeParams{ProtocolVersion: rpcwire.Version}
	if clientInfo != nil {
		params.ClientInfo = &rpcwire.PeerInfo{Name: clientInfo.Name, Version: clientInfo.Version}
	}
	var raw json.RawMessage
	if err := conn.Call(ctx, "initialize", params, &raw, rpc2.StringID()); err != nil {
		return c.callError(ctx, stream, err)
	}

	var result rpcwire.Initialized
	if err := rpcwire.DecodeStrict(raw, &result); err != nil {
		_ = conn.Close()
		return fmt.Errorf("invalid initialize result: %w", err)
	}
	if result.ProtocolVersion != rpcwire.Version || result.ServerInfo.Name == "" || result.ServerInfo.Version == "" {
		_ = conn.Close()
		return errors.New("invalid initialize result")
	}
	c.mu.Lock()
	c.initialized = conn
	c.mu.Unlock()
	if out == nil {
		return nil
	}
	return rpcwire.DecodeStrict(raw, out)
}

func (c *WebSocketClient) ensureInitialized(ctx context.Context) (*rpc2.Conn, *rpcwire.Stream, error) {
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	c.initMu.Lock()
	defer c.initMu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	conn, stream, err := c.connection(ctx)
	if err != nil {
		return nil, nil, err
	}
	c.mu.Lock()
	ready := c.initialized == conn
	c.mu.Unlock()
	if ready {
		return conn, stream, nil
	}
	if err := c.initializeConnection(ctx, conn, stream, nil, nil); err != nil {
		return nil, nil, err
	}
	return conn, stream, nil
}

func (c *WebSocketClient) call(ctx context.Context, method string, params, out any) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	conn, stream, err := c.ensureInitialized(ctx)
	if err != nil {
		return err
	}
	var raw json.RawMessage
	if err = conn.Call(ctx, method, params, &raw, rpc2.StringID()); err != nil {
		return c.callError(ctx, stream, err)
	}
	if out == nil {
		return nil
	}
	return decodeResult(raw, out)
}

// Close releases the connection and all pending calls.
func (c *WebSocketClient) Close() error {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil
	}
	c.closed = true
	conn, cancel := c.conn, c.cancel
	c.conn, c.stream, c.cancel, c.initialized = nil, nil, nil, nil
	c.mu.Unlock()

	if cancel != nil {
		cancel()
	}
	if conn == nil {
		return nil
	}
	if err := conn.Close(); !errors.Is(err, rpc2.ErrClosed) {
		return err
	}
	return nil
}

func (c *WebSocketClient) connection(ctx context.Context) (*rpc2.Conn, *rpcwire.Stream, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return nil, nil, errors.New("WebSocket client is closed")
	}
	if c.conn != nil {
		select {
		case <-c.conn.DisconnectNotify():
			if profileErr := c.stream.ProtocolError(); profileErr != nil {
				return nil, nil, profileErr
			}
			return nil, nil, errors.New("WebSocket connection is closed")
		default:
			return c.conn, c.stream, nil
		}
	}

	endpoint, err := websocketEndpoint(c.serverURL)
	if err != nil {
		return nil, nil, err
	}
	headers := http.Header{}
	if c.token != "" {
		headers.Set("Authorization", "Bearer "+c.token)
	}
	dialer := websocket.Dialer{
		Proxy:            http.ProxyFromEnvironment,
		HandshakeTimeout: rpcHandshakeTimeout,
	}
	socket, response, err := dialer.DialContext(ctx, endpoint, headers)
	if response != nil {
		_ = response.Body.Close()
	}
	if err != nil {
		if response != nil {
			return nil, nil, fmt.Errorf("WebSocket handshake failed: %s", response.Status)
		}
		return nil, nil, err
	}

	stream := rpcwire.NewStream(socket, rpcwire.RoleClient, 0)
	life, cancel := context.WithCancel(context.Background())
	conn := rpc2.NewConn(life, stream, clientHandler{}, rpc2.SetLogger(rpcLogger{}))
	c.conn, c.stream, c.cancel = conn, stream, cancel
	return conn, stream, nil
}

func (c *WebSocketClient) callError(ctx context.Context, stream *rpcwire.Stream, err error) error {
	if ctxErr := ctx.Err(); ctxErr != nil {
		return ctxErr
	}
	var rpcErr *rpc2.Error
	if errors.As(err, &rpcErr) {
		if rpcErr.Code == rpcwire.CodeServiceError && rpcErr.Data != nil && len(*rpcErr.Data) > 0 {
			var operation runtime.OperationError
			if json.Unmarshal(*rpcErr.Data, &operation) == nil && operation.Code != "" {
				return &operation
			}
		}
		response := &rpcwire.RPCError{Code: rpcErr.Code, Message: rpcErr.Message}
		if rpcErr.Data != nil {
			response.Data = append(response.Data, (*rpcErr.Data)...)
		}
		return response
	}
	if errors.Is(err, rpc2.ErrClosed) {
		if profileErr := stream.ProtocolError(); profileErr != nil {
			return profileErr
		}
	}
	return err
}

type clientHandler struct{}

func (clientHandler) Handle(ctx context.Context, conn *rpc2.Conn, req *rpc2.Request) {
	if req.Notif {
		return
	}
	_ = conn.ReplyWithError(ctx, req.ID, &rpc2.Error{Code: rpc2.CodeMethodNotFound, Message: "Method not found"})
}

func websocketEndpoint(serverURL string) (string, error) {
	parsed, err := url.Parse(serverURL)
	if err != nil || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", errors.New("invalid MASS server URL")
	}
	switch parsed.Scheme {
	case "http":
		parsed.Scheme = "ws"
	case "https":
		parsed.Scheme = "wss"
	case "ws", "wss":
	default:
		return "", errors.New("invalid MASS server URL")
	}
	path := strings.TrimSuffix(parsed.Path, "/")
	switch path {
	case "":
		parsed.Path = "/rpc"
	case "/rpc":
		parsed.Path = path
	default:
		return "", errors.New("MASS server URL must not include a path")
	}
	return parsed.String(), nil
}

type rpcLogger struct{}

func (rpcLogger) Printf(format string, args ...any) {
	slog.Default().Debug(strings.TrimSuffix(fmt.Sprintf(format, args...), "\n"))
}
