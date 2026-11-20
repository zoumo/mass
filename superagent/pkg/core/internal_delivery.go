package core

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"time"

	"github.com/zoumo/mass/mass-go/pkg/resource"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

type inputBatch struct {
	BindingID     string            `json:"bindingId,omitempty"`
	Generation    int               `json:"generation,omitempty"`
	Events        []json.RawMessage `json:"events"`
	SchemaVersion int               `json:"schemaVersion,omitempty"`
}

const inlineMessageBytes = 2 * 1024

type internalDeliveryBatch struct {
	workerIssueRevision int
	wire                []byte
	items               []internalDelivery
	queuedAt            time.Time
	method              string
	links               []trace.Link
}

// businessDeliveryCurrent is evaluated in both the selection and final admission
// transactions. Extend it with kind-specific checks against the referenced facts
// when moving a producer whose events can expire (for example by Goal revision).
func businessDeliveryCurrent(ctx context.Context, tx *sql.Tx, d internalDelivery) (bool, error) {
	if d.Recipient.Kind == "worker" {
		return workDeliveryCurrent(ctx, tx, d)
	}
	if d.Recipient.Kind != "topic" || d.Kind == routedMessageKind || d.Kind == contextSupplement {
		return true, nil
	}
	var topic Topic
	if err := readJSON(ctx, tx, "SELECT body FROM topics WHERE id=?", &topic, d.Recipient.TopicID); err != nil {
		return false, err
	}
	return !terminal(topic.Status), nil
}

func encodeLegacyInternalDelivery(d internalDelivery) (json.RawMessage, error) {
	event := struct {
		internalDelivery
		BodyPreview    string             `json:"body_preview,omitempty"`
		ContentPreview string             `json:"content_preview,omitempty"`
		Truncated      bool               `json:"truncated,omitempty"`
		ContentRef     *deliveryReference `json:"content_ref,omitempty"`
	}{internalDelivery: d}
	truncate := func() {
		event.Truncated = true
		event.ContentRef = &deliveryReference{Kind: "internal_delivery", ID: d.ID}
	}
	if len(d.Body) > previewBytes {
		event.BodyPreview = truncatePreview(string(d.Body))
		event.Body = nil
		truncate()
	}
	if d.Content != nil && len(d.Content.Data) > previewBytes {
		var prose struct {
			Text   string `json:"text"`
			Prompt string `json:"prompt"`
		}
		if err := json.Unmarshal(d.Content.Data, &prose); err != nil {
			return nil, err
		}
		event.ContentPreview = prose.Text + prose.Prompt
		if len(event.ContentPreview) > previewBytes {
			event.ContentPreview = truncatePreview(event.ContentPreview)
		}
		event.Content = nil
		truncate()
	}
	raw, err := json.Marshal(event)
	if err != nil || len(raw) <= batchBytes-512 {
		return raw, err
	}
	// Unabridged content and references remain available through delivery_get.
	event.Content = nil
	event.ContentPreview = ""
	event.Refs = nil
	truncate()
	return json.Marshal(event)
}

