package core

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/zoumo/mass/massd"
	rpcwire "github.com/zoumo/mass/massd/pkg/jsonrpc"
)

type Server struct {
	admitting                    atomic.Bool
	bindingOperations            sync.Map
	sessionInputs                sync.Map
	internalEventDispatchRetries sync.Map
	runtimeOperations            sync.RWMutex
	issueWatches                 sync.Map
	issueWatchGroup              sync.WaitGroup
	executionCancel              context.CancelFunc
	executionDone                chan struct{}
	observerCancel               context.CancelFunc
	observerLife                 context.Context
	observerTelemetry            *observerTelemetryGateway
	observerTraces               *observerTraceReader
	embeddedMassd                *massd.Instance
	massdHTTP                    http.Handler
	credentials                  instanceCredentials
	store                        *Store
	config                       Config
	http                         *http.Server
	listener                     net.Listener
	listenAuthority              string
}

// MassdMountPath is the Core HTTP prefix for the embedded MASS instance.
const MassdMountPath = "/apis/massd"

// StartWithContext starts Core and uses ctx for startup operations.
func StartWithContext(ctx context.Context, c Config, options TelemetryOptions) (_ *Server, result error) {
	if c.Massd != nil {
		external := *c.Massd
		if err := external.validateEndpoint(); err != nil {
			return nil, err
		}
		if err := external.loadToken(); err != nil {
			return nil, err
		}
		c.Massd = &external
	}
	gateway, err := newObserverTelemetryGateway(c.WebOTLPEndpoint)
	if err != nil {
		return nil, err
	}
	traces, err := newObserverTraceReader(c.OpenObserve)
	if err != nil {
		return nil, err
	}
	store, err := OpenStore(c, options)
	if err != nil {
		return nil, err
	}
	defer func() {
		if result != nil {
			result = errors.Join(result, store.Close())
		}
	}()
	credentials, err := c.loadCredentials(true)
	if err != nil {
		return nil, err
	}
	var instance *massd.Instance
	if c.Massd == nil {
		instance, err = startEmbeddedMassd(ctx, c.DataDir, options, credentials.adminToken)
		if err != nil {
			return nil, err
		}
		defer func() {
			if result != nil {
				result = errors.Join(result, instance.Shutdown(context.Background()))
			}
		}()
	}
	if err := c.prepareMCPFiles(); err != nil {
		return nil, err
	}
	l, err := (&net.ListenConfig{}).Listen(ctx, "tcp", c.Listen)
	if err != nil {
		return nil, problem("unavailable", "cannot bind listen address")
	}
	defer func() {
		if result != nil {
			result = errors.Join(result, l.Close())
		}
	}()
	host, _, _ := net.SplitHostPort(c.Listen)
	_, port, _ := net.SplitHostPort(l.Addr().String())
	s := &Server{store: store, config: c, listener: l, embeddedMassd: instance, credentials: credentials, observerTelemetry: gateway, observerTraces: traces}
	if host != "" && net.ParseIP(host) == nil {
		s.listenAuthority = net.JoinHostPort(host, port)
	}
	if instance != nil {
		s.massdHTTP = http.StripPrefix(MassdMountPath, instance.Handler())
	}
	if c.Sessions != nil {
		if err := store.resetReadyBindings(ctx); err != nil {
			return nil, err
		}
	}
	life, cancel := context.WithCancel(context.Background())
	s.executionCancel = cancel
	s.executionDone = make(chan struct{})
	if c.Sessions != nil {
		go s.coordinate(life)
	} else {
		go func() {
			defer close(s.executionDone)
			store.scanScheduledTasks(life)
		}()
	}
	s.observerLife, s.observerCancel = context.WithCancel(context.Background())
	routes := s.newHTTPAPI()
	s.http = &http.Server{Handler: s.guardHTTP(routes.router), ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 30 * time.Second}
	s.admitting.Store(true)
	return s, nil
}

func (s *Server) Endpoint() string {
	endpoint := url.URL{Scheme: "http", Host: s.listener.Addr().String()}
	return endpoint.String()
}

