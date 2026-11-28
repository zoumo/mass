package core

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"net/http"
	"time"

	"github.com/zoumo/mass/massd/pkg/resource"
)

type SessionBindingInspection struct {
	SessionBinding         SessionBinding `json:"sessionBinding"`
	SessionState           string         `json:"sessionState,omitempty"`
	SessionResourceVersion string         `json:"sessionResourceVersion,omitempty"`
	RuntimeType            string         `json:"runtimeType,omitempty"`
	RuntimeState           string         `json:"runtimeState,omitempty"`
	ObservationGap         string         `json:"observationGap,omitempty"`
}

func (s *Server) authorizeOperator(w http.ResponseWriter, r *http.Request) bool {
	if r.Header.Get("Origin") != "" || r.Header.Get("Sec-Fetch-Site") != "" {
		s.fail(w, problem("forbidden", "operator access requires a non-browser client"))
		return false
	}
	if s.credentials.adminToken == "" {
		s.fail(w, problem("not_found", "operator access is not configured"))
		return false
	}
	got := sha256.Sum256([]byte(r.Header.Get("Authorization")))
	expected := sha256.Sum256([]byte("Bearer " + s.credentials.adminToken))
	if subtle.ConstantTimeCompare(got[:], expected[:]) != 1 {
		s.fail(w, problem("unauthorized", "valid Core admin token is required"))
		return false
	}
	return true
}

func (s *Server) bindingEvidence(w http.ResponseWriter, r *http.Request, id string) {
	binding, spec, err := s.store.bindingAndSpec(r.Context(), id)
	if err != nil {
		s.fail(w, err)
		return
	}
	inspection := SessionBindingInspection{SessionBinding: binding}
	s.observeBinding(r.Context(), spec, &inspection)
	s.write(w, http.StatusOK, inspection)
}

func (s *Server) observeBinding(ctx context.Context, spec resource.SessionSpec, i *SessionBindingInspection) {
	if !i.SessionBinding.PreparationStarted {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	i.ObservationGap = "massd_session_unavailable"
	c, err := s.massClient()
	if err != nil {
		return
	}
	defer func() { _ = c.Close() }()
	var rt resource.Runtime
	if c.GetRuntime(ctx, i.SessionBinding.RuntimeRef, &rt) == nil {
		i.RuntimeType, i.RuntimeState = rt.Spec.Type, rt.Status.State
	}
	var session resource.Session
	if c.GetSession(ctx, bindingSessionRef(i.SessionBinding), &session) != nil {
		return
	}
	if !bindingMatches(i.SessionBinding, spec, session) {
		i.ObservationGap = "massd_session_binding_mismatch"
		return
	}
	i.SessionState, i.SessionResourceVersion, i.ObservationGap = string(session.Status.State), session.Metadata.ResourceVersion, ""
}
