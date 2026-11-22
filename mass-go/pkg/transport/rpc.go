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
	"github.com/zoumo/mass/mass-go/pkg/runtime"
)

const rpcHandshakeTimeout = 5 * time.Second

type rpcEndpoint struct {
	life          context.Context
	api           API
	writeTimeout  time.Duration
	serverVersion string
	origins       map[string]struct{}
	telemetry     *rpcTelemetry

	mu             sync.Mutex
	closed         bool
	externalClosed bool
	conns          map[*rpc2.Conn]rpcConnectionEntry
}

type rpcConnectionEntry struct {
	stream     *rpcwire.Stream
	cancel     context.CancelFunc
	connection *rpcConnection
}

func newRPCEndpoint(life context.Context, api API, opts Options) *rpcEndpoint {
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
		api:           api,
		writeTimeout:  opts.WriteTimeout,
		serverVersion: version,
		origins:       origins,
		telemetry:     newRPCTelemetry(opts),
		conns:         map[*rpc2.Conn]rpcConnectionEntry{},
	}
}

func (s *rpcEndpoint) handle(w http.ResponseWriter, r *http.Request) {
	if s.isExternalClosed() {
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
	headers := http.Header{}
	headers.Set(rpcwire.TraceContextHeader, rpcwire.TraceContextFormat)
	socket, err := upgrader.Upgrade(w, r, headers)
	if err != nil {
		return
	}
	stream, err := rpcwire.NewStream(socket, rpcwire.RoleServer, s.writeTimeout)
	if err != nil {
		_ = socket.Close()
		return
	}
	_ = s.attach(stream, true)
}

func (s *rpcEndpoint) attach(stream *rpcwire.Stream, external bool) error {
	ctx, cancel := context.WithCancel(s.life)
	rpcConnection := &rpcConnection{
		api:           s.api,
		serverVersion: s.serverVersion,
		stream:        stream,
		inflight:      map[string]struct{}{},
		endpoint:      s,
		external:      external,
	}
	s.mu.Lock()
	if s.closed || external && s.externalClosed {
		s.mu.Unlock()
		cancel()
		_ = stream.Close()
		return runtime.Unavailable("not_ready")
	}
	// NewConn begins reading immediately. Hold admission until this connection
	// is visible to shutdown, so no request can run outside the drain registry.
	connection := rpc2.NewConn(ctx, stream, &rpcHandler{connection: rpcConnection}, rpc2.SetLogger(rpcLogger{}))
	s.conns[connection] = rpcConnectionEntry{stream: stream, cancel: cancel, connection: rpcConnection}
	s.mu.Unlock()
	go func() {
		<-connection.DisconnectNotify()
		rpcConnection.disconnect()
		cancel()
		_ = rpcConnection.waitDrained(s.life)
		s.untrack(connection)
	}()
	return nil
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

func (s *rpcEndpoint) isExternalClosed() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.closed || s.externalClosed
}

func (s *rpcEndpoint) untrack(conn *rpc2.Conn) {
	s.mu.Lock()
	delete(s.conns, conn)
	s.mu.Unlock()
}

func (s *rpcEndpoint) closeExternalAdmission() {
	s.mu.Lock()
	s.externalClosed = true
	s.mu.Unlock()
}

func (s *rpcEndpoint) closeAdmission() {
	s.mu.Lock()
	s.closed = true
	s.mu.Unlock()
}

func (s *rpcEndpoint) markSubscriptionsForShutdown() {
	cause := &Error{Code: "unavailable", Reason: "server_shutdown", Message: "Server is shutting down"}
	s.mu.Lock()
	connections := make([]*rpcConnection, 0, len(s.conns))
	for _, entry := range s.conns {
		connections = append(connections, entry.connection)
	}
	s.mu.Unlock()
	for _, connection := range connections {
		connection.markSubscriptionForShutdown(cause)
	}
}

func (s *rpcEndpoint) drain(ctx context.Context) error {
	s.mu.Lock()
	connections := make([]*rpcConnection, 0, len(s.conns))
	for _, entry := range s.conns {
		connections = append(connections, entry.connection)
	}
	s.mu.Unlock()
	for _, connection := range connections {
		connection.shutdownSubscription(ctx)
		if err := connection.waitDrained(ctx); err != nil {
			return err
		}
	}
	return nil
}

type rpcConnection struct {
	api           API
	serverVersion string
	stream        *rpcwire.Stream

	// Only the connection's reader changes protocol state.
	initialized bool
	terminal    bool

	mu               sync.Mutex
	inflight         map[string]struct{}
	disconnected     bool
	subscription     *rpcSubscription
	nextSubscription uint64
	drainWaiters     []chan struct{}
	endpoint         *rpcEndpoint
	external         bool
}

func (c *rpcConnection) shutdownSubscription(ctx context.Context) {
	c.mu.Lock()
	subscription := c.subscription
	c.mu.Unlock()
	if subscription == nil {
		return
	}
	ack := make(chan struct{})
	select {
	case subscription.stop <- rpcSubscriptionStop{ack: ack, cause: &Error{Code: "unavailable", Reason: "server_shutdown", Message: "Server is shutting down"}}:
		select {
		case <-ack:
		case <-ctx.Done():
		}
	case <-subscription.done:
	case <-ctx.Done():
	}
}

func (c *rpcConnection) markSubscriptionForShutdown(cause error) {
	c.mu.Lock()
	subscription := c.subscription
	c.mu.Unlock()
	if subscription != nil {
		subscription.markShutdown(cause)
	}
}

func (s *rpcEndpoint) closeAll() {
	s.mu.Lock()
	s.closed = true
	conns := s.conns
	s.conns = map[*rpc2.Conn]rpcConnectionEntry{}
	s.mu.Unlock()
	var closing sync.WaitGroup
	for conn, entry := range conns {
		closing.Go(func() {
			entry.cancel()
			// Interrupt a blocked write before closing the RPC connection.
			_ = entry.stream.Close()
			_ = conn.Close()
		})
	}
	closing.Wait()
}

type rpcHandler struct {
	connection *rpcConnection
}

func (h *rpcHandler) Handle(ctx context.Context, conn *rpc2.Conn, req *rpc2.Request) {
	h.connection.handle(ctx, conn, req)
}

func (c *rpcConnection) handle(ctx context.Context, conn *rpc2.Conn, req *rpc2.Request) {
	if c.terminal {
		return
	}
	if req.Notif {
		slog.Default().Debug("ignored JSON-RPC notification", "method", req.Method)
		return
	}
	var telemetry *rpcTelemetry
	if c.endpoint != nil {
		telemetry = c.endpoint.telemetry
	}
	ctx, finish := telemetry.start(ctx, req)
	key := req.ID.String()
	admitted, unique := c.beginRequest(key)
	if admitted && !unique {
		_ = conn.Close()
		finish()
		return
	}
	complete := func() {
		finish()
		if admitted {
			c.end(key)
		}
	}
	reply := func(response *rpc2.Response, closeAfterWrite bool) bool {
		written := func(err error) {
			recordRPCResponse(ctx, response, err)
			if closeAfterWrite {
				_ = conn.Close()
			}
			complete()
		}
		if err := c.stream.WriteResponseAsync(response, written); err != nil {
			written(err)
			return false
		}
		return true
	}
	dispatch := func(call func()) {
		go func() {
			defer complete()
			call()
		}()
	}
	if !admitted {
		reply(serviceErrorResponse(ctx, req.ID, &Error{Code: "unavailable", Reason: "not_ready", Message: "API admission is closed"}), false)
		return
	}

	if req.Method == "initialize" {
		response, initialize, closeAfterWrite := c.prepareInitialize(req)
		c.terminal = closeAfterWrite
		// Put the success response in send order before permitting dispatch.
		if reply(response, closeAfterWrite) && initialize {
			c.initialized = true
		}
		return
	}
	if !c.initialized {
		reply(rpcErrorResponse(req.ID, rpcwire.CodeProtocolState, "Connection is not initialized", "not_initialized"), false)
		return
	}
	if req.Method == "session.subscribe" {
		ref, err := decodeSessionRefParams(req.Params)
		if err != nil {
			reply(rpcErrorResponse(req.ID, rpc2.CodeInvalidParams, "Invalid params", ""), false)
			return
		}
		subscription, ok := c.reserveSubscription(conn)
		if !ok {
			reply(serviceErrorResponse(ctx, req.ID, subscriptionAlreadyActive()), false)
			return
		}
		dispatch(func() { c.dispatchSubscribe(ctx, conn, req, ref, subscription) })
		return
	}
	if req.Method == "session.unsubscribe" {
		subscriptionID, err := decodeSubscriptionID(req.Params)
		if err != nil {
			reply(rpcErrorResponse(req.ID, rpc2.CodeInvalidParams, "Invalid params", ""), false)
			return
		}
		dispatch(func() { c.dispatchUnsubscribe(ctx, conn, req, subscriptionID) })
		return
	}
	call, known, paramsErr := c.prepareRuntimeCall(req.Method, req.Params)
	if !known {
		call, known, paramsErr = c.prepareSessionCall(req.Method, req.Params)
	}
	if !known {
		reply(rpcErrorResponse(req.ID, rpc2.CodeMethodNotFound, "Method not found", ""), false)
		return
	}
	if paramsErr != nil {
		reply(rpcErrorResponse(req.ID, rpc2.CodeInvalidParams, "Invalid params", ""), false)
		return
	}
	dispatch(func() { c.dispatchCall(ctx, conn, req, call) })
}

// Admit and register under the endpoint lock so closing admission cannot miss
// an operation that has already been allowed to access the Service.
func (c *rpcConnection) beginRequest(id string) (admitted, unique bool) {
	if c.endpoint != nil {
		c.endpoint.mu.Lock()
		defer c.endpoint.mu.Unlock()
		if c.endpoint.closed || c.external && c.endpoint.externalClosed {
			return false, false
		}
	}
	return true, c.begin(id)
}

func (c *rpcConnection) begin(id string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, duplicate := c.inflight[id]; duplicate || c.disconnected {
		return false
	}
	c.inflight[id] = struct{}{}
	return true
}

func (c *rpcConnection) end(id string) {
	c.mu.Lock()
	delete(c.inflight, id)
	c.mu.Unlock()
	c.maybeDrained()
}

func (c *rpcConnection) maybeDrained() {
	c.mu.Lock()
	if !c.drainedLocked() {
		c.mu.Unlock()
		return
	}
	waiters := c.drainWaiters
	c.drainWaiters = nil
	c.mu.Unlock()
	for _, waiter := range waiters {
		close(waiter)
	}
}

func (c *rpcConnection) drainedLocked() bool {
	if len(c.inflight) != 0 {
		return false
	}
	if c.subscription != nil {
		select {
		case <-c.subscription.done:
		default:
			return false
		}
	}
	return true
}

func (c *rpcConnection) waitDrained(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	c.mu.Lock()
	if c.drainedLocked() {
		c.mu.Unlock()
		return nil
	}
	waiter := make(chan struct{})
	c.drainWaiters = append(c.drainWaiters, waiter)
	c.mu.Unlock()

	select {
	case <-waiter:
		return nil
	case <-ctx.Done():
		c.mu.Lock()
		for i, candidate := range c.drainWaiters {
			if candidate == waiter {
				c.drainWaiters = append(c.drainWaiters[:i], c.drainWaiters[i+1:]...)
				break
			}
		}
		c.mu.Unlock()
		return ctx.Err()
	}
}

func (c *rpcConnection) prepareInitialize(req *rpc2.Request) (response *rpc2.Response, initialize, closeAfterWrite bool) {
	if c.initialized {
		return rpcErrorResponse(req.ID, rpcwire.CodeProtocolState, "Connection is already initialized", "already_initialized"), false, false
	}
	params, unsupported := decodeInitialize(req.Params)
	if params == nil && !unsupported {
		return rpcErrorResponse(req.ID, rpc2.CodeInvalidParams, "Invalid params", ""), false, false
	}
	if unsupported {
		return rpcErrorResponse(req.ID, rpcwire.CodeProtocolState, "Unsupported protocol version", "unsupported_protocol_version"), false, true
	}

	response = &rpc2.Response{ID: req.ID}
	if err := response.SetResult(rpcwire.Initialized{
		ProtocolVersion: rpcwire.Version,
		ServerInfo: rpcwire.PeerInfo{
			Name:    rpcwire.ServerName,
			Version: c.serverVersion,
		},
	}); err != nil {
		return internalErrorResponse(req.ID), false, false
	}
	return response, true, false
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

func rpcErrorResponse(id rpc2.ID, code int64, message, reason string) *rpc2.Response {
	rpcErr := &rpc2.Error{Code: code, Message: message}
	if reason != "" {
		data, err := json.Marshal(rpcwire.ProtocolErrorData{Reason: reason})
		if err != nil {
			return internalErrorResponse(id)
		}
		rpcErr.Data = (*json.RawMessage)(&data)
	}
	return &rpc2.Response{ID: id, Error: rpcErr}
}

type rpcLogger struct{}

func (rpcLogger) Printf(format string, args ...any) {
	slog.Default().Debug(strings.TrimSuffix(fmt.Sprintf(format, args...), "\n"))
}