func (s *Server) Serve() error {
	err := s.http.Serve(s.listener)
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

func (s *Server) Shutdown(ctx context.Context) (result error) {
	s.admitting.Store(false)
	if s.embeddedMassd != nil {
		// Stop admitting WebSocket work while Core drains its own calls.
		s.embeddedMassd.Handler().CloseExternalAdmission()
		defer func() {
			if result != nil {
				s.embeddedMassd.Handler().Shutdown()
			}
		}()
	}
	if s.observerTraces != nil {
		defer s.observerTraces.client.CloseIdleConnections()
	}
	if s.observerTelemetry != nil {
		defer s.observerTelemetry.client.CloseIdleConnections()
	}
	// Shutdown closes admission before waiting for active requests. A timeout never
	// closes SQLite underneath an active transaction; the process exits without a false receipt.
	if s.executionCancel != nil {
		s.executionCancel()
	}
	s.observerCancel()
	err := s.http.Shutdown(ctx)
	if err != nil {
		_ = s.http.Close()
		return err
	}
	if s.executionDone != nil {
		select {
		case <-s.executionDone:
		case <-ctx.Done():
			return errors.Join(err, ctx.Err())
		}
	}
	if s.embeddedMassd == nil {
		return s.store.Close()
	}
	return errors.Join(s.embeddedMassd.Shutdown(ctx), s.store.Close())
}

func (s *Server) guardHTTP(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Cross-Origin-Resource-Policy", "same-origin")
		expectedHost := s.listener.Addr().String()
		if local, ok := r.Context().Value(http.LocalAddrContextKey).(*net.TCPAddr); ok {
			expectedHost = net.JoinHostPort(local.IP.String(), strconv.Itoa(local.Port))
		}
		allowedReadHost := isTrustedResourceRead(r.URL.Path) && isReadMethod(r.Method) && slices.Contains(s.config.ReadAllowedHosts, r.Host)
		operatorMessageList := r.URL.Path == triggerMessagesPath && hasOperatorMessageFilter(r.URL.Query())
		directHost := matchesCoreHost(r.Host, expectedHost) || matchesCoreHost(r.Host, s.listenAuthority)
		if !directHost && (!allowedReadHost || operatorMessageList) {
			s.fail(w, problem("forbidden", "Host must match the Core endpoint"))
			return
		}
		if site := r.Header.Get("Sec-Fetch-Site"); site != "" && site != "same-origin" && site != "none" {
			s.fail(w, problem("forbidden", "cross-site requests are not allowed"))
			return
		}
		if s.massdHTTP != nil && r.URL.Path == MassdMountPath+rpcwire.WebSocketPath {
			// MASS owns WebSocket Origin and bearer checks.
			next.ServeHTTP(w, r)
			return
		}
		if origin := r.Header.Get("Origin"); origin != "" && origin != "http://"+r.Host {
			s.fail(w, problem("forbidden", "foreign Origin is not allowed"))
			return
		}
		next.ServeHTTP(w, r)
	})
}

func matchesCoreHost(host, authority string) bool {
	return authority != "" && (strings.EqualFold(host, authority) || strings.EqualFold(host+":80", authority))
}

func isReadMethod(method string) bool {
	return method == http.MethodGet || method == http.MethodHead
}

func (s *Server) coreRecord(ctx context.Context, kind, id string, trace, observe bool) (json.RawMessage, error) {
	raw, err := s.store.getRecord(ctx, kind, id, nil, nil, trace)
	if err == nil && kind == "issues" && observe {
		raw = s.observeIssue(ctx, raw)
	}
	return raw, err
}

func (s *Server) fail(w http.ResponseWriter, err error) {
	status, body := coreFailure(err)
	s.write(w, status, body)
}

func coreFailure(err error) (int, coreHTTPError) {
	var e *Error
	if !errors.As(err, &e) {
		e = problem("unavailable", "operation failed")
	}
	status := http.StatusBadRequest
	switch e.Code {
	case "unauthorized":
		status = 401
	case "forbidden":
		status = 403
	case "not_found":
		status = 404
	case "conflict", "failed_precondition", "unsupported", "stale_binding":
		status = 409
	case "unavailable":
		status = 503
	}
	return status, coreHTTPError{Error: e}
}

func (s *Server) write(w http.ResponseWriter, status int, v any) {
	body, err := s.responseJSON(context.Background(), v)
	if err != nil {
		http.Error(w, "response encoding failed", http.StatusServiceUnavailable)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = io.WriteString(w, body+"\n")
}

func (s *Server) responseJSON(ctx context.Context, v any) (string, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	// Never return a reusable credential, even if untrusted text contains it.
	body := string(b)
	configured := []string{s.credentials.triggerToken, s.credentials.adminToken}
	if s.config.Massd != nil {
		configured = append(configured, s.config.Massd.token)
	}
	for _, token := range configured {
		if token == "" {
			continue
		}
		encoded, err := json.Marshal(token)
		if err != nil {
			return "", err
		}
		needle := string(encoded[1 : len(encoded)-1])
		// MCP tool text includes an escaped copy of the structured JSON.
		if needle != token {
			doubleEncoded, err := json.Marshal(needle)
			if err != nil {
				return "", err
			}
			body = strings.ReplaceAll(body, string(doubleEncoded[1:len(doubleEncoded)-1]), "[redacted]")
		}
		body = strings.ReplaceAll(body, needle, "[redacted]")
	}
	roleTokens, err := s.store.bindingTokens(ctx)
	if err != nil {
		return "", err
	}
	for _, token := range roleTokens {
		if token != "" {
			body = strings.ReplaceAll(body, token, "[redacted]")
		}
	}
	return body, nil
}