func (s *Store) selectInternalBatch(ctx context.Context, b Binding) (internalDeliveryBatch, error) {
	var batch internalDeliveryBatch
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return batch, err
	}
	defer func() { _ = tx.Rollback() }()
	if err := checkBinding(ctx, tx, b); err != nil {
		return batch, err
	}
	rows, err := tx.QueryContext(ctx, `SELECT `+internalDeliveryColumns+`
 FROM internal_deliveries WHERE `+deliveryRecipientKind+`=? AND `+deliveryRecipientOwner+`=?
 AND status IN ('pending','dispatching')

 ORDER BY rowid LIMIT ?`, b.Role, b.OwnerID, batchLimit)
	if err != nil {
		return batch, err
	}
	defer func() { _ = rows.Close() }()
	type queued struct {
		item internalDelivery
		at   string
	}
	var queue []queued
	for rows.Next() {
		var q queued
		if err := scanInternalDelivery(rows, &q.item, &q.at); err != nil {
			return batch, err
		}
		queue = append(queue, q)
	}
	if err := rows.Err(); err != nil {
		return batch, err
	}
	if err := rows.Close(); err != nil {
		return batch, err
	}
	var deadline time.Time
	var workerIssue *Issue
	batch.method = "inject"
	var envelope inputBatch
	if b.AgentPromptVersion == 0 {
		envelope.BindingID, envelope.Generation, envelope.SchemaVersion = b.ID, b.Generation, 4
	}
	at, full := time.Now(), false
	for _, q := range queue {
		current, err := businessDeliveryCurrent(ctx, tx, q.item)
		if err != nil {
			return batch, err
		}
		if !current {
			if err := supersedeInternalDelivery(ctx, tx, q.item.ID); err != nil {
				return batch, err
			}
			continue
		}
		created, err := time.Parse(time.RFC3339Nano, q.at)
		if err != nil {
			return batch, err
		}
		if len(batch.items) == 0 {
			deadline = created.Add(batchWindow)
		}
		if created.After(deadline) {
			break
		}
		var raw, referenceOnly json.RawMessage
		if b.AgentPromptVersion == 0 {
			raw, err = encodeLegacyInternalDelivery(q.item)
		} else {
			view := q.item
			view.promptVersion = b.AgentPromptVersion
			raw, err = view.MarshalAgentPromptJSON()
			if err == nil && len(raw) > previewBytes {
				view.truncated = true
				raw, err = view.MarshalAgentPromptJSON()
			}
			inlineTopic := b.Role == "topic" && view.Kind == routedMessageKind
			inlineRouter := b.Role == "router" && b.AgentPromptVersion >= agentPromptVersion && view.Kind == routeRequestKind
			maxViewBytes := min(previewBytes, batchBytes-len(batch.wire)-1)
			workerContext := false
			if err == nil && !view.truncated && view.Content == nil && len(view.Refs) == 1 &&
				view.Refs[0].Kind == messageRefKind && (inlineTopic || inlineRouter) {
				err = view.loadInlineTriggerMessage(ctx, tx, b)
			} else if err == nil && !view.truncated && b.AgentPromptVersion >= agentPromptVersion {
				switch {
				case b.Role == "topic" && view.Kind == reportSubmittedKind:
					err = view.loadInlineReport(ctx, tx, b)
				case b.Role == "worker" && (view.Kind == workerStarted || view.Kind == workerUpdateKind || view.Kind == contextSupplement):
					if workerIssue == nil {
						issue, _, readErr := currentWorkerIssue(ctx, tx, b)
						if readErr != nil {
							return batch, readErr
						}
						workerIssue = &issue
					}
					err = view.loadInlineWorkerContext(ctx, tx, workerIssue, maxViewBytes)
					workerContext = view.issue != nil || len(view.messages) != 0
				}
			}
			if err == nil && (view.Content != q.item.Content || view.issue != nil || len(view.messages) != 0) {
				var enriched json.RawMessage
				enriched, err = view.MarshalAgentPromptJSON()
				if err == nil && (b.AgentPromptVersion < agentPromptVersion || len(enriched) <= maxViewBytes) {
					referenceOnly, raw = raw, enriched
					if workerContext {
						batch.workerIssueRevision = workerIssue.Revision
					}
				}
			}
		}
		if err != nil {
			return batch, err
		}
		envelope.Events = append(envelope.Events, raw)
		wire, err := json.Marshal(envelope)
		if err != nil {
			return batch, err
		}
		if len(wire) > batchBytes && referenceOnly != nil {
			envelope.Events[len(envelope.Events)-1] = referenceOnly
			wire, err = json.Marshal(envelope)
			if err != nil {
				return batch, err
			}
		}
		if len(wire) > batchBytes {
			full = true
			break
		}
		batch.wire = wire
		if len(batch.items) == 0 {
			batch.queuedAt = created
		}
		batch.items = append(batch.items, q.item)
		if q.item.Kind != contextSupplement {
			batch.method = "start_or_steer"
		}
	}
	if len(batch.items) == 0 || !full && len(batch.items) < batchLimit && at.Before(deadline) {
		batch = internalDeliveryBatch{}
	}
	if s.telemetry.tracesEnabled {
		for _, item := range batch.items {
			batch.links = internalEnqueue.links(ctx, tx, batch.links, item.ID, 0)
		}
	}
	return batch, tx.Commit()
}

