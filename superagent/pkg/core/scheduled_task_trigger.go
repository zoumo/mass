package core

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"go.opentelemetry.io/otel/trace"
)

// Configured external Sources cannot use the Core namespace.
const scheduledTaskSource = "core:scheduled-task"

type dueScheduledTask struct {
	id       string
	revision int
	runAt    string
}

func dueScheduledTasks(ctx context.Context, db interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}, cutoff time.Time) ([]dueScheduledTask, error) {
	rows, err := db.QueryContext(ctx, `SELECT id,body->>'$.revision',next_run_at FROM scheduled_tasks
 WHERE status='active' AND deleted_at IS NULL AND next_run_at<=? ORDER BY next_run_at,seq`, cutoff.UTC().Format(utcTimestamp))
	if err != nil {
		return nil, storageError()
	}
	defer func() { _ = rows.Close() }()
	var result []dueScheduledTask
	for rows.Next() {
		var task dueScheduledTask
		if err := rows.Scan(&task.id, &task.revision, &task.runAt); err != nil {
			return nil, storageError()
		}
		result = append(result, task)
	}
	if rows.Err() != nil {
		return nil, storageError()
	}
	return result, nil
}

func recoverScheduledTasks(ctx context.Context, tx *sql.Tx, startedAt time.Time) error {
	missed, err := dueScheduledTasks(ctx, tx, startedAt.Add(-time.Nanosecond))
	if err != nil {
		return err
	}
	for _, candidate := range missed {
		task, err := readScheduledTask(ctx, tx, "id=?", candidate.id)
		if err != nil {
			return err
		}
		if err := task.advance(ctx, tx, startedAt.Add(-time.Nanosecond), startedAt); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) scanScheduledTasks(ctx context.Context) {
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	for ctx.Err() == nil {
		if err := s.fireDueScheduledTasks(ctx, scheduledTaskNow()); err != nil && ctx.Err() == nil {
			slog.Warn("ScheduledTask scan failed", "error", err)
		}
		select {
		case <-ctx.Done():
		case <-ticker.C:
		}
	}
}

func (s *Store) fireDueScheduledTasks(ctx context.Context, cutoff time.Time) error {
	candidates, err := dueScheduledTasks(ctx, s.db, cutoff)
	if err != nil {
		return err
	}
	var result error
	for _, candidate := range candidates {
		if ctx.Err() != nil {
			break
		}
		commitGate("scheduled_task_before_fire")
		result = errors.Join(result, s.fireScheduledTask(ctx, candidate))
	}
	return result
}

func (s *Store) fireScheduledTask(ctx context.Context, candidate dueScheduledTask) (retErr error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return storageError()
	}
	defer func() { _ = tx.Rollback() }()
	task, err := readScheduledTask(ctx, tx, "id=? AND deleted_at IS NULL", candidate.id)
	if errorCode(err) == "not_found" {
		return nil
	}
	if err != nil {
		return err
	}
	at := scheduledTaskNow()
	if task.Status != scheduledTaskActive || task.Revision != candidate.revision ||
		task.NextRunAt != candidate.runAt || task.NextRunAt > at.Format(utcTimestamp) {
		return nil
	}
	if err := task.checkOwner(ctx, tx); err != nil {
		return err
	}
	ctx, finish := s.startScheduledTaskTrigger(ctx, tx, task, "scheduled_task.fire")
	defer func() { finish(operationOutcome(retErr, "fired")) }()
	sourceMessageID := task.ID + "/scheduled/" + strconv.Itoa(task.Revision) + "/" + task.NextRunAt
	if _, err := admitScheduledTaskMessage(ctx, tx, task, sourceMessageID, "scheduled", task.NextRunAt); err != nil {
		return err
	}
	// A slow scan produces one message for the claimed time and skips elapsed slots.
	advanceAt := scheduledTaskNow()
	if advanceAt.Before(at) {
		advanceAt = at
	}
	if err := task.advance(ctx, tx, advanceAt, advanceAt); err != nil {
		return err
	}
	commitGate("scheduled_task_before_commit")
	if err := tx.Commit(); err != nil {
		return storageError()
	}
	commitGate("scheduled_task_after_commit")
	return nil
}

