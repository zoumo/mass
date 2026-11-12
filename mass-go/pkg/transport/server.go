// Package transport serves the WebSocket JSON-RPC endpoint and health probes.
package transport

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"net"
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	"github.com/zoumo/mass/mass-go/pkg/service"
)

type API interface {
	service.RuntimeService
	service.SessionService
}

// Options selects authenticated instance access or explicit trusted local access.
type Options struct {
	Token          string
	LocalOnly      bool
	AllowedOrigins []string
	WriteTimeout   time.Duration
	ServerVersion  string
}
type Server struct {
	api     API
	options Options
	ready   atomic.Bool
	life    context.Context
	stop    context.CancelFunc
	handler http.Handler
	rpc     *rpcEndpoint
}

func New(api API, opts Options) (*Server, error) {
	if (opts.LocalOnly && opts.Token != "") || (!opts.LocalOnly && opts.Token == "") || strings.ContainsAny(opts.Token, " \t\r\n") || opts.WriteTimeout <= 0 {
		return nil, Invalid()
	}
	ctx, cancel := context.WithCancel(context.Background())
	s := &Server{api: api, options: opts, life: ctx, stop: cancel}
	s.rpc = newRPCEndpoint(ctx, s.api, opts)
	s.ready.Store(true)
	s.handler = s.routes()
	return s, nil
}

// CloseAdmission rejects new RPC connections and marks observations for a
// shutdown terminal cause while leaving established connections available for
// final Service events.
func (s *Server) CloseAdmission() {
	s.ready.Store(false)
	s.rpc.closeAdmission()
	s.rpc.markSubscriptionsForShutdown()
}

// Drain waits, within ctx, for established connections to finish in-flight
// calls and to forward their terminal subscription notifications.
func (s *Server) Drain(ctx context.Context) error { return s.rpc.drain(ctx) }

// DrainAndClose waits for established connections to become idle, then closes
// them. The wait is bounded by ctx.
func (s *Server) DrainAndClose(ctx context.Context) error {
	s.CloseAdmission()
	err := s.rpc.drain(ctx)
	s.rpc.closeAll()
	return err
}

// Shutdown immediately closes admission and owned WebSocket connections.
// Service retains operation ownership.
func (s *Server) Shutdown() {
	s.ready.Store(false)
	s.stop()
	s.rpc.closeAll()
}
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) { s.handler.ServeHTTP(w, r) }

func (s *Server) authorized(r *http.Request) bool {
	if s.options.LocalOnly {
		host, _, err := net.SplitHostPort(r.RemoteAddr)
		return err == nil && net.ParseIP(host).IsLoopback()
	}
	headers := r.Header.Values("Authorization")
	if len(headers) != 1 {
		return false
	}
	parts := strings.Fields(headers[0])
	return len(parts) == 2 && strings.EqualFold(parts[0], "Bearer") && subtle.ConstantTimeCompare([]byte(parts[1]), []byte(s.options.Token)) == 1
}

func (s *Server) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/rpc", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", http.MethodGet)
			fail(w, http.StatusMethodNotAllowed, Invalid())
			return
		}
		s.rpc.handle(w, r)
	})
	for path, state := range map[string]string{"/livez": "alive", "/readyz": "ready"} {
		mux.HandleFunc(path, func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodGet {
				w.Header().Set("Allow", http.MethodGet)
				fail(w, http.StatusMethodNotAllowed, Invalid())
				return
			}
			if path == "/readyz" && !s.ready.Load() {
				fail(w, http.StatusServiceUnavailable, &Error{Code: "unavailable", Reason: "not_ready", Message: "API admission is closed"})
				return
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(map[string]string{"status": state})
		})
	}
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		fail(w, http.StatusNotFound, &Error{Code: "not_found", Reason: "route_not_found", Message: "Route not found"})
	})
	return s.guard(mux)
}

// guard authenticates every route except the health probes and prevents HTTP
// request bodies from being drained on early rejection.
func (s *Server) guard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		probe := r.URL.Path == "/livez" || r.URL.Path == "/readyz"
		if !probe && !s.authorized(r) {
			fail(w, http.StatusUnauthorized, &Error{Code: "unauthenticated", Reason: "invalid_credentials", Message: "Valid instance credentials required"})
			return
		}
		if r.Body != nil && r.Body != http.NoBody {
			defer r.Body.Close()
		}
		next.ServeHTTP(w, r)
	})
}
