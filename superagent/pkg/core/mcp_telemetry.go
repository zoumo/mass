package core

import (
	"context"
	"errors"
	"time"

	rpc2 "github.com/sourcegraph/jsonrpc2"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"
)

type mcpCallTelemetryContextKey struct{}

type mcpCallTelemetry struct {
	role, callerID, topicID, issueID, sessionBindingID string
	targetIssueID, eventID, action                     string
}

func newMCPCallTelemetry(b SessionBinding) *mcpCallTelemetry {
	u := &mcpCallTelemetry{role: b.Role, callerID: b.OwnerID, sessionBindingID: b.ID}
	switch b.Role {
	case "topic":
		u.topicID = b.OwnerID
	case "issue":
		u.topicID, u.issueID = b.TopicID, b.OwnerID
	}
	return u
}

func mcpCallTelemetryFrom(ctx context.Context) *mcpCallTelemetry {
	u, _ := ctx.Value(mcpCallTelemetryContextKey{}).(*mcpCallTelemetry)
	return u
}

func markMCPIssueTarget(ctx context.Context, issueID string) {
	if u := mcpCallTelemetryFrom(ctx); u != nil && u.role == "topic" {
		u.targetIssueID = issueID
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

func (s *Store) recordMCPTool(ctx context.Context, start time.Time, usage *mcpCallTelemetry, tool string, err error) {
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
}
