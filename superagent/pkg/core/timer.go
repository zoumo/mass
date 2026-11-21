package core

import (
	"context"
	"database/sql"
	"log/slog"
	"regexp"
	"strings"
	"time"

	"go.opentelemetry.io/otel/trace"
)

const (
	timerScanInterval = 250 * time.Millisecond
	timerFollowup     = "timer.followup"
	// Configured channel sources cannot contain a colon.
	timerSource = "core:timer"
	timerStatus = "json_extract(body,'$.status')"
	timerDueAt  = "json_extract(body,'$.dueAt')"
)

var timerTime = regexp.MustCompile(`^[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}(\.[0-9]{1,9})?(Z|[+-]([01][0-9]|2[0-3]):[0-5][0-9])$`)

type Timer struct {
	ID               string     `json:"id"`
	TopicID          string     `json:"topicId"`
	TopicGeneration  int        `json:"topicGeneration"`
	Revision         int        `json:"revision"`
	DueAt            string     `json:"dueAt"`
	Content          string     `json:"content"`
	Type             string     `json:"type"`
	Materials        []Material `json:"materials,omitempty"`
	Status           string     `json:"status"`
	CreatedAt        string     `json:"createdAt"`
	UpdatedAt        string     `json:"updatedAt"`
	FiredAt          string     `json:"firedAt,omitempty"`
	TriggerMessageID string     `json:"triggerMessageId,omitempty"`
	ThreadKey        string     `json:"threadKey,omitempty"`
}

type SetTimerRequest struct {
	OperationID string     `json:"operationId"`
	DueAt       string     `json:"dueAt"`
	Content     string     `json:"content"`
	Materials   []Material `json:"materials,omitempty"`
}

type CancelTimerRequest struct {
	OperationID      string `json:"operationId"`
	TimerID          string `json:"timerId"`
	ExpectedRevision int    `json:"expectedRevision"`
}

type UpdateTimerRequest struct {
	CancelTimerRequest
	DueAt     string     `json:"dueAt"`
	Content   string     `json:"content"`
	Materials []Material `json:"materials,omitempty"`
}

func (t *Timer) setDetails(ctx context.Context, tx *sql.Tx, dueAt, content string, materials []Material) error {
	due, err := time.Parse(time.RFC3339Nano, dueAt)
	if err != nil || !timerTime.MatchString(dueAt) || due.UTC().Year() < 0 || due.UTC().Year() > 9999 {
		return problem("invalid_argument", "dueAt must be RFC3339 with Z or a numeric offset")
	}
	if strings.TrimSpace(content) == "" {
		return problem("invalid_argument", "Timer content is required")
	}
	if err := validateMaterials(ctx, tx, t.TopicID, materials); err != nil {
		return err
	}
	t.DueAt, t.Content, t.Materials = due.UTC().Format(utcTimestamp), content, materials
	return nil
}

func setTimer(r SetTimerRequest) workMutation {
	return func(ctx context.Context, tx *sql.Tx, b SessionBinding) (any, error) {
		key := creationKey("timer", b.ID, r.OperationID)
		digest := creationDigest(r)
		id, err := numberedCreation(ctx, tx, digest, "SELECT id,creation_digest FROM scheduled_tasks WHERE creation_key=?", key)
		if err != nil {
			return nil, err
		}
		if id != "" {
			var t Timer
			err := readJSON(ctx, tx, "SELECT body FROM scheduled_tasks WHERE id=?", &t, id)
			return t, err
		}
		topic, err := currentTopic(ctx, tx, b)
		if err != nil {
			return nil, err
		}
		at := now()
		t := Timer{
			TopicID: topic.ID, TopicGeneration: topic.LifecycleGeneration, Revision: 1,
			Type: timerFollowup, Status: "scheduled", CreatedAt: at, UpdatedAt: at,
		}
		if err := t.setDetails(ctx, tx, r.DueAt, r.Content, r.Materials); err != nil {
			return nil, err
		}
		var number int64
		if err := tx.QueryRowContext(ctx, `UPDATE topics SET timer_number=timer_number+1
 WHERE id=? RETURNING timer_number`, t.TopicID).Scan(&number); err != nil {
			return nil, storageError()
		}
		if err := tx.QueryRowContext(ctx, `INSERT INTO scheduled_tasks(topic_id,number,created_at,body,creation_key,creation_digest)
 VALUES(?,?,?,'{}',?,?) RETURNING id`, t.TopicID, number, at, key, digest).Scan(&t.ID); err != nil {
			return nil, storageError()
		}
		return t, saveTimerRevision(ctx, tx, t)
	}
}

func changeTimer(r CancelTimerRequest, update *UpdateTimerRequest) workMutation {
	return func(ctx context.Context, tx *sql.Tx, b SessionBinding) (any, error) {
		if r.TimerID == "" {
			return nil, problem("invalid_argument", "timerId is required")
		}
		var t Timer
		if err := readJSON(ctx, tx, "SELECT body FROM scheduled_tasks WHERE id=? AND topic_id=?", &t, r.TimerID, b.OwnerID); err != nil {
			return nil, err
		}
		if err := checkRevision(t.ID, r.ExpectedRevision, t.Revision); err != nil {
			return nil, err
		}
		if t.Status != "scheduled" {
			return nil, problem("failed_precondition", "only scheduled Timers can change; create a new Timer for another followup")
		}
		topic, err := currentTopic(ctx, tx, b)
		if err != nil {
			return nil, err
		}
		if t.TopicGeneration != topic.LifecycleGeneration {
			return nil, problem("failed_precondition", "Timer belongs to an earlier Topic lifecycle")
		}
		if update == nil {
			t.Status = "canceled"
		} else if err := t.setDetails(ctx, tx, update.DueAt, update.Content, update.Materials); err != nil {
			return nil, err
		}
		t.Revision++
		t.UpdatedAt = now()
		return t, saveTimerRevision(ctx, tx, t)
	}
}

