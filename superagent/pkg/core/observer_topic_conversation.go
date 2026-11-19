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
 d.created_at,d.id,
 json_object('actor',json_extract(d.sender,'$.kind'),'recipient','topic',
  'messageId',d.message_id,'deliveryId',d.id,'threadKey',m.thread_key,
  'summary',substr(json_extract(m.body,'$.text'),1,240),'status',d.status)
 FROM routing_deliveries d LEFT JOIN trigger_messages m ON m.id=d.message_id
 WHERE d.kind='routed_message' AND json_extract(d.recipient,'$.kind')='topic'
  AND json_extract(d.sender,'$.kind') IN ('router','core') AND d.seq<=:deliveries
  AND (:topic='' OR d.topic_id=:topic)
 UNION ALL
 SELECT i.topic_id,'issue_created',i.created_at,i.id,
 json_object('actor','topic','issueId',i.id,'title',json_extract(i.body,'$.title'))
 FROM issues i WHERE i.seq<=:issues AND (:topic='' OR i.topic_id=:topic)
 UNION ALL
 SELECT i.topic_id,'worker_dispatch',d.created_at,d.id,
 json_object('actor','topic','recipient','worker','deliveryId',d.id,'issueId',i.id,
  'executionId',x.id,'generation',x.generation,'title',json_extract(i.body,'$.title'),
  'summary',substr(coalesce(json_extract(d.content,'$.data.text'),json_extract(d.content,'$.data.prompt'),
    json_extract(d.body,'$.instruction')),1,240),
  'status',d.status,
  'dispatchKind',CASE WHEN d.kind='worker_update' AND d.id GLOB 'goal-update_*' THEN 'goal_notification' ELSE d.kind END)
 FROM internal_deliveries d
 JOIN executions x ON x.id=json_extract(d.recipient,'$.execution_id')
 JOIN issues i ON i.id=x.issue_id AND i.id=json_extract(d.refs,'$[0].id')
 WHERE d.kind IN ('worker_started','worker_update','context_supplement')
  AND json_extract(d.sender,'$.kind')='topic' AND json_extract(d.sender,'$.topic_id')=i.topic_id
  AND json_extract(d.recipient,'$.kind')='worker' AND json_extract(d.refs,'$[0].kind')='issue'
  AND d.rowid<=:deliveries AND i.seq<=:issues AND (:topic='' OR i.topic_id=:topic)
 UNION ALL
 SELECT i.topic_id,'report',r.created_at,r.id,
 json_object('actor','worker','recipient','topic','reportId',r.id,'issueId',i.id,
  'executionId',r.execution_id,'generation',json_extract(r.body,'$.generation'),
  'title',json_extract(i.body,'$.title'),'reportKind',json_extract(r.body,'$.kind'),
  'summary',substr(json_extract(r.body,'$.summary'),1,240),'deliveryId',json_extract(r.body,'$.eventId'))
 FROM issues i JOIN reports r ON r.issue_id=i.id
 WHERE r.seq<=:reports AND i.seq<=:issues AND json_extract(r.body,'$.historical')=0
  AND (:topic='' OR i.topic_id=:topic)`

const conversationFacts = `WITH events(topic_id,kind,at,ref,detail) AS (` + conversationEvents + `),
 facts AS (SELECT *,kind||'/'||ref AS event_id FROM events)`

const conversationTimeline = conversationFacts + `
 SELECT json_patch(json_object('id',event_id,'kind',kind,'topicId',topic_id,'at',at),detail),at,event_id
 FROM facts WHERE (:filter='all' OR kind=:filter) AND (:event='' OR event_id=:event)
  AND (:afterAt='' OR (at,event_id)<(:afterAt,:afterID))
 ORDER BY at DESC,event_id DESC LIMIT :limit`

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
	Topics     int64  `json:"topics"`
	Issues     int64  `json:"issues"`
	Deliveries int64  `json:"deliveries"`
	Reports    int64  `json:"reports"`
	At         string `json:"at"`
	ID         string `json:"id"`
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
	case "all", "router_routed", "thread_direct", "issue_created", "worker_dispatch", "report":
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
 (SELECT coalesce(max(rowid),0) FROM internal_deliveries),
 (SELECT coalesce(max(seq),0) FROM reports)`).Scan(&c.Topics, &c.Issues, &c.Deliveries, &c.Reports); err != nil {
		return p, storageError()
	}
	query := conversationTopics
	args := []any{
		sql.Named("topic", scope.Topic), sql.Named("issues", c.Issues), sql.Named("deliveries", c.Deliveries),
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
