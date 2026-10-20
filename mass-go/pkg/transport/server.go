// Package transport maps the HTTP v1 contract to application services.
package transport

import (
	"context"
	"crypto/subtle"
	"encoding/base64"
	"mime"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/zoumo/mass/mass-go/pkg/events"
	"github.com/zoumo/mass/mass-go/pkg/resource"
	"github.com/zoumo/mass/mass-go/pkg/runtime"
	"github.com/zoumo/mass/mass-go/pkg/service"
	"github.com/zoumo/mass/mass-go/pkg/store"
)

type API interface {
	service.RuntimeService
	service.SessionService
}

// Options selects either authenticated instance access or explicit trusted local access.
type Options struct {
	Token     string
	LocalOnly bool
}
type Server struct {
	api     API
	options Options
	ready   atomic.Bool
	life    context.Context
	stop    context.CancelFunc
}

func New(api API, opts Options) (*Server, error) {
	if (opts.LocalOnly && opts.Token != "") || (!opts.LocalOnly && opts.Token == "") || strings.ContainsAny(opts.Token, " \t\r\n") {
		return nil, Invalid()
	}
	ctx, cancel := context.WithCancel(context.Background())
	s := &Server{api: api, options: opts, life: ctx, stop: cancel}
	s.ready.Store(true)
	return s, nil
}

// Shutdown closes admission and observation only. Service retains all operation ownership.
func (s *Server) Shutdown() { s.ready.Store(false); s.stop() }

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	// Early rejections may leave an unread body; close it under an expired read
	// deadline so net/http cannot block draining it after this handler returns.
	if hasBody(r) {
		defer func() { _ = http.NewResponseController(w).SetReadDeadline(time.Now()); _ = r.Body.Close() }()
	}
	if r.URL.Path == "/livez" || r.URL.Path == "/readyz" {
		if r.Method != http.MethodGet {
			methodError(w, "GET")
			return
		}
		if r.URL.RawQuery != "" || hasBody(r) {
			fail(w, 400, Invalid())
			return
		}
		if r.URL.Path == "/readyz" && !s.ready.Load() {
			fail(w, 503, &Error{Code: "unavailable", Reason: "not_ready", Message: "API admission is closed"})
			return
		}
		state := "alive"
		if r.URL.Path == "/readyz" {
			state = "ready"
		}
		respond(w, 200, map[string]string{"status": state}, nil)
		return
	}
	if !s.authorized(r) {
		w.Header().Set("WWW-Authenticate", "Bearer")
		fail(w, 401, &Error{Code: "unauthenticated", Reason: "invalid_credentials", Message: "Valid instance credentials required"})
		return
	}
	if !s.ready.Load() {
		fail(w, 503, &Error{Code: "unavailable", Reason: "not_ready", Message: "API admission is closed"})
		return
	}
	if _, ok := r.Header["Idempotency-Key"]; ok {
		fail(w, 400, Invalid())
		return
	}
	route, ok := matchRoute(r.URL.Path)
	if !ok {
		fail(w, 404, &Error{Code: "not_found", Reason: "route_not_found", Message: "Route not found"})
		return
	}
	allowed := route.method
	resolveMethod(&route, r.Method)
	if r.Method != route.method {
		methodError(w, allowed)
		return
	}
	if route.err != nil {
		fail(w, 400, PublicError(route.err))
		return
	}
	media := "application/json"
	if route.op == "session.observe" {
		media = "text/event-stream"
	}
	if !accepts(r.Header.Get("Accept"), media) {
		fail(w, 406, Invalid())
		return
	}
	if (r.Method == http.MethodGet || r.Method == http.MethodDelete) && hasBody(r) {
		fail(w, 400, Invalid())
		return
	}
	query, err := parseQuery(r.URL.RawQuery, route.op)
	if err != nil {
		respond(w, 0, nil, err)
		return
	}
	if err = r.Context().Err(); err != nil {
		fail(w, 504, &Error{Code: "deadline_exceeded", Reason: "request_expired", Message: "Request expired before dispatch"})
		return
	}
	// Request cancellation ends only observation. An admitted mutation remains owned by Service.
	ctx := context.WithoutCancel(r.Context())
	var value any
	status := 200
	switch route.op {
	case "runtime.create":
		var input runtimeCreate
		if err = decodeBody(w, r, &input); err == nil {
			var v resource.Runtime
			v, err = s.api.CreateRuntime(resource.Runtime{Metadata: resource.Metadata{Name: input.Metadata.Name, Namespace: input.Metadata.Namespace}, Spec: input.Spec})
			value = v
			if err == nil {
				w.Header().Set("Location", "/v1/runtimes/"+url.PathEscape(v.Metadata.Name))
				status = 201
			}
		}
	case "runtime.list":
		value, err = s.api.ListRuntimes(resource.RuntimeQuery{Type: query.Get("type"), State: query.Get("state")})
	case "runtime.get":
		value, err = s.api.GetRuntime(route.name)
	case "runtime.update":
		var spec resource.RuntimeSpec
		if err = decodeBody(w, r, &spec); err == nil {
			value, err = s.api.UpdateRuntime(route.name, spec)
		}
	case "runtime.start", "runtime.stop", "runtime.delete":
		if err = emptyBody(w, r); err == nil {
			switch route.op {
			case "runtime.start":
				value, err = s.api.StartRuntime(ctx, route.name)
			case "runtime.stop":
				value, err = s.api.StopRuntime(ctx, route.name)
			case "runtime.delete":
				err = s.api.DeleteRuntime(route.name)
				status = 204
			}
		}
	case "session.create":
		var input sessionCreate
		if err = decodeBody(w, r, &input); err == nil {
			var v resource.Session
			v, err = s.api.CreateSession(ctx, resource.Session{Metadata: resource.Metadata{Name: input.Metadata.Name, Namespace: input.Metadata.Namespace}, Spec: input.Spec})
			value = v
			if err == nil {
				status = 201
				w.Header().Set("Location", "/v1/namespaces/"+url.PathEscape(v.Metadata.Namespace)+"/sessions/"+url.PathEscape(v.Metadata.Name))
			}
		}
	case "session.list":
		value, err = s.api.ListSessions(resource.SessionQuery{Namespace: query.Get("namespace"), RuntimeRef: query.Get("runtimeRef"), State: events.SessionState(query.Get("state")), After: query.Get("after"), Limit: queryLimit(query)})
	case "session.get":
		value, err = s.api.GetSession(route.ref)
	case "session.history":
		value, err = s.api.ListHistoryEvents(route.ref, store.HistoryQuery{After: query.Get("after"), Limit: queryLimit(query), Types: query["types"]})
	case "session.input":
		var input runtime.Input
		if err = decodeBody(w, r, &input); err == nil {
			value, err = s.api.SendPrompt(ctx, route.ref, input)
		}
	case "session.answer":
		var answer any
		if err = decodeBody(w, r, &answer); err == nil {
			value, err = s.api.AnswerInteraction(ctx, route.ref, route.interaction, answer)
		}
	case "session.archive", "session.resume", "session.cancel":
		if err = emptyBody(w, r); err == nil {
			switch route.op {
			case "session.archive":
				value, err = s.api.ArchiveSession(ctx, route.ref)
			case "session.resume":
				value, err = s.api.ResumeSession(ctx, route.ref)
			case "session.cancel":
				value, err = s.api.Cancel(ctx, route.ref)
			}
		}
	case "session.observe":
		if r.Header.Get("Last-Event-ID") != "" {
			fail(w, 400, Invalid())
			return
		}
		s.observe(w, r, route.ref)
		return
	}
	respond(w, status, value, err)
}

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

