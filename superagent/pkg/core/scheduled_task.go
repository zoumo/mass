package core

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/zoumo/mass/massd/pkg/events"
	"github.com/zoumo/mass/massd/pkg/runtime"
)

const (
	scheduledTaskActive    = "active"
	scheduledTaskPaused    = "paused"
	scheduledTaskCompleted = "completed"
)

type ScheduledTaskOwner struct {
	Type string `json:"type" enum:"topic"`
	ID   string `json:"id"`
}

type ScheduledTask struct {
	ID        string                `json:"id"`
	Owner     *ScheduledTaskOwner   `json:"owner,omitempty"`
	Schedule  ScheduledTaskSchedule `json:"schedule"`
	Content   []events.ContentBlock `json:"content"`
	Status    string                `json:"status" enum:"active,paused,completed"`
	NextRunAt string                `json:"nextRunAt,omitempty" format:"date-time"`
	Revision  int                   `json:"revision"`
	CreatedAt string                `json:"createdAt" format:"date-time"`
	UpdatedAt string                `json:"updatedAt" format:"date-time"`
}

type CreateScheduledTaskRequest struct {
	OperationID string                `json:"operationId"`
	Schedule    ScheduledTaskSchedule `json:"schedule"`
	Content     []events.ContentBlock `json:"content" minItems:"1"`
}

type ScheduledTaskRevision struct {
	OperationID      string `json:"operationId"`
	ScheduledTaskID  string `json:"scheduledTaskId"`
	ExpectedRevision int    `json:"expectedRevision" minimum:"1"`
}

type ScheduledTaskChanges struct {
	Schedule *ScheduledTaskSchedule `json:"schedule,omitempty"`
	Content  *[]events.ContentBlock `json:"content,omitempty" minItems:"1"`
	Status   *string                `json:"status,omitempty" enum:"active,paused"`
}

type UpdateScheduledTaskRequest struct {
	ScheduledTaskRevision
	ScheduledTaskChanges
}

type scheduledTaskRecord struct {
	ScheduledTask
	anchor          time.Time
	topicGeneration int
}

func readScheduledTask(ctx context.Context, tx *sql.Tx, filter string, args ...any) (scheduledTaskRecord, error) {
	var task scheduledTaskRecord
	var raw []byte
	var anchor string
	err := tx.QueryRowContext(ctx, `SELECT body,schedule_anchor,topic_generation FROM scheduled_tasks WHERE `+filter, args...).
		Scan(&raw, &anchor, &task.topicGeneration)
	if errors.Is(err, sql.ErrNoRows) {
		return task, problem("not_found", "resource not found in current scope")
	}
	if err != nil || json.Unmarshal(raw, &task.ScheduledTask) != nil {
		return task, storageError()
	}
	task.anchor, err = time.Parse(utcTimestamp, anchor)
	if err != nil {
		return task, storageError()
	}
	return task, nil
}

func (t scheduledTaskRecord) save(ctx context.Context, tx *sql.Tx) error {
	return putJSON(ctx, tx, "UPDATE scheduled_tasks SET schedule_anchor=?2,body=?3 WHERE id=?1",
		t.ScheduledTask, t.ID, t.anchor.Format(utcTimestamp))
}

func (t scheduledTaskRecord) saveRevision(ctx context.Context, tx *sql.Tx) error {
	if err := t.save(ctx, tx); err != nil {
		return err
	}
	return scheduledTaskRevision.save(ctx, tx, t.ID, t.Revision)
}

func validateScheduledContent(content []events.ContentBlock) error {
	if err := (runtime.Input{Content: content}).Validate(); err != nil {
		return problem("invalid_argument", err.Error())
	}
	return nil
}

