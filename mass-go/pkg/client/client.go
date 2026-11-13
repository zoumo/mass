package client

import (
	"context"
	"crypto/tls"
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

// Package client is the public JSON-RPC over WebSocket service client.

// ClientInfo identifies this client during WebSocket initialize.
type ClientInfo struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

// Options are required assembly choices for one client.
type Options struct {
	WriteTimeout              time.Duration
	SubscriptionQueueCapacity int
}

// Client owns one ordinary RPC connection and any observations opened through it.
// It is lazy: New performs no network I/O.
type Client struct {
	serverURL                 string
	token                     string
	tlsConfig                 *tls.Config
	writeTimeout              time.Duration
	subscriptionQueueCapacity int

	initMu sync.Mutex

	mu           sync.Mutex
	conn         *rpc2.Conn
	stream       *rpcwire.Stream
	cancel       context.CancelFunc
	initialized  *rpc2.Conn
	closed       bool
	observations map[*Subscription]struct{}
}

// SetTLSClientConfig configures the WebSocket handshake transport before first
// use. It is only meaningful for wss:// endpoints.
func (c *Client) SetTLSClientConfig(config *tls.Config) {
	c.tlsConfig = config
}

// New creates a lazy JSON-RPC client without opening a connection.
func New(serverURL, token string, opts Options) (*Client, error) {
	if opts.WriteTimeout <= 0 {
		return nil, errors.New("client write timeout must be positive")
	}
	if opts.SubscriptionQueueCapacity <= 0 {
		return nil, errors.New("client subscription queue capacity must be positive")
	}
	return &Client{
		serverURL:                 serverURL,
		token:                     token,
		writeTimeout:              opts.WriteTimeout,
		subscriptionQueueCapacity: opts.SubscriptionQueueCapacity,
		observations:              map[*Subscription]struct{}{},
	}, nil
}

// Initialize performs the complete side-effect-free MASS protocol handshake.
func (c *Client) Initialize(ctx context.Context, clientInfo *ClientInfo, out any) error {
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
	return c.initializeConnection(ctx, conn, stream, clientInfo, out, true)
}

func (c *Client) initializeConnection(
	ctx context.Context,
	conn *rpc2.Conn,
	stream *rpcwire.Stream,
	clientInfo *ClientInfo,
	out any,
	owned bool,
) error {
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
	if owned {
		c.mu.Lock()
		c.initialized = conn
		c.mu.Unlock()
	}
	if out == nil {
		return nil
	}
	return rpcwire.DecodeStrict(raw, out)
}

func (c *Client) ensureInitialized(ctx context.Context) (*rpc2.Conn, *rpcwire.Stream, error) {
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
	if err := c.initializeConnection(ctx, conn, stream, nil, nil, true); err != nil {
		return nil, nil, err
	}
	return conn, stream, nil
}

func (c *Client) call(ctx context.Context, method string, params, out any) error {
	return c.callWith(ctx, method, params, out, false)
}

func (c *Client) callWork(ctx context.Context, method string, params, out any) error {
	return c.callWith(ctx, method, params, out, true)
}

func (c *Client) callWith(ctx context.Context, method string, params, out any, work bool) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	conn, stream, err := c.ensureInitialized(ctx)
	if err != nil {
		return err
	}
	waiter, err := conn.DispatchCall(ctx, method, params, rpc2.StringID())
	if err != nil {
		if work {
			return unconfirmedWork()
		}
		return c.callError(ctx, stream, err)
	}
	var raw json.RawMessage
	if err = waiter.Wait(ctx, &raw); err != nil {
		mapped := c.callError(ctx, stream, err)
		if work {
			var rpcErr *rpc2.Error
			if !errors.As(err, &rpcErr) && stream.ProtocolError() == nil {
				return unconfirmedWork()
			}
		}
		return mapped
	}
	if out == nil {
		return nil
	}
	return decodeResult(raw, out)
}

func unconfirmedWork() *runtime.OperationError {
	return &runtime.OperationError{
		Code:    "outcome_unknown",
		Reason:  "operation_unconfirmed",
		Message: "Operation result unconfirmed; inspect state before further action",
	}
}

// Close releases the connection and all pending calls.
func (c *Client) Close() error {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil
	}
	c.closed = true
	conn, cancel := c.conn, c.cancel
	c.conn, c.stream, c.cancel, c.initialized = nil, nil, nil, nil
	observations := make([]*Subscription, 0, len(c.observations))
	for subscription := range c.observations {
		observations = append(observations, subscription)
	}
	c.observations = map[*Subscription]struct{}{}
	c.mu.Unlock()

	if cancel != nil {
		cancel()
	}
	var result error
	if conn != nil {
		if err := conn.Close(); !errors.Is(err, rpc2.ErrClosed) {
			result = errors.Join(result, err)
		}
	}
	for _, subscription := range observations {
		subscription.finish(errors.New("WebSocket client is closed"))
		if err := subscription.closeTransport(); err != nil {
			result = errors.Join(result, err)
		}
	}
	return result
}

func (c *Client) connection(ctx context.Context) (*rpc2.Conn, *rpcwire.Stream, error) {
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

	conn, stream, cancel, err := c.dial(ctx, clientHandler{})
	if err != nil {
		return nil, nil, err
	}
	c.conn, c.stream, c.cancel = conn, stream, cancel
	return conn, stream, nil
}

func (c *Client) dial(ctx context.Context, handler rpc2.Handler) (*rpc2.Conn, *rpcwire.Stream, context.CancelFunc, error) {
	endpoint, err := websocketEndpoint(c.serverURL)
	if err != nil {
		return nil, nil, nil, err
	}
	headers := http.Header{}
	if c.token != "" {
		headers.Set("Authorization", "Bearer "+c.token)
	}
	dialer := websocket.Dialer{
		Proxy:            http.ProxyFromEnvironment,
		HandshakeTimeout: rpcHandshakeTimeout,
		TLSClientConfig:  c.tlsConfig,
	}
	socket, response, err := dialer.DialContext(ctx, endpoint, headers)
	if response != nil {
		_ = response.Body.Close()
	}
	if err != nil {
		if response != nil {
			return nil, nil, nil, fmt.Errorf("WebSocket handshake failed: %s", response.Status)
		}
		return nil, nil, nil, err
	}

	stream, err := rpcwire.NewStream(socket, rpcwire.RoleClient, c.writeTimeout)
	if err != nil {
		_ = socket.Close()
		return nil, nil, nil, err
	}
	life, cancel := context.WithCancel(context.Background())
	conn := rpc2.NewConn(life, stream, handler, rpc2.SetLogger(rpcLogger{}))
	return conn, stream, cancel, nil
}

func (c *Client) callError(ctx context.Context, stream *rpcwire.Stream, err error) error {
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

type clientHandler struct {
	subscription *Subscription
}

func (h clientHandler) Handle(ctx context.Context, conn *rpc2.Conn, req *rpc2.Request) {
	if h.subscription != nil {
		h.subscription.handle(req)
		return
	}
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
		return "", errors.New("MASS server URL must be a ws:// or wss:// endpoint ending in /rpc")
	}
	return parsed.String(), nil
}

type rpcLogger struct{}

func (rpcLogger) Printf(format string, args ...any) {
	slog.Default().Debug(strings.TrimSuffix(fmt.Sprintf(format, args...), "\n"))
}
