package core

import (
	"context"
	"database/sql"
	"encoding/json"
	"log/slog"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const (
	timerScanInterval = 250 * time.Millisecond
	timerFollowup     = "timer.followup"
	timerStatus       = "json_extract(body,'$.status')"
	timerDueAt        = "json_extract(body,'$.dueAt')"
	timerSchema       = `
CREATE TABLE timers (
 seq INTEGER PRIMARY KEY AUTOINCREMENT, id TEXT NOT NULL UNIQUE, topic_id TEXT NOT NULL REFERENCES topics(id),
 created_at TEXT NOT NULL, body BLOB NOT NULL
);
CREATE INDEX timers_topic ON timers(topic_id,created_at,id);
CREATE INDEX timers_due ON timers(` + timerDueAt + `,seq) WHERE ` + timerStatus + `='scheduled';
`
)

var timerTime = regexp.MustCompile(`^[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}(\.[0-9]{1,9})?(Z|[+-]([01][0-9]|2[0-3]):[0-5][0-9])$`)

type Timer struct {
	ID              string     `json:"id"`
	TopicID         string     `json:"topicId"`
	TopicGeneration int        `json:"topicGeneration"`
	Revision        int        `json:"revision"`
	DueAt           string     `json:"dueAt"`
	Content         string     `json:"content"`
	Materials       []Material `json:"materials,omitempty"`
	Status          string     `json:"status"`
	CreatedAt       string     `json:"createdAt"`
	UpdatedAt       string     `json:"updatedAt"`
	FiredAt         string     `json:"firedAt,omitempty"`
	FollowupEventID string     `json:"followupEventId,omitempty"`
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
	return func(ctx context.Context, tx *sql.Tx, b Binding) (any, error) {
		topic, err := currentTopic(ctx, tx, b)
		if err != nil {
			return nil, err
		}
		at := now()
		t := Timer{
			ID: newID("timer"), TopicID: topic.ID, TopicGeneration: topic.LifecycleGeneration, Revision: 1,
			Status: "scheduled", CreatedAt: at, UpdatedAt: at,
		}
		if err := t.setDetails(ctx, tx, r.DueAt, r.Content, r.Materials); err != nil {
			return nil, err
		}
		return t, putJSON(ctx, tx, "INSERT INTO timers(id,topic_id,created_at,body) VALUES(?,?,?,?)", t, t.ID, t.TopicID, at)
	}
}

func changeTimer(r CancelTimerRequest, update *UpdateTimerRequest) workMutation {
	return func(ctx context.Context, tx *sql.Tx, b Binding) (any, error) {
		if r.TimerID == "" {
			return nil, problem("invalid_argument", "timerId is required")
		}
		var t Timer
		if err := readJSON(ctx, tx, "SELECT body FROM timers WHERE id=? AND topic_id=?", &t, r.TimerID, b.OwnerID); err != nil {
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
		return t, saveTimer(ctx, tx, t)
	}
}

func saveTimer(ctx context.Context, tx *sql.Tx, t Timer) error {
	return putJSON(ctx, tx, "UPDATE timers SET body=?2 WHERE id=?1", t, t.ID)
}

func invalidateTimers(ctx context.Context, tx *sql.Tx, topicID, at string) error {
	if _, err := tx.ExecContext(ctx, `UPDATE timers SET body=json_set(body,'$.status','invalidated','$.updatedAt',?)
 WHERE topic_id=? AND `+timerStatus+`='scheduled'`, at, topicID); err != nil {
		return storageError()
	}
	return nil
}

func (s *Store) scanTimers(ctx context.Context) {
	tick := time.NewTicker(timerScanInterval)
	defer tick.Stop()
	for ctx.Err() == nil {
		if err := s.fireDueTimers(ctx, now()); err != nil && ctx.Err() == nil {
			slog.Warn("Timer scan failed", "error", err)
		}
		select {
		case <-ctx.Done():
		case <-tick.C:
		}
	}
}

func (s *Store) fireDueTimers(ctx context.Context, cutoff string) error {
	rows, err := s.db.QueryContext(ctx, `SELECT id,json_extract(body,'$.revision') FROM timers
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

func (s *Store) fireTimer(ctx context.Context, id string, revision int, cutoff string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return storageError()
	}
	defer func() { _ = tx.Rollback() }()
	var t Timer
	if err := readJSON(ctx, tx, "SELECT body FROM timers WHERE id=?", &t, id); err != nil {
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
	at := now()
	if t.DueAt > at {
		return nil
	}
	t.Status, t.FiredAt, t.UpdatedAt = "fired", at, at
	t.FollowupEventID = t.ID + "_followup_" + strconv.Itoa(t.Revision)
	payload, err := json.Marshal(t)
	if err != nil {
		return storageError()
	}
	if err := saveTimer(ctx, tx, t); err != nil {
		return err
	}
	if err := EnqueueEvent(ctx, tx, Event{
		ID: t.FollowupEventID, Type: timerFollowup, At: t.FiredAt, Intent: "act",
		TargetRole: "topic", TargetID: t.TopicID, TopicGeneration: &t.TopicGeneration, Revision: t.Revision, Payload: payload,
	}); err != nil {
		return err
	}
	commitGate("timer_before_commit")
	if err := tx.Commit(); err != nil {
		return storageError()
	}
	commitGate("timer_after_commit")
	return nil
}
