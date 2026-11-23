package core

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"net/url"
	"strconv"
	"strings"
)

// Both the sidebar and timeline use these same facts. Scope and sequence
// fences apply before filtering, ranking and pagination.
const conversationEvents = `
 SELECT d.topic_id,
 CASE json_extract(d.sender,'$.kind') WHEN 'router' THEN 'router_routed' ELSE 'thread_direct' END,
 d.created_at,d.event_id,
 json_object('actor',json_extract(d.sender,'$.kind'),'recipient','topic',
  'messageId',d.message_id,'eventId',d.event_id,'threadKey',trigger_messages.thread_key,
  'summary',substr(` + observerMessagePreview + `,1,240),'status',d.status)
 FROM routing_events d LEFT JOIN trigger_messages ON trigger_messages.id=d.message_id
 WHERE d.kind='routed_message' AND json_extract(d.recipient,'$.kind')='topic'
  AND json_extract(d.sender,'$.kind') IN ('router','core') AND d.seq<=:internalEvents
  AND (:topic='' OR d.topic_id=:topic)
 UNION ALL
 SELECT i.topic_id,'issue_created',i.created_at,i.id,
 json_object('actor','topic','issueId',i.id,'title',json_extract(i.body,'$.title'))
 FROM issues i WHERE i.seq<=:issues AND (:topic='' OR i.topic_id=:topic)
 UNION ALL
 SELECT i.topic_id,'issue_session_dispatch',d.created_at,d.event_id,
 json_object('actor',json_extract(d.sender,'$.kind'),'recipient','issue','eventId',d.event_id,'issueId',i.id,
  'sessionBindingId',b.id,'title',json_extract(i.body,'$.title'),
  'summary',substr(coalesce(json_extract(d.content,'$.data.text'),json_extract(d.content,'$.data.prompt'),
    json_extract(d.body,'$.instruction')),1,240),
  'content',json(d.content),
  'status',d.status,
  'dispatchKind',CASE WHEN d.kind='issue_session_update' AND d.event_id GLOB 'goal-update_*' THEN 'goal_notification' ELSE d.kind END)
 FROM internal_events d
 JOIN session_bindings b ON b.role='issue' AND b.id=json_extract(d.recipient,'$.session_binding_id')
 JOIN issues i ON i.id=b.owner_id AND i.id=json_extract(d.refs,'$[0].id')
 WHERE d.kind IN ('issue_session_started','issue_session_update','context_supplement')
  AND ((json_extract(d.sender,'$.kind')='topic' AND json_extract(d.sender,'$.topic_id')=i.topic_id)
   OR (json_extract(d.sender,'$.kind')='core' AND d.kind='issue_session_started'))
  AND json_extract(d.recipient,'$.kind')='issue' AND json_extract(d.refs,'$[0].kind')='issue'
  AND d.rowid<=:internalEvents AND i.seq<=:issues AND (:topic='' OR i.topic_id=:topic)
 UNION ALL
 SELECT i.topic_id,'report',r.created_at,r.id,
 json_object('actor','issue','recipient','topic','reportId',r.id,'issueId',i.id,
  'sessionBindingId',r.session_binding_id,'historical',json(r.body->'$.historical'),
  'title',json_extract(i.body,'$.title'),'reportKind',json_extract(r.body,'$.kind'),
  'summary',substr(json_extract(r.body,'$.summary'),1,240),'eventId',json_extract(r.body,'$.eventId'))
 FROM issues i JOIN reports r ON r.issue_id=i.id
 WHERE r.seq<=:reports AND i.seq<=:issues
  AND (:topic='' OR i.topic_id=:topic)`

const conversationFacts = `WITH events(topic_id,kind,at,ref,detail) AS (` + conversationEvents + `),
 facts AS (SELECT *,kind||'/'||ref AS event_id FROM events)`

const conversationPage = conversationFacts + `
 , page AS (SELECT * FROM facts WHERE (:filter='all' OR kind=:filter) AND (:event='' OR event_id=:event)
  AND (:afterAt='' OR (at,event_id)<(:afterAt,:afterID))
 ORDER BY at DESC,event_id DESC LIMIT :limit)`

const (
	conversationBody     = `json_patch(json_object('id',event_id,'kind',kind,'topicId',topic_id,'at',at),detail)`
	conversationTimeline = conversationPage + ` SELECT ` + conversationBody + `,at,event_id FROM page ORDER BY at DESC,event_id DESC`
)

var (
	issueCreationTracesSQL         = observerTraceList("issues", "page.ref")
	conversationEventTracesSQL     = observerTraceList("internal-events", "json_extract(page.detail,'$.eventId')")
	conversationTimelineWithTraces = conversationPage + `
 SELECT json_set(` + conversationBody + `,
  '$.traceAssociations',json(CASE WHEN kind='issue_created' THEN ` + issueCreationTracesSQL + `
  ELSE ` + conversationEventTracesSQL + ` END)),at,event_id
 FROM page ORDER BY at DESC,event_id DESC`
)

