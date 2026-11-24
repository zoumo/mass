package core

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"slices"
	"strings"
	"time"

	"github.com/zoumo/mass/massd/pkg/events"
	"github.com/zoumo/mass/massd/pkg/runtime"
	coreent "github.com/zoumo/mass/superagent/pkg/core/internal/ent"
	"github.com/zoumo/mass/superagent/pkg/core/internal/ent/predicate"
	"github.com/zoumo/mass/superagent/pkg/core/internal/ent/scheduledtask"
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
	Content     []events.ContentBlock `json:"content" minItems:"1" nullable:"false"`
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

func (r *UpdateScheduledTaskRequest) UnmarshalJSON(data []byte) error {
	type wire UpdateScheduledTaskRequest
	return decodeScheduledTaskUpdate(data, (*wire)(r))
}

var scheduledTaskChangeFields = []string{"schedule", "content", "status"}

func decodeScheduledTaskUpdate(data []byte, request any) error {
	if err := decodeStrict(data, request); err != nil {
		return err
	}
	// A null JSON member and an omitted member both decode to a nil pointer.
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return err
	}
	for name, raw := range fields {
		if bytes.Equal(raw, []byte("null")) && slices.ContainsFunc(scheduledTaskChangeFields, func(field string) bool {
			return strings.EqualFold(name, field)
		}) {
			return problem("invalid_argument", name+" must not be null")
		}
	}
	return nil
}

type scheduledTaskRecord struct {
	ScheduledTask
	seq             int64
	anchor          time.Time
	topicGeneration int
	deletedAt       *string
}

func queryScheduledTask(ctx context.Context, query *coreent.ScheduledTaskSelect) (*coreent.ScheduledTask, error) {
	row, err := query.Only(ctx)
	if coreent.IsNotFound(err) {
		return nil, problem("not_found", "resource not found in current scope")
	}
	if err != nil {
		return nil, storageError()
	}
	return row, nil
}

func readScheduledTask(ctx context.Context, tasks *coreent.ScheduledTaskClient, filters ...predicate.ScheduledTask) (scheduledTaskRecord, error) {
	var task scheduledTaskRecord
	row, err := queryScheduledTask(ctx, tasks.Query().Where(filters...).
		Select(scheduledtask.FieldBody, scheduledtask.FieldScheduleAnchor, scheduledtask.FieldTopicGeneration))
	if err != nil {
		return task, err
	}
	if err := json.Unmarshal(row.Body, &task.ScheduledTask); err != nil {
		return task, storageError()
	}
	task.anchor, err = time.Parse(utcTimestamp, row.ScheduleAnchor)
	if err != nil {
		return task, storageError()
	}
	task.seq, task.topicGeneration = row.ID, row.TopicGeneration
	return task, nil
}

func readScheduledTaskBody(ctx context.Context, tx *sql.Tx, id string, binding *SessionBinding) (json.RawMessage, error) {
	query := entClientForTx(tx).ScheduledTask.Query().Where(scheduledtask.BusinessIDEQ(id), scheduledtask.DeletedAtIsNil())
	if binding != nil {
		query.Where(scheduledtask.OwnerTypeEQ(scheduledtask.OwnerTypeTopic), scheduledtask.OwnerIDEQ(binding.OwnerID))
	}
	row, err := queryScheduledTask(ctx, query.Select(scheduledtask.FieldBody))
	if err != nil {
		return nil, err
	}
	return row.Body, nil
}

func (t *scheduledTaskRecord) save(ctx context.Context, tasks *coreent.ScheduledTaskClient) error {
	body, err := json.Marshal(t.ScheduledTask)
	if err != nil {
		return storageError()
	}
	updated, err := tasks.Update().Where(scheduledtask.IDEQ(t.seq)).
		SetScheduleAnchor(t.anchor.Format(utcTimestamp)).SetBody(body).SetNillableDeletedAt(t.deletedAt).Save(ctx)
	if err != nil || updated != 1 {
		return storageError()
	}
	return nil
}