func createScheduledTask(r CreateScheduledTaskRequest) workMutation {
	return func(ctx context.Context, tx *sql.Tx, b SessionBinding) (any, error) {
		scope := b.ID
		if b.Role == "operator" {
			scope = b.Role
		}
		key := creationKey("scheduled-task", scope, r.OperationID)
		digest := creationDigest(r)
		id, err := numberedCreation(ctx, tx, digest, "SELECT id,creation_digest FROM scheduled_tasks WHERE creation_key=?", key)
		if err != nil {
			return nil, err
		}
		if id != "" {
			var task ScheduledTask
			err := readJSON(ctx, tx, "SELECT body FROM scheduled_tasks WHERE id=?", &task, id)
			return task, err
		}
		if err := validateScheduledContent(r.Content); err != nil {
			return nil, err
		}
		at := scheduledTaskNow()
		next, err := r.Schedule.next(at, at)
		if err != nil {
			return nil, err
		}
		task := scheduledTaskRecord{
			ScheduledTask: ScheduledTask{
				Schedule: r.Schedule, Content: r.Content, Status: scheduledTaskActive,
				NextRunAt: next.UTC().Format(utcTimestamp), Revision: 1,
				CreatedAt: at.Format(utcTimestamp), UpdatedAt: at.Format(utcTimestamp),
			},
			anchor: at,
		}
		var ownerType, ownerID any
		if b.Role != "operator" {
			topic, err := currentTopic(ctx, tx, b)
			if err != nil {
				return nil, err
			}
			task.Owner = &ScheduledTaskOwner{Type: "topic", ID: topic.ID}
			task.topicGeneration = topic.LifecycleGeneration
			ownerType, ownerID = task.Owner.Type, task.Owner.ID
		}
		body, err := json.Marshal(task.ScheduledTask)
		if err != nil {
			return nil, storageError()
		}
		if err := tx.QueryRowContext(ctx, `INSERT INTO scheduled_tasks(owner_type,owner_id,topic_generation,created_at,schedule_anchor,body,creation_key,creation_digest)
 VALUES(?,?,?,?,?,?,?,?) RETURNING id`, ownerType, ownerID, task.topicGeneration, task.CreatedAt, task.CreatedAt, body, key, digest).Scan(&task.ID); err != nil {
			return nil, storageError()
		}
		return task.ScheduledTask, task.saveRevision(ctx, tx)
	}
}

func scheduledTaskForChange(ctx context.Context, tx *sql.Tx, b SessionBinding, r ScheduledTaskRevision) (scheduledTaskRecord, error) {
	if r.ScheduledTaskID == "" {
		return scheduledTaskRecord{}, problem("invalid_argument", "scheduledTaskId is required")
	}
	var task scheduledTaskRecord
	var err error
	if b.Role == "operator" {
		task, err = readScheduledTask(ctx, tx, "id=? AND owner_type IS NULL AND deleted_at IS NULL", r.ScheduledTaskID)
	} else {
		task, err = readScheduledTask(ctx, tx, "id=? AND owner_type='topic' AND owner_id=? AND deleted_at IS NULL", r.ScheduledTaskID, b.OwnerID)
	}
	if err != nil {
		return task, err
	}
	return task, checkRevision(task.ID, r.ExpectedRevision, task.Revision)
}

func updateScheduledTask(r UpdateScheduledTaskRequest) workMutation {
	return func(ctx context.Context, tx *sql.Tx, b SessionBinding) (any, error) {
		task, err := scheduledTaskForChange(ctx, tx, b, r.ScheduledTaskRevision)
		if err != nil {
			return nil, err
		}
		if task.Status == scheduledTaskCompleted {
			return nil, problem("failed_precondition", "completed ScheduledTask cannot be updated")
		}
		if err := task.checkOwner(ctx, tx); err != nil {
			return nil, err
		}
		if err := task.apply(r.ScheduledTaskChanges, scheduledTaskNow()); err != nil {
			return nil, err
		}
		return task.ScheduledTask, task.saveRevision(ctx, tx)
	}
}

