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
	err := s.db.QueryRowContext(ctx, "SELECT body FROM session_bindings WHERE token=?", token).Scan(&data)
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
	if b.State == "revoked" && !historicalWorker {
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
	case "execution_template_query":
		if b.Role != "topic" {
			err = problem("forbidden", "only Topic can query Worker templates")
			break
		}
		var args struct{}
		if err = decodeStrict(raw, &args); err == nil {
			result = map[string]any{"items": s.config.Execution.WorkerTemplates}
		}
	case "issue_create", "issue_start", "issue_update_requirements", "issue_update_blockers", "issue_worker_update",
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
	case "message_content_schema_list", "message_content_schema_get":
		if b.Role != "router" && b.Role != "topic" {
			err = problem("forbidden", "only Router and Topic can query message content schemas")
			break
		}
		if tool == "message_content_schema_list" {
			var args struct{}
			if err = decodeStrict(raw, &args); err == nil {
				items := make([]contentSchemaSummary, 0, len(contentSchemas))
				for _, schema := range contentSchemas {
					items = append(items, schema.contentSchemaSummary)
				}
				result = map[string]any{"items": items}
			}
			break
		}
		var args struct {
			Type string `json:"type"`
		}
		if err = decodeStrict(raw, &args); err == nil {
			if schema, ok := contentSchemaByType(args.Type); ok {
				result = schema
			} else {
				err = problem("invalid_argument", "type must identify a supported message content schema")
			}
		}
	case "outbox_message_reply_structured":
		var args ReplyRequest
		err = decodeStrict(raw, &args)
		if err == nil {
			result, err = s.store.reply(r.Context(), b, args)
		}
	case "delivery_get":
		var args struct {
			ID string `json:"delivery_id"`
		}
		err = decodeStrict(raw, &args)
		if err == nil && args.ID == "" {
			err = problem("invalid_argument", "delivery_id is required")
		}
		if err == nil {
			result, err = s.store.getInternalDelivery(r.Context(), b, args.ID)
		}
	case "source_query", "topic_query", "work_query":
		var args QueryRequest
		err = decodeStrict(raw, &args)
		if err == nil {
			if tool == "source_query" && args.Collection != "messages" && args.Collection != "threads" && args.Collection != "outbox" && args.Collection != "routing-deliveries" {
				err = problem("forbidden", "source queries expose only Messages, Threads, routing deliveries and own replies")
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
	if kind == "outbox" && (b.Role == "router" || b.Role == "topic") {
		return " AND EXISTS(SELECT 1" + outboxAuthor + " AND b.role=? AND b.owner_id=?)", []any{b.Role, b.OwnerID}, nil
	}
	if b.Role == "router" {
		switch kind {
		case "messages", "threads", "topics", "routing-deliveries":
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
		return " AND id IN (SELECT message_id FROM topic_message_history WHERE topic_id=?)", []any{b.OwnerID}, nil
	case "routing-deliveries":
		return " AND topic_id=?", []any{b.OwnerID}, nil
	case "threads":
		return " AND key IN (SELECT thread_key FROM thread_subscriptions WHERE topic_id=?)", []any{b.OwnerID}, nil
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
	case "internal-deliveries":
		return internalDeliveryProjection
	case "routing-deliveries":
		return `json_patch(json_object('id',id,'delivery_id',id,'kind',kind,'createdAt',created_at,'status',status,'sentAt',sent_at,
 'messageId',message_id,'topicId',topic_id,'returnedDeliveryId',returned_id,'sender',json(sender),
 'recipient',json(recipient),'refs',json(refs),'returned',json(CASE WHEN EXISTS(
 SELECT 1 FROM internal_deliveries r WHERE r.kind='route_request' AND ` + returnedDeliveryID + `=routing_deliveries.id
 ) THEN 'true' ELSE 'false' END)),json_object('content',json(content),'body',json(body)))`
	case "threads":
		if b != nil && b.Role == "topic" {
			// The Topic can see its subscription through the Thread scope. Peer subscriptions are visible to Router and Observer.
			return "json_object('key',key,'ref',json_object('source',source,'id',native_id),'createdAt',created_at,'lastMessageAt',NULLIF(last_message_at,''))"
		}
		return "json_object('key',key,'ref',json_object('source',source,'id',native_id),'createdAt',created_at,'lastMessageAt',NULLIF(last_message_at,''),'subscriptions',json(subscriptions))"
	case "outbox":
		return outboxProjection
	case "messages":
		base := "json_set(body,'$.routing',json(CASE WHEN " + routingState + " IS NOT NULL THEN json_object('round'," + routingRound + ",'state'," + routingState + ") END))"
		if b == nil || b.Role == "router" {
			base = "json_set(" + base + ",'$.relations',json((SELECT json_group_array(json(body)) FROM (SELECT body FROM routing_relations WHERE message_id=trigger_messages.id ORDER BY seq))))"
		}
		if b != nil {
			if b.Role != "topic" {
				return base
			}
			// The authorization scope is already bound by the Message query,
			// but a multi-Topic Thread may host another Topic's questions.
			return "json_patch(" + base + ",json_object('quotedReply',json(" + quotedReplyForTopic + ")))"
		}
		return "json_patch(" + base + ",json_object('quotedReply',json(" + quotedReplyProjection + ")))"
	case "topics":
		if b != nil && b.Role == "router" {
			return "json_object('id',id,'title',json_extract(body,'$.title'),'description',json_extract(body,'$.description'),'goal',json_extract(body,'$.goal'),'status',json_extract(body,'$.status'),'revision',json_extract(body,'$.revision'),'createdAt',created_at,'updatedAt',json_extract(body,'$.updatedAt'))"
		}
		return `json_patch(topics.body,COALESCE((SELECT json_object('stop',json_extract(st.body,'$.stop')) FROM session_bindings st
 WHERE st.id=json_extract(topics.body,'$.cancellation.bindingId')),'{}'))`
	case "executions":
		return `json_set(executions.body,'$.preparation',json((SELECT body FROM session_bindings WHERE id=executions.binding_id)),
 '$.stop',json((SELECT json_extract(body,'$.stop') FROM session_bindings WHERE id=executions.binding_id)),
 '$.startup',json(` + workNotificationProjection("executions", "startEventId") + `),
 '$.reminderScheduled',json(CASE WHEN json_extract(executions.body,'$.reminderEventId') IS NULL THEN 'false' ELSE 'true' END),
 '$.reminder',json(` + workNotificationProjection("executions", "reminderEventId") + `),
 '$.reminderEligibility',CASE
  WHEN json_extract(executions.body,'$.authority')='historical' THEN 'execution_retired'
  WHEN EXISTS (SELECT 1 FROM reports WHERE execution_id=executions.id) THEN 'report_received'
  WHEN json_extract(executions.body,'$.reminderEventId') IS NOT NULL THEN 'already_scheduled'
  WHEN json_extract(executions.body,'$.workObservation.state') IN ('awaiting_receipt','observing') THEN 'awaiting_correlated_end'
  ELSE 'end_unconfirmed' END)`
	case "issues":
		return `json_patch(issues.body,json_object('latestRequirementsNotification',json_remove(` + workNotificationProjection("issues", "latestRequirementsEventId") + `,'$.payload','$.content')))`
	case "timers":
		return `json_patch(scheduled_tasks.body,coalesce((
 SELECT json_object('triggerMessageId',m.id,'threadKey',m.thread_key,'followup',
 json_object('id',d.id,'kind',d.kind,'status',d.status,'sentAt',d.sent_at,'messageId',d.message_id,'topicId',d.topic_id))
 FROM trigger_messages m JOIN routing_deliveries d ON d.id=json_extract(m.body,'$.eventId')
 WHERE m.id=scheduled_tasks.trigger_message_id
 ),'{}'))`
	}
	return "body"
}

// Capability comparison is explicit so startup-file identity cannot override the authenticated binding.
func capabilityMatches(c capability, b Binding) bool {
	a := sha256.Sum256([]byte(c.BindingID + "\x00" + c.Role + "\x00" + c.OwnerID + "\x00" + c.TopicID + "\x00" + c.IssueID))
	v := sha256.Sum256([]byte(b.ID + "\x00" + b.Role + "\x00" + b.OwnerID + "\x00" + b.TopicID + "\x00" + b.IssueID))
	return subtle.ConstantTimeCompare(a[:], v[:]) == 1 && c.Generation == b.Generation
}

// Projection placeholders precede filter placeholders in both list and detail queries.
func projectionArgs(kind string, b *Binding) []any {
	if kind == "messages" && b != nil && b.Role == "topic" {
		return []any{b.OwnerID}
	}
	return nil
}
