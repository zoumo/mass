package core

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"net/url"
	"strconv"
)

// A Topic timeline reads several independent append sequences. Issue lifecycle
// and blocker resolutions live inside mutable Issue snapshots and have their
// own action timestamps. Capture both fences in the first read transaction.
type topicActivityCursor struct {
	Topic           string `json:"topic"`
	Filter          string `json:"filter"`
	Cutoff          string `json:"cutoff"`
	Issues          int64  `json:"issues"`
	SessionBindings int64  `json:"sessionBindings"`
	InternalEvents  int64  `json:"internalEvents"`
	Reports         int64  `json:"reports"`
	At              string `json:"at"`
	ID              string `json:"id"`
}

const topicMessageEvents = `
 SELECT d.kind, d.created_at, d.event_id, NULL, json_extract(d.sender,'$.kind'),
 json_object('messageId',d.message_id,'title',substr(json_extract(m.body,'$.text'),1,120),
  'status',d.status,'eventId',d.event_id,'returnedEventId',d.returned_event_id)
 FROM routing_events d LEFT JOIN trigger_messages m ON m.id=d.message_id
 WHERE d.topic_id=:topic AND d.seq<=:internalEvents`

const topicIssueEvents = `
 SELECT 'issue_created', i.created_at, i.id AS ref, i.id AS issue_id, 'topic', '{}'
 FROM owned i
 UNION ALL
 SELECT 'issue_session_started', x.created_at, x.id, x.owner_id, json_extract(d.sender,'$.kind'),
 json_object('sessionBindingId',x.id,'goalRevision',json_extract(x.body,'$.startGoalRevision'),
  'eventId',d.event_id,'status',d.status)
 FROM owned i JOIN session_bindings x ON x.role='issue' AND x.owner_id=i.id LEFT JOIN internal_events d ON d.event_id=json_extract(x.body,'$.startEventId')
 WHERE x.seq<=:sessionBindings
 UNION ALL
 SELECT CASE WHEN d.event_id=json_extract(i.body,'$.latestRequirementsEventId') OR d.event_id GLOB 'goal-update_*'
  THEN 'goal_notification' ELSE d.kind END, d.created_at,d.event_id,i.id,json_extract(d.sender,'$.kind'),
 json_object('goalRevision',json_extract(d.refs,'$[0].goal_revision'),'eventId',d.event_id,
  'sessionBindingId',json_extract(d.recipient,'$.session_binding_id'),'status',d.status)
 FROM internal_events d JOIN owned i ON i.id=json_extract(d.refs,'$[0].id')
 WHERE d.rowid<=:internalEvents AND d.kind IN ('issue_session_update','context_supplement')
  AND json_extract(d.refs,'$[0].kind')='issue' AND json_extract(d.recipient,'$.kind')='issue'
 UNION ALL
 SELECT 'report_submitted', json_extract(r.value,'$.createdAt'),json_extract(r.value,'$.id'),i.id,'issue',
 json_object('reportId',json_extract(r.value,'$.id'),'sessionBindingId',json_extract(r.value,'$.sessionBindingId'),
  'historical',json(r.value->'$.historical'),'goalRevision',json_extract(r.value,'$.goalRevision'),
  'eventId',json_extract(r.value,'$.eventId'),
  'summary',substr(json_extract(r.value,'$.summary'),1,160))
 FROM owned i,json_each(i.body,'$.reports') r WHERE json_extract(r.value,'$.sequence')<=:reports
 UNION ALL
 SELECT 'blocker_resolved',json_extract(b.value,'$.resolution.at'),json_extract(b.value,'$.id'),i.id,'topic',
 json_object('blockerId',json_extract(b.value,'$.id'))
 FROM owned i,json_each(i.body,'$.resolvedBlockers') b
 UNION ALL
 SELECT l.key,coalesce(json_extract(l.value,'$.completedAt'),json_extract(l.value,'$.at')),i.id AS ref,i.id AS issue_id,'topic',
 json_object('sessionBindingId',json_extract(l.value,'$.sessionBindingId'))
 FROM owned i,json_each(i.body) l WHERE l.type='object' AND l.key IN ('completion','cancellation','reopening')
 UNION ALL
 SELECT 'issue_snapshot',json_extract(i.body,'$.updatedAt'),i.id AS ref,i.id AS issue_id,NULL,
 json_object('goalRevision',json_extract(i.body,'$.goalRevision'),'issueStatus',json_extract(i.body,'$.status'))
 FROM owned i WHERE json_extract(i.body,'$.updatedAt')<>i.created_at AND (
   json_array_length(i.body,'$.blockers')>0 OR json_extract(i.body,'$.goalRevision')>1
   OR (json_extract(i.body,'$.status')='completed' AND json_extract(i.body,'$.completion') IS NULL)
   OR (json_extract(i.body,'$.status')='canceled' AND json_extract(i.body,'$.cancellation') IS NULL))`

