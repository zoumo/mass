package transport

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	rpc2 "github.com/sourcegraph/jsonrpc2"

	rpcwire "github.com/zoumo/mass/mass-go/pkg/jsonrpc"
)

const rpcHandshakeTimeout = 5 * time.Second

type rpcEndpoint struct {
	life          context.Context
	writeTimeout  time.Duration
	serverVersion string
	origins       map[string]struct{}

	mu     sync.Mutex
	closed bool
	conns  map[*rpc2.Conn]context.CancelFunc
}

func newRPCEndpoint(life context.Context, opts Options) *rpcEndpoint {
	version := opts.ServerVersion
	if version == "" {
		version = "dev"
	}
	origins := make(map[string]struct{}, len(opts.AllowedOrigins))
	for _, origin := range opts.AllowedOrigins {
		origins[origin] = struct{}{}
	}
	return &rpcEndpoint{
		life:          life,
		writeTimeout:  opts.WriteTimeout,
		serverVersion: version,
		origins:       origins,
		conns:         map[*rpc2.Conn]context.CancelFunc{},
	}
}

func (s *rpcEndpoint) handle(w http.ResponseWriter, r *http.Request) {
	if s.isClosed() {
		fail(w, http.StatusServiceUnavailable, &Error{Code: "unavailable", Reason: "not_ready", Message: "API admission is closed"})
		return
	}
	if !s.originAllowed(r) {
		fail(w, http.StatusForbidden, &Error{Code: "permission_denied", Reason: "origin_not_allowed", Message: "Origin not allowed"})
		return
	}
	upgrader := websocket.Upgrader{
		HandshakeTimeout: rpcHandshakeTimeout,
		CheckOrigin:      func(*http.Request) bool { return true },
	}
	socket, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	if s.isClosed() {
		_ = socket.Close()
		return
	}

	ctx, cancel := context.WithCancel(s.life)
	stream := rpcwire.NewStream(socket, rpcwire.RoleServer, s.writeTimeout)
	connection := rpc2.NewConn(ctx, stream, &rpcHandler{connection: &rpcConnection{
		serverVersion: s.serverVersion,
		inflight:      map[string]struct{}{},
	}}, rpc2.SetLogger(rpcLogger{}))
	if !s.track(connection, cancel) {
		cancel()
		_ = connection.Close()
		return
	}
	go func() {
		<-connection.DisconnectNotify()
		cancel()
		s.untrack(connection)
	}()
}

func (s *rpcEndpoint) originAllowed(r *http.Request) bool {
	origins := r.Header.Values("Origin")
	if len(origins) == 0 {
		return true
	}
	if len(origins) != 1 {
		return false
	}
	_, ok := s.origins[origins[0]]
	return ok
}

func (s *rpcEndpoint) isClosed() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.closed
}

func (s *rpcEndpoint) track(conn *rpc2.Conn, cancel context.CancelFunc) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return false
	}
	s.conns[conn] = cancel
	return true
}

func (s *rpcEndpoint) untrack(conn *rpc2.Conn) {
	s.mu.Lock()
	delete(s.conns, conn)
	s.mu.Unlock()
}

func (s *rpcEndpoint) closeAll() {
	s.mu.Lock()
	s.closed = true
	conns := make([]*rpc2.Conn, 0, len(s.conns))
	for conn, cancel := range s.conns {
		conns = append(conns, conn)
		cancel()
	}
	s.conns = map[*rpc2.Conn]context.CancelFunc{}
	s.mu.Unlock()
	for _, conn := range conns {
		_ = conn.Close()
	}
}

type rpcHandler struct {
	connection *rpcConnection
}

func (h *rpcHandler) Handle(ctx context.Context, conn *rpc2.Conn, req *rpc2.Request) {
	h.connection.handle(ctx, conn, req)
}

type rpcConnection struct {
	serverVersion string

	mu          sync.Mutex
	initialized bool
	inflight    map[string]struct{}
}