func hasBody(r *http.Request) bool {
	return r.Body != nil && r.Body != http.NoBody && (r.ContentLength != 0 || len(r.TransferEncoding) > 0)
}

func accepts(header, media string) bool {
	if header == "" {
		return true
	}
	best := -1
	quality := 0.0
	for part := range strings.SplitSeq(header, ",") {
		kind, params, err := mime.ParseMediaType(strings.TrimSpace(part))
		if err != nil {
			continue
		}
		q := 1.0
		if raw, ok := params["q"]; ok {
			q, err = strconv.ParseFloat(raw, 64)
			if err != nil || q < 0 || q > 1 {
				continue
			}
		}
		specificity := -1
		switch {
		case kind == media:
			specificity = 2
		case kind == strings.Split(media, "/")[0]+"/*":
			specificity = 1
		case kind == "*/*":
			specificity = 0
		}
		if specificity > best {
			best = specificity
			quality = q
		}
	}
	return best >= 0 && quality > 0
}

type route struct {
	method, op, name, interaction string
	ref                           resource.SessionRef
	err                           error
}

var (
	resourceName  = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?([.][a-z0-9]([-a-z0-9]*[a-z0-9])?)*$`)
	namespaceName = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`)
)

func validName(v string) bool      { return len(v) > 0 && len(v) <= 253 && resourceName.MatchString(v) }
func validNamespace(v string) bool { return len(v) > 0 && len(v) <= 63 && namespaceName.MatchString(v) }
func decodeRef(v string) (string, error) {
	b, e := base64.RawURLEncoding.Strict().DecodeString(v)
	if e != nil || len(b) == 0 || !utf8.Valid(b) || base64.RawURLEncoding.EncodeToString(b) != v {
		return "", Invalid()
	}
	return string(b), nil
}

