package core

import (
	"context"
	"encoding/json"
	"time"

	"github.com/zoumo/mass/massd/pkg/resource"
)

type issueSessionObservation struct {
	SessionBindingID string `json:"sessionBindingId"`
	State            string `json:"state"`
	ObservedAt       string `json:"observedAt,omitempty"`
	ObservationGap   string `json:"observationGap,omitempty"`
}

func (s *Server) observeIssue(ctx context.Context, raw json.RawMessage) json.RawMessage {
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil {
		return raw
	}
	var sessionBindingID string
	if json.Unmarshal(fields["currentSessionBindingId"], &sessionBindingID) != nil || sessionBindingID == "" {
		return raw
	}
	state, err := json.Marshal(s.observeIssueSession(ctx, sessionBindingID))
	if err != nil {
		return raw
	}
	fields["issueSession"] = state
	data, err := json.Marshal(fields)
	if err != nil {
		return raw
	}
	return data
}

func (s *Server) observeIssueSession(ctx context.Context, sessionBindingID string) issueSessionObservation {
	observation := issueSessionObservation{SessionBindingID: sessionBindingID, State: "unknown", ObservationGap: "issue_session_unavailable"}
	ctx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	var b SessionBinding
	var body []byte
	if s.config.Sessions == nil || s.store.db.QueryRowContext(ctx, "SELECT body FROM session_bindings WHERE id=? AND role='issue' AND state='ready'", sessionBindingID).Scan(&body) != nil ||
		json.Unmarshal(body, &b) != nil ||
		b.SessionID == "" || b.SessionUID == "" {
		return observation
	}
	c, err := s.massClient()
	if err != nil {
		return observation
	}
	defer func() { _ = c.Close() }()
	var session resource.Session
	if err := c.GetSession(ctx, resource.SessionRef{SessionID: b.SessionID}, &session); err != nil {
		return observation
	}
	if session.Metadata.UID != b.SessionUID || session.Status.SessionID != b.SessionID ||
		session.Metadata.Namespace != b.Namespace || session.Metadata.Name != b.Name {
		observation.ObservationGap = "issue_session_identity_mismatch"
		return observation
	}
	if session.Status.State != "" {
		observation.State, observation.ObservedAt, observation.ObservationGap = string(session.Status.State), now(), ""
		if observation.State == "unknown" {
			observation.ObservationGap = "massd_session_state_unknown"
		}
	}
	return observation
}
