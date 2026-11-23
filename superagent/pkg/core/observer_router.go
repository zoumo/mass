package core

import (
	"context"
	"database/sql"
	"fmt"
)

// Freeze delivery membership before ranking. A return can promote a Message
// that was already present when the first page was read.
const routerMessagePage = `
WITH ranked AS (
 SELECT d.message_id,d.event_id,d.created_at,
 row_number() OVER (PARTITION BY d.message_id ORDER BY d.created_at DESC,d.seq DESC) AS position
 FROM routing_events d
 WHERE d.seq<=?1 AND (json_extract(d.recipient,'$.kind')='router' OR json_extract(d.sender,'$.kind')='router')
 %s
), page AS (
 SELECT r.message_id,r.event_id,r.created_at,trigger_messages.source,trigger_messages.routing_state,
 substr(` + observerMessagePreview + `,1,240) AS preview
 FROM ranked r JOIN trigger_messages ON trigger_messages.id=r.message_id
 WHERE position=1 AND (?2='' OR (r.created_at,r.message_id)<(?2,?3))
 ORDER BY r.created_at DESC,r.message_id DESC LIMIT ?4
)
SELECT json_object(
 'id',p.message_id,'text',p.preview,'source',p.source,'state',p.routing_state,
 'activityAt',p.created_at,'activityId',p.event_id,'activityKind',a.kind,'returnedEventId',a.returned_event_id,
 'activityReason',substr(json_extract(a.body,'$'),1,240),'activityTopicId',a.topic_id,
 'activityTopicTitle',json_extract(t.body,'$.title'),
 'targets',json((SELECT json_group_array(json_object('id',d.event_id,'topicId',d.topic_id,
 'title',json_extract(target.body,'$.title'),'reason',substr(json_extract(d.body,'$'),1,240)) ORDER BY d.created_at,d.seq)
 FROM routing_events d LEFT JOIN topics target ON target.id=d.topic_id
 WHERE d.message_id=p.message_id AND ` + activeRoute + `))),
 p.created_at,p.message_id
FROM page p JOIN routing_events a ON a.event_id=p.event_id
LEFT JOIN topics t ON t.id=a.topic_id
ORDER BY p.created_at DESC,p.message_id DESC`

func (s *Store) observerRouterMessages(ctx context.Context, q QueryRequest) (Page, error) {
	if q.Limit < 1 || q.Limit > 200 || q.ThreadKey != "" || q.IssueID != "" || q.TopicID != "" {
		return Page{}, problem("invalid_argument", "router-messages accepts a limit between 1 and 200 and an optional messageId")
	}
	c := cursor{Kind: "router-messages", Parent: q.MessageID, Newest: true}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return Page{}, storageError()
	}
	defer func() { _ = tx.Rollback() }()
	if q.Cursor == "" {
		if err := tx.QueryRowContext(ctx, "SELECT coalesce(max(rowid),0) FROM internal_events").Scan(&c.Upper); err != nil {
			return Page{}, storageError()
		}
	} else if err := c.resume(q.Cursor); err != nil {
		return Page{}, err
	}
	args := []any{c.Upper, c.At, c.ID, q.Limit + 1}
	filter := ""
	if q.MessageID != "" {
		filter = "AND d.message_id=?5"
		args = append(args, q.MessageID)
	}
	return readListPage(ctx, tx, fmt.Sprintf(routerMessagePage, filter), args, q.Limit, c)
}
