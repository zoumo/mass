package core

import (
	"context"
	"database/sql"
)

type observerHealth struct {
	Status        string         `json:"status"`
	Ingress       string         `json:"ingress"`
	SchemaVersion int            `json:"schemaVersion"`
	Router        string         `json:"router"`
	TopicStates   map[string]int `json:"topicStates"`
	IssueStates   map[string]int `json:"issueStates"`
	EventStates   map[string]int `json:"eventStates"`
	OutboxStates  map[string]int `json:"outboxStates"`
	UnknownStops  int            `json:"unknownStops"`
	UnknownEchoes int            `json:"unknownEchoes"`
}

func (s *Store) observerHealth(ctx context.Context) (observerHealth, error) {
	health := observerHealth{Status: "ready", Ingress: "ready", SchemaVersion: schemaVersion}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return health, storageError()
	}
	defer func() { _ = tx.Rollback() }()
	err = tx.QueryRowContext(ctx, `SELECT
 COALESCE((SELECT state FROM session_bindings WHERE role='router' AND state<>'revoked'),'not_started'),
 (SELECT count(*) FROM session_bindings WHERE json_extract(body,'$.stop.workState')='unknown'),
 (SELECT count(*) FROM trigger_messages WHERE json_extract(body,'$.echo.status')='echo_unknown')`).
		Scan(&health.Router, &health.UnknownStops, &health.UnknownEchoes)
	if err != nil {
		return health, storageError()
	}
	for _, group := range []struct {
		counts *map[string]int
		query  string
	}{
		{&health.TopicStates, `SELECT json_extract(body,'$.status') AS state,count(*) FROM topics GROUP BY state`},
		{&health.IssueStates, `SELECT json_extract(body,'$.status') AS state,count(*) FROM issues GROUP BY state`},
		{&health.EventStates, `SELECT status,count(*) FROM internal_events GROUP BY status`},
		{&health.OutboxStates, `SELECT status AS state,count(*) FROM outbox GROUP BY status`},
	} {
		*group.counts, err = observerCounts(ctx, tx, group.query)
		if err != nil {
			return health, storageError()
		}
	}
	return health, nil
}

func observerCounts(ctx context.Context, tx *sql.Tx, query string) (map[string]int, error) {
	rows, err := tx.QueryContext(ctx, query)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	counts := make(map[string]int)
	for rows.Next() {
		var state string
		var count int
		if err := rows.Scan(&state, &count); err != nil {
			return nil, err
		}
		counts[state] = count
	}
	return counts, rows.Err()
}

// Browser lists are previews. Full text and causal evidence are retrieved by ID.
func observerListProjection(kind, full string) string {
	switch kind {
	case "messages":
		return `json_set(json_remove(body,'$.meta','$.quotedMessage','$.channel'), '$.text', substr(json_extract(body,'$.text'),1,240))`
	case "outbox":
		return `json_set(json_remove(` + full + `,'$.content'), '$.preview',
 substr(COALESCE(json_extract(outbox.content,'$.data.text'),json_extract(outbox.content,'$.data.prompt')),1,240))`
	case "issues":
		return `json_remove(` + full + `,'$.goal','$.materials','$.reports','$.resolvedBlockers')`
	case "session-bindings":
		return `json_remove(` + full + `,'$.startup.payload','$.startup.receipt','$.startup.content','$.reminder.payload','$.reminder.receipt','$.reminder.content')`
	case "internal-events":
		return `json_set(json_remove(` + full + `,'$.body','$.content'),'$.goalUpdate',json(CASE WHEN kind='issue_session_update' AND event_id GLOB 'goal-update_*' THEN 'true' ELSE 'false' END))`
	case "reports":
		return `json_remove(` + full + `,'$.details','$.evidence','$.questions')`
	case "timers":
		return `json_set(json_remove(` + full + `,'$.materials'),'$.content',substr(json_extract(body,'$.content'),1,240))`
	default:
		return full
	}
}

const internalEventProjection = `json_patch(json_object(
 'event_id',event_id,'kind',kind,'createdAt',created_at,'status',status,'sentAt',sent_at,
 'sender',json(sender),'recipient',json(recipient),'refs',json(refs),
 'revision',json_extract(refs,'$[0].goal_revision')),
 json_object('content',json(content),'body',json(body)))`

func workNotificationProjection(table, field string) string {
	return "(SELECT " + internalEventProjection + " FROM internal_events WHERE event_id=json_extract(" + table + ".body,'$." + field + "'))"
}
