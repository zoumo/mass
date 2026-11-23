package core

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"net"
	"net/http"
	"strings"
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
	host, _, _ := net.SplitHostPort(r.RemoteAddr)
	if !net.ParseIP(host).IsLoopback() || r.Header.Get("Origin") != "" || r.Header.Get("Sec-Fetch-Site") != "" {
		s.fail(w, problem("forbidden", "operator access is available to local CLI clients"))
		return false
	}
	if s.config.operatorToken == "" {
		s.fail(w, problem("not_found", "operator recovery is not configured"))
		return false
	}
	got := sha256.Sum256([]byte(r.Header.Get("Authorization")))
	expected := sha256.Sum256([]byte("Bearer " + s.config.operatorToken))
	if subtle.ConstantTimeCompare(got[:], expected[:]) != 1 {
		s.fail(w, problem("unauthorized", "operator credential is required"))
		return false
	}
	return true
}

func (s *Server) inspectBinding(w http.ResponseWriter, r *http.Request) {
	if !readOnly(w, r) || !s.authorizeOperator(w, r) {
		return
	}
	s.bindingEvidence(w, r, strings.TrimPrefix(r.URL.Path, "/v1/operator/session-bindings/"))
}

func (s *Server) bindingEvidence(w http.ResponseWriter, r *http.Request, id string) {
	tx, err := s.store.db.BeginTx(r.Context(), &sql.TxOptions{ReadOnly: true})
	if err != nil {
		s.fail(w, storageError())
		return
	}
	defer func() { _ = tx.Rollback() }()
	var inspection SessionBindingInspection
	var spec resource.SessionSpec
	if err := readJSON(r.Context(), tx, "SELECT body FROM session_bindings WHERE id=?", &inspection.SessionBinding, id); err != nil {
		s.fail(w, err)
		return
	}
	if err := readJSON(r.Context(), tx, "SELECT spec FROM session_bindings WHERE id=?", &spec, id); err != nil {
		s.fail(w, err)
		return
	}
	if err := tx.Commit(); err != nil {
		s.fail(w, storageError())
		return
	}
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
