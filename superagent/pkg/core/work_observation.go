package core

import (
	"context"
	"encoding/json"
	"time"

	"github.com/zoumo/mass/mass-go/pkg/resource"
)

type workerSessionObservation struct {
	ExecutionID    string `json:"executionId"`
	State          string `json:"state"`
	ObservedAt     string `json:"observedAt,omitempty"`
	ObservationGap string `json:"observationGap,omitempty"`
}

func (s *Server) observeIssue(ctx context.Context, raw json.RawMessage) json.RawMessage {
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil {
		return raw
	}
	var executionID string
	if json.Unmarshal(fields["currentExecutionId"], &executionID) != nil || executionID == "" {
		return raw
	}
	state, err := json.Marshal(s.observeWorkerSession(ctx, executionID))
	if err != nil {
		return raw
	}
	fields["workerSession"] = state
	data, err := json.Marshal(fields)
	if err != nil {
		return raw
	}
	return data
}

func (s *Server) observeWorkerSession(ctx context.Context, executionID string) workerSessionObservation {
	observation := workerSessionObservation{ExecutionID: executionID, State: "unknown", ObservationGap: "worker_session_unavailable"}
	ctx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	var b Binding
	var body []byte
	if s.config.Execution == nil || s.store.db.QueryRowContext(ctx, `SELECT bindings.body FROM executions
 JOIN bindings ON bindings.id=executions.binding_id WHERE executions.id=? AND bindings.state='ready'`, executionID).Scan(&body) != nil ||
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
		observation.ObservationGap = "worker_session_identity_mismatch"
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