const conversationTopics = conversationFacts + `,
 recent AS (SELECT topic_id,max(at) AS last_message_at FROM facts WHERE kind<>'issue_created' GROUP BY topic_id),
 ranked AS (
  SELECT t.id,t.body,t.created_at,r.last_message_at,
   CASE WHEN r.last_message_at IS NULL THEN '0'||t.created_at ELSE '1'||r.last_message_at END AS sort_at
  FROM topics t LEFT JOIN recent r ON r.topic_id=t.id WHERE t.seq<=:topics
   AND (:search='' OR instr(lower(json_extract(t.body,'$.title')),:search)>0 OR instr(lower(t.id),:search)>0)
 )
 SELECT json_patch(json_object('id',id,'title',json_extract(body,'$.title'),
  'status',json_extract(body,'$.status'),'createdAt',created_at),json_object('lastMessageAt',last_message_at)),sort_at,id
 FROM ranked WHERE (:afterAt='' OR (sort_at,id)<(:afterAt,:afterID))
 ORDER BY sort_at DESC,id DESC LIMIT :limit`

type conversationScope struct {
	Topic  string `json:"topic,omitempty"`
	Search string `json:"search,omitempty"`
	Filter string `json:"filter"`
}

type conversationCursor struct {
	conversationScope
	Topics         int64  `json:"topics"`
	Issues         int64  `json:"issues"`
	InternalEvents int64  `json:"internalEvents"`
	Reports        int64  `json:"reports"`
	At             string `json:"at"`
	ID             string `json:"id"`
}

func (s *Store) topicConversation(ctx context.Context, q url.Values) (Page, error) {
	p := Page{Items: []json.RawMessage{}}
	scope := conversationScope{Topic: q.Get("topicId"), Search: strings.ToLower(strings.TrimSpace(q.Get("search"))), Filter: q.Get("filter")}
	if scope.Filter == "" {
		scope.Filter = "all"
	}
	event := q.Get("eventId")
	cursorToken := q.Get("cursor")
	switch scope.Filter {
	case "all", "router_routed", "thread_direct", "issue_created", "issue_session_dispatch", "report":
	default:
		return p, problem("invalid_argument", "invalid Topic conversation filter")
	}
	if (scope.Topic == "" && (event != "" || scope.Filter != "all")) || (scope.Topic != "" && scope.Search != "") ||
		(event != "" && (cursorToken != "" || scope.Filter != "all")) {
		return p, problem("invalid_argument", "invalid Topic conversation scope or filter")
	}
	limit := 20
	if raw := q.Get("limit"); raw != "" {
		var err error
		limit, err = strconv.Atoi(raw)
		if err != nil || limit < 1 || limit > 200 {
			return p, problem("invalid_argument", "limit must be between 1 and 200")
		}
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return p, storageError()
	}
	defer func() { _ = tx.Rollback() }()
	c := conversationCursor{conversationScope: scope}
	if cursorToken != "" {
		var next conversationCursor
		data, err := base64.RawURLEncoding.DecodeString(cursorToken)
		if err != nil || json.Unmarshal(data, &next) != nil || next.conversationScope != scope || next.At == "" || next.ID == "" {
			return p, problem("invalid_argument", "invalid or mismatched Topic conversation cursor")
		}
		c = next
	} else if err := tx.QueryRowContext(ctx, `SELECT
 (SELECT coalesce(max(seq),0) FROM topics),
 (SELECT coalesce(max(seq),0) FROM issues),
 (SELECT coalesce(max(rowid),0) FROM internal_events),
 (SELECT coalesce(max(seq),0) FROM reports)`).Scan(&c.Topics, &c.Issues, &c.InternalEvents, &c.Reports); err != nil {
		return p, storageError()
	}
	query := conversationTopics
	args := []any{
		sql.Named("topic", scope.Topic), sql.Named("issues", c.Issues), sql.Named("internalEvents", c.InternalEvents),
		sql.Named("reports", c.Reports), sql.Named("afterAt", c.At), sql.Named("afterID", c.ID), sql.Named("limit", limit+1),
	}
	if scope.Topic == "" {
		args = append(args, sql.Named("topics", c.Topics), sql.Named("search", scope.Search))
	} else {
		var exists bool
		if err := tx.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM topics WHERE id=?)", scope.Topic).Scan(&exists); err != nil {
			return p, storageError()
		}
		if !exists {
			return p, problem("not_found", "Topic not found")
		}
		query = conversationTimeline
		if s.hasTraceAssociations {
			query = conversationTimelineWithTraces
		}
		args = append(args, sql.Named("filter", scope.Filter), sql.Named("event", event))
	}
	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		return p, storageError()
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		if len(p.Items) == limit {
			data, err := json.Marshal(c)
			if err != nil {
				return p, storageError()
			}
			p.NextCursor = base64.RawURLEncoding.EncodeToString(data)
			break
		}
		var raw []byte
		if err := rows.Scan(&raw, &c.At, &c.ID); err != nil {
			return p, storageError()
		}
		p.Items = append(p.Items, raw)
	}
	if err := rows.Err(); err != nil {
		return p, storageError()
	}
	if event != "" && len(p.Items) == 0 {
		return p, problem("not_found", "Topic conversation event not found")
	}
	return p, nil
}
