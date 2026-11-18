package core

import (
	"context"
	"database/sql"
)

type observerHealth struct {
	Status             string         `json:"status"`
	Ingress            string         `json:"ingress"`
	SchemaVersion      int            `json:"schemaVersion"`
	Router             string         `json:"router"`
	ChannelStates      map[string]int `json:"channelStates"`
	ChannelConnections map[string]int `json:"channelConnections"`
	ChannelAdmissions  map[string]int `json:"channelAdmissions"`
	TopicStates        map[string]int `json:"topicStates"`
	IssueStates        map[string]int `json:"issueStates"`
	DeliveryStates     map[string]int `json:"deliveryStates"`
	OutboxStates       map[string]int `json:"outboxStates"`
	UnboundEvents      int            `json:"unboundEvents"`
	UnknownStops       int            `json:"unknownStops"`
	UnknownEchoes      int            `json:"unknownEchoes"`
}

func (s *Store) observerHealth(ctx context.Context) (observerHealth, error) {
	health := observerHealth{Status: "ready", Ingress: "ready", SchemaVersion: schemaVersion}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return health, storageError()
	}
	defer func() { _ = tx.Rollback() }()
	err = tx.QueryRowContext(ctx, `SELECT
 COALESCE((SELECT state FROM bindings WHERE role='router' AND state<>'revoked'),'not_started'),
 (SELECT count(*) FROM events e WHERE NOT e.invalidated AND NOT EXISTS (SELECT 1 FROM deliveries d WHERE d.event_id=e.id)),
 (SELECT count(*) FROM binding_stops WHERE json_extract(body,'$.workState')='unknown'),
 (SELECT count(*) FROM messages WHERE json_extract(body,'$.echo.status')='echo_unknown')`).
		Scan(&health.Router, &health.UnboundEvents, &health.UnknownStops, &health.UnknownEchoes)
	if err != nil {
		return health, storageError()
	}
	for _, group := range []struct {
		counts *map[string]int
		query  string
	}{
		{&health.ChannelStates, `SELECT json_extract(` + channelHealthProjection + `,'$.status') AS state,count(*) FROM channel_health GROUP BY state`},
		{&health.ChannelConnections, `SELECT json_extract(body,'$.connection') AS state,count(*) FROM channel_health WHERE julianday(expires_at)>julianday('now') GROUP BY state`},
		{&health.ChannelAdmissions, `SELECT json_extract(body,'$.admission') AS state,count(*) FROM channel_health WHERE julianday(expires_at)>julianday('now') GROUP BY state`},
		{&health.TopicStates, `SELECT json_extract(body,'$.status') AS state,count(*) FROM topics GROUP BY state`},
		{&health.IssueStates, `SELECT json_extract(body,'$.status') AS state,count(*) FROM issues GROUP BY state`},
		{&health.DeliveryStates, `SELECT status,count(*) FROM deliveries GROUP BY status
 UNION ALL SELECT 'needsAttention',count(*) FROM deliveries WHERE status NOT IN ('delivered','superseded') AND (` + deliveryAttention + `)`},
		{&health.OutboxStates, `SELECT json_extract(body,'$.status') AS state,count(*) FROM outbox GROUP BY state`},
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
		return `json_set(json_remove(body,'$.channel'), '$.text', substr(json_extract(body,'$.text'),1,240))`
	case "events":
		return `json_remove(` + full + `,'$.payload','$.receipt')`
	case "issues":
		return `json_remove(` + full + `,'$.requirements','$.materials')`
	case "executions":
		return `json_remove(` + full + `,'$.startup.payload','$.startup.receipt','$.reminder.payload','$.reminder.receipt')`
	case "reports":
		return `json_remove(body,'$.details','$.evidence','$.questions')`
	case "timers":
		return `json_set(json_remove(` + full + `,'$.materials'),'$.content',substr(json_extract(body,'$.content'),1,240))`
	default:
		return full
	}
}
