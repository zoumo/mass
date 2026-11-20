package core

import (
	"context"
	"database/sql"
	"encoding/json"
	"slices"

	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"
)

const traceAssociationSchema = `CREATE TABLE IF NOT EXISTS trace_associations (
 collection TEXT NOT NULL,
 object_id TEXT NOT NULL,
 phase TEXT NOT NULL,
 revision INTEGER NOT NULL,
 traceparent TEXT NOT NULL CHECK (length(traceparent) = 55),
 tracestate TEXT NOT NULL CHECK (length(tracestate) <= 512),
 created_at TEXT NOT NULL,
 PRIMARY KEY (collection, object_id, phase, revision)
) WITHOUT ROWID`

const traceAssociationLimit = 20

type tracePhase struct{ collection, phase string }

var (
	messageAdmission = tracePhase{"messages", "admission"}
	internalEnqueue  = tracePhase{"internal-deliveries", "enqueue"}
	internalDispatch = tracePhase{"internal-deliveries", "dispatch"}
	bindingCreation  = tracePhase{"session-bindings", "creation"}
	timerRevision    = tracePhase{"timers", "revision"}
	executionCreate  = tracePhase{"executions", "creation"}
	outboxEnqueue    = tracePhase{"outbox", "enqueue"}
)

// Only spans created with Core's opted-in provider can populate the table.
type traceAssociationContextKey struct{}

func (p tracePhase) save(ctx context.Context, tx *sql.Tx, id string, revision int) error {
	if ctx.Value(traceAssociationContextKey{}) == nil {
		return nil
	}
	carrier := make(propagation.MapCarrier, 2)
	(propagation.TraceContext{}).Inject(ctx, carrier)
	if carrier.Get("traceparent") == "" {
		return nil
	}
	state := carrier.Get("tracestate")
	if len(state) > 512 {
		state = ""
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO trace_associations
 (collection,object_id,phase,revision,traceparent,tracestate,created_at) VALUES(?,?,?,?,?,?,?)
 ON CONFLICT(collection,object_id,phase,revision) DO NOTHING`, p.collection, id, p.phase, revision, carrier.Get("traceparent"), state, now())
	if err != nil {
		return storageError()
	}
	return nil
}

type traceAssociationReader interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func (p tracePhase) links(ctx context.Context, db traceAssociationReader, links []trace.Link, id string, revision int) []trace.Link {
	if len(links) >= traceAssociationLimit {
		return links
	}
	sc := p.context(ctx, db, id, revision)
	if !sc.IsValid() || slices.ContainsFunc(links, func(link trace.Link) bool {
		return link.SpanContext.TraceID() == sc.TraceID() && link.SpanContext.SpanID() == sc.SpanID()
	}) {
		return links
	}
	return append(links, trace.Link{SpanContext: sc})
}

func (p tracePhase) context(ctx context.Context, db traceAssociationReader, id string, revision int) trace.SpanContext {
	var parent, state string
	err := db.QueryRowContext(ctx, `SELECT traceparent,tracestate FROM trace_associations
 WHERE collection=? AND object_id=? AND phase=? AND revision=?`, p.collection, id, p.phase, revision).Scan(&parent, &state)
	if err != nil {
		return trace.SpanContext{}
	}
	return readTraceContext(parent, state)
}

func readTraceContext(parent, state string) trace.SpanContext {
	carrier := propagation.MapCarrier{"traceparent": parent, "tracestate": state}
	return trace.SpanContextFromContext((propagation.TraceContext{}).Extract(context.Background(), carrier))
}

func addTraceLinks(ctx context.Context, links []trace.Link) {
	span := trace.SpanFromContext(ctx)
	for _, link := range links {
		span.AddLink(link)
	}
}

type observerTraceAssociation struct {
	TraceID string `json:"traceId"`
	Phase   string `json:"phase"`
}

// The Observer detail projection exposes only the identifiers needed for lookup.
func observeTraceAssociations(ctx context.Context, tx *sql.Tx, collection, id string, body json.RawMessage) json.RawMessage {
	switch collection {
	case "messages", "internal-deliveries", "timers", "executions", "outbox":
	default:
		return body
	}
	rows, err := tx.QueryContext(ctx, `SELECT phase,traceparent FROM trace_associations
 WHERE collection=? AND object_id=? ORDER BY phase,revision DESC LIMIT ?`, collection, id, traceAssociationLimit)
	if err != nil {
		return body
	}
	defer func() { _ = rows.Close() }()
	var associations []observerTraceAssociation
	for rows.Next() {
		var phase, parent string
		if err := rows.Scan(&phase, &parent); err != nil {
			return body
		}
		if sc := readTraceContext(parent, ""); sc.IsValid() {
			entry := observerTraceAssociation{TraceID: sc.TraceID().String(), Phase: phase}
			if !slices.Contains(associations, entry) {
				associations = append(associations, entry)
			}
		}
	}
	if rows.Err() != nil || len(associations) == 0 {
		return body
	}
	var record map[string]json.RawMessage
	if json.Unmarshal(body, &record) != nil || record == nil {
		return body
	}
	projection, err := json.Marshal(associations)
	if err != nil {
		return body
	}
	record["traceAssociations"] = projection
	result, err := json.Marshal(record)
	if err != nil {
		return body
	}
	return result
}
