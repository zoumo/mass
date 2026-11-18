package core

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
)

func decodeStrict(data []byte, v any) error {
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if err := d.Decode(v); err != nil {
		return problem("invalid_argument", "invalid or unknown JSON fields")
	}
	if err := d.Decode(new(any)); err != io.EOF {
		return problem("invalid_argument", "exactly one JSON value is required")
	}
	return nil
}

func (s *Store) authenticateRole(ctx context.Context, header string) (Binding, error) {
	var b Binding
	if !strings.HasPrefix(header, "Bearer ") {
		return b, problem("unauthorized", "role credential is required")
	}
	token := strings.TrimPrefix(header, "Bearer ")
	// Secret lookup stays inside Core; no capability material is included in projections.
	var data []byte
	err := s.db.QueryRowContext(ctx, "SELECT body FROM bindings WHERE token=?", token).Scan(&data)
	if errors.Is(err, sql.ErrNoRows) {
		return b, problem("unauthorized", "invalid role credential")
	}
	if err != nil || json.Unmarshal(data, &b) != nil {
		return b, storageError()
	}
	return b, nil
}

func (s *Server) role(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	b, err := s.store.authenticateRole(r.Context(), r.Header.Get("Authorization"))
	if err != nil {
		s.fail(w, err)
		return
	}
	var raw []byte
	raw, err = io.ReadAll(r.Body)
	if err != nil {
		s.fail(w, problem("invalid_argument", "cannot read tool request"))
		return
	}
	var result any
	tool := strings.TrimPrefix(r.URL.Path, "/v1/role/")
	historicalWorker := b.Role == "worker" && (tool == "report_submit" || tool == "binding_get")
	cancelReplay := b.Role == "topic" && tool == "topic_cancel"
	if b.State == "revoked" && !historicalWorker && !cancelReplay {
		s.fail(w, problem("stale_binding", "binding was revoked"))
		return
	}
	switch tool {
	case "binding_get":
		var args struct{}
		err = decodeStrict(raw, &args)
		result = b
		if b.Role == "worker" {
			// The stdio handshake proves only startup identity, even for historical credentials.
			result = Binding{ID: b.ID, Role: b.Role, OwnerID: b.OwnerID, TopicID: b.TopicID, IssueID: b.IssueID, Generation: b.Generation}
		}
	case "issue_create", "issue_start", "issue_update_requirements", "issue_update_blockers",
		"issue_complete", "issue_cancel", "issue_reopen", "topic_complete", "topic_cancel", "topic_reopen",
		"timer_set", "timer_update", "timer_cancel":
		result, err = s.store.workCommand(r.Context(), b, tool, raw)
	case "issue_get_current", "issue_material_get":
		result, err = s.store.workerQuery(r.Context(), b, tool, raw)
	case "report_submit":
		result, err = s.store.workerReport(r.Context(), b, raw)
	case "pr_register":
		result, err = s.store.registerPR(r.Context(), b, raw)
	case "route_decide":
		var args RouteRequest
		err = decodeStrict(raw, &args)
		if err == nil {
			result, err = s.store.Route(r.Context(), b, args)
		}
	case "message_return":
		var args ReturnRequest
		err = decodeStrict(raw, &args)
		if err == nil {
			result, err = s.store.Return(r.Context(), b, args)
		}
	case "message_reply":
		var args ReplyRequest
		err = decodeStrict(raw, &args)
		if err == nil {
			result, err = s.store.reply(r.Context(), b, args)
		}
	case "event_ack", "event_get":
		var args struct {
			EventID string `json:"eventId"`
		}
		err = decodeStrict(raw, &args)
		if err == nil && args.EventID == "" {
			err = problem("invalid_argument", "eventId is required")
		}
		if err == nil {
			if tool == "event_ack" {
				result, err = s.store.ackEvent(r.Context(), b, args.EventID)
			} else {
				result, err = s.store.getScoped(r.Context(), "events", args.EventID, &b, nil)
			}
		}
	case "source_query", "topic_query", "work_query":
		var args QueryRequest
		err = decodeStrict(raw, &args)
		if err == nil {
			if tool == "source_query" && args.Collection != "messages" && args.Collection != "threads" && args.Collection != "outbox" {
				err = problem("forbidden", "source queries expose only Messages, Threads and own replies")
			}
			if tool == "topic_query" && args.Collection != "topics" {
				err = problem("forbidden", "Topic query exposes only Topic summaries/current Topic")
			}
			if tool == "work_query" && (b.Role != "topic" || args.Collection != "issues" && args.Collection != "executions" && args.Collection != "reports" && args.Collection != "timers" && args.Collection != "pull-requests") {
				err = problem("forbidden", "work query exposes only the bound Topic's Issues, Executions, Reports, Timers and PR associations")
			}
		}
		if err == nil {
			result, err = s.store.RoleQuery(r.Context(), b, args)
		}
		if raw, ok := result.(json.RawMessage); err == nil && ok && tool == "work_query" && args.Collection == "issues" {
			result = s.observeIssue(r.Context(), raw)
		}
	default:
		err = problem("forbidden", "tool is outside current role capability")
	}
	if err != nil {
		s.fail(w, err)
		return
	}
	s.write(w, http.StatusOK, map[string]any{"result": result})
}

