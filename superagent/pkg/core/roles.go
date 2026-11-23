package core

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
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

func (s *Store) authenticateRole(ctx context.Context, header string) (SessionBinding, error) {
	var b SessionBinding
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

func (s *Server) callRoleTool(ctx context.Context, b SessionBinding, tool string, raw []byte) (any, error) {
	var result any
	var err error
	switch tool {
	case "get_message", "list_messages", "list_topics", "get_topic", "get_issue", "list_issues", "get_report", "get_scheduled_task":
		result, err = s.mcpQuery(ctx, b, tool, raw)
	case "update_topic":
		result, err = s.store.updateMCPTopic(ctx, b, raw)
	case "update_issue":
		result, err = s.store.updateMCPIssue(ctx, b, raw)
	case "create_issue", "start_issue", "create_scheduled_task", "update_scheduled_task", "delete_scheduled_task":
		result, err = s.store.workCommand(ctx, b, tool, raw)
	case "submit_report":
		result, err = s.store.submitIssueReport(ctx, b, raw)
	case "route_messages":
		var args RouteRequest
		err = decodeStrict(raw, &args)
		if err == nil {
			result, err = s.store.Route(ctx, b, args)
		}
	case "decline_message":
		var args DeclineMessageRequest
		err = decodeStrict(raw, &args)
		if err == nil {
			result, err = s.store.DeclineMessage(ctx, b, args)
		}
	case "send_reply":
		var args struct {
			OperationID string            `json:"operationId"`
			MessageID   string            `json:"messageId"`
			Content     StructuredContent `json:"content"`
			BlockerRef  *BlockerRef       `json:"blockerRef,omitempty"`
		}
		err = decodeStrict(raw, &args)
		if err == nil && args.MessageID == "" {
			err = problem("invalid_argument", "messageId is required")
		}
		if err == nil && b.Role != "topic" && args.BlockerRef != nil {
			err = problem("forbidden", "only Topic can attach a blocker")
		}
		if err == nil {
			result, err = s.store.reply(ctx, b, ReplyRequest{OperationID: args.OperationID, MessageID: args.MessageID, Content: args.Content, BlockerRef: args.BlockerRef})
		}
	case "get_internal_event":
		var args struct {
			ID string `json:"event_id"`
		}
		err = decodeStrict(raw, &args)
		if err == nil && args.ID == "" {
			err = problem("invalid_argument", "event_id is required")
		}
		if err == nil {
			var event internalEvent
			event, err = s.store.getInternalEvent(ctx, b, args.ID)
			if err == nil {
				var raw []byte
				raw, err = event.MarshalAgentPromptJSON()
				result = json.RawMessage(raw)
			}
		}
	default:
		err = problem("forbidden", "tool is outside current role capability")
	}
	return result, err
}

type QueryRequest struct {
	Collection      string `json:"collection"`
	ID              string `json:"id,omitempty"`
	ThreadID        string `json:"threadId,omitempty"`
	TopicID         string `json:"topicId,omitempty"`
	IssueID         string `json:"issueId,omitempty"`
	MessageID       string `json:"messageId,omitempty"`
	OwnerType       string `json:"ownerType,omitempty"`
	OwnerID         string `json:"ownerId,omitempty"`
	Source          string `json:"source,omitempty"`
	ScheduledTaskID string `json:"scheduledTaskId,omitempty"`
	Cursor          string `json:"cursor,omitempty"`
	Limit           int    `json:"limit,omitempty"`
	preview         bool
	observerTrace   bool
	newest          bool
	search          string
	status          string
}

func (s *Store) RoleQuery(ctx context.Context, b SessionBinding, q QueryRequest) (any, error) {
	if q.ID != "" {
		if q.Cursor != "" || q.ThreadID != "" || q.TopicID != "" || q.IssueID != "" || q.MessageID != "" || q.OwnerType != "" || q.OwnerID != "" || q.Source != "" || q.ScheduledTaskID != "" || q.Limit != 0 {
			return nil, problem("invalid_argument", "detail query cannot have list parameters")
		}
		return s.getScoped(ctx, q.Collection, q.ID, &b, nil)
	}
	if q.Limit == 0 {
		q.Limit = 50
	}
	return s.listScoped(ctx, q, &b, nil)
}

func scopeSQL(kind string, b *SessionBinding) (string, []any, error) {
	if b == nil {
		return "", nil, nil
	}
	if kind == "outbox" && (b.Role == "router" || b.Role == "topic") {
		return " AND EXISTS(SELECT 1" + outboxAuthor + " AND b.role=? AND b.owner_id=?)", []any{b.Role, b.OwnerID}, nil
	}
	if b.Role == "router" {
		switch kind {
		case "messages", "threads", "topics", "routing-events":
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
	case "routing-events":
		return " AND topic_id=?", []any{b.OwnerID}, nil
	case "threads":
		return " AND id IN (SELECT thread_id FROM thread_subscriptions WHERE topic_id=?)", []any{b.OwnerID}, nil
	case "topics":
		return " AND id=?", []any{b.OwnerID}, nil
	case "issues", "pull-requests":
		return " AND topic_id=?", []any{b.OwnerID}, nil
	case "scheduled-tasks":
		return " AND owner_type='topic' AND owner_id=? AND deleted_at IS NULL", []any{b.OwnerID}, nil
	case "reports":
		return " AND issue_id IN (SELECT id FROM issues WHERE topic_id=?)", []any{b.OwnerID}, nil
	default:
		return "", nil, problem("forbidden", "Topic cannot query outside its own sources and Topic")
	}
}

const reportCurrentSQL = `EXISTS (
 SELECT 1 FROM issues WHERE issues.id=reports.issue_id AND json_extract(reports.body,'$.historical')=0
  AND json_extract(issues.body,'$.currentSessionBindingId')=reports.session_binding_id
  AND json_extract(issues.body,'$.goalRevision')=json_extract(reports.body,'$.goalRevision')
)`

const routingEventProjection = `json_patch(json_object('event_id',event_id,'kind',kind,'createdAt',created_at,'status',status,'sentAt',sent_at,
 'messageId',message_id,'topicId',topic_id,'returnedEventId',returned_event_id,'sender',json(sender),
 'recipient',json(recipient),'refs',json(refs),'returned',json(CASE WHEN EXISTS(
 SELECT 1 FROM internal_events r WHERE r.kind='route_request' AND ` + returnedEventID + `=routing_events.event_id
 ) THEN 'true' ELSE 'false' END)),json_object('content',json(content),'body',json(body)))`

func projection(kind string, b *SessionBinding) string {
	switch kind {
	case "internal-events":
		return internalEventProjection
	case "routing-events":
		return routingEventProjection
	case "threads":
		if b != nil && b.Role == "topic" {
			// The Topic can see its subscription through the Thread scope. Peer subscriptions are visible to Router and Observer.
			return "json_object('id',id,'createdAt',created_at,'lastMessageAt',NULLIF(last_message_at,''))"
		}
		return "json_object('id',id,'createdAt',created_at,'lastMessageAt',NULLIF(last_message_at,''),'subscriptions',json(subscriptions))"
	case "outbox":
		return outboxProjection
	case "messages":
		if b != nil {
			if b.Role == "topic" {
				return "json_patch(" + agentSourceMessageProjection + ",json_object('quotedReply',json(" + quotedReplyForTopic + ")))"
			}
			return `json_set(` + agentSourceMessageProjection + `,
 '$.routing',json(CASE WHEN ` + routingState + ` IS NOT NULL
 THEN json_object('round',` + routingRound + `,'state',` + routingState + `) END),
 '$.relations',json((SELECT json_group_array(json(relation)) FROM (
 SELECT json_patch(
 json_object('topicId',topic_id,'action',body->>'$.action','eventId',body->>'$.eventId'),
 json_object('reason',CASE WHEN body->>'$.action'='returned' THEN body->>'$.reason' END)) AS relation
 FROM routing_relations WHERE message_id=trigger_messages.id ORDER BY seq))))`
		}
		base := "json_set(json_remove(body,'$.replyTarget','$.inReplyToOutboxId'),'$.routing',json(CASE WHEN " + routingState + " IS NOT NULL THEN json_object('round'," + routingRound + ",'state'," + routingState + ") END))"
		base = "json_set(" + base + ",'$.relations',json((SELECT json_group_array(json(body)) FROM (SELECT body FROM routing_relations WHERE message_id=trigger_messages.id ORDER BY seq))))"
		return "json_patch(" + base + ",json_object('quotedReply',json(" + recordedReplyProjection + ")))"
	case "topics":
		if b != nil && b.Role == "router" {
			return "json_object('id',id,'title',json_extract(body,'$.title'),'description',json_extract(body,'$.description'),'goal',json_extract(body,'$.goal'),'status',json_extract(body,'$.status'),'revision',json_extract(body,'$.revision'),'createdAt',created_at,'updatedAt',json_extract(body,'$.updatedAt'))"
		}
		return `json_patch(topics.body,COALESCE((SELECT json_object('stop',json_extract(st.body,'$.stop')) FROM session_bindings st
 WHERE st.id=json_extract(topics.body,'$.cancellation.sessionBindingId')),'{}'))`
	case "session-bindings":
		return `CASE WHEN role<>'issue' THEN body ELSE json_set(body,
 '$.startup',json(` + workNotificationProjection("session_bindings", "startEventId") + `),
 '$.reminderScheduled',json(CASE WHEN json_extract(body,'$.reminderEventId') IS NULL THEN 'false' ELSE 'true' END),
 '$.reminder',json(` + workNotificationProjection("session_bindings", "reminderEventId") + `),
 '$.reminderEligibility',CASE
  WHEN state='revoked' THEN 'session_binding_retired'
  WHEN EXISTS (SELECT 1 FROM reports WHERE session_binding_id=session_bindings.id AND ` + reportCurrentSQL + `) THEN 'report_received'
  WHEN json_extract(body,'$.reminderEventId') IS NOT NULL THEN 'already_scheduled'
  WHEN json_extract(body,'$.workObservation.state') IN ('awaiting_receipt','observing') THEN 'awaiting_correlated_end'
  ELSE 'end_unconfirmed' END) END`
	case "reports":
		current := `json_set(reports.body,'$.current',json(CASE WHEN ` + reportCurrentSQL + ` THEN 'true' ELSE 'false' END))`
		if b != nil {
			return `json_remove(` + current + `,'$.sessionBindingId')`
		}
		return current
	case "issues":
		return `json_patch(issues.body,json_object('latestRequirementsNotification',json_remove(` + workNotificationProjection("issues", "latestRequirementsEventId") + `,'$.payload','$.content')))`
	}
	return "body"
}

const agentSourceMessageProjection = `json_object('id',id,'source',source,'threadId',thread_id,
 'content',json(body->'$.content'))`

// Projection placeholders precede filter placeholders in both list and detail queries.
func projectionArgs(kind string, b *SessionBinding) []any {
	if kind == "messages" && b != nil && b.Role == "topic" {
		return []any{b.OwnerID}
	}
	return nil
}
