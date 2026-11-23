package core

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/zoumo/mass/massd"
)

type Server struct {
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
	massdHTTPToken               string
	store                        *Store
	config                       Config
	http                         *http.Server
	listener                     net.Listener
}

// MassdMountPath is the Core HTTP prefix for an explicitly exposed MASS instance.
const MassdMountPath = "/massd"

func Start(c Config) (*Server, error) {
	return StartWithTelemetry(c, TelemetryOptions{})
}

// StartWithTelemetry starts Core with optional process-owned OTel providers.
func StartWithTelemetry(c Config, options TelemetryOptions) (*Server, error) {
	return StartWithContext(context.Background(), c, options)
}

// StartWithContext starts Core and uses ctx for startup operations.
func StartWithContext(ctx context.Context, c Config, options TelemetryOptions) (_ *Server, result error) {
	var embedded *EmbeddedMassdConfig
	if c.Massd != nil {
		embedded = c.Massd.Embedded
	}
	if embedded != nil {
		if c.Massd.Endpoint != "" || c.Massd.TokenFile != "" {
			return nil, problem("invalid_argument", "embedded MASS requires exclusive MASS configuration")
		}
		if err := embedded.validate(); err != nil {
			return nil, err
		}
	}
	exposureToken, err := c.massdExposureToken()
	if err != nil {
		return nil, err
	}
	gateway, err := newObserverTelemetryGateway(c.ObserverOTLPEndpoint)
	if err != nil {
		return nil, err
	}
	traces, err := newObserverTraceReader(c.ObserverTrace)
	if err != nil {
		return nil, err
	}
	if embedded != nil {
		if err := validateMassdDirectories(c.DataDir, embedded.DataDir); err != nil {
			return nil, err
		}
	}
	store, err := openStore(c, options)
	if err != nil {
		return nil, err
	}
	defer func() {
		if result != nil {
			result = errors.Join(result, store.Close())
		}
	}()
	var instance *massd.Instance
	if embedded != nil {
		instance, err = embedded.start(ctx, options, exposureToken)
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
	s := &Server{store: store, config: c, listener: l, embeddedMassd: instance, observerTelemetry: gateway, observerTraces: traces}
	if exposureToken != "" {
		s.massdHTTP = http.StripPrefix(MassdMountPath, instance.Handler())
		s.massdHTTPToken = exposureToken
	}
	if c.Sessions != nil {
		if _, err = store.db.ExecContext(ctx, `UPDATE session_bindings SET state='pending',next_attempt_at='',body=json_set(body,'$.state','pending','$.nextAttemptAt','')
 WHERE state='ready'`); err != nil {
			return nil, storageError()
		}
		life, cancel := context.WithCancel(context.Background())
		s.executionCancel = cancel
		s.executionDone = make(chan struct{})
		go s.coordinate(life)
	}
	s.observerLife, s.observerCancel = context.WithCancel(context.Background())
	routes := s.newHTTPAPI()
	s.http = &http.Server{Handler: s.guardHTTP(routes.router), ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 30 * time.Second}
	return s, nil
}
func (s *Server) Endpoint() string { return "http://" + s.listener.Addr().String() }
func (s *Server) Serve() error {
	err := s.http.Serve(s.listener)
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

func (s *Server) Shutdown(ctx context.Context) (result error) {
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
		if local, ok := r.Context().Value(http.LocalAddrContextKey).(net.Addr); ok {
			expectedHost = local.String()
		}
		observerAPI := strings.HasPrefix(r.URL.Path, "/v1/observer/")
		observerUI := r.URL.Path == "/observer" || strings.HasPrefix(r.URL.Path, "/observer/")
		allowedObserverHost := (observerAPI || observerUI || isTrustedResourceRead(r.URL.Path)) && isReadMethod(r.Method) && slices.Contains(s.config.ObserverAllowedHosts, r.Host)
		if r.Host != expectedHost && !allowedObserverHost {
			s.fail(w, problem("forbidden", "Host must match the Core endpoint"))
			return
		}
		if s.massdHTTP != nil && strings.HasPrefix(r.URL.Path, MassdMountPath+"/") {
			// MASS owns Origin and bearer checks. The Upgrade uses the original writer.
			s.massdHTTP.ServeHTTP(w, r)
			return
		}
		if origin := r.Header.Get("Origin"); origin != "" && origin != "http://"+r.Host {
			s.fail(w, problem("forbidden", "foreign Origin is not allowed"))
			return
		}
		if site := r.Header.Get("Sec-Fetch-Site"); site != "" && site != "same-origin" && site != "none" {
			s.fail(w, problem("forbidden", "cross-site requests are not allowed"))
			return
		}
		next.ServeHTTP(w, r)
	})
}