func (s *Store) topicActivity(ctx context.Context, q url.Values) (Page, error) {
	p := Page{Items: []json.RawMessage{}}
	topic, filter := q.Get("topicId"), q.Get("filter")
	if filter == "" {
		filter = "all"
	}
	if topic == "" || q.Get("messageId") != "" || q.Get("threadKey") != "" || q.Get("issueId") != "" || (filter != "all" && filter != "message" && filter != "issue") || (q.Get("order") != "" && q.Get("order") != "newest") {
		return p, problem("invalid_argument", "topic-activity requires a topicId, a valid filter and newest order")
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
	var exists bool
	if err = tx.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM topics WHERE id=?)", topic).Scan(&exists); err != nil {
		return p, storageError()
	}
	if !exists {
		return p, problem("not_found", "Topic not found")
	}
	c := topicActivityCursor{Topic: topic, Filter: filter, Cutoff: now()}
	if raw := q.Get("cursor"); raw != "" {
		data, err := base64.RawURLEncoding.DecodeString(raw)
		var read topicActivityCursor
		if err != nil || json.Unmarshal(data, &read) != nil || read.Topic != topic || read.Filter != filter || read.At == "" || read.ID == "" || read.Cutoff == "" {
			return p, problem("invalid_argument", "invalid or mismatched Topic activity cursor")
		}
		c = read
	} else {
		err = tx.QueryRowContext(ctx, `SELECT
 (SELECT coalesce(max(seq),0) FROM issues WHERE topic_id=?1),
 (SELECT coalesce(max(seq),0) FROM session_bindings),
 (SELECT coalesce(max(rowid),0) FROM internal_events),
 (SELECT coalesce(max(json_extract(r.value,'$.sequence')),0) FROM issues i,json_each(i.body,'$.reports') r WHERE i.topic_id=?1)`, topic).Scan(&c.Issues, &c.SessionBindings, &c.InternalEvents, &c.Reports)
		if err != nil {
			return p, storageError()
		}
	}
	events := topicIssueEvents
	switch filter {
	case "all":
		events = topicMessageEvents + " UNION ALL " + topicIssueEvents
	case "message":
		events = topicMessageEvents
	}
	const body = `json_patch(json_object('id',event_id,'kind',kind,'at',at,'actor',actor,'issueId',issue_id,
  'title',title),extra)`
	projection := body
	if s.hasTraceAssociations {
		projection = `json_set(` + body + `,'$.traceAssociations',json(CASE
 WHEN kind='issue_created' THEN ` + observerTraceList("issues", "page.ref") + `
 WHEN kind='issue_session_started' AND json_extract(extra,'$.eventId') IS NULL THEN ` + observerTraceList("session-bindings", "page.ref") + `
 WHEN kind IN ('completion','cancellation','reopening') THEN ` + issueLifecycleTraces("page.issue_id", "page.at") + `
 ELSE ` + observerTraceList("internal-events", "json_extract(page.extra,'$.eventId')") + ` END))`
	}
	query := `WITH owned AS (SELECT id,body,created_at FROM issues WHERE topic_id=:topic AND seq<=:issues),
 events(kind,at,ref,issue_id,actor,extra) AS (` + events + `),
 activity AS (SELECT *,kind||'/'||ref||'/'||at AS event_id FROM events),
 page AS (SELECT activity.*,json_extract(i.body,'$.title') AS title FROM activity LEFT JOIN owned i ON i.id=activity.issue_id
 WHERE at IS NOT NULL AND at<>'' AND at<=:cutoff AND (:afterAt='' OR (at,event_id)<(:afterAt,:afterID))
 ORDER BY at DESC,event_id DESC LIMIT :limit)
 SELECT event_id,at,` + projection + ` FROM page ORDER BY at DESC,event_id DESC`
	args := []any{sql.Named("topic", topic), sql.Named("issues", c.Issues), sql.Named("cutoff", c.Cutoff), sql.Named("afterAt", c.At), sql.Named("afterID", c.ID), sql.Named("limit", limit+1), sql.Named("internalEvents", c.InternalEvents)}
	if filter != "message" {
		args = append(args, sql.Named("sessionBindings", c.SessionBindings), sql.Named("reports", c.Reports))
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
		if err = rows.Scan(&c.ID, &c.At, &raw); err != nil {
			return p, storageError()
		}
		p.Items = append(p.Items, raw)
	}
	if rows.Err() != nil {
		return p, storageError()
	}
	return p, nil
}
