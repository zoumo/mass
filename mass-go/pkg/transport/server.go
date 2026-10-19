// Package transport implements the experimental Go HTTP mapping, not a finalized harness binding.
package transport

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"github.com/zoumo/mass/mass-go/pkg/resource"
	"github.com/zoumo/mass/mass-go/pkg/runtime"
	"github.com/zoumo/mass/mass-go/pkg/service"
	"github.com/zoumo/mass/mass-go/pkg/store"
	"io"
	"net/http"
)

type API interface {
	service.RuntimeService
	service.SessionService
}
type Error struct {
	Code    string `json:"code"`
	Reason  string `json:"reason"`
	Message string `json:"message"`
}

func (e *Error) Error() string { return e.Code + ": " + e.Reason }

// Never expose native errors, paths, configuration or dynamic diagnostics.
func PublicError(err error) *Error {
	var public *Error
	if errors.As(err, &public) {
		return public
	}
	switch {
	case errors.Is(err, runtime.ErrResourcesReleased):
		return &Error{"failed_precondition", "native_open_failed", "Native session open failed; owned resources released"}
	case errors.Is(err, runtime.ErrInvalidArgument):
		return &Error{"invalid_argument", "invalid_request", "Invalid request"}
	case errors.Is(err, runtime.ErrFailedPrecondition):
		return &Error{"failed_precondition", "operation_rejected", "Operation rejected by current resource state"}
	case errors.Is(err, store.ErrNotFound):
		return &Error{"not_found", "resource_not_found", "Resource not found"}
	case errors.Is(err, store.ErrConflict):
		return &Error{"conflict", "resource_conflict", "Resource conflict"}
	case errors.Is(err, store.ErrReadOnly):
		return &Error{"unavailable", "read_only", "Store is read only"}
	default:
		return &Error{"outcome_unknown", "operation_unconfirmed", "Operation failed; side effects may remain. Inspect state before any further action"}
	}
}
func Invalid() *Error { return &Error{"invalid_argument", "invalid_request", "Invalid request"} }

type Response struct {
	Result json.RawMessage `json:"result,omitempty"`
	Error  *Error          `json:"error,omitempty"`
}

// Params is a transport request only; each operation decodes its own strict shape.
type Params struct {
	Name         string                `json:"name,omitempty"`
	Session      resource.SessionRef   `json:"session,omitempty"`
	Runtime      *resource.Runtime     `json:"runtime,omitempty"`
	Create       *resource.Session     `json:"create,omitempty"`
	Spec         *resource.RuntimeSpec `json:"spec,omitempty"`
	RuntimeQuery resource.RuntimeQuery `json:"runtimeQuery,omitempty"`
	SessionQuery resource.SessionQuery `json:"sessionQuery,omitempty"`
	History      store.HistoryQuery    `json:"history,omitempty"`
	Input        runtime.Input         `json:"input,omitempty"`
	Interaction  string                `json:"interaction,omitempty"`
	Answer       map[string]any        `json:"answer,omitempty"`
}