type QueryRequest struct {
	Collection string `json:"collection"`
	ID         string `json:"id,omitempty"`
	ThreadKey  string `json:"threadKey,omitempty"`
	TopicID    string `json:"topicId,omitempty"`
	IssueID    string `json:"issueId,omitempty"`
	MessageID  string `json:"messageId,omitempty"`
	Cursor     string `json:"cursor,omitempty"`
	Limit      int    `json:"limit,omitempty"`
	preview    bool
	newest     bool
}

func (s *Store) RoleQuery(ctx context.Context, b Binding, q QueryRequest) (any, error) {
	if q.ID != "" {
		if q.Cursor != "" || q.ThreadKey != "" || q.TopicID != "" || q.IssueID != "" || q.MessageID != "" || q.Limit != 0 {
			return nil, problem("invalid_argument", "detail query cannot have list parameters")
		}
		return s.getScoped(ctx, q.Collection, q.ID, &b, nil)
	}
	if q.Limit == 0 {
		q.Limit = 50
	}
	return s.listScoped(ctx, q, &b, nil)
}

func scopeSQL(kind string, b *Binding) (string, []any, error) {
	if b == nil {
		return "", nil, nil
	}
	if kind == "events" {
		return " AND EXISTS (SELECT 1 FROM deliveries d WHERE d.event_id=events.id AND d.binding_id=?)", []any{b.ID}, nil
	}
	if kind == "outbox" && (b.Role == "router" || b.Role == "topic") {
		return " AND json_extract(body,'$.authorRole')=? AND COALESCE(json_extract(body,'$.authorId'),'')=?", []any{b.Role, b.OwnerID}, nil
	}
	if b.Role == "router" {
		switch kind {
		case "messages", "threads", "topics":
			return "", nil, nil
		default:
			return "", nil, problem("forbidden", "Router can query only sources and Topic summaries")
		}
	}
	if b.Role != "topic" {
		return "", nil, problem("forbidden", "unsupported role")
	}
	switch kind {
	case "messages":
		return " AND id IN (SELECT message_id FROM message_topics WHERE topic_id=?)", []any{b.OwnerID}, nil
	case "threads":
		return " AND key IN (SELECT thread_key FROM subscriptions WHERE topic_id=?)", []any{b.OwnerID}, nil
	case "topics":
		return " AND id=?", []any{b.OwnerID}, nil
	case "issues", "timers", "pull-requests":
		return " AND topic_id=?", []any{b.OwnerID}, nil
	case "reports", "executions":
		return " AND issue_id IN (SELECT id FROM issues WHERE topic_id=?)", []any{b.OwnerID}, nil
	default:
		return "", nil, problem("forbidden", "Topic cannot query outside its own sources and Topic")
	}
}