// loadInlineTriggerMessage uses the same scope as source_query. A matched Topic
// quote carries the open blocker snapshot from the same transaction.
func (d *internalDelivery) loadInlineTriggerMessage(ctx context.Context, tx *sql.Tx, b Binding) error {
	scope, args, err := scopeSQL("messages", &b)
	if err != nil {
		return err
	}
	var text, threadKey string
	var routing []byte
	var hasQuote bool
	var quotedReply []byte
	columns := "body->>'$.text'"
	var projectionArgs []any
	filter := " AND body->>'$.quotedMessage' IS NULL"
	dest := []any{&text}
	if b.Role == "router" {
		columns += ",thread_key,json_object('round'," + routingRound + ",'state'," + routingState + ")"
		filter += " AND " + routingState + "='pending' AND " + routingRound + "=1"
		dest = append(dest, &threadKey, &routing)
	} else if b.AgentPromptVersion >= agentPromptVersion {
		columns += ",body->>'$.quotedMessage' IS NOT NULL,CASE WHEN body->>'$.quotedMessage' IS NOT NULL THEN " + quotedReplyForTopic + " END"
		projectionArgs = append(projectionArgs, b.OwnerID)
		dest = append(dest, &hasQuote, &quotedReply)
		filter = ""
	}
	queryArgs := append(projectionArgs, d.Refs[0].ID, timerSource, pullRequestSource, pullRequestSource, inlineMessageBytes)
	err = tx.QueryRowContext(ctx, `SELECT `+columns+` FROM trigger_messages
 WHERE id=? AND source<>? AND source<>'github' AND source<>? AND body->>'$.threadRef.source'<>?
 AND (body->>'$.meta' IS NULL OR body->>'$.meta'='{}')
 AND length(CAST(body->>'$.text' AS BLOB))<=?`+filter+scope, append(queryArgs, args...)...).Scan(dest...)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if strings.TrimSpace(text) == "" || hasQuote && len(quotedReply) == 0 {
		return nil
	}
	data, err := json.Marshal(markdownContentData{Text: text})
	if err != nil {
		return err
	}
	content := StructuredContent{Type: "markdown", Data: data}
	contentJSON, err := json.Marshal(content)
	if err != nil {
		return err
	}
	if len(contentJSON) > inlineMessageBytes {
		return nil
	}
	if hasQuote {
		var match struct {
			BlockerRef BlockerRef `json:"blockerRef"`
		}
		if err := json.Unmarshal(quotedReply, &match); err != nil {
			return err
		}
		issue, err := topicIssue(ctx, tx, b, match.BlockerRef.IssueID)
		if errorCode(err) == "not_found" {
			return nil
		}
		if err != nil {
			return err
		}
		if terminal(issue.Status) || !slices.ContainsFunc(issue.Blockers, func(blocker Blocker) bool {
			return blocker.ID == match.BlockerRef.BlockerID
		}) {
			return nil
		}
		d.quotedReply, d.issue = quotedReply, &issue
	}
	d.Content, d.threadKey, d.routing = &content, threadKey, routing
	return nil
}

