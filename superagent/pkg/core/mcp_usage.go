package core

import (
	"context"
	"errors"
	"log/slog"
	"net/url"
	"strconv"
	"time"

	rpc2 "github.com/sourcegraph/jsonrpc2"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"
)

const mcpUsagePath = "/v1/observer/mcp-tool-usage"

const mcpUsageSchema = `
CREATE TABLE IF NOT EXISTS mcp_tool_calls (
 seq INTEGER PRIMARY KEY, at TEXT NOT NULL, caller_role TEXT NOT NULL,
 caller_id TEXT NOT NULL, topic_id TEXT NOT NULL, issue_id TEXT NOT NULL,
 session_binding_id TEXT NOT NULL, target_issue_id TEXT NOT NULL,
 event_id TEXT NOT NULL, tool TEXT NOT NULL, action TEXT NOT NULL,
 outcome TEXT NOT NULL, duration_ns INTEGER NOT NULL, trace_id TEXT NOT NULL,
 span_id TEXT NOT NULL, replayed INTEGER NOT NULL, changed INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS mcp_tool_calls_at ON mcp_tool_calls(at);
CREATE INDEX IF NOT EXISTS mcp_tool_calls_caller ON mcp_tool_calls(caller_role,caller_id,at);
CREATE INDEX IF NOT EXISTS mcp_tool_calls_session_binding ON mcp_tool_calls(session_binding_id,at);
CREATE INDEX IF NOT EXISTS mcp_tool_calls_topic ON mcp_tool_calls(topic_id,at);
CREATE INDEX IF NOT EXISTS mcp_tool_calls_issue ON mcp_tool_calls(issue_id,at);
CREATE INDEX IF NOT EXISTS mcp_tool_calls_target_issue ON mcp_tool_calls(target_issue_id,at);
`

type mcpUsageContextKey struct{}

type mcpUsage struct {
	role, callerID, topicID, issueID, sessionBindingID string
	targetIssueID, eventID, action                     string
	replayed, changed, noChange                        bool
}

func newMCPUsage(b SessionBinding) *mcpUsage {
	u := &mcpUsage{role: b.Role, callerID: b.OwnerID, sessionBindingID: b.ID}
	switch b.Role {
	case "topic":
		u.topicID = b.OwnerID
	case "issue":
		u.topicID, u.issueID = b.TopicID, b.OwnerID
	}
	return u
}

func mcpUsageFrom(ctx context.Context) *mcpUsage {
	u, _ := ctx.Value(mcpUsageContextKey{}).(*mcpUsage)
	return u
}

func markMCPIssueTarget(ctx context.Context, issueID string) {
	if u := mcpUsageFrom(ctx); u != nil && u.role == "topic" {
		u.targetIssueID = issueID
	}
}

func markMCPMutation(ctx context.Context, replayed, changed bool) {
	if u := mcpUsageFrom(ctx); u != nil {
		u.replayed, u.changed = replayed, changed && !u.noChange
	}
}

func mcpOutcome(err error) string {
	if err == nil {
		return "ok"
	}
	var business *Error
	if errors.As(err, &business) {
		return business.Code
	}
	var protocol *rpc2.Error
	if errors.As(err, &protocol) {
		return "invalid_argument"
	}
	return "unavailable"
}