// serveLegacy serves callers that have yet to switch to the resource routes.
func (s *Server) serveLegacy(w http.ResponseWriter, r *http.Request) {
	if strings.HasPrefix(r.URL.Path, "/v1/telemetry/") {
		s.observerTelemetry.serve(w, r)
		return
	}
	observerAPI := strings.HasPrefix(r.URL.Path, "/v1/observer/")
	observerUI := r.URL.Path == "/observer" || strings.HasPrefix(r.URL.Path, "/observer/")
	if s.store.telemetry.enabled {
		observed, request := s.store.telemetry.startHTTP(w, r)
		defer observed.finishRequest()
		w, r = observed, request
	}
	switch {
	case r.URL.Path == "/v1/channel/messages":
		if r.Method != http.MethodPost && !isReadMethod(r.Method) {
			w.Header().Set("Allow", "GET, HEAD, POST")
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		source, ok := s.authenticate(r)
		if !ok {
			s.fail(w, problem("unauthorized", "valid source credential is required"))
			return
		}
		if isReadMethod(r.Method) {
			ids := r.URL.Query()["sourceMessageId"]
			if len(ids) != 1 || strings.TrimSpace(ids[0]) == "" {
				s.fail(w, problem("invalid_argument", "one nonblank sourceMessageId is required"))
				return
			}
			receipt, err := s.store.channelMessageReceipt(r.Context(), source, ids[0])
			if err != nil {
				s.fail(w, err)
				return
			}
			s.write(w, http.StatusOK, receipt)
			return
		}
		if !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
			s.fail(w, problem("invalid_argument", "Content-Type must be application/json"))
			return
		}
		receipt, err := s.store.submitChannelMessage(r.Context(), source, r.Body)
		if err != nil {
			s.fail(w, err)
			return
		}
		s.write(w, http.StatusOK, receipt)
	case r.URL.Path == "/v1/channel/outbox" || strings.HasPrefix(r.URL.Path, "/v1/channel/outbox/"):
		s.channelOutbox(w, r)
	case r.URL.Path == "/v1/operator/input-recovery":
		s.recoverInput(w, r)
	case strings.HasPrefix(r.URL.Path, "/v1/operator/session-bindings/"):
		s.inspectBinding(w, r)
	case observerAPI:
		if !readOnly(w, r) {
			return
		}
		s.query(w, r)
	case observerUI:
		if !readOnly(w, r) {
			return
		}
		serveObserver(w, r, s.observerTelemetry != nil)
	default:
		if !s.testDispatch(w, r) {
			s.fail(w, problem("not_found", "route not found"))
		}
	}
}

func readOnly(w http.ResponseWriter, r *http.Request) bool {
	if !isReadMethod(r.Method) {
		w.Header().Set("Allow", "GET, HEAD")
		w.WriteHeader(http.StatusMethodNotAllowed)
		return false
	}
	return true
}

func isReadMethod(method string) bool {
	return method == http.MethodGet || method == http.MethodHead
}

func (s *Server) authenticate(r *http.Request) (Source, bool) {
	header := r.Header.Get("Authorization")
	if !strings.HasPrefix(header, "Bearer ") {
		return Source{}, false
	}
	hash := sha256.Sum256([]byte(strings.TrimPrefix(header, "Bearer ")))
	for _, source := range s.config.Sources {
		if subtle.ConstantTimeCompare(hash[:], source.tokenHash[:]) == 1 {
			return source, true
		}
	}
	return Source{}, false
}