func matchRoute(path string) (route, bool) {
	parts := strings.Split(strings.TrimPrefix(path, "/"), "/")
	r := route{}
	if len(parts) < 2 || parts[0] != "v1" {
		return r, false
	}
	if len(parts) == 2 {
		switch parts[1] {
		case "runtimes":
			r.method = "GET, POST"
			r.op = "runtime.collection"
		case "sessions":
			r.method = "GET, POST"
			r.op = "session.collection"
		default:
			return r, false
		}
		return r, true
	}
	var rest []string
	switch parts[1] {
	case "runtimes":
		r.name = parts[2]
		if !validName(r.name) {
			r.err = Invalid()
		}
		if len(parts) == 3 {
			r.method = "GET, DELETE"
			r.op = "runtime.resource"
			return r, true
		}
		if len(parts) != 4 {
			return r, false
		}
		r.method = "POST"
		switch parts[3] {
		case "spec":
			r.method = "PUT"
			r.op = "runtime.update"
		case "start":
			r.op = "runtime.start"
		case "stop":
			r.op = "runtime.stop"
		default:
			return r, false
		}
		return r, true
	case "sessions":
		r.ref.SessionID, r.err = decodeRef(parts[2])
		rest = parts[3:]
	case "namespaces":
		if len(parts) < 5 || parts[3] != "sessions" {
			return r, false
		}
		r.ref = resource.SessionRef{Namespace: parts[2], Name: parts[4]}
		if !validNamespace(parts[2]) || !validName(parts[4]) {
			r.err = Invalid()
		}
		rest = parts[5:]
	default:
		return r, false
	}
	r.method = "POST"
	if len(rest) == 0 {
		r.method = "GET"
		r.op = "session.get"
		return r, true
	}
	if len(rest) == 1 {
		switch rest[0] {
		case "resume", "archive", "cancel":
			r.op = "session." + rest[0]
		case "inputs":
			r.op = "session.input"
		case "events":
			r.method = "GET"
			r.op = "session.history"
		default:
			return r, false
		}
		return r, true
	}
	if len(rest) == 2 && rest[0] == "events" && rest[1] == "stream" {
		r.method = "GET"
		r.op = "session.observe"
		return r, true
	}
	if len(rest) == 3 && rest[0] == "interactions" && rest[2] == "answer" {
		r.op = "session.answer"
		var err error
		r.interaction, err = decodeRef(rest[1])
		if err != nil {
			r.err = err
		}
		return r, true
	}
	return r, false
}

func parseQuery(raw, op string) (url.Values, error) {
	q, err := url.ParseQuery(raw)
	if err != nil {
		return nil, Invalid()
	}
	allowed := map[string]bool{}
	switch op {
	case "runtime.list":
		allowed = map[string]bool{"type": true, "state": true}
	case "session.list":
		allowed = map[string]bool{"namespace": true, "runtimeRef": true, "state": true, "after": true, "limit": true}
	case "session.history":
		allowed = map[string]bool{"after": true, "limit": true, "types": true}
	}
	for key, values := range q {
		if !allowed[key] || len(values) == 0 || (key != "types" && len(values) != 1) {
			return nil, Invalid()
		}
		if slices.Contains(values, "") {
			return nil, Invalid()
		}
	}
	if v := q.Get("limit"); v != "" {
		for _, c := range v {
			if c < '0' || c > '9' {
				return nil, Invalid()
			}
		}
		n, e := strconv.Atoi(v)
		if e != nil || n < 1 {
			return nil, Invalid()
		}
	}
	if ns := q.Get("namespace"); ns != "" && !validNamespace(ns) {
		return nil, Invalid()
	}
	if state := q.Get("state"); state != "" {
		valid := false
		states := []string{"stopped", "starting", "running", "stopping", "unknown"}
		if op == "session.list" {
			states = []string{"offline", "opening", "idle", "running", "requires_action", "closing", "archived", "unknown"}
		}
		for _, v := range states {
			valid = valid || state == v
		}
		if !valid {
			return nil, Invalid()
		}
	}
	for _, kind := range q["types"] {
		if !eventTypes[kind] {
			return nil, Invalid()
		}
	}
	return q, nil
}

var eventTypes = func() map[string]bool {
	out := map[string]bool{}
	for s := range strings.FieldsSeq("user.message agent.message agent.thinking agent.tool_call agent.tool_result agent.message_chunk agent.thinking_chunk agent.tool_call_chunk agent.plan session.state_update session.error session.info_update session.config_option_update session.available_commands_update session.usage_update session.interaction_requested session.interaction_closed") {
		out[s] = true
	}
	return out
}()

func queryLimit(q url.Values) int {
	if q.Get("limit") == "" {
		return 100
	}
	n, _ := strconv.Atoi(q.Get("limit"))
	return n
}

func methodError(w http.ResponseWriter, allowed string) {
	w.Header().Set("Allow", allowed)
	fail(w, 405, Invalid())
}

// resolveMethod chooses collection/resource operations without redirects or ServeMux method fallbacks.
func resolveMethod(r *route, method string) {
	switch r.op {
	case "runtime.collection":
		switch method {
		case "POST":
			r.method = "POST"
			r.op = "runtime.create"
		case "GET":
			r.method = "GET"
			r.op = "runtime.list"
		}
	case "session.collection":
		switch method {
		case "POST":
			r.method = "POST"
			r.op = "session.create"
		case "GET":
			r.method = "GET"
			r.op = "session.list"
		}
	case "runtime.resource":
		switch method {
		case "DELETE":
			r.method = "DELETE"
			r.op = "runtime.delete"
		case "GET":
			r.method = "GET"
			r.op = "runtime.get"
		}
	}
}
