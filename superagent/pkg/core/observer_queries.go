package core

import (
	"context"
	"database/sql"
)

type observerHealth struct {
	Status         string         `json:"status"`
	Ingress        string         `json:"ingress"`
	SchemaVersion  int            `json:"schemaVersion"`
	Router         string         `json:"router"`
	TopicStates    map[string]int `json:"topicStates"`
	IssueStates    map[string]int `json:"issueStates"`
	DeliveryStates map[string]int `json:"deliveryStates"`
	OutboxStates   map[string]int `json:"outboxStates"`
	UnknownStops   int            `json:"unknownStops"`
	UnknownEchoes  int            `json:"unknownEchoes"`
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
		{&health.DeliveryStates, `SELECT status,count(*) FROM internal_deliveries GROUP BY status`},
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
		return `json_set(` + full + `, '$.text', substr(outbox.text,1,240))`
	case "issues":
		return `json_remove(` + full + `,'$.goal','$.materials','$.reports','$.resolvedBlockers')`
	case "executions":
		return `json_remove(` + full + `,'$.startup.payload','$.startup.receipt','$.startup.content','$.reminder.payload','$.reminder.receipt','$.reminder.content')`
	case "internal-deliveries":
		return `json_remove(` + full + `,'$.content.body')`
	case "reports":
		return `json_remove(body,'$.details','$.evidence','$.questions')`
	case "timers":
		return `json_set(json_remove(` + full + `,'$.materials'),'$.content',substr(json_extract(body,'$.content'),1,240))`
	default:
		return full
	}
}

const internalDeliveryProjection = `json_object(
 'id',id,'delivery_id',id,'kind',kind,'createdAt',created_at,'status',status,'sentAt',sent_at,
 'sender',json(sender),'recipient',json(recipient),'content',json(content),
 'revision',json_extract(content,'$.refs[0].goal_revision'))`

func workNotificationProjection(table, field string) string {
	return "(SELECT " + internalDeliveryProjection + " FROM internal_deliveries WHERE id=json_extract(" + table + ".body,'$." + field + "'))"
}
