// Package httpserver exposes the experimental Go Runtime over HTTP JSON-RPC/SSE.
// Session names and observation history are process-local, not durable storage.
package httpserver

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"sync"
	"time"

	rt "github.com/zoumo/mass/mass-go/pkg/runtime"
	"github.com/zoumo/mass/mass-go/pkg/runtime/catalog"
)

type Server struct {
	registry *catalog.Catalog
	ctx      context.Context
	cancel   context.CancelFunc
	mu       sync.Mutex
	sessions map[string]*binding
	dirs     map[string]string
}

type binding struct {
	control                sync.Mutex
	mu                     sync.Mutex
	name, runtimeName, cwd string
	worker                 rt.Runtime
	operation              rt.CreationOperation
	session                rt.Session
	history                []json.RawMessage
	bytes                  int
	incomplete             bool
	ended                  bool
	failure                string
	watchers               map[chan json.RawMessage]struct{}
}

func New(registrations []rt.Registration) (*Server, error) {
	ctx, cancel := context.WithCancel(context.Background())
	s := &Server{registry: catalog.New(), ctx: ctx, cancel: cancel, sessions: map[string]*binding{}, dirs: map[string]string{}}
	for _, registration := range registrations {
		if _, err := s.registry.Register(registration); err != nil {
			cancel()
			return nil, err
		}
		s.dirs[registration.Name] = registration.Config.Dir
	}
	// Runtime lifecycle is a separate single-consumer stream; keep it drained so
	// session traffic does not exhaust the Runtime's lifecycle delivery budget.
	for _, snapshot := range s.registry.List() {
		worker, _ := s.registry.Get(snapshot.Name)
		go func() {
			for {
				if _, err := worker.Next(ctx); err != nil {
					return
				}
			}
		}()
	}
	return s, nil
}

func (s *Server) Close() {
	for _, snapshot := range s.registry.List() {
		worker, _ := s.registry.Get(snapshot.Name)
		_ = worker.Stop()
	}
	s.cancel()
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /livez", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]string{"status": "alive", "implementation": "go"})
	})
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]string{"status": "ready", "implementation": "go"})
	})
	mux.HandleFunc("POST /api/v1/agentd/jsonrpc", s.rpc)
	return mux
}

type request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}

func writeJSON(w http.ResponseWriter, value any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(value)
}

func (s *Server) rpc(w http.ResponseWriter, r *http.Request) {
	var req request
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 2<<20))
	if err := dec.Decode(&req); err != nil {
		writeJSON(w, map[string]any{"jsonrpc": "2.0", "id": nil, "error": map[string]any{"code": -32600, "message": "invalid request"}})
		return
	}
	var extra any
	var id any
	if dec.Decode(&extra) != io.EOF || req.JSONRPC != "2.0" || req.Method == "" || (len(req.ID) > 0 && (json.Unmarshal(req.ID, &id) != nil || !validID(id))) {
		writeJSON(w, map[string]any{"jsonrpc": "2.0", "id": nil, "error": map[string]any{"code": -32600, "message": "invalid request"}})
		return
	}
	if len(req.Params) == 0 {
		req.Params = json.RawMessage(`{}`)
	}
	if req.Method == "agentd/session/watch_event" && len(req.ID) > 0 {
		if err := s.watch(w, r, req); err != nil {
			s.reply(w, req, nil, err)
		}
		return
	}
	result, err := s.call(req.Method, req.Params)
	if len(req.ID) == 0 {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	s.reply(w, req, result, err)
}

func validID(id any) bool {
	switch id.(type) {
	case nil, string, float64:
		return true
	}
	return false
}

func (s *Server) reply(w http.ResponseWriter, req request, result any, err error) {
	response := map[string]any{"jsonrpc": "2.0", "id": req.ID}
	if err == nil {
		response["result"] = result
	} else {
		kind, code := "invalid_params", -32602
		switch {
		case errors.Is(err, rt.ErrUnsupported):
			kind = "unsupported_capability"
		case errors.Is(err, rt.ErrConflict):
			kind = "conflict"
		case errors.Is(err, rt.ErrBusy):
			kind = "busy"
		case errors.Is(err, rt.ErrNotFound):
			kind = "not_found"
		case errors.Is(err, rt.ErrBackpressure):
			kind = "backpressure"
		case errors.Is(err, rt.ErrOutcomeUnknown):
			kind = "outcome_unknown"
			code = -32603
		case errors.Is(err, rt.ErrUnavailable):
			kind = "runtime_unavailable"
			code = -32603
		case errors.Is(err, errMethod):
			kind = "method_not_found"
			code = -32601
		}
		// Runtime errors may include private process diagnostics; return safe kinds.
		response["error"] = map[string]any{"code": code, "message": kind, "data": map[string]string{"kind": kind}}
	}
	writeJSON(w, response)
}