func (t *scheduledTaskRecord) apply(changes ScheduledTaskChanges, at time.Time) error {
	if changes.Schedule == nil && changes.Content == nil && changes.Status == nil {
		return problem("invalid_argument", "provide a schedule, content or status change")
	}
	previousStatus := t.Status
	if changes.Status != nil {
		switch *changes.Status {
		case scheduledTaskActive, scheduledTaskPaused:
			t.Status = *changes.Status
		default:
			return problem("invalid_argument", "status must be active or paused")
		}
	}
	if changes.Content != nil {
		if err := validateScheduledContent(*changes.Content); err != nil {
			return err
		}
		t.Content = *changes.Content
	}
	if changes.Schedule != nil {
		t.Schedule, t.anchor = *changes.Schedule, at
	}
	if changes.Schedule != nil || previousStatus != t.Status && t.Status == scheduledTaskActive {
		next, err := t.Schedule.next(t.anchor, at)
		if err != nil {
			return err
		}
		if previousStatus == scheduledTaskPaused && changes.Schedule == nil && t.Schedule.Type == "once" && next.Before(at) {
			t.Status = scheduledTaskCompleted
		}
		t.NextRunAt = next.UTC().Format(utcTimestamp)
	}
	if t.Status != scheduledTaskActive {
		t.NextRunAt = ""
	}
	t.Revision++
	t.UpdatedAt = at.Format(utcTimestamp)
	return nil
}

func deleteScheduledTask(r ScheduledTaskRevision) workMutation {
	return func(ctx context.Context, tx *sql.Tx, b SessionBinding) (any, error) {
		task, err := scheduledTaskForChange(ctx, tx, b, r)
		if err != nil {
			return nil, err
		}
		if b.Role == "topic" {
			if _, err := currentTopic(ctx, tx, b); err != nil {
				return nil, err
			}
		}
		task.end(scheduledTaskNow())
		task.Revision++
		if _, err := tx.ExecContext(ctx, "UPDATE scheduled_tasks SET deleted_at=? WHERE id=?", task.UpdatedAt, task.ID); err != nil {
			return nil, storageError()
		}
		return map[string]string{"scheduledTaskId": task.ID}, task.saveRevision(ctx, tx)
	}
}

func (t *scheduledTaskRecord) end(at time.Time) {
	t.Status, t.NextRunAt, t.UpdatedAt = scheduledTaskCompleted, "", at.UTC().Format(utcTimestamp)
}

func (t scheduledTaskRecord) checkOwner(ctx context.Context, tx *sql.Tx) error {
	if t.Owner == nil {
		return nil
	}
	var topic Topic
	if err := readJSON(ctx, tx, "SELECT body FROM topics WHERE id=?", &topic, t.Owner.ID); err != nil {
		return err
	}
	if topic.Status != "active" || topic.LifecycleGeneration != t.topicGeneration {
		return problem("failed_precondition", "ScheduledTask owner is outside its active Topic lifecycle")
	}
	return nil
}

func (s *Store) scheduledTaskExists(ctx context.Context, id string) (bool, error) {
	var exists bool
	if err := s.db.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM scheduled_tasks WHERE id=?)", id).Scan(&exists); err != nil {
		return false, storageError()
	}
	return exists, nil
}

func closeTopicScheduledTasks(ctx context.Context, tx *sql.Tx, topicID string, generation int, at string) error {
	if _, err := tx.ExecContext(ctx, `UPDATE scheduled_tasks SET body=json_remove(
 json_set(body,'$.status','completed','$.updatedAt',?), '$.nextRunAt')
 WHERE owner_type='topic' AND owner_id=? AND topic_generation=? AND deleted_at IS NULL AND status IN ('active','paused')`,
		at, topicID, generation); err != nil {
		return storageError()
	}
	// A task can be deleted or have finished scheduling while its Message is still in flight.
	if _, err := tx.ExecContext(ctx, `UPDATE internal_events SET status='superseded'
 WHERE status IN ('pending','dispatching') AND event_id IN (
 SELECT m.body->>'$.eventId' FROM scheduled_tasks t JOIN trigger_messages m ON m.scheduled_task_id=t.id
 WHERE t.owner_type='topic' AND t.owner_id=? AND t.topic_generation=?)`, topicID, generation); err != nil {
		return storageError()
	}
	return nil
}
