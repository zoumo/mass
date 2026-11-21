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
	"strconv"
	"strings"
	"sync"
	"time"
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
	outboxCancel                 context.CancelFunc
	outboxDone                   chan struct{}
	observerLife                 context.Context
	observerTelemetry            *observerTelemetryGateway
	observerTraces               *observerTraceReader
	store                        *Store
	config                       Config
	http                         *http.Server
	listener                     net.Listener
}

func Start(c Config) (*Server, error) {
	return StartWithTelemetry(c, TelemetryOptions{})
}

// StartWithTelemetry starts Core with optional process-owned OTel providers.
func StartWithTelemetry(c Config, options TelemetryOptions) (*Server, error) {
	gateway, err := newObserverTelemetryGateway(c.ObserverOTLPEndpoint)
	if err != nil {
		return nil, err
	}
	traces, err := newObserverTraceReader(c.ObserverTrace)
	if err != nil {
		return nil, err
	}
	store, err := openStore(c, options)
	if err != nil {
		return nil, err
	}
	l, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", c.Listen)
	if err != nil {
		_ = store.Close()
		return nil, problem("unavailable", "cannot bind listen address")
	}
	s := &Server{store: store, config: c, listener: l, observerTelemetry: gateway, observerTraces: traces}
	if c.Execution != nil {
		if _, err = store.db.ExecContext(context.Background(), `UPDATE session_bindings SET state='pending',next_attempt_at='',body=json_set(body,'$.state','pending','$.nextAttemptAt','')
 WHERE state='ready'`); err != nil {
			_ = l.Close()
			_ = store.Close()
			return nil, storageError()
		}
		life, cancel := context.WithCancel(context.Background())
		s.executionCancel = cancel
		s.executionDone = make(chan struct{})
		go s.coordinate(life)
	}
	outboxLife, cancel := context.WithCancel(context.Background())
	s.outboxCancel, s.outboxDone = cancel, make(chan struct{})
	s.observerLife = outboxLife
	go func() {
		defer close(s.outboxDone)
		store.outboxMaintenance(outboxLife)
	}()
	s.http = &http.Server{Handler: http.HandlerFunc(s.serve), ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 30 * time.Second}
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

func (s *Server) Shutdown(ctx context.Context) error {
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
	s.outboxCancel()
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
	select {
	case <-s.outboxDone:
	case <-ctx.Done():
		return ctx.Err()
	}
	return s.store.Close()
}

func (s *Server) serve(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Cross-Origin-Resource-Policy", "same-origin")
	expectedHost := s.listener.Addr().String()
	if local, ok := r.Context().Value(http.LocalAddrContextKey).(net.Addr); ok {
		expectedHost = local.String()
	}
	if r.Host != expectedHost {
		s.fail(w, problem("forbidden", "Host must match the Core endpoint"))
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
	if strings.HasPrefix(r.URL.Path, "/v1/telemetry/") {
		s.observerTelemetry.serve(w, r)
		return
	}
	if r.URL.Path == mcpPath {
		s.serveMCP(w, r)
		return
	}
	if s.store.telemetry.enabled {
		observed, request := s.store.telemetry.startHTTP(w, r)
		defer observed.finishRequest()
		w, r = observed, request
	}
	switch {
	case r.URL.Path == "/v1/channel/messages":
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", "POST")
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		source, ok := s.authenticate(r)
		if !ok {
			s.fail(w, problem("unauthorized", "valid source credential is required"))
			return
		}
		if !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
			s.fail(w, problem("invalid_argument", "Content-Type must be application/json"))
			return
		}
		var request SubmitRequest
		dec := json.NewDecoder(r.Body)
		dec.DisallowUnknownFields()
		if err := dec.Decode(&request); err != nil {
			s.fail(w, problem("invalid_argument", "invalid submitMessage JSON"))
			return
		}
		if err := dec.Decode(new(any)); err != io.EOF {
			s.fail(w, problem("invalid_argument", "exactly one JSON value is required"))
			return
		}
		receipt, err := s.store.Submit(r.Context(), source, request)
		if err != nil {
			s.fail(w, err)
			return
		}
		s.write(w, http.StatusOK, receipt)
	case r.URL.Path == "/v1/channel/outbox" || strings.HasPrefix(r.URL.Path, "/v1/channel/outbox/"):
		s.channelOutbox(w, r)
	case r.URL.Path == "/v1/operator/input-recovery":
		s.recoverInput(w, r)
	case r.URL.Path == "/v1/operator/echo-release":
		s.releaseEcho(w, r)
	case strings.HasPrefix(r.URL.Path, "/v1/operator/bindings/"):
		s.inspectBinding(w, r)
	case strings.HasPrefix(r.URL.Path, "/v1/observer/"):
		if !readOnly(w, r) {
			return
		}
		s.query(w, r)
	case r.URL.Path == "/observer" || strings.HasPrefix(r.URL.Path, "/observer/"):
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
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		w.WriteHeader(http.StatusMethodNotAllowed)
		return false
	}
	return true
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
	if r.URL.Path == mcpUsagePath || r.URL.Path == mcpUsagePath+"/calls" || r.URL.Path == mcpUsagePath+"/metrics" {
		mode := strings.TrimPrefix(r.URL.Path, mcpUsagePath+"/")
		usage, err := s.store.mcpToolUsage(r.Context(), r.URL.Query(), mode)
		if err != nil {
			s.fail(w, err)
			return
		}
		s.write(w, http.StatusOK, usage)
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
	var result any
	var err error
	if detail {
		var raw json.RawMessage
		raw, err = s.store.getRecord(r.Context(), kind, id, nil, nil, r.URL.Query().Get("trace") == "true")
		result = raw
		if err == nil && kind == "issues" && r.Method == http.MethodGet {
			result = s.observeIssue(r.Context(), raw)
		}
	} else {
		limit := 50
		if kind == "router-messages" {
			limit = 20
		}
		if value := r.URL.Query().Get("limit"); value != "" {
			limit, err = strconv.Atoi(value)
			if err != nil {
				s.fail(w, problem("invalid_argument", "limit must be an integer"))
				return
			}
		}
		q := r.URL.Query()
		query := QueryRequest{
			Collection: kind, ThreadKey: q.Get("threadKey"),
			TopicID: q.Get("topicId"), IssueID: q.Get("issueId"), MessageID: q.Get("messageId"), Cursor: q.Get("cursor"), Limit: limit,
			preview:       q.Get("preview") == "true",
			newest:        q.Get("order") == "newest",
			observerTrace: q.Get("trace") == "true",
		}
		if kind == "router-messages" {
			if q.Get("order") != "" && !query.newest {
				s.fail(w, problem("invalid_argument", "router-messages uses newest order"))
				return
			}
			result, err = s.store.observerRouterMessages(r.Context(), query)
		} else {
			result, err = s.store.listScoped(r.Context(), query, nil, nil)
		}
	}
	if err != nil {
		s.fail(w, err)
		return
	}
	s.write(w, 200, result)
}

func (s *Server) fail(w http.ResponseWriter, err error) {
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
	s.write(w, status, map[string]any{"error": e})
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
	// Never return a reusable source credential, even if untrusted text contains it.
	body := string(b)
	var configured []string
	for _, source := range s.config.Sources {
		configured = append(configured, source.token)
	}
	if s.config.Execution != nil {
		configured = append(configured, s.config.Execution.token, s.config.Execution.operatorToken)
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
