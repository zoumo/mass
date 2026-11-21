package core

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"slices"

	"go.opentelemetry.io/otel/trace"
)

const triggerEventArg = "triggerEventId"

const triggerEventDescription = "For an action caused by an input event, copy its event_id into triggerEventId. Use the event that prompted this action, not a Message ID or a piece of evidence. Omit the field for proactive actions with no triggering event."

func traceRoleTool(tool string) bool {
	switch tool {
	case "route_messages", "create_issue", "start_issue", "submit_report", "update_issue", "update_topic":
		return true
	default:
		return false
	}
}

// Strip tracing metadata before strict decoding and computing business idempotency digests.
func (s *Store) roleTraceInput(ctx context.Context, b Binding, raw []byte, tool string) ([]byte, trace.SpanContext, error) {
	var parent trace.SpanContext
	var fields map[string]json.RawMessage
	if err := decodeStrict(raw, &fields); err != nil {
		return nil, parent, err
	}
	trigger, present := fields[triggerEventArg]
	if !present {
		return raw, parent, nil
	}
	var id string
	if err := json.Unmarshal(trigger, &id); err != nil || id == "" || len(id) > 128 {
		return nil, parent, problem("invalid_argument", "triggerEventId must identify an internal event")
	}
	delete(fields, triggerEventArg)

	var kind, firstReference string
	err := s.db.QueryRowContext(ctx, `SELECT kind,coalesce(json_extract(refs,'$[0].id'),'')
 FROM internal_events WHERE event_id=? AND `+eventRecipientKind+`=? AND `+eventRecipientOwner+`=?
 AND status IN ('dispatching','sent')`, id, b.Role, b.OwnerID).Scan(&kind, &firstReference)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, parent, problem("not_found", "triggering event is unavailable to this role")
	}
	if err != nil {
		return nil, parent, storageError()
	}

	singleSource := true
	if tool == "route_messages" && b.Role == "router" {
		var messages []string
		if err := json.Unmarshal(fields["messageIds"], &messages); err != nil ||
			kind != routeRequestKind || !slices.Contains(messages, firstReference) {
			return nil, parent, problem("invalid_argument", "triggerEventId must identify a route request for a selected Message")
		}
		singleSource = len(messages) == 1
	}
	if u := mcpUsageFrom(ctx); u != nil {
		u.eventID = id
	}
	if s.telemetry.tracesEnabled && singleSource {
		enqueued := internalEnqueue.context(ctx, s.db, id, 0)
		dispatched := internalDispatch.context(ctx, s.db, id, b.Generation)
		// A batch may have its own trace when it has multiple sources.
		// Use the individual event's trace in that case.
		if dispatched.IsValid() {
			parent = dispatched
			if enqueued.IsValid() && dispatched.TraceID() != enqueued.TraceID() {
				parent = enqueued
			}
		}
	}
	args, err := json.Marshal(fields)
	return args, parent, err
}