func (d *internalDelivery) loadInlineReport(ctx context.Context, tx *sql.Tx, b Binding) error {
	if len(d.Refs) != 1 || d.Refs[0].Kind != "report" {
		return nil
	}
	var report Report
	err := readJSON(ctx, tx, `SELECT r.body FROM reports r
 JOIN issues i ON i.id=r.issue_id WHERE r.id=? AND i.topic_id=?`, &report, d.Refs[0].ID, b.OwnerID)
	if errorCode(err) == "not_found" {
		return nil
	}
	if err != nil || report.Historical {
		return err
	}
	issue, err := topicIssue(ctx, tx, b, report.IssueID)
	if err != nil {
		return err
	}
	if issue.CurrentExecutionID == report.ExecutionID && issue.RequirementsRevision == report.Revision {
		d.issue, d.report = &issue, &report
	}
	return nil
}

func (d *internalDelivery) loadInlineWorkerContext(ctx context.Context, tx *sql.Tx, issue *Issue, maxBytes int) error {
	if d.Kind != contextSupplement {
		// A durable startup still reaches the Worker after a new blocker.
		// Without an inline Issue the Worker must read the current state.
		if !canContinueIssue(*issue) {
			return nil
		}
		d.issue = issue
	}
	raw, err := d.MarshalAgentPromptJSON()
	if err != nil {
		return err
	}
	remaining := maxBytes - len(raw) - len(`,"messages":[]`)
	if remaining <= 0 {
		return nil
	}
	var ids []string
	for _, material := range issue.Materials {
		if material.MessageID == "" {
			continue
		}
		if d.Kind == contextSupplement && !slices.ContainsFunc(d.Refs, func(ref deliveryReference) bool {
			return ref.Kind == messageRefKind && ref.ID == material.MessageID
		}) {
			continue
		}
		if !slices.Contains(ids, material.MessageID) {
			ids = append(ids, material.MessageID)
		}
	}
	if len(ids) == 0 {
		return nil
	}
	payload, err := json.Marshal(ids)
	if err != nil {
		return err
	}
	rows, err := tx.QueryContext(ctx, `SELECT m.id,m.body->>'$.text',m.body->>'$.actor',
 COALESCE(m.body->>'$.at',''),COALESCE(m.body->>'$.url','')
 FROM json_each(?) ref JOIN trigger_messages m ON m.id=ref.value
 WHERE length(CAST(m.body->>'$.text' AS BLOB))<=?
 ORDER BY ref.key`, string(payload), inlineMessageBytes)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var message Message
		if err := rows.Scan(&message.ID, &message.Text, &message.Actor, &message.At, &message.URL); err != nil {
			return err
		}
		raw, err := message.MarshalAgentPromptJSON()
		if err != nil {
			return err
		}
		if len(raw) > inlineMessageBytes || len(raw) > remaining {
			continue
		}
		d.messages = append(d.messages, raw)
		remaining -= len(raw) + 1 // Space for the comma before a subsequent item.
		if remaining <= 0 {
			break
		}
	}
	return rows.Err()
}

