package core

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	"github.com/zoumo/mass/massd/pkg/events"
	"github.com/zoumo/mass/massd/pkg/resource"
	"github.com/zoumo/mass/massd/pkg/runtime"
)

type inputBatch struct {
	Events []json.RawMessage `json:"events"`
}

const inlineMessageBytes = 2 * 1024

type internalEventBatch struct {
	boundIssueRevision int
	input              runtime.Input
	items              []internalEvent
	queuedAt           time.Time
	method             string
	links              []trace.Link
}

// businessEventCurrent is evaluated in both the selection and final admission
// transactions. Extend it with kind-specific checks against the referenced facts
// when moving a producer whose events can expire (for example by Goal revision).
func businessEventCurrent(ctx context.Context, tx *sql.Tx, d internalEvent) (bool, error) {
	if d.Recipient.Kind == "issue" {
		return workEventCurrent(ctx, tx, d)
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

func (s *Store) selectInternalBatch(ctx context.Context, b SessionBinding) (internalEventBatch, error) {
	var batch internalEventBatch
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return batch, err
	}
	defer func() { _ = tx.Rollback() }()
	if err := checkBinding(ctx, tx, b); err != nil {
		return batch, err
	}
	rows, err := tx.QueryContext(ctx, `SELECT `+internalEventColumns+`
 FROM internal_events WHERE `+eventRecipientKind+`=? AND `+eventRecipientOwner+`=?
 AND status IN ('pending','dispatching')

 ORDER BY rowid LIMIT ?`, b.Role, b.eventRecipientID(), batchLimit)
	if err != nil {
		return batch, err
	}
	defer func() { _ = rows.Close() }()
	type queued struct {
		item internalEvent
		at   string
	}
	var queue []queued
	for rows.Next() {
		var q queued
		if err := scanInternalEvent(rows, &q.item, &q.at); err != nil {
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
	var boundTopic *Topic
	var boundIssue *Issue
	batch.method = "inject"
	var envelope inputBatch
	var blocks []events.ContentBlock
	batchSize := len(`{"events":[]}`)
	at, full := time.Now(), false
	for _, q := range queue {
		current, err := businessEventCurrent(ctx, tx, q.item)
		if err != nil {
			return batch, err
		}
		if !current {
			if err := supersedeInternalEvent(ctx, tx, q.item.ID); err != nil {
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
		var raw json.RawMessage
		view := q.item
		raw, err = view.MarshalAgentPromptJSON()
		if err == nil && len(raw) > previewBytes {
			view.truncated = true
			raw, err = view.MarshalAgentPromptJSON()
		}
		inlineTopic := b.Role == "topic" && view.Kind == routedMessageKind
		inlineRouter := b.Role == "router" && view.Kind == routeRequestKind
		maxViewBytes := min(previewBytes, batchBytes-batchSize-1)
		issueContext := false
		if err == nil && !view.truncated && inlineTopic {
			if boundTopic == nil {
				var topic Topic
				if err := readJSON(ctx, tx, "SELECT body FROM topics WHERE id=?", &topic, b.OwnerID); err != nil {
					return batch, err
				}
				boundTopic = &topic
			}
			view.topic = boundTopic
		}
		if err == nil && !view.truncated && view.Content == nil && len(view.Refs) != 0 &&
			view.Refs[0].Kind == messageRefKind && (inlineTopic || inlineRouter) {
			var structured bool
			structured, err = view.loadStructuredTriggerMessage(ctx, tx, b)
			if err == nil && !structured && len(view.Refs) == 1 {
				err = view.loadInlineTriggerMessage(ctx, tx, b)
			}
		} else if err == nil && !view.truncated {
			switch {
			case b.Role == "topic" && view.Kind == reportSubmittedKind:
				err = view.loadInlineReport(ctx, tx, b)
			case b.Role == "issue" && (view.Kind == issueSessionStarted || view.Kind == issueSessionUpdateKind || view.Kind == contextSupplement):
				if boundIssue == nil {
					issue, _, readErr := currentBoundIssue(ctx, tx, b)
					if readErr != nil {
						return batch, readErr
					}
					boundIssue = &issue
				}
				err = view.loadInlineIssueContext(ctx, tx, boundIssue, maxViewBytes)
				issueContext = view.issue != nil || len(view.messages) != 0 || len(view.messageContent) != 0
			}
		}
		var contents []eventMessageContent
		var contentSize int
		if err == nil && (view.Content != q.item.Content || view.topic != nil || view.issue != nil || len(view.messages) != 0 || len(view.followup) != 0 || len(view.routing) != 0 || len(view.messageContent) != 0) {
			var enriched json.RawMessage
			enriched, err = view.MarshalAgentPromptJSON()
			for _, content := range view.messageContent {
				contentSize += content.size
			}
			if err == nil && contentSize != 0 && len(enriched)+contentSize > maxViewBytes {
				view.messageContent, contentSize = nil, 0
				enriched, err = view.MarshalAgentPromptJSON()
			}
			if err == nil && len(enriched)+contentSize <= maxViewBytes {
				raw = enriched
				contents = view.messageContent
				if issueContext {
					batch.boundIssueRevision = boundIssue.Revision
				}
			} else {
				contentSize = 0
			}
		}
		if err != nil {
			return batch, err
		}
		eventSize := len(raw) + contentSize
		if len(envelope.Events) != 0 {
			eventSize++ // Comma between event objects.
		}
		if batchSize+eventSize > batchBytes {
			full = true
			break
		}
		batchSize += eventSize
		envelope.Events = append(envelope.Events, raw)
		for _, content := range contents {
			blocks = append(blocks, content.Blocks...)
		}
		if len(batch.items) == 0 {
			batch.queuedAt = created
		}
		batch.items = append(batch.items, q.item)
		if q.item.Kind != contextSupplement {
			batch.method = "start_or_steer"
		}
	}
	if len(batch.items) == 0 || !full && len(batch.items) < batchLimit && at.Before(deadline) {
		batch = internalEventBatch{}
	} else {
		header, err := json.Marshal(envelope)
		if err != nil {
			return batch, err
		}
		batch.input = runtime.Input{Content: append([]events.ContentBlock{events.Text(string(header))}, blocks...)}
	}
	if s.telemetry.tracesEnabled {
		for _, item := range batch.items {
			batch.links = internalEnqueue.links(ctx, tx, batch.links, item.ID, 0)
		}
	}
	return batch, tx.Commit()
}

// loadStructuredTriggerMessage uses the same role scope as get_message. It
// loads complete block groups that fit and leaves larger Messages as references.
func (d *internalEvent) loadStructuredTriggerMessage(ctx context.Context, tx *sql.Tx, b SessionBinding) (bool, error) {
	scope, args, err := scopeSQL("messages", &b)
	if err != nil {
		return false, err
	}
	columns := `CASE WHEN length(CAST(body->'$.content' AS BLOB))<=? THEN body->'$.content' END, thread_key`
	params := []any{inlineMessageBytes}
	var content, routing, followup []byte
	targets := []any{&content, &d.threadKey}
	if b.Role == "router" {
		columns += ",json_object('round'," + routingRound + ",'state'," + routingState + ")"
		scope += " AND " + routingState + "='pending'"
		targets = append(targets, &routing)
	} else {
		columns += "," + messageFollowupProjection
		params = append(params, b.OwnerID)
		targets = append(targets, &followup)
	}
	id := d.Refs[0].ID
	params = append(params, id)
	params = append(params, args...)
	err = tx.QueryRowContext(ctx, `SELECT `+columns+` FROM trigger_messages
 WHERE id=? AND json_type(body,'$.content')='array'`+scope, params...).Scan(targets...)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	d.routing, d.followup = routing, followup
	if len(content) != 0 {
		if err := d.attachMessageContent(id, content); err != nil {
			return false, err
		}
	}
	return true, nil
}

func (d *internalEvent) attachMessageContent(id string, raw json.RawMessage) error {
	if len(raw) == 0 || len(raw) > inlineMessageBytes {
		return nil
	}
	var blocks []events.ContentBlock
	if err := json.Unmarshal(raw, &blocks); err != nil {
		return err
	}
	if len(blocks) != 0 {
		// Replacing the array brackets with a leading comma gives its size in Input.Content.
		d.messageContent = append(d.messageContent, eventMessageContent{ID: id, Blocks: blocks, size: len(raw) - 1})
	}
	return nil
}

func (c eventMessageContent) reference() messageContentReference {
	return messageContentReference{MessageID: c.ID, BlockCount: len(c.Blocks)}
}

// loadInlineTriggerMessage uses the same scope as get_trigger_message. A matched Topic
// quote carries the open blocker snapshot from the same transaction.
func (d *internalEvent) loadInlineTriggerMessage(ctx context.Context, tx *sql.Tx, b SessionBinding) error {
	scope, args, err := scopeSQL("messages", &b)
	if err != nil {
		return err
	}
	var text, threadKey string
	var routing []byte
	var followup []byte
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
	} else {
		columns += ",body->>'$.quotedMessage' IS NOT NULL,CASE WHEN body->>'$.quotedMessage' IS NOT NULL THEN " + quotedReplyForTopic + " END"
		projectionArgs = append(projectionArgs, b.OwnerID)
		dest = append(dest, &hasQuote, &quotedReply)
		filter = ""
	}
	sourceFilter := " AND source<>? AND (body->>'$.meta' IS NULL OR body->>'$.meta'='{}')"
	if b.Role == "topic" {
		columns += ",CASE WHEN source=? THEN body->'$.meta' END"
		projectionArgs = append(projectionArgs, timerSource)
		dest = append(dest, &followup)
		sourceFilter = " AND (source=? OR body->>'$.meta' IS NULL OR body->>'$.meta'='{}')"
	}
	projectionArgs = append(projectionArgs, d.Refs[0].ID, timerSource, pullRequestSource, pullRequestSource, inlineMessageBytes)
	err = tx.QueryRowContext(ctx, `SELECT `+columns+` FROM trigger_messages
 WHERE id=?`+sourceFilter+` AND source<>'github' AND source<>? AND body->>'$.threadRef.source'<>?
 AND length(CAST(body->>'$.text' AS BLOB))<=?`+filter+scope, append(projectionArgs, args...)...).Scan(dest...)
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
	if len(followup) != 0 && string(followup) != "null" {
		d.followup = agentFollowupMeta(followup)
	}
	d.Content, d.threadKey, d.routing = &content, threadKey, routing
	return nil
}

func (d *internalEvent) loadInlineReport(ctx context.Context, tx *sql.Tx, b SessionBinding) error {
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
	report.Current = reportIsCurrent(report, issue)
	if report.Current {
		d.issue, d.report = &issue, &report
	}
	return nil
}

func (d *internalEvent) loadInlineIssueContext(ctx context.Context, tx *sql.Tx, issue *Issue, maxBytes int) error {
	if d.Kind != contextSupplement {
		// A durable startup still reaches the Issue Session after a new blocker.
		// Without an inline Issue the Issue Session must read the current state.
		if !canContinueIssue(*issue) {
			return nil
		}
		d.issue = issue
	}
	raw, err := d.MarshalAgentPromptJSON()
	if err != nil {
		return err
	}
	remaining := maxBytes - len(raw)
	if remaining <= 0 {
		return nil
	}
	materials := make(map[string]struct{}, len(issue.Materials))
	ids := make([]string, 0, len(issue.Materials))
	for _, material := range issue.Materials {
		if material.MessageID == "" {
			continue
		}
		if _, exists := materials[material.MessageID]; !exists {
			materials[material.MessageID] = struct{}{}
			ids = append(ids, material.MessageID)
		}
	}
	if d.Kind == contextSupplement {
		ids = ids[:0]
		for _, ref := range d.Refs {
			if _, authorized := materials[ref.ID]; ref.Kind == messageRefKind && authorized {
				ids = append(ids, ref.ID)
				delete(materials, ref.ID)
			}
		}
	}
	if len(ids) == 0 {
		return nil
	}
	payload, err := json.Marshal(ids)
	if err != nil {
		return err
	}
	rows, err := tx.QueryContext(ctx, `SELECT m.id,m.body->'$.content',
 COALESCE(m.body->>'$.text',''),COALESCE(m.body->>'$.actor',''),
 COALESCE(m.body->>'$.at',''),COALESCE(m.body->>'$.url','')
 FROM json_each(?) ref JOIN trigger_messages m ON m.id=ref.value
 WHERE length(CAST(COALESCE(m.body->'$.content',m.body->>'$.text') AS BLOB))<=?
 ORDER BY ref.key`, string(payload), inlineMessageBytes)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var message Message
		var blocks []byte
		if err := rows.Scan(&message.ID, &blocks, &message.Text, &message.Actor, &message.At, &message.URL); err != nil {
			return err
		}
		if len(blocks) != 0 {
			before := len(d.messageContent)
			if err := d.attachMessageContent(message.ID, blocks); err != nil {
				return err
			}
			if len(d.messageContent) == before {
				continue
			}
			content := d.messageContent[before]
			ref, err := json.Marshal(content.reference())
			if err != nil {
				return err
			}
			required := content.size + len(ref) + 1
			if before == 0 {
				required += len(`,"messageContent":[]`) - 1
			}
			if required > remaining {
				d.messageContent = d.messageContent[:before]
			} else {
				remaining -= required
			}
			continue
		}
		legacy, err := message.MarshalAgentPromptJSON()
		if err != nil {
			return err
		}
		required := len(legacy) + 1
		if len(d.messages) == 0 {
			required += len(`,"messages":[]`) - 1
		}
		if len(legacy) > inlineMessageBytes || required > remaining {
			continue
		}
		d.messages = append(d.messages, legacy)
		remaining -= required
	}
	return rows.Err()
}

func (s *Store) admitInternalBatch(ctx context.Context, b SessionBinding, batch internalEventBatch, watch *issueWatch) (bool, error) {
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
	if batch.boundIssueRevision != 0 {
		issue, _, err := currentBoundIssue(ctx, tx, b)
		if err != nil {
			return false, err
		}
		if issue.Revision != batch.boundIssueRevision {
			return false, problem("conflict", "Issue changed before Issue Session input submission")
		}
	}
	for _, item := range batch.items {
		current, err := businessEventCurrent(ctx, tx, item)
		if err != nil {
			return false, err
		}
		if !current {
			return false, problem("stale_binding", "internal event is no longer current")
		}
		res, err := tx.ExecContext(ctx, `UPDATE internal_events SET status='dispatching'
 WHERE event_id=? AND status IN ('pending','dispatching')`, item.ID)
		if err != nil {
			return false, err
		}
		if n, err := res.RowsAffected(); err != nil || n != 1 {
			return false, problem("stale_binding", "internal event was settled or superseded")
		}
		if err := internalDispatch.save(ctx, tx, item.ID, b.Generation); err != nil {
			return false, err
		}
	}
	var armed bool
	if watch != nil {
		armed, err = beginIssueObservation(ctx, tx, b, watch.evidence)
		if err != nil {
			return false, err
		}
	}
	return armed, tx.Commit()
}

func (s *Store) finishInternalBatch(ctx context.Context, batch internalEventBatch, a Attempt, watch *issueWatch) error {
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
			if _, err := tx.ExecContext(ctx, `UPDATE internal_events SET status=?,sent_at=?
 WHERE event_id=? AND status='dispatching'`, status, at, item.ID); err != nil {
				return err
			}
		}
	}
	if watch != nil {
		if err := settleIssueObservation(ctx, tx, watch.evidence); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Server) coordinateInternalEvents(life, call context.Context, b SessionBinding, spec resource.SessionSpec) bool {
	if retry, ok := s.internalEventDispatchRetries.Load(b.ID); ok && time.Now().Before(retry.(time.Time)) {
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
	call, finish := s.store.telemetry.startOperation(call, "internal_event.dispatch", b.Role, trace.WithSpanKind(trace.SpanKindConsumer), trace.WithLinks(batch.links...))
	s.store.telemetry.recordQueueDuration(call, "internal_event", batch.queuedAt)
	trace.SpanFromContext(call).SetAttributes(
		attribute.Int("superagent.event.batch.size", len(batch.items)),
		attribute.String("superagent.event.dispatch.method", batch.method),
	)
	if len(batch.items) == 1 {
		trace.SpanFromContext(call).SetAttributes(attribute.String("superagent.event.id", batch.items[0].ID))
	}
	a := Attempt{ID: newID("attempt")}
	admitted := false
	a, watch := s.dispatchSessionInput(call, b, spec, a, batch.method, batch.input, func(watch *issueWatch) (bool, error) {
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
		commitGate("internal_event_before_settlement")
		settled := persistInputSettlement(life, batch.items[0].ID, func(ctx context.Context) error {
			return s.store.finishInternalBatch(trace.ContextWithSpan(ctx, trace.SpanFromContext(call)), batch, a, watch)
		})
		if !settled {
			outcome = "unknown"
		}
		if watch != nil {
			s.startIssueWatch(life, b, watch, settled)
		}
	}
	if a.Status != "delivered" {
		s.internalEventDispatchRetries.Store(b.ID, time.Now().Add(time.Second))
	}
	s.updateBindingAfterDispatch(life, b, a)
	finish(outcome)
	return true
}
