package jsonrpc

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"sync"

	"github.com/sourcegraph/jsonrpc2"
)

// Method is a handler for a single RPC method.
// The unmarshal callback lets the handler decode params into a typed value;
// the framework calls it and maps decode errors to InvalidParams (-32602).
type Method func(ctx context.Context, unmarshal func(any) error) (any, error)

// ServiceDesc describes a set of RPC methods under a named service.
type ServiceDesc struct {
	Methods map[string]Method
}

// UnaryServerInfo carries metadata about the current RPC call.
type UnaryServerInfo struct {
	FullMethod string
}

// Interceptor is a middleware function wrapping RPC method execution.
// Chain multiple interceptors with WithInterceptor (last registered = outermost).
type Interceptor func(
	ctx context.Context,
	unmarshal func(any) error,
	info *UnaryServerInfo,
	method Method,
) (any, error)

// ServerOption configures a Server.
type ServerOption func(*Server)

// WithInterceptor adds an interceptor to the server.
func WithInterceptor(i Interceptor) ServerOption {
	return func(s *Server) {
		s.interceptors = append(s.interceptors, i)
	}
}

// Server is a transport-agnostic JSON-RPC 2.0 server.
// It dispatches incoming requests to registered services.
type Server struct {
	services     map[string]*ServiceDesc
	interceptors []Interceptor
	logger       *slog.Logger

	mu   sync.Mutex
	done chan struct{}
	once sync.Once
}

// NewServer creates a new Server.
func NewServer(logger *slog.Logger, opts ...ServerOption) *Server {
	s := &Server{
		services: make(map[string]*ServiceDesc),
		logger:   logger,
		done:     make(chan struct{}),
	}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

// RegisterService registers a service's methods with the server.
// The fullMethod for each method is "service/method".
func (s *Server) RegisterService(name string, desc *ServiceDesc) {
	s.services[name] = desc
}

// Serve accepts connections from the provided net.Listener and dispatches
// requests until the listener is closed or Shutdown is called.
func (s *Server) Serve(ln net.Listener) error {
	for {
		conn, err := ln.Accept()
		if err != nil {
			select {
			case <-s.done:
				return nil
			default:
			}
			return fmt.Errorf("jsonrpc: accept: %w", err)
		}
		go s.handleConn(conn)
	}
}

// Shutdown signals the server to stop accepting connections.
// Existing connections are not forcibly closed.
func (s *Server) Shutdown(ctx context.Context) error {
	s.once.Do(func() { close(s.done) })
	return nil
}

func (s *Server) handleConn(nc net.Conn) {
	ctx := context.Background()
	stream := jsonrpc2.NewPlainObjectStream(nc)
	h := jsonrpc2.AsyncHandler(&serverHandler{srv: s})
	c := jsonrpc2.NewConn(ctx, stream, h)
	<-c.DisconnectNotify()
}

type serverHandler struct {
	srv *Server
}

func (h *serverHandler) Handle(ctx context.Context, conn *jsonrpc2.Conn, req *jsonrpc2.Request) {
	if req.Notif {
		return
	}

	peer := newPeer(conn)
	ctx = contextWithPeer(ctx, peer)

	// Parse "service/method" format.
	serviceName, methodName, ok := splitMethod(req.Method)
	if !ok {
		_ = conn.ReplyWithError(ctx, req.ID, &jsonrpc2.Error{
			Code:    jsonrpc2.CodeMethodNotFound,
			Message: fmt.Sprintf("invalid method format: %q", req.Method),
		})
		return
	}

	svc, ok := h.srv.services[serviceName]
	if !ok {
		_ = conn.ReplyWithError(ctx, req.ID, &jsonrpc2.Error{
			Code:    jsonrpc2.CodeMethodNotFound,
			Message: fmt.Sprintf("service not found: %q", serviceName),
		})
		return
	}

	method, ok := svc.Methods[methodName]
	if !ok {
		_ = conn.ReplyWithError(ctx, req.ID, &jsonrpc2.Error{
			Code:    jsonrpc2.CodeMethodNotFound,
			Message: fmt.Sprintf("method not found: %q", req.Method),
		})
		return
	}

	unmarshal := func(dst any) error {
		if req.Params == nil {
			return fmt.Errorf("missing params")
		}
		return json.Unmarshal(*req.Params, dst)
	}

	info := &UnaryServerInfo{FullMethod: req.Method}
	result, err := h.srv.dispatch(ctx, unmarshal, info, method)

	if err != nil {
		rpcErr := toRPCError(err)
		_ = conn.ReplyWithError(ctx, req.ID, &jsonrpc2.Error{
			Code:    rpcErr.Code,
			Message: rpcErr.Message,
		})
		return
	}
	_ = conn.Reply(ctx, req.ID, result)
}

// dispatch runs the method through the interceptor chain.
func (s *Server) dispatch(ctx context.Context, unmarshal func(any) error, info *UnaryServerInfo, method Method) (any, error) {
	if len(s.interceptors) == 0 {
		return method(ctx, unmarshal)
	}

	// Build chain: iterate forward so that each new wrapper encloses the
	// previous one. The last interceptor registered ends up as the outermost
	// layer because it is the final one to wrap the accumulated inner chain.
	chain := method
	for i := range s.interceptors {
		i := i
		next := chain
		chain = func(ctx context.Context, u func(any) error) (any, error) {
			return s.interceptors[i](ctx, u, info, next)
		}
	}
	return chain(ctx, unmarshal)
}

// toRPCError converts a Go error to *RPCError.
// *RPCError is used as-is; others become InternalError.
func toRPCError(err error) *RPCError {
	if rpcErr, ok := err.(*RPCError); ok {
		return rpcErr
	}
	return ErrInternal(err.Error())
}

// splitMethod splits "service/method" into (service, method, true).
// Returns ("", "", false) if the format is invalid.
func splitMethod(fullMethod string) (string, string, bool) {
	for i, c := range fullMethod {
		if c == '/' {
			return fullMethod[:i], fullMethod[i+1:], true
		}
	}
	return "", "", false
}