func (t *scheduledTaskRecord) advance(ctx context.Context, tx *sql.Tx, after, at time.Time) error {
	if t.Schedule.Type == "once" {
		t.end(at)
	} else {
		next, err := t.Schedule.next(t.anchor, after)
		if err != nil {
			return err
		}
		t.NextRunAt, t.UpdatedAt = next.UTC().Format(utcTimestamp), at.UTC().Format(utcTimestamp)
	}
	return t.save(ctx, tx)
}

func (s *Store) startScheduledTaskTrigger(ctx context.Context, tx *sql.Tx, task scheduledTaskRecord, operation string) (context.Context, func(string)) {
	var links []trace.Link
	if s.telemetry.tracesEnabled {
		links = scheduledTaskRevision.links(ctx, tx, nil, task.ID, task.Revision)
	}
	return s.telemetry.startOperation(ctx, operation, "", trace.WithLinks(links...))
}

func (s *Store) triggerScheduledTask(ctx context.Context, taskID, operationID string) (_ Receipt, retErr error) {
	if strings.TrimSpace(taskID) == "" || strings.TrimSpace(operationID) == "" {
		return Receipt{}, problem("invalid_argument", "scheduledTaskId and operationId are required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Receipt{}, storageError()
	}
	defer func() { _ = tx.Rollback() }()
	// The configured Bearer represents a single Operator authority across token rotations.
	sourceMessageID := taskID + "/manual/" + creationKey("operator", taskID, operationID)
	var m Message
	err = readJSON(ctx, tx, "SELECT body FROM trigger_messages WHERE source=? AND account_id='' AND native_id=?", &m, scheduledTaskSource, sourceMessageID)
	if err == nil {
		return messageReceipt(m, true), nil
	}
	if errorCode(err) != "not_found" {
		return Receipt{}, err
	}
	task, err := readScheduledTask(ctx, tx, "id=? AND deleted_at IS NULL", taskID)
	if err != nil {
		return Receipt{}, err
	}
	if task.Status != scheduledTaskActive {
		return Receipt{}, problem("failed_precondition", "only an active ScheduledTask may be triggered")
	}
	if err := task.checkOwner(ctx, tx); err != nil {
		return Receipt{}, err
	}
	ctx, finish := s.startScheduledTaskTrigger(ctx, tx, task, "scheduled_task.trigger")
	defer func() { finish(operationOutcome(retErr, "triggered")) }()
	receipt, err := admitScheduledTaskMessage(ctx, tx, task, sourceMessageID, "manual", "")
	if err != nil {
		return Receipt{}, err
	}
	if err := tx.Commit(); err != nil {
		return Receipt{}, storageError()
	}
	return receipt, nil
}

func admitScheduledTaskMessage(ctx context.Context, tx *sql.Tx, task scheduledTaskRecord, sourceMessageID, method, scheduledFor string) (Receipt, error) {
	source := Source{Source: scheduledTaskSource}
	request := SubmitTriggerMessageRequest{Message: TriggerMessage{
		SourceMessageID: sourceMessageID,
		ThreadID:        "core%3Ascheduled-task:" + task.ID,
		Content:         task.Content,
	}}
	message, canonical, err := request.prepare(source)
	if err != nil {
		return Receipt{}, err
	}
	message.scheduled = &scheduledTaskActivation{
		taskID: task.ID, revision: task.Revision, method: method, scheduledFor: scheduledFor,
	}
	if task.Owner != nil {
		message.scheduled.topicID = task.Owner.ID
	}
	return admitTriggerMessage(ctx, tx, source, message, canonical)
}

func scheduledTaskDeliveryCurrent(ctx context.Context, tx *sql.Tx, d internalEvent) (bool, error) {
	if len(d.Refs) == 0 {
		return true, nil
	}
	var current bool
	err := tx.QueryRowContext(ctx, `SELECT NOT EXISTS (
 SELECT 1 FROM trigger_messages m JOIN scheduled_tasks t ON t.id=m.scheduled_task_id
 JOIN topics p ON p.id=t.owner_id
 WHERE m.id=? AND t.owner_type='topic' AND t.owner_id=?
 AND (p.body->>'$.status'<>'active' OR p.body->>'$.lifecycleGeneration'<>t.topic_generation))`,
		d.Refs[0].ID, d.Recipient.TopicID).Scan(&current)
	if err != nil {
		return false, storageError()
	}
	return current, nil
}