func projection(kind string, b *Binding) string {
	switch kind {
	case "events":
		return eventProjection()
	case "deliveries":
		return `json_set(body,'$.needsAttention',json(CASE WHEN ` + deliveryAttention + ` THEN 'true' ELSE 'false' END))`
	case "channel-health":
		return channelHealthProjection
	case "threads":
		return "json_patch(json_object('key',key,'ref',json_object('source',source,'id',native_id),'createdAt',created_at),json_object('lastMessageAt',NULLIF(last_message_at,'')))"
	case "messages":
		base := "json_set(body,'$.routing',json((SELECT json_object('round',round,'state',state) FROM message_routes WHERE message_id=messages.id)))"
		if b == nil || b.Role == "router" {
			return "json_set(" + base + ",'$.relations',json((SELECT json_group_array(json(body)) FROM (SELECT body FROM relations WHERE message_id=messages.id ORDER BY seq))))"
		}
		return base
	case "topics":
		if b != nil && b.Role == "router" {
			return "json_object('id',id,'title',json_extract(body,'$.title'),'description',json_extract(body,'$.description'),'status',json_extract(body,'$.status'),'revision',json_extract(body,'$.revision'),'createdAt',created_at,'updatedAt',json_extract(body,'$.updatedAt'))"
		}
		return `json_patch(topics.body,COALESCE((SELECT json_object('stop',json(st.body)) FROM binding_stops st
 WHERE st.binding_id=json_extract(topics.body,'$.cancellation.bindingId')),'{}'))`
	case "executions":
		return `json_set(executions.body,'$.preparation',json((SELECT body FROM bindings WHERE id=executions.binding_id)),
 '$.stop',json((SELECT body FROM binding_stops WHERE binding_id=executions.binding_id)),
 '$.startup',json((SELECT ` + eventProjection() + ` FROM events WHERE id=json_extract(executions.body,'$.startEventId'))),
 '$.reminderScheduled',json(CASE WHEN json_extract(executions.body,'$.reminderEventId') IS NULL THEN 'false' ELSE 'true' END),
 '$.reminder',json((SELECT ` + eventProjection() + ` FROM events WHERE id=json_extract(executions.body,'$.reminderEventId'))),
 '$.reminderEligibility',CASE
  WHEN json_extract(executions.body,'$.authority')='historical' THEN 'execution_retired'
  WHEN EXISTS (SELECT 1 FROM reports WHERE execution_id=executions.id) THEN 'report_received'
  WHEN json_extract(executions.body,'$.reminderEventId') IS NOT NULL THEN 'already_scheduled'
  WHEN json_extract(executions.body,'$.workObservation.state') IN ('awaiting_receipt','observing') THEN 'awaiting_correlated_end'
  ELSE 'end_unconfirmed' END)`
	case "issues":
		return `json_patch(issues.body,COALESCE((SELECT json_object('latestRequirementsNotification',json_remove(` + eventProjection() + `,'$.payload'))
 FROM events WHERE id=json_extract(issues.body,'$.latestRequirementsEventId')),'{}'))`
	case "timers":
		return `json_patch(timers.body,COALESCE((SELECT json_object('followup',json_remove(` + eventProjection() + `,'$.payload'))
 FROM events WHERE id=json_extract(timers.body,'$.followupEventId')),'{}'))`
	}
	return "body"
}

// Capability comparison is explicit so startup-file identity cannot override the authenticated binding.
func capabilityMatches(c capability, b Binding) bool {
	a := sha256.Sum256([]byte(c.BindingID + "\x00" + c.Role + "\x00" + c.OwnerID + "\x00" + c.TopicID + "\x00" + c.IssueID))
	v := sha256.Sum256([]byte(b.ID + "\x00" + b.Role + "\x00" + b.OwnerID + "\x00" + b.TopicID + "\x00" + b.IssueID))
	return subtle.ConstantTimeCompare(a[:], v[:]) == 1 && c.Generation == b.Generation
}