func (t *scheduledTaskRecord) saveRevision(ctx context.Context, tx *sql.Tx, tasks *coreent.ScheduledTaskClient) error {
	if err := t.save(ctx, tasks); err != nil {
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
		tasks := entClientForTx(tx).ScheduledTask
		existing, err := tasks.Query().Where(scheduledtask.CreationKeyEQ(key)).
			Select(scheduledtask.FieldCreationDigest, scheduledtask.FieldBody).Only(ctx)
		switch {
		case err == nil:
			if existing.CreationDigest != digest {
				return nil, problem("conflict", "operationId has different creation parameters")
			}
			var task ScheduledTask
			if err := json.Unmarshal(existing.Body, &task); err != nil {
				return nil, storageError()
			}
			return task, nil
		case !coreent.IsNotFound(err):
			return nil, storageError()
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
		if b.Role != "operator" {
			topic, err := currentTopic(ctx, tx, b)
			if err != nil {
				return nil, err
			}
			task.Owner = &ScheduledTaskOwner{Type: "topic", ID: topic.ID}
			task.topicGeneration = topic.LifecycleGeneration
		}
		body, err := json.Marshal(task.ScheduledTask)
		if err != nil {
			return nil, storageError()
		}
		create := tasks.Create().SetTopicGeneration(task.topicGeneration).SetCreatedAt(task.CreatedAt).
			SetScheduleAnchor(task.CreatedAt).SetBody(body).SetCreationKey(key).SetCreationDigest(digest)
		if task.Owner != nil {
			create.SetOwnerType(scheduledtask.OwnerTypeTopic).SetOwnerID(task.Owner.ID)
		}
		row, err := create.Save(ctx)
		if err != nil {
			return nil, storageError()
		}
		// Ent gets the SQLite sequence from INSERT. Read the generated public ID
		// before persisting it in the task body and saving its revision trace.
		stored, err := tasks.Query().Where(scheduledtask.IDEQ(row.ID)).Select(scheduledtask.FieldBusinessID).Only(ctx)
		if err != nil || stored.BusinessID == "" {
			return nil, storageError()
		}
		task.seq, task.ID = row.ID, stored.BusinessID
		return task.ScheduledTask, task.saveRevision(ctx, tx, tasks)
	}
}

func scheduledTaskForChange(ctx context.Context, tasks *coreent.ScheduledTaskClient, b SessionBinding, r ScheduledTaskRevision) (scheduledTaskRecord, error) {
	if r.ScheduledTaskID == "" {
		return scheduledTaskRecord{}, problem("invalid_argument", "scheduledTaskId is required")
	}
	filters := []predicate.ScheduledTask{scheduledtask.BusinessIDEQ(r.ScheduledTaskID), scheduledtask.DeletedAtIsNil()}
	if b.Role == "operator" {
		filters = append(filters, scheduledtask.OwnerTypeIsNil())
	} else {
		filters = append(filters, scheduledtask.OwnerTypeEQ(scheduledtask.OwnerTypeTopic), scheduledtask.OwnerIDEQ(b.OwnerID))
	}
	task, err := readScheduledTask(ctx, tasks, filters...)
	if err != nil {
		return task, err
	}
	return task, checkRevision(task.ID, r.ExpectedRevision, task.Revision)
}

func updateScheduledTask(r UpdateScheduledTaskRequest) workMutation {
	return func(ctx context.Context, tx *sql.Tx, b SessionBinding) (any, error) {
		tasks := entClientForTx(tx).ScheduledTask
		task, err := scheduledTaskForChange(ctx, tasks, b, r.ScheduledTaskRevision)
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
		return task.ScheduledTask, task.saveRevision(ctx, tx, tasks)
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
		tasks := entClientForTx(tx).ScheduledTask
		task, err := scheduledTaskForChange(ctx, tasks, b, r)
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
		task.deletedAt = &task.UpdatedAt
		return map[string]string{"scheduledTaskId": task.ID}, task.saveRevision(ctx, tx, tasks)
	}
}

func (t *scheduledTaskRecord) end(at time.Time) {
	t.Status, t.NextRunAt, t.UpdatedAt = scheduledTaskCompleted, "", at.UTC().Format(utcTimestamp)
}

func (t *scheduledTaskRecord) checkOwner(ctx context.Context, tx *sql.Tx) error {
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
	exists, err := s.entities.ScheduledTask.Query().Where(scheduledtask.BusinessIDEQ(id)).Exist(ctx)
	if err != nil {
		return false, storageError()
	}
	return exists, nil
}

func closeTopicScheduledTasks(ctx context.Context, tx *sql.Tx, topicID string, generation int, at string) error {
	// Let SQLite patch each matching body and recompute the generated columns
	// in one statement while keeping other JSON members intact.
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