func (s *Store) admitInternalBatch(ctx context.Context, b Binding, batch internalDeliveryBatch, watch *workerWatch) (bool, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback() }()
	if err := checkBinding(ctx, tx, b); err != nil {
		return false, err
	}
	var ready bool
	err = tx.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM session_bindings WHERE id=? AND state='ready'
 AND json_extract(body,'$.sessionId')=? AND json_extract(body,'$.sessionUid')=?)`,
		b.ID, b.SessionID, b.SessionUID).Scan(&ready)
	if err != nil {
		return false, err
	}
	if !ready {
		return false, problem("stale_binding", "Session binding changed before submission")
	}
	if batch.workerIssueRevision != 0 {
		issue, _, err := currentWorkerIssue(ctx, tx, b)
		if err != nil {
			return false, err
		}
		if issue.Revision != batch.workerIssueRevision {
			return false, problem("conflict", "Issue changed before Worker input submission")
		}
	}
	for _, item := range batch.items {
		current, err := businessDeliveryCurrent(ctx, tx, item)
		if err != nil {
			return false, err
		}
		if !current {
			return false, problem("stale_binding", "internal delivery is no longer current")
		}
		res, err := tx.ExecContext(ctx, `UPDATE internal_deliveries SET status='dispatching'
 WHERE id=? AND status IN ('pending','dispatching')`, item.ID)
		if err != nil {
			return false, err
		}
		if n, err := res.RowsAffected(); err != nil || n != 1 {
			return false, problem("stale_binding", "internal delivery was settled or superseded")
		}
		if err := internalDispatch.save(ctx, tx, item.ID, b.Generation); err != nil {
			return false, err
		}
	}
	var armed bool
	if watch != nil {
		armed, err = beginWorkerObservation(ctx, tx, b, watch.evidence)
		if err != nil {
			return false, err
		}
	}
	return armed, tx.Commit()
}

func (s *Store) finishInternalBatch(ctx context.Context, batch internalDeliveryBatch, a Attempt, watch *workerWatch) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	var at any
	status := "dispatching"
	switch {
	case a.Status == "delivered":
		status, at = "sent", now()
	case a.Status == "pending" || a.Status == "blocked" && a.ObservationGap == "":
		status = "pending"
	}
	if status != "dispatching" {
		for _, item := range batch.items {
			if _, err := tx.ExecContext(ctx, `UPDATE internal_deliveries SET status=?,sent_at=?
 WHERE id=? AND status='dispatching'`, status, at, item.ID); err != nil {
				return err
			}
		}
	}
	if watch != nil {
		if err := settleWorkerObservation(ctx, tx, watch.evidence); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Server) coordinateInternalDelivery(life, call context.Context, b Binding, spec resource.SessionSpec) bool {
	if retry, ok := s.internalDeliveryRetries.Load(b.ID); ok && time.Now().Before(retry.(time.Time)) {
		return false
	}
	batch, err := s.store.selectInternalBatch(life, b)
	if err != nil || len(batch.items) == 0 || life.Err() != nil {
		return false
	}
	if len(batch.items) == 1 && len(batch.links) == 1 {
		call = trace.ContextWithRemoteSpanContext(call, batch.links[0].SpanContext)
		batch.links = nil
	}
	call, finish := s.store.telemetry.startOperation(call, "internal_delivery.dispatch", b.Role, trace.WithSpanKind(trace.SpanKindConsumer), trace.WithLinks(batch.links...))
	s.store.telemetry.recordQueueDuration(call, "internal_delivery", batch.queuedAt)
	trace.SpanFromContext(call).SetAttributes(
		attribute.Int("superagent.delivery.batch.size", len(batch.items)),
		attribute.String("superagent.delivery.method", batch.method),
	)
	if len(batch.items) == 1 {
		trace.SpanFromContext(call).SetAttributes(attribute.String("superagent.delivery.id", batch.items[0].ID))
	}
	a := Attempt{ID: newID("attempt")}
	admitted := false
	a, watch := s.dispatchSessionInput(call, b, spec, a, batch.method, batch.wire, func(watch *workerWatch) (bool, error) {
		armed, err := s.store.admitInternalBatch(call, b, batch, watch)
		admitted = err == nil
		return armed, err
	})
	outcome := a.Status
	switch outcome {
	case "blocked_unknown", "transport_unknown":
		outcome = "unknown"
	case "blocked":
		outcome = "failed"
	}
	if admitted {
		commitGate("internal_delivery_before_settlement")
		settled := persistInputSettlement(life, batch.items[0].ID, func(ctx context.Context) error {
			return s.store.finishInternalBatch(trace.ContextWithSpan(ctx, trace.SpanFromContext(call)), batch, a, watch)
		})
		if !settled {
			outcome = "unknown"
		}
		if watch != nil {
			s.startWorkerWatch(life, b, watch, settled)
		}
	}
	if a.Status != "delivered" {
		s.internalDeliveryRetries.Store(b.ID, time.Now().Add(time.Second))
	}
	s.updateBindingAfterDelivery(life, b, a)
	finish(outcome)
	return true
}