var errMethod = errors.New("unknown method")

func decode(raw json.RawMessage, value any) error {
	if len(raw) == 0 || raw[0] != '{' {
		return rt.ErrInvalidInput
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	return d.Decode(value)
}

type selector struct {
	SessionName string `json:"sessionName"`
}

func (s *Server) lookup(raw json.RawMessage) (*binding, error) {
	var p selector
	if err := decode(raw, &p); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	b := s.sessions[p.SessionName]
	if b == nil {
		return nil, rt.ErrNotFound
	}
	return b, nil
}

func (s *Server) call(method string, raw json.RawMessage) (any, error) {
	switch method {
	case "agentd/status":
		return map[string]any{"implementation": "go", "history": "memory", "runtimes": s.registry.List()}, nil
	case "agentd/runtime/types":
		return []map[string]string{{"type": "codex"}, {"type": "claude"}}, nil
	case "agentd/runtime/list":
		result := []any{}
		for _, snap := range s.registry.List() {
			result = append(result, runtimeView(snap))
		}
		return result, nil
	case "agentd/runtime/status":
		var p struct {
			Name string `json:"name"`
		}
		if err := decode(raw, &p); err != nil {
			return nil, err
		}
		worker, err := s.registry.Get(p.Name)
		if err != nil {
			return nil, err
		}
		return runtimeView(worker.Status()), nil
	case "agentd/session/new":
		return s.create(raw)
	case "agentd/session/list":
		s.mu.Lock()
		items := make([]*binding, 0, len(s.sessions))
		for _, b := range s.sessions {
			items = append(items, b)
		}
		s.mu.Unlock()
		sort.Slice(items, func(i, j int) bool { return items[i].name < items[j].name })
		result := []any{}
		for _, b := range items {
			result = append(result, b.view())
		}
		return result, nil
	case "agentd/session/status":
		b, err := s.lookup(raw)
		if err != nil {
			return nil, err
		}
		return b.view(), nil
	case "agentd/session/get_event":
		return s.history(raw)
	case "agentd/session/prompt":
		return s.prompt(raw)
	case "agentd/session/archive", "agentd/session/cancel":
		b, err := s.lookup(raw)
		if err != nil {
			return nil, err
		}
		b.control.Lock()
		defer b.control.Unlock()
		b.mu.Lock()
		session := b.session
		op := b.operation
		b.mu.Unlock()
		if session == nil {
			result, err := b.worker.CancelOperation(s.ctx, op.OperationRef)
			return result, err
		}
		if method == "agentd/session/archive" {
			receipt, err := b.worker.CloseSession(s.ctx, session.Key(), rt.CloseDetach)
			return receipt, err
		}
		target := session.Snapshot().Foreground.OperationRef
		if target == "" {
			return map[string]any{}, nil
		}
		return session.Cancel(s.ctx, target)
	case "agentd/session/resume", "agentd/session/set_config_option":
		return nil, rt.ErrUnsupported
	default:
		return nil, errMethod
	}
}

func runtimeView(snap rt.Snapshot) any {
	return map[string]any{"name": snap.Name, "type": snap.Type, "enabled": true, "status": snap}
}

func (s *Server) create(raw json.RawMessage) (any, error) {
	var p struct {
		SessionName string `json:"sessionName"`
		Spec        struct {
			RuntimeName string `json:"runtimeName"`
			Cwd         string `json:"cwd"`
		} `json:"spec"`
		InitialInput *string `json:"initialInput,omitempty"`
	}
	if err := decode(raw, &p); err != nil {
		return nil, err
	}
	if p.SessionName == "" || len(p.SessionName) > 128 || p.Spec.Cwd != s.dirs[p.Spec.RuntimeName] {
		return nil, rt.ErrInvalidInput
	}
	worker, err := s.registry.Get(p.Spec.RuntimeName)
	if err != nil {
		return nil, err
	}
	b := &binding{name: p.SessionName, runtimeName: p.Spec.RuntimeName, cwd: p.Spec.Cwd, worker: worker, history: []json.RawMessage{}, watchers: map[chan json.RawMessage]struct{}{}}
	// Reserve the name before activation; no HTTP caller owns the native operation.
	s.mu.Lock()
	if s.sessions[b.name] != nil {
		s.mu.Unlock()
		return nil, rt.ErrConflict
	}
	if len(s.sessions) >= 128 {
		s.mu.Unlock()
		return nil, rt.ErrBackpressure
	}
	s.sessions[b.name] = b
	s.mu.Unlock()
	b.mu.Lock()
	if err = worker.Start(s.ctx); err == nil {
		b.operation, err = worker.CreateSession(s.ctx, rt.SessionOptions{InitialInput: p.InitialInput})
	}
	if err != nil {
		b.failure = "creation_failed"
		b.ended = true
		b.mu.Unlock()
		return nil, err
	}
	b.mu.Unlock()
	go s.observe(b)
	return b.view(), nil
}

func (b *binding) view() any {
	b.mu.Lock()
	defer b.mu.Unlock()
	op := b.operation
	if op.OperationRef != "" {
		if current, err := b.worker.GetOperation(op.OperationRef); err == nil {
			op = current
		}
	}
	view := map[string]any{"sessionName": b.name, "spec": map[string]string{"runtimeName": b.runtimeName, "cwd": b.cwd}, "operation": op, "historyMode": "memory", "historyIncomplete": b.incomplete, "connected": false, "lifecycle": op.Phase}
	if b.failure != "" {
		view["failure"] = b.failure
	}
	if op.Key != nil {
		view["sessionId"] = op.Key.SessionID
	}
	if b.session != nil {
		snapshot := b.session.Snapshot()
		view["snapshot"] = snapshot
		view["lifecycle"] = snapshot.Attachment
		view["connected"] = snapshot.Attachment == "attached"
	}
	return view
}

func (s *Server) observe(b *binding) {
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	defer func() {
		b.mu.Lock()
		b.ended = true
		for ch := range b.watchers {
			close(ch)
			delete(b.watchers, ch)
		}
		b.mu.Unlock()
	}()
	for {
		b.mu.Lock()
		op, err := b.worker.GetOperation(b.operation.OperationRef)
		if err == nil {
			b.operation = op
		}
		b.mu.Unlock()
		if err != nil {
			return
		}
		if op.Phase == "opened" && op.Key != nil {
			session, err := b.worker.GetSession(*op.Key)
			if err != nil {
				return
			}
			b.mu.Lock()
			b.session = session
			b.mu.Unlock()
			projector := rt.NewEventProjector(b.name, b.cwd)
			for {
				event, err := session.Next(s.ctx)
				if err != nil {
					if !errors.Is(err, context.Canceled) {
						b.disconnect(projector, session.Key().SessionID, "attachment_unavailable")
					}
					if !errors.Is(err, io.EOF) && !errors.Is(err, context.Canceled) {
						b.mu.Lock()
						b.incomplete = true
						b.failure = "event_stream_failed"
						b.mu.Unlock()
					}
					return
				}
				public, err := projector.Project(event)
				if err != nil {
					b.mu.Lock()
					b.incomplete = true
					b.failure = "event_projection_failed"
					b.mu.Unlock()
					b.disconnect(projector, session.Key().SessionID, "event_projection_failed")
					_ = session.Close()
					return
				}
				if public == nil {
					continue
				}
				if err := b.publish(public); err != nil {
					_ = session.Close()
					return
				}
			}
		}
		if op.Phase == "closed" || op.Phase == "outcome_unknown" {
			return
		}
		select {
		case <-s.ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (b *binding) disconnect(projector *rt.EventProjector, sessionID, reason string) {
	// Transport loss is unknown state, never idle or a complete content fact.
	state, _ := projector.Project(rt.Event{Kind: rt.TurnCompleted, SessionID: sessionID, Origin: rt.EventOrigin{Attribution: "root"}, Work: &rt.WorkPayload{Status: "unknown"}})
	if state != nil {
		_ = b.publish(state)
	}
	_ = b.publish(&rt.PublicEvent{SchemaVersion: 2, SessionName: b.name, SessionID: sessionID, Type: "session.error", Time: time.Now().UTC(), Payload: map[string]string{"reason": reason, "message": "session attachment unavailable"}})
}

// Encode once: history and live subscribers receive the same public fact.
// Previews have no cursor and never consume the retained-history budget.
func (b *binding) publish(event *rt.PublicEvent) error {
	data, err := json.Marshal(event)
	b.mu.Lock()
	defer b.mu.Unlock()
	if err != nil || (event.Seq != 0 && (len(b.history) >= 1024 || b.bytes+len(data) > 4<<20)) {
		b.incomplete = true
		b.failure = "history_limit_reached"
		return rt.ErrIncomplete
	}
	if event.Seq != 0 {
		b.history = append(b.history, json.RawMessage(data))
		b.bytes += len(data)
	}
	for ch := range b.watchers {
		select {
		case ch <- data:
		default:
			close(ch)
			delete(b.watchers, ch)
		}
	}
	return nil
}

func (s *Server) prompt(raw json.RawMessage) (any, error) {
	var p struct {
		SessionName string `json:"sessionName"`
		Prompt      []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"prompt"`
	}
	if err := decode(raw, &p); err != nil {
		return nil, err
	}
	if len(p.Prompt) != 1 || p.Prompt[0].Type != "text" {
		return nil, rt.ErrInvalidInput
	}
	selectorJSON, _ := json.Marshal(selector{p.SessionName})
	b, err := s.lookup(selectorJSON)
	if err != nil {
		return nil, err
	}
	b.control.Lock()
	defer b.control.Unlock()
	b.mu.Lock()
	session := b.session
	op := b.operation
	b.mu.Unlock()
	if session == nil {
		return b.worker.ContinueCreation(s.ctx, op.OperationRef, p.Prompt[0].Text)
	}
	return session.Submit(s.ctx, rt.TextInput(p.Prompt[0].Text))
}

func (s *Server) history(raw json.RawMessage) (any, error) {
	var p struct {
		SessionName string `json:"sessionName"`
		After       uint64 `json:"afterSeq"`
		Limit       *int   `json:"limit"`
	}
	if err := decode(raw, &p); err != nil {
		return nil, err
	}
	limit := 100
	if p.Limit != nil {
		limit = *p.Limit
	}
	if limit < 1 || limit > 1000 {
		return nil, rt.ErrInvalidInput
	}
	selectorJSON, _ := json.Marshal(selector{p.SessionName})
	b, err := s.lookup(selectorJSON)
	if err != nil {
		return nil, err
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	events := []json.RawMessage{}
	next := p.After
	for _, event := range b.history {
		var envelope struct {
			Seq uint64 `json:"seq"`
		}
		_ = json.Unmarshal(event, &envelope)
		if envelope.Seq > p.After {
			events = append(events, event)
			next = envelope.Seq
			if len(events) == limit {
				break
			}
		}
	}
	return map[string]any{"events": events, "nextSeq": next, "historyIncomplete": b.incomplete}, nil
}

func (s *Server) watch(w http.ResponseWriter, r *http.Request, req request) error {
	b, err := s.lookup(req.Params)
	if err != nil {
		return err
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		return rt.ErrUnsupported
	}
	ch := make(chan json.RawMessage, 16)
	b.mu.Lock()
	if b.ended {
		b.mu.Unlock()
		return rt.ErrUnavailable
	}
	if len(b.watchers) >= 16 {
		b.mu.Unlock()
		return rt.ErrBackpressure
	}
	b.watchers[ch] = struct{}{}
	b.mu.Unlock()
	defer func() { b.mu.Lock(); delete(b.watchers, ch); b.mu.Unlock() }()
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(200)
	flusher.Flush()
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case event, ok := <-ch:
			if !ok {
				return nil
			}
			_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(10 * time.Second))
			if _, err := fmt.Fprintf(w, "data: %s\n\n", event); err != nil {
				return nil
			}
			flusher.Flush()
		case <-ticker.C:
			_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(10 * time.Second))
			if _, err := fmt.Fprint(w, ": keepalive\n\n"); err != nil {
				return nil
			}
			flusher.Flush()
		case <-r.Context().Done():
			return nil
		case <-s.ctx.Done():
			return nil
		}
	}
}