func (c *rpcConnection) handle(ctx context.Context, conn *rpc2.Conn, req *rpc2.Request) {
	if req.Notif {
		slog.Default().Debug("ignored JSON-RPC notification", "method", req.Method)
		return
	}
	key := req.ID.String()
	if !c.begin(key) {
		_ = conn.Close()
		return
	}
	defer c.end(key)

	if req.Method == "initialize" {
		c.initialize(ctx, conn, req)
		return
	}
	if !c.isInitialized() {
		replyError(ctx, conn, req.ID, rpcwire.CodeProtocolState, "Connection is not initialized", "not_initialized")
		return
	}
	replyError(ctx, conn, req.ID, rpc2.CodeMethodNotFound, "Method not found", "")
}

func (c *rpcConnection) begin(id string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, duplicate := c.inflight[id]; duplicate {
		return false
	}
	c.inflight[id] = struct{}{}
	return true
}

func (c *rpcConnection) end(id string) {
	c.mu.Lock()
	delete(c.inflight, id)
	c.mu.Unlock()
}

func (c *rpcConnection) isInitialized() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.initialized
}

func (c *rpcConnection) initialize(ctx context.Context, conn *rpc2.Conn, req *rpc2.Request) {
	if c.isInitialized() {
		replyError(ctx, conn, req.ID, rpcwire.CodeProtocolState, "Connection is already initialized", "already_initialized")
		return
	}
	params, unsupported := decodeInitialize(req.Params)
	if params == nil && !unsupported {
		replyError(ctx, conn, req.ID, rpc2.CodeInvalidParams, "Invalid params", "")
		return
	}
	if unsupported {
		replyError(ctx, conn, req.ID, rpcwire.CodeProtocolState, "Unsupported protocol version", "unsupported_protocol_version")
		_ = conn.Close()
		return
	}

	response := &rpc2.Response{ID: req.ID}
	if err := response.SetResult(rpcwire.Initialized{
		ProtocolVersion: rpcwire.Version,
		ServerInfo: rpcwire.PeerInfo{
			Name:    rpcwire.ServerName,
			Version: c.serverVersion,
		},
	}); err != nil {
		return
	}
	if err := conn.SendResponse(ctx, response); err != nil {
		return
	}
	c.mu.Lock()
	c.initialized = true
	c.mu.Unlock()
}

func decodeInitialize(raw *json.RawMessage) (*rpcwire.InitializeParams, bool) {
	if raw == nil {
		return nil, false
	}
	data := bytes.TrimSpace(*raw)
	if len(data) == 0 || data[0] != '{' {
		return nil, false
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(data, &fields) != nil {
		return nil, false
	}
	var params rpcwire.InitializeParams
	if rpcwire.DecodeStrict(data, &params) != nil || params.ProtocolVersion == "" {
		return nil, false
	}
	if clientInfo, present := fields["clientInfo"]; present {
		if bytes.Equal(bytes.TrimSpace(clientInfo), []byte("null")) {
			return nil, false
		}
		var info rpcwire.PeerInfo
		if rpcwire.DecodeStrict(clientInfo, &info) != nil || info.Name == "" || info.Version == "" {
			return nil, false
		}
		params.ClientInfo = &info
	}
	if params.ProtocolVersion != rpcwire.Version {
		return &params, true
	}
	return &params, false
}

func replyError(ctx context.Context, conn *rpc2.Conn, id rpc2.ID, code int64, message, reason string) {
	rpcErr := &rpc2.Error{Code: code, Message: message}
	if reason != "" {
		data, err := json.Marshal(rpcwire.ProtocolErrorData{Reason: reason})
		if err != nil {
			return
		}
		rpcErr.Data = (*json.RawMessage)(&data)
	}
	_ = conn.SendResponse(ctx, &rpc2.Response{ID: id, Error: rpcErr})
}

type rpcLogger struct{}

func (rpcLogger) Printf(format string, args ...any) {
	slog.Default().Debug(strings.TrimSuffix(fmt.Sprintf(format, args...), "\n"))
}