func validRef(r resource.SessionRef) bool {
	return (r.SessionID != "" && r.Namespace == "" && r.Name == "") || (r.SessionID == "" && r.Namespace != "" && r.Name != "")
}
func Dispatch(ctx context.Context, s API, op string, p Params) (any, error) {
	switch op {
	case "runtime.create":
		if p.Runtime == nil {
			return nil, Invalid()
		}
		return s.CreateRuntime(*p.Runtime)
	case "runtime.list":
		return s.ListRuntimes(p.RuntimeQuery)
	case "session.create":
		if p.Create == nil {
			return nil, Invalid()
		}
		return s.CreateSession(ctx, *p.Create)
	case "session.list":
		return s.ListSessions(p.SessionQuery)
	}
	switch op {
	case "runtime.get", "runtime.update", "runtime.start", "runtime.stop", "runtime.delete":
		if p.Name == "" {
			return nil, Invalid()
		}
		switch op {
		case "runtime.get":
			return s.GetRuntime(p.Name)
		case "runtime.update":
			if p.Spec == nil {
				return nil, Invalid()
			}
			return s.UpdateRuntime(p.Name, *p.Spec)
		case "runtime.start":
			return s.StartRuntime(ctx, p.Name)
		case "runtime.stop":
			return s.StopRuntime(ctx, p.Name)
		case "runtime.delete":
			return nil, s.DeleteRuntime(p.Name)
		}
	}
	if !validRef(p.Session) {
		return nil, Invalid()
	}
	switch op {
	case "session.get":
		return s.GetSession(p.Session)
	case "session.archive":
		return s.ArchiveSession(ctx, p.Session)
	case "session.resume":
		return s.ResumeSession(ctx, p.Session)
	case "session.input":
		return s.SendPrompt(ctx, p.Session, p.Input)
	case "session.answer":
		if p.Interaction == "" || p.Answer == nil {
			return nil, Invalid()
		}
		return s.AnswerInteraction(ctx, p.Session, p.Interaction, p.Answer)
	case "session.cancel":
		return s.Cancel(ctx, p.Session)
	case "session.history":
		return s.ListHistoryEvents(p.Session, p.History)
	}
	return nil, Invalid()
}
func decode(r *http.Request, v any) error {
	b, e := io.ReadAll(io.LimitReader(r.Body, (1<<20)+1))
	if e != nil || len(b) > 1<<20 {
		return Invalid()
	}
	trimmed := bytes.TrimSpace(b)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return Invalid()
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(b, &fields) != nil {
		return Invalid()
	}
	for _, key := range []string{"history", "sessionQuery"} {
		if raw, ok := fields[key]; ok {
			var query map[string]json.RawMessage
			if json.Unmarshal(raw, &query) != nil {
				return Invalid()
			}
			if limit, ok := query["limit"]; ok {
				var n int
				if json.Unmarshal(limit, &n) != nil || n < 1 {
					return Invalid()
				}
			}
		}
	}
	d := json.NewDecoder(bytes.NewReader(b))
	d.UseNumber()
	d.DisallowUnknownFields()
	if d.Decode(v) != nil {
		return Invalid()
	}
	if d.Decode(new(any)) != io.EOF {
		return Invalid()
	}
	return nil
}

func respond(w http.ResponseWriter, v any, err error) {
	w.Header().Set("Content-Type", "application/json")
	if err != nil {
		public := PublicError(err)
		status := http.StatusInternalServerError
		switch public.Code {
		case "invalid_argument":
			status = 400
		case "not_found":
			status = 404
		case "conflict", "failed_precondition":
			status = 409
		case "unavailable":
			status = 503
		}
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(Response{Error: public})
		return
	}
	b, e := json.Marshal(v)
	if e != nil {
		respond(w, nil, e)
		return
	}
	_ = json.NewEncoder(w).Encode(Response{Result: b})
}
func New(s API) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /experimental/operations/{operation}", func(w http.ResponseWriter, r *http.Request) {
		var p Params
		if err := decode(r, &p); err != nil {
			respond(w, nil, err)
			return
		}
		v, e := Dispatch(r.Context(), s, r.PathValue("operation"), p)
		respond(w, v, e)
	})
	mux.HandleFunc("POST /experimental/observe", func(w http.ResponseWriter, r *http.Request) {
		var ref resource.SessionRef
		if decode(r, &ref) != nil || !validRef(ref) {
			respond(w, nil, Invalid())
			return
		}
		sub, err := s.SubscribeEvents(ref)
		if err != nil {
			respond(w, nil, err)
			return
		}
		defer sub.Close()
		if _, ok := w.(http.Flusher); !ok {
			respond(w, nil, errors.New("stream unavailable"))
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		rc := http.NewResponseController(w)
		send := func(kind string, v any) error {
			b, e := json.Marshal(v)
			if e != nil {
				return e
			}
			if _, e = io.WriteString(w, "event: "+kind+"\ndata: "+string(b)+"\n\n"); e != nil {
				return e
			}
			return rc.Flush()
		}
		if send("ready", struct{}{}) != nil {
			return
		}
		for {
			e, err := sub.Next(r.Context())
			if err != nil {
				if r.Context().Err() != nil {
					return
				}
				if errors.Is(err, io.EOF) {
					_ = send("end", struct{}{})
				} else {
					_ = send("error", &Error{"observation_failed", "subscription_terminated", "Observation delivery ended abnormally; inspect committed history"})
				}
				return
			}
			if send("session", e) != nil {
				return
			}
		}
	})
	return mux
}