func saveTimerRevision(ctx context.Context, tx *sql.Tx, t Timer) error {
	if err := putJSON(ctx, tx, "UPDATE scheduled_tasks SET body=?2 WHERE id=?1", t, t.ID); err != nil {
		return err
	}
	return timerRevision.save(ctx, tx, t.ID, t.Revision)
}

func invalidateTimers(ctx context.Context, tx *sql.Tx, topicID, at string) error {
	if _, err := tx.ExecContext(ctx, `UPDATE scheduled_tasks SET body=json_set(body,'$.status','invalidated','$.updatedAt',?)
 WHERE topic_id=? AND `+timerStatus+`='scheduled'`, at, topicID); err != nil {
		return storageError()
	}
	// The Message and Thread remain readable even if their original followup expires.
	if _, err := tx.ExecContext(ctx, `UPDATE internal_events SET status='superseded'
 WHERE event_id IN (SELECT json_extract(m.body,'$.eventId') FROM scheduled_tasks t
 JOIN trigger_messages m ON m.id=t.trigger_message_id WHERE t.topic_id=?)
 AND status IN ('pending','dispatching')`, topicID); err != nil {
		return storageError()
	}
	return nil
}

func (s *Store) scanTimers(ctx context.Context) {
	tick := time.NewTicker(timerScanInterval)
	defer tick.Stop()
	for ctx.Err() == nil {
		if err := s.fireDueTimers(ctx, timerNow().Format(utcTimestamp)); err != nil && ctx.Err() == nil {
			slog.Warn("Timer scan failed", "error", err)
		}
		select {
		case <-ctx.Done():
		case <-tick.C:
		}
	}
}

func (s *Store) fireDueTimers(ctx context.Context, cutoff string) error {
	rows, err := s.db.QueryContext(ctx, `SELECT id,json_extract(body,'$.revision') FROM scheduled_tasks
 WHERE `+timerStatus+`='scheduled' AND `+timerDueAt+`<=? ORDER BY `+timerDueAt+`,seq`, cutoff)
	if err != nil {
		return storageError()
	}
	defer func() { _ = rows.Close() }()
	type candidate struct {
		id       string
		revision int
	}
	var candidates []candidate
	for rows.Next() {
		var c candidate
		if err := rows.Scan(&c.id, &c.revision); err != nil {
			return storageError()
		}
		candidates = append(candidates, c)
	}
	if rows.Err() != nil || rows.Close() != nil {
		return storageError()
	}
	for _, c := range candidates {
		commitGate("timer_before_fire")
		if err := s.fireTimer(ctx, c.id, c.revision, cutoff); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) fireTimer(ctx context.Context, id string, revision int, cutoff string) (retErr error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return storageError()
	}
	defer func() { _ = tx.Rollback() }()
	var t Timer
	if err := readJSON(ctx, tx, "SELECT body FROM scheduled_tasks WHERE id=?", &t, id); err != nil {
		return err
	}
	if t.Status != "scheduled" || t.Revision != revision || t.DueAt > cutoff {
		return nil
	}
	var topic Topic
	if err := readJSON(ctx, tx, "SELECT body FROM topics WHERE id=?", &topic, t.TopicID); err != nil {
		return err
	}
	if topic.Status != "active" || topic.LifecycleGeneration != t.TopicGeneration {
		return nil
	}
	at := timerNow().Format(utcTimestamp)
	if t.DueAt > at {
		return nil
	}
	var links []trace.Link
	if s.telemetry.tracesEnabled {
		links = timerRevision.links(ctx, tx, nil, t.ID, t.Revision)
	}
	ctx, finish := s.telemetry.startOperation(ctx, "timer.fire", "", trace.WithLinks(links...))
	defer func() { finish(operationOutcome(retErr, "fired")) }()
	t.Status, t.FiredAt, t.UpdatedAt = "fired", at, at
	receipt, err := admitTimerMessage(ctx, tx, t)
	if err != nil {
		return err
	}
	if err := putJSON(ctx, tx, "UPDATE scheduled_tasks SET trigger_message_id=?2,body=?3 WHERE id=?1", t, t.ID, receipt.MessageID); err != nil {
		return err
	}
	commitGate("timer_before_commit")
	if err := tx.Commit(); err != nil {
		return storageError()
	}
	commitGate("timer_after_commit")
	return nil
}

func admitTimerMessage(ctx context.Context, tx *sql.Tx, t Timer) (Receipt, error) {
	source := Source{Source: timerSource}
	ref := ThreadRef{Source: source.Source, ID: t.ID}
	request := SubmitRequest{Message: MessageInput{
		Source: source.Source, SourceMessageID: t.ID, ThreadRef: ref,
		Actor: "Core", Text: t.Content, At: t.FiredAt,
		Meta: map[string]any{
			"type": t.Type, "taskId": t.ID, "topicId": t.TopicID,
			"taskRevision": t.Revision, "dueAt": t.DueAt, "materials": t.Materials,
		},
	}}
	if err := request.Message.validate(source); err != nil {
		return Receipt{}, err
	}
	canonical, err := request.Message.canonical()
	if err != nil {
		return Receipt{}, storageError()
	}
	key, at := ThreadKey(ref), now()
	if err := upsertThread(ctx, tx, key, ref, at, ""); err != nil {
		return Receipt{}, err
	}
	if _, err := ensureSubscription(ctx, tx, Subscription{TopicID: t.TopicID, ThreadKey: key, CreatedAt: at}); err != nil {
		return Receipt{}, err
	}
	return admitTriggerMessage(ctx, tx, source, request, canonical)
}
