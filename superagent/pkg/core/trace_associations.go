package core

import (
	"context"
	"database/sql"
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
	internalEnqueue  = tracePhase{"internal-events", "enqueue"}
	internalDispatch = tracePhase{"internal-events", "dispatch"}
	bindingCreation  = tracePhase{"session-bindings", "creation"}
	timerRevision    = tracePhase{"timers", "revision"}
	issueCreation    = tracePhase{"issues", "creation"}
	issueLifecycle   = tracePhase{"issue-lifecycle", "transition"}
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

// Expressions are built from fixed collection names and internal SQL columns.
// Look up associations through their primary key inside the page query.
func observerTraceProjection(collection, id, body string) string {
	association := ""
	switch collection {
	case "routing-events":
		collection = "internal-events"
	case "reports":
		collection, id = "internal-events", "json_extract("+body+",'$.eventId')"
	case "issues":
		association = issueCreationTraces(id)
	case "messages", "internal-events", "timers", "executions", "outbox":
	default:
		return body
	}
	if association == "" {
		association = observerTraceList(collection, id)
	}
	return "json_set(" + body + ",'$.traceAssociations',json(" + association + "))"
}

func observerTraceList(collection, id string) string {
	return `(SELECT json_group_array(json_object('phase',phase,'traceId',trace_id)) FROM (
 SELECT DISTINCT phase,substr(traceparent,4,32) AS trace_id FROM trace_associations
 WHERE collection='` + collection + `' AND object_id=` + id + ` ORDER BY phase,revision DESC LIMIT 20))`
}

func issueCreationTraces(id string) string {
	// Existing Issues may predate the transactional creation association.
	// A successful, state-changing create_issue on the target proves creation.
	return `coalesce(nullif(` + observerTraceList("issues", id) + `,'[]'),
 (SELECT json_array(json_object('phase','creation','traceId',trace_id)) FROM mcp_tool_calls
 WHERE target_issue_id=` + id + ` AND caller_role='topic' AND tool='create_issue' AND changed=1
 AND length(trace_id)=32 ORDER BY seq LIMIT 1),'[]')`
}

func issueLifecycleTraces(id, at string) string {
	return observerTraceList("issue-lifecycle", "("+id+"||'/'||"+at+")")
}

func observerIssueActivityTraces(body, id string) string {
	// The activity uses the transition timestamp stored in the same transaction.
	projection := "json_set(" + body + ",'$.lifecycleTraces',json_object("
	for index, transition := range []struct{ kind, at string }{{"completion", "completedAt"}, {"cancellation", "at"}, {"reopening", "at"}} {
		if index != 0 {
			projection += ","
		}
		at := "json_extract(" + body + ",'$." + transition.kind + "." + transition.at + "')"
		projection += "'" + transition.kind + "',json(" + issueLifecycleTraces(id, at) + ")"
	}
	return projection + `),'$.reports',json((SELECT json_group_array(json(` + observerTraceProjection("reports", "", "report.value") + `))
 FROM json_each(issues.body,'$.reports') report)))`
}
