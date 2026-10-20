package transport

// Work, recovery and event operations retain explicit chi handlers until their
// owning tickets migrate their request and response schemas.
import (
	"context"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/danielgtaylor/huma/v2"
	"github.com/go-chi/chi/v5"
	"github.com/zoumo/mass/mass-go/pkg/resource"
	"github.com/zoumo/mass/mass-go/pkg/runtime"
	"github.com/zoumo/mass/mass-go/pkg/store"
)

func (s *Server) registerSessions(api huma.API, r chi.Router) {
	s.registerSessionIdentity(api)
	for _, base := range []string{"/apis/v1/sessions/{sessionId}", "/apis/v1/namespaces/{namespace}/sessions/{nameOrID}"} {
		r.MethodFunc("POST", base+"/resume", s.sessionHandler(nil, func(w http.ResponseWriter, r *http.Request, ref resource.SessionRef, _ url.Values) {
			if e := emptyBody(w, r); e != nil {
				respond(w, 0, nil, e)
				return
			}
			v, e := s.api.ResumeSession(context.WithoutCancel(r.Context()), ref)
			respond(w, 200, v, e)
		}))
		r.MethodFunc("POST", base+"/archive", s.sessionHandler(nil, func(w http.ResponseWriter, r *http.Request, ref resource.SessionRef, _ url.Values) {
			if e := emptyBody(w, r); e != nil {
				respond(w, 0, nil, e)
				return
			}
			v, e := s.api.ArchiveSession(context.WithoutCancel(r.Context()), ref)
			respond(w, 200, v, e)
		}))
		r.MethodFunc("POST", base+"/cancel", s.sessionHandler(nil, func(w http.ResponseWriter, r *http.Request, ref resource.SessionRef, _ url.Values) {
			if e := emptyBody(w, r); e != nil {
				respond(w, 0, nil, e)
				return
			}
			v, e := s.api.Cancel(context.WithoutCancel(r.Context()), ref)
			respond(w, 200, v, e)
		}))
		r.MethodFunc("POST", base+"/inputs", s.sessionHandler(nil, func(w http.ResponseWriter, r *http.Request, ref resource.SessionRef, _ url.Values) {
			var in runtime.Input
			if e := decodeBody(w, r, &in); e != nil {
				respond(w, 0, nil, e)
				return
			}
			v, e := s.api.SendPrompt(context.WithoutCancel(r.Context()), ref, in)
			respond(w, 200, v, e)
		}))
		r.MethodFunc("POST", base+"/interactions/{interaction64}/answer", s.sessionHandler(nil, func(w http.ResponseWriter, r *http.Request, ref resource.SessionRef, _ url.Values) {
			id, e := decodeRef(chi.URLParam(r, "interaction64"))
			if e != nil {
				respond(w, 0, nil, e)
				return
			}
			var answer any
			if e = decodeBody(w, r, &answer); e != nil {
				respond(w, 0, nil, e)
				return
			}
			v, e := s.api.AnswerInteraction(context.WithoutCancel(r.Context()), ref, id, answer)
			respond(w, 200, v, e)
		}))
		r.MethodFunc("GET", base+"/events", s.sessionHandler([]string{"after", "limit", "types"}, func(w http.ResponseWriter, r *http.Request, ref resource.SessionRef, q url.Values) {
			v, e := s.api.ListHistoryEvents(ref, store.HistoryQuery{After: q.Get("after"), Limit: queryLimit(q), Types: q["types"]})
			respond(w, 200, v, e)
		}))
		r.MethodFunc("GET", base+"/events/stream", s.sessionHandler(nil, func(w http.ResponseWriter, r *http.Request, ref resource.SessionRef, _ url.Values) {
			if r.Header.Get("Last-Event-ID") != "" {
				fail(w, 400, Invalid())
				return
			}
			s.observe(w, r, ref)
		}))
	}
}
func (s *Server) sessionHandler(allowed []string, handler func(http.ResponseWriter, *http.Request, resource.SessionRef, url.Values)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ref := resource.SessionRef{}
		if id := chi.URLParam(r, "sessionId"); id != "" {
			decoded, e := url.PathUnescape(id)
			if e == nil {
				e = resource.ValidateSessionID(decoded)
			}
			if e != nil {
				respond(w, 0, nil, e)
				return
			}
			ref.SessionID = decoded
		}
		if name := chi.URLParam(r, "nameOrID"); name != "" {
			namespace, err := url.PathUnescape(chi.URLParam(r, "namespace"))
			target, targetErr := url.PathUnescape(name)
			if err != nil || targetErr != nil {
				respond(w, 0, nil, Invalid())
				return
			}
			ref, err = s.api.ResolveSession(namespace, target)
			if err != nil {
				respond(w, 0, nil, err)
				return
			}
		}
		media := "application/json"
		if strings.HasSuffix(r.URL.Path, "/events/stream") {
			media = "text/event-stream"
		}
		if !accepts(r.Header.Get("Accept"), media) {
			fail(w, 406, Invalid())
			return
		}
		q, e := sessionQuery(r.URL.RawQuery, allowed)
		if e != nil {
			respond(w, 0, nil, e)
			return
		}
		if r.Context().Err() != nil {
			fail(w, 504, &Error{Code: "deadline_exceeded", Reason: "request_expired", Message: "Request expired before dispatch"})
			return
		}
		handler(w, r, ref, q)
	}
}
func sessionQuery(raw string, allowed []string) (url.Values, error) {
	q, e := url.ParseQuery(raw)
	if e != nil {
		return nil, Invalid()
	}
	for key, values := range q {
		valid := false
		for _, v := range allowed {
			valid = valid || v == key
		}
		if !valid || (key != "types" && len(values) != 1) {
			return nil, Invalid()
		}
		for _, v := range values {
			if v == "" || !utf8.ValidString(v) {
				return nil, Invalid()
			}
		}
	}
	if limit := q.Get("limit"); limit != "" {
		for _, c := range limit {
			if c < '0' || c > '9' {
				return nil, Invalid()
			}
		}
		n, e := strconv.Atoi(limit)
		if e != nil || n < 1 {
			return nil, Invalid()
		}
	}
	if state := q.Get("state"); state != "" {
		valid := false
		for _, v := range []string{"offline", "opening", "idle", "running", "requires_action", "closing", "archived", "unknown"} {
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