func (s *Store) recordMCPTool(ctx context.Context, start time.Time, usage *mcpUsage, tool string, err error) {
	duration, outcome := time.Since(start), mcpOutcome(err)
	attrs := []attribute.KeyValue{
		attribute.String("superagent.role", usage.role),
		attribute.String("superagent.tool", tool),
		attribute.String("superagent.outcome", outcome),
	}
	if usage.action != "" {
		attrs = append(attrs, attribute.String("superagent.action", usage.action))
	}
	s.telemetry.mcpToolDuration.Record(ctx, duration.Seconds(), metric.WithAttributes(attrs...))
	span := trace.SpanFromContext(ctx)
	span.SetAttributes(attrs...)
	for key, value := range map[string]string{
		"superagent.caller.id":          usage.callerID,
		"superagent.topic.id":           usage.topicID,
		"superagent.issue.id":           usage.issueID,
		"superagent.session_binding.id": usage.sessionBindingID,
		"superagent.target.issue.id":    usage.targetIssueID,
		"superagent.event.id":           usage.eventID,
	} {
		if value != "" {
			span.SetAttributes(attribute.String(key, value))
		}
	}
	if err != nil {
		span.SetStatus(codes.Error, "")
	}
	sc := span.SpanContext()
	var traceID, spanID string
	if sc.IsValid() {
		traceID, spanID = sc.TraceID().String(), sc.SpanID().String()
	}
	// Client cancellation still leaves a Core-side attempt to record.
	persist, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
	defer cancel()
	if _, err := s.db.ExecContext(persist, `INSERT INTO mcp_tool_calls
 (at,caller_role,caller_id,topic_id,issue_id,session_binding_id,target_issue_id,event_id,tool,action,outcome,duration_ns,trace_id,span_id,replayed,changed)
 VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, start.UTC().Format(utcTimestamp), usage.role, usage.callerID, usage.topicID,
		usage.issueID, usage.sessionBindingID, usage.targetIssueID, usage.eventID, tool, usage.action, outcome, duration.Nanoseconds(), traceID, spanID, usage.replayed, usage.changed && err == nil); err != nil {
		slog.ErrorContext(ctx, "record MCP tool call", "role", usage.role, "tool", tool, "error", err)
	}
}

type mcpUsageRow struct {
	Role       string `json:"role"`
	CallerID   string `json:"callerId"`
	Tool       string `json:"tool"`
	Action     string `json:"action,omitempty"`
	Outcome    string `json:"outcome"`
	Count      int64  `json:"count"`
	Replayed   int64  `json:"replayed"`
	Changed    int64  `json:"changed"`
	DurationNS int64  `json:"durationNs"`
}

type mcpUsagePage struct {
	Count int64         `json:"count"`
	Items []mcpUsageRow `json:"items"`
}

type mcpCallRecord struct {
	Sequence         int64  `json:"sequence"`
	At               string `json:"at"`
	Role             string `json:"role"`
	CallerID         string `json:"callerId"`
	TopicID          string `json:"topicId,omitempty"`
	IssueID          string `json:"issueId,omitempty"`
	SessionBindingID string `json:"sessionBindingId,omitempty"`
	TargetIssueID    string `json:"targetIssueId,omitempty"`
	EventID          string `json:"eventId,omitempty"`
	Tool             string `json:"tool"`
	Action           string `json:"action,omitempty"`
	Outcome          string `json:"outcome"`
	DurationNS       int64  `json:"durationNs"`
	TraceID          string `json:"traceId,omitempty"`
	SpanID           string `json:"spanId,omitempty"`
	Replayed         bool   `json:"replayed"`
	Changed          bool   `json:"changed"`
}

type mcpCallPage struct {
	Items      []mcpCallRecord `json:"items"`
	NextBefore int64           `json:"nextBefore,omitempty"`
}

// The caller filters select the owner of the call. Resource filters provide
// the containing Topic or calls about an Issue with their callers intact.
func (s *Store) mcpToolUsage(ctx context.Context, q url.Values, mode string) (any, error) {
	calls := mode == "calls"
	metrics := mode == "metrics"
	window := 24 * time.Hour
	limit := 50
	where := " WHERE 1=1"
	var args []any
	for key, values := range q {
		if len(values) != 1 || values[0] == "" {
			return nil, problem("invalid_argument", "use one nonempty value per usage filter")
		}
		value := values[0]
		switch key {
		case "role":
			if value != "router" && value != "topic" && value != "issue" {
				return nil, problem("invalid_argument", "role must be router, topic or issue")
			}
			where += " AND caller_role=?"
		case "callerId":
			where += " AND caller_id=?"
		case "sessionBindingId":
			where += " AND session_binding_id=?"
		case "tool":
			where += " AND tool=?"
		case "window":
			switch value {
			case "1h":
				window = time.Hour
			case "24h":
				window = 24 * time.Hour
			case "7d":
				window = 7 * 24 * time.Hour
			case "30d":
				window = 30 * 24 * time.Hour
			default:
				return nil, problem("invalid_argument", "window must be 1h, 24h, 7d or 30d")
			}
			continue
		case "messageId":
			where += ` AND (event_id IN (SELECT event_id FROM routing_events WHERE message_id=?)`
			if s.hasTraceAssociations {
				where += ` OR (trace_id<>'' AND trace_id IN (SELECT substr(traceparent,4,32) FROM trace_associations WHERE collection='messages' AND object_id=?))`
				args = append(args, value)
			}
			where += ")"
		case "topicId":
			where += " AND topic_id=?"
		case "issueId":
			where += " AND (issue_id=? OR target_issue_id=?)"
			args = append(args, value)
		case "limit", "before":
			n, err := strconv.ParseInt(value, 10, 64)
			if !calls || err != nil || n < 1 || key == "limit" && n > 200 {
				return nil, problem("invalid_argument", "invalid call page limit or before cursor")
			}
			if key == "limit" {
				limit = int(n)
				continue
			}
			where += " AND seq<?"
		case "from", "until":
			at, err := time.Parse(time.RFC3339Nano, value)
			if err != nil {
				return nil, problem("invalid_argument", "from and until must use RFC3339")
			}
			value = at.UTC().Format(utcTimestamp)
			if key == "from" {
				where += " AND at>=?"
			} else {
				where += " AND at<?"
			}
		default:
			return nil, problem("invalid_argument", "unknown usage filter")
		}
		args = append(args, value)
	}
	end := time.Now().UTC().Truncate(time.Second).Add(time.Second)
	start := end.Add(-window)
	if metrics || q.Has("window") {
		where += " AND at>=? AND at<?"
		args = append(args, start.Format(utcTimestamp), end.Format(utcTimestamp))
	}
	if metrics {
		return s.mcpToolMetrics(ctx, where, args, start, end)
	}
	if calls {
		return s.mcpToolCalls(ctx, where, args, limit)
	}
	page := mcpUsagePage{Items: []mcpUsageRow{}}
	rows, err := s.db.QueryContext(ctx, `SELECT caller_role,caller_id,tool,action,outcome,COUNT(*),SUM(replayed),SUM(changed),SUM(duration_ns)
 FROM mcp_tool_calls`+where+` GROUP BY caller_role,caller_id,tool,action,outcome ORDER BY 1,2,3,4,5`, args...)
	if err != nil {
		return page, storageError()
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var row mcpUsageRow
		if err := rows.Scan(&row.Role, &row.CallerID, &row.Tool, &row.Action, &row.Outcome, &row.Count, &row.Replayed, &row.Changed, &row.DurationNS); err != nil {
			return page, storageError()
		}
		page.Count += row.Count
		page.Items = append(page.Items, row)
	}
	if rows.Err() != nil {
		return page, storageError()
	}
	return page, nil
}

func (s *Store) mcpToolCalls(ctx context.Context, where string, args []any, limit int) (mcpCallPage, error) {
	page := mcpCallPage{Items: []mcpCallRecord{}}
	rows, err := s.db.QueryContext(ctx, `SELECT seq,at,caller_role,caller_id,topic_id,issue_id,session_binding_id,target_issue_id,event_id,tool,action,outcome,duration_ns,trace_id,span_id,replayed,changed
 FROM mcp_tool_calls`+where+` ORDER BY seq DESC LIMIT ?`, append(args, limit+1)...)
	if err != nil {
		return page, storageError()
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		if len(page.Items) == limit {
			page.NextBefore = page.Items[limit-1].Sequence
			break
		}
		var row mcpCallRecord
		if err := rows.Scan(&row.Sequence, &row.At, &row.Role, &row.CallerID, &row.TopicID, &row.IssueID, &row.SessionBindingID, &row.TargetIssueID,
			&row.EventID, &row.Tool, &row.Action, &row.Outcome, &row.DurationNS, &row.TraceID, &row.SpanID, &row.Replayed, &row.Changed); err != nil {
			return page, storageError()
		}
		page.Items = append(page.Items, row)
	}
	if rows.Err() != nil {
		return page, storageError()
	}
	return page, nil
}