func (s *Server) query(w http.ResponseWriter, r *http.Request) {
	if id, ok := strings.CutPrefix(r.URL.Path, observerTracePath); ok {
		result, err := s.observerTraces.read(r.Context(), id, r.URL.Query())
		if err != nil {
			s.fail(w, err)
			return
		}
		s.write(w, http.StatusOK, result)
		return
	}
	path := strings.TrimPrefix(r.URL.Path, "/v1/observer/")
	if r.URL.Path == observerPerformancePath {
		result, err := s.observerTraces.performance(r.Context(), r.URL.Query())
		if err != nil {
			s.fail(w, err)
			return
		}
		s.write(w, http.StatusOK, result)
		return
	}
	if path == "health" {
		state, err := s.store.observerHealth(r.Context())
		if err != nil {
			s.fail(w, err)
			return
		}
		s.write(w, 200, state)
		return
	}
	if path == "topic-activity" {
		query := r.URL.Query()
		read := s.store.topicActivity
		if query.Get("mode") == "conversation" {
			read = s.store.topicConversation
		}
		page, err := read(r.Context(), query)
		if err != nil {
			s.fail(w, err)
			return
		}
		s.write(w, http.StatusOK, page)
		return
	}
	if id, ok := strings.CutPrefix(path, "session-preview/"); ok {
		s.sessionPreview(w, r, id)
		return
	}
	kind, id, detail := strings.Cut(path, "/")
	if detail {
		s.readCoreRecord(w, r, kind, id)
		return
	}
	s.readCoreCollection(w, r, kind)
}

func (s *Server) readCoreRecord(w http.ResponseWriter, r *http.Request, kind, id string) {
	raw, err := s.coreRecord(r.Context(), kind, id, r.URL.Query().Get("trace") == "true", r.Method == http.MethodGet)
	if err != nil {
		s.fail(w, err)
		return
	}
	s.write(w, http.StatusOK, raw)
}

func (s *Server) coreRecord(ctx context.Context, kind, id string, trace, observe bool) (json.RawMessage, error) {
	raw, err := s.store.getRecord(ctx, kind, id, nil, nil, trace)
	if err == nil && kind == "issues" && observe {
		raw = s.observeIssue(ctx, raw)
	}
	return raw, err
}

func (s *Server) readCoreCollection(w http.ResponseWriter, r *http.Request, kind string) {
	q := r.URL.Query()
	limit := 50
	if kind == "router-messages" {
		limit = 20
	}
	limit, err := coreListLimit(q.Get("limit"), limit)
	if err != nil {
		s.fail(w, err)
		return
	}
	query := QueryRequest{
		Collection: kind, ThreadKey: q.Get("threadKey"),
		TopicID: q.Get("topicId"), IssueID: q.Get("issueId"), MessageID: q.Get("messageId"), Cursor: q.Get("cursor"), Limit: limit,
		preview:       q.Get("preview") == "true",
		newest:        q.Get("order") == "newest",
		observerTrace: q.Get("trace") == "true",
	}
	var page Page
	if kind == "router-messages" {
		if q.Get("order") != "" && !query.newest {
			s.fail(w, problem("invalid_argument", "router-messages uses newest order"))
			return
		}
		page, err = s.store.observerRouterMessages(r.Context(), query)
	} else {
		page, err = s.store.listScoped(r.Context(), query, nil, nil)
	}
	if err != nil {
		s.fail(w, err)
		return
	}
	s.write(w, http.StatusOK, page)
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
	var configured []string
	for _, source := range s.config.Sources {
		configured = append(configured, source.token)
	}
	if s.config.Massd != nil {
		configured = append(configured, s.config.Massd.token)
	}
	configured = append(configured, s.config.operatorToken, s.massdHTTPToken)
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
	rows, queryErr := s.store.db.QueryContext(ctx, "SELECT token FROM session_bindings")
	if queryErr != nil {
		return "", queryErr
	}
	for rows.Next() {
		var token string
		if scanErr := rows.Scan(&token); scanErr != nil {
			_ = rows.Close()
			return "", scanErr
		}
		if token != "" {
			body = strings.ReplaceAll(body, token, "[redacted]")
		}
	}
	queryErr = rows.Err()
	_ = rows.Close()
	if queryErr != nil {
		return "", queryErr
	}
	return body, nil
}
