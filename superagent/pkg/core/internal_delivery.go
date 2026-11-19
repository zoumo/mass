package core

import (
	"context"
	"database/sql"
	"encoding/json"
	"time"

	"github.com/zoumo/mass/mass-go/pkg/resource"
)

type inputBatch struct {
	BindingID     string            `json:"bindingId"`
	Generation    int               `json:"generation"`
	Events        []json.RawMessage `json:"events"`
	SchemaVersion int               `json:"schemaVersion"`
}

type internalDeliveryBatch struct {
	wire   []byte
	items  []internalDelivery
	method string
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

func encodeInternalDelivery(d internalDelivery) (json.RawMessage, error) {
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
	batch.method = "inject"
	envelope := inputBatch{BindingID: b.ID, Generation: b.Generation, SchemaVersion: 4}
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
		raw, err := encodeInternalDelivery(q.item)
		if err != nil {
			return batch, err
		}
		envelope.Events = append(envelope.Events, raw)
		wire, err := json.Marshal(envelope)
		if err != nil {
			return batch, err
		}
		if len(wire) > batchBytes {
			full = true
			break
		}
		batch.wire = wire
		batch.items = append(batch.items, q.item)
		if q.item.Kind != contextSupplement {
			batch.method = "start_or_steer"
		}
	}
	if len(batch.items) == 0 || !full && len(batch.items) < batchLimit && at.Before(deadline) {
		batch = internalDeliveryBatch{}
	}
	return batch, tx.Commit()
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
	a := Attempt{ID: newID("attempt")}
	admitted := false
	a, watch := s.dispatchSessionInput(call, b, spec, a, batch.method, batch.wire, func(watch *workerWatch) (bool, error) {
		armed, err := s.store.admitInternalBatch(call, b, batch, watch)
		admitted = err == nil
		return armed, err
	})
	if admitted {
		commitGate("internal_delivery_before_settlement")
		settled := persistInputSettlement(life, batch.items[0].ID, func(ctx context.Context) error {
			return s.store.finishInternalBatch(ctx, batch, a, watch)
		})
		if watch != nil {
			s.startWorkerWatch(life, b, watch, settled)
		}
	}
	if a.Status != "delivered" {
		s.internalDeliveryRetries.Store(b.ID, time.Now().Add(time.Second))
	}
	s.updateBindingAfterDelivery(life, b, a)
	return true
}
