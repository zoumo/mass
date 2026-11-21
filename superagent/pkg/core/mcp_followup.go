package core

import (
	"context"
	"database/sql"
	"encoding/json"
)

type followupRevision struct {
	mutationRevision
	FollowupID string `json:"followupId"`
}

type updateFollowupArgs struct {
	followupRevision
	DueAt     *string     `json:"dueAt,omitempty"`
	Content   *string     `json:"content,omitempty"`
	Materials *[]Material `json:"materials,omitempty"`
}

func (s *Store) followupCommand(ctx context.Context, b SessionBinding, tool string, raw []byte) (any, error) {
	if tool == "schedule_followup" {
		var r SetTimerRequest
		if err := decodeStrict(raw, &r); err != nil {
			return nil, err
		}
		return s.commitMCPMutation(ctx, b, tool, r.OperationID, r, setTimer(r))
	}
	var r updateFollowupArgs
	if tool == "cancel_followup" {
		if err := decodeStrict(raw, &r.followupRevision); err != nil {
			return nil, err
		}
	} else {
		if err := decodeStrict(raw, &r); err != nil {
			return nil, err
		}
		if r.DueAt == nil && r.Content == nil && r.Materials == nil {
			return nil, problem("invalid_argument", "provide at least one followup update")
		}
	}
	return s.commitMCPMutation(ctx, b, tool, r.OperationID, r, func(ctx context.Context, tx *sql.Tx, b SessionBinding) (any, error) {
		if r.FollowupID == "" {
			return nil, problem("invalid_argument", "followupId is required")
		}
		var item Timer
		if err := readJSON(ctx, tx, "SELECT body FROM scheduled_tasks WHERE id=? AND topic_id=?", &item, r.FollowupID, b.OwnerID); err != nil {
			return nil, err
		}
		old := CancelTimerRequest{OperationID: r.OperationID, TimerID: r.FollowupID, ExpectedRevision: r.ExpectedRevision}
		if tool == "cancel_followup" {
			return changeTimer(old, nil)(ctx, tx, b)
		}
		if r.DueAt != nil {
			item.DueAt = *r.DueAt
		}
		if r.Content != nil {
			item.Content = *r.Content
		}
		if r.Materials != nil {
			item.Materials = *r.Materials
		}
		return changeTimer(old, &UpdateTimerRequest{CancelTimerRequest: old, DueAt: item.DueAt, Content: item.Content, Materials: item.Materials})(ctx, tx, b)
	})
}

// Keep older stored Message metadata readable while exposing the MCP followup
// identifier and revision on the agent-facing event and Message view.
func agentFollowupMeta(meta json.RawMessage) json.RawMessage {
	var fields map[string]json.RawMessage
	if json.Unmarshal(meta, &fields) != nil || string(fields["type"]) != `"timer.followup"` {
		return meta
	}
	fields["type"] = json.RawMessage(`"followup"`)
	if id := fields["taskId"]; len(id) != 0 {
		fields["followupId"] = id
		delete(fields, "taskId")
	}
	if revision := fields["taskRevision"]; len(revision) != 0 {
		fields["revision"] = revision
		delete(fields, "taskRevision")
	}
	raw, err := json.Marshal(fields)
	if err != nil {
		return meta
	}
	return raw
}
