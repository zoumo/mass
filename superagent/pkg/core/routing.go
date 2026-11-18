package core

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
)

const routingSchema = `
CREATE INDEX events_target_order ON events(binding_id,seq,status);
CREATE TABLE core_settings (key TEXT PRIMARY KEY, value TEXT NOT NULL);
CREATE TABLE topics (seq INTEGER PRIMARY KEY AUTOINCREMENT, id TEXT NOT NULL UNIQUE, created_at TEXT NOT NULL, body BLOB NOT NULL);
CREATE TABLE bindings (seq INTEGER PRIMARY KEY AUTOINCREMENT, id TEXT NOT NULL UNIQUE, role TEXT NOT NULL, owner_id TEXT NOT NULL, generation INTEGER NOT NULL, created_at TEXT NOT NULL, token TEXT NOT NULL UNIQUE, spec BLOB NOT NULL, state TEXT NOT NULL, next_attempt_at TEXT NOT NULL DEFAULT '', body BLOB NOT NULL, UNIQUE(role,owner_id,generation));
CREATE UNIQUE INDEX sole_router ON bindings(role) WHERE role='router';
CREATE TABLE message_routes (message_id TEXT PRIMARY KEY REFERENCES messages(id), round INTEGER NOT NULL, state TEXT NOT NULL);
CREATE TABLE message_topics (message_id TEXT NOT NULL REFERENCES messages(id), topic_id TEXT NOT NULL REFERENCES topics(id), active INTEGER NOT NULL, round INTEGER NOT NULL, PRIMARY KEY(message_id,topic_id));
CREATE TABLE subscriptions (seq INTEGER PRIMARY KEY AUTOINCREMENT, id TEXT NOT NULL UNIQUE, topic_id TEXT NOT NULL REFERENCES topics(id), thread_key TEXT NOT NULL REFERENCES threads(key), created_at TEXT NOT NULL, body BLOB NOT NULL, UNIQUE(topic_id,thread_key));
CREATE TABLE decisions (seq INTEGER PRIMARY KEY AUTOINCREMENT, id TEXT NOT NULL UNIQUE, created_at TEXT NOT NULL, body BLOB NOT NULL);
CREATE TABLE relations (seq INTEGER PRIMARY KEY AUTOINCREMENT, id TEXT NOT NULL UNIQUE, message_id TEXT NOT NULL REFERENCES messages(id), topic_id TEXT NOT NULL REFERENCES topics(id), action TEXT NOT NULL, created_at TEXT NOT NULL, body BLOB NOT NULL);
CREATE TABLE role_operations (binding_id TEXT NOT NULL REFERENCES bindings(id), kind TEXT NOT NULL, operation_id TEXT NOT NULL, fingerprint BLOB NOT NULL, receipt BLOB NOT NULL, PRIMARY KEY(binding_id,kind,operation_id));
`

type Topic struct {
	ID                  string           `json:"id"`
	Title               string           `json:"title"`
	Description         string           `json:"description,omitempty"`
	Workspace           string           `json:"workspace"`
	Status              string           `json:"status"`
	Revision            int              `json:"revision"`
	BindingID           string           `json:"bindingId"`
	CreatedAt           string           `json:"createdAt"`
	UpdatedAt           string           `json:"updatedAt"`
	Completion          *Completion      `json:"completion,omitempty"`
	LifecycleGeneration int              `json:"lifecycleGeneration,omitempty"`
	Cancellation        *LifecycleChange `json:"cancellation,omitempty"`
	Reopening           *LifecycleChange `json:"reopening,omitempty"`
	Stop                *StopRequest     `json:"stop,omitempty"`
}
type Binding struct {
	ID                   string        `json:"id"`
	Role                 string        `json:"role"`
	OwnerID              string        `json:"ownerId"`
	Generation           int           `json:"generation"`
	Workspace            string        `json:"workspace"`
	Namespace            string        `json:"namespace"`
	Name                 string        `json:"name"`
	RuntimeRef           string        `json:"runtimeRef"`
	RuntimeType          string        `json:"runtimeType,omitempty"`
	RuntimeUID           string        `json:"runtimeUid,omitempty"`
	RuntimeVersion       string        `json:"runtimeResourceVersion,omitempty"`
	PreviousOwnerUnknown bool          `json:"previousOwnerUnknown,omitempty"`
	State                string        `json:"state"`
	CreatedAt            string        `json:"createdAt"`
	SessionID            string        `json:"sessionId,omitempty"`
	SessionUID           string        `json:"sessionUid,omitempty"`
	LastError            string        `json:"lastError,omitempty"`
	NextAttemptAt        string        `json:"nextAttemptAt,omitempty"`
	TopicID              string        `json:"topicId,omitempty"`
	IssueID              string        `json:"issueId,omitempty"`
	PreparationStarted   bool          `json:"preparationStarted"`
	CapabilityFileID     string        `json:"capabilityFileId,omitempty"`
	MCPWorkingDirectory  string        `json:"mcpWorkingDirectory,omitempty"`
	PreviousBindingID    string        `json:"previousBindingId,omitempty"`
	Close                *BindingClose `json:"close,omitempty"`
	ReuseError           string        `json:"reuseError,omitempty"`
	RecoveryOperationID  string        `json:"recoveryOperationId,omitempty"`
}
type NewTopic struct {
	Title       string `json:"title"`
	Description string `json:"description,omitempty"`
	Workspace   string `json:"workspace"`
}
type RouteTarget struct {
	TopicID          string    `json:"topicId,omitempty"`
	Create           *NewTopic `json:"create,omitempty"`
	MessageIDs       []string  `json:"messageIds"`
	RedeliveryReason string    `json:"redeliveryReason,omitempty"`
}
type RouteRequest struct {
	OperationID string         `json:"operationId"`
	MessageIDs  []string       `json:"messageIds"`
	Rounds      map[string]int `json:"rounds"`
	Kind        string         `json:"kind"`
	Targets     []RouteTarget  `json:"targets,omitempty"`
	Reason      string         `json:"reason"`
}
type RouteDecision struct {
	ID string `json:"id"`
	RouteRequest
	CreatedAt string `json:"createdAt"`
}
type ReturnRequest struct {
	OperationID string `json:"operationId"`
	MessageID   string `json:"messageId"`
	Round       int    `json:"round"`
	Reason      string `json:"reason"`
}
type Relation struct {
	ID         string `json:"id"`
	MessageID  string `json:"messageId"`
	TopicID    string `json:"topicId"`
	Action     string `json:"action"`
	Round      int    `json:"round"`
	Reason     string `json:"reason"`
	DecisionID string `json:"decisionId,omitempty"`
	CreatedAt  string `json:"createdAt"`
}

type Subscription struct {
	ID            string `json:"id"`
	TopicID       string `json:"topicId"`
	ThreadKey     string `json:"threadKey"`
	MessageID     string `json:"messageId,omitempty"`
	PullRequestID string `json:"pullRequestId,omitempty"`
	CreatedAt     string `json:"createdAt"`
}

func ensureSubscription(ctx context.Context, tx *sql.Tx, sub Subscription) (string, error) {
	sub.ID = newID("sub")
	if err := putJSON(ctx, tx, "INSERT INTO subscriptions(id,topic_id,thread_key,created_at,body) VALUES(?,?,?,?,?) ON CONFLICT(topic_id,thread_key) DO NOTHING",
		sub, sub.ID, sub.TopicID, sub.ThreadKey, sub.CreatedAt); err != nil {
		return "", err
	}
	if err := tx.QueryRowContext(ctx, "SELECT id FROM subscriptions WHERE topic_id=? AND thread_key=?", sub.TopicID, sub.ThreadKey).Scan(&sub.ID); err != nil {
		return "", storageError()
	}
	return sub.ID, nil
}

func putJSON(ctx context.Context, tx *sql.Tx, query string, value any, args ...any) error {
	b, err := json.Marshal(value)
	if err != nil {
		return storageError()
	}
	args = append(args, b)
	if _, err = tx.ExecContext(ctx, query, args...); err != nil {
		return storageError()
	}
	return nil
}

func readJSON(ctx context.Context, tx *sql.Tx, query string, dest any, args ...any) error {
	var b []byte
	if err := tx.QueryRowContext(ctx, query, args...).Scan(&b); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return problem("not_found", "resource not found")
		}
		return storageError()
	}
	if json.Unmarshal(b, dest) != nil {
		return storageError()
	}
	return nil
}

func acceptRoute(ctx context.Context, tx *sql.Tx, e *Event) error {
	rows, err := tx.QueryContext(ctx, "SELECT topic_id FROM subscriptions WHERE thread_key=? ORDER BY topic_id", e.ThreadKey)
	if err != nil {
		return storageError()
	}
	var ids []string
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			_ = rows.Close()
			return storageError()
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	_ = rows.Close()
	if err != nil {
		return storageError()
	}
	state := "pending"
	if len(ids) == 1 {
		state = "routed"
		e.TargetRole = "topic"
		e.TargetID = ids[0]
		if err := attachMessage(ctx, tx, e.MessageID, ids[0], 1, "single subscription", "", e.At); err != nil {
			return err
		}
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO message_routes(message_id,round,state) VALUES(?,1,?)", e.MessageID, state); err != nil {
		return storageError()
	}
	var binding string
	err = tx.QueryRowContext(ctx, "SELECT id FROM bindings WHERE role=? AND owner_id=? AND state<>'revoked'", e.TargetRole, e.TargetID).Scan(&binding)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return storageError()
	}
	e.BindingID = binding
	return EnqueueEvent(ctx, tx, *e)
}

func attachMessage(ctx context.Context, tx *sql.Tx, message, topic string, round int, reason, decision, at string) error {
	var key string
	if err := tx.QueryRowContext(ctx, "SELECT thread_key FROM messages WHERE id=?", message).Scan(&key); err != nil {
		return storageError()
	}
	if _, err := tx.ExecContext(ctx, "INSERT INTO message_topics(message_id,topic_id,active,round) VALUES(?,?,1,?) ON CONFLICT(message_id,topic_id) DO UPDATE SET active=1,round=excluded.round", message, topic, round); err != nil {
		return storageError()
	}
	if _, err := ensureSubscription(ctx, tx, Subscription{TopicID: topic, ThreadKey: key, MessageID: message, CreatedAt: at}); err != nil {
		return err
	}
	r := Relation{ID: newID("rel"), MessageID: message, TopicID: topic, Action: "received", Round: round, Reason: reason, DecisionID: decision, CreatedAt: at}
	return putJSON(ctx, tx, "INSERT INTO relations(id,message_id,topic_id,action,created_at,body) VALUES(?,?,?,?,?,?)", r, r.ID, message, topic, r.Action, at)
}

func roleReplay(ctx context.Context, tx *sql.Tx, binding, kind, operation string, req any) ([]byte, []byte, error) {
	if strings.TrimSpace(operation) == "" {
		return nil, nil, problem("invalid_argument", "operationId is required")
	}
	b, err := json.Marshal(req)
	if err != nil {
		return nil, nil, problem("invalid_argument", "invalid parameters")
	}
	fp := sha256.Sum256(b)
	var old, receipt []byte
	err = tx.QueryRowContext(ctx, "SELECT fingerprint,receipt FROM role_operations WHERE binding_id=? AND kind=? AND operation_id=?", binding, kind, operation).Scan(&old, &receipt)
	if err == nil {
		if !bytes.Equal(old, fp[:]) {
			return nil, nil, problem("conflict", "operationId has different parameters")
		}
		return receipt, fp[:], nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return nil, nil, storageError()
	}
	return nil, fp[:], nil
}

func roleReceipt(ctx context.Context, tx *sql.Tx, binding, kind, op string, fp []byte, result any) error {
	return putJSON(ctx, tx, "INSERT INTO role_operations(binding_id,kind,operation_id,fingerprint,receipt) VALUES(?,?,?,?,?)", result, binding, kind, op, fp)
}

func validateRoute(r RouteRequest) error {
	if len(r.MessageIDs) == 0 || len(r.Rounds) != len(r.MessageIDs) || strings.TrimSpace(r.Reason) == "" {
		return problem("invalid_argument", "explicit messageIds, rounds and reason are required")
	}
	if r.Kind != "route" && r.Kind != "ignore" {
		return problem("invalid_argument", "kind must be route or ignore; clarification is delivered separately by Outbox")
	}
	ids := map[string]bool{}
	for _, id := range r.MessageIDs {
		if id == "" || ids[id] || r.Rounds[id] < 1 {
			return problem("invalid_argument", "messageIds must be unique and each needs a positive round")
		}
		ids[id] = true
	}
	if r.Kind == "ignore" {
		if len(r.Targets) > 0 {
			return problem("invalid_argument", "ignore cannot have targets")
		}
		return nil
	}
	covered := map[string]bool{}
	targets := map[string]bool{}
	for _, target := range r.Targets {
		if (target.Create == nil) == (target.TopicID == "") || len(target.MessageIDs) == 0 {
			return problem("invalid_argument", "each target needs one topicId or create and explicit messageIds")
		}
		if target.Create != nil {
			if strings.TrimSpace(target.Create.Title) == "" || !filepath.IsAbs(target.Create.Workspace) {
				return problem("invalid_argument", "new Topic needs title and an absolute workspace selected by Router")
			}
		} else {
			if targets[target.TopicID] {
				return problem("invalid_argument", "combine duplicate target Topic entries")
			}
			targets[target.TopicID] = true
		}
		seen := map[string]bool{}
		for _, id := range target.MessageIDs {
			if !ids[id] || seen[id] {
				return problem("invalid_argument", "target messageIds must be a unique subset of decision messageIds")
			}
			seen[id] = true
			covered[id] = true
		}
	}
	if len(covered) != len(ids) {
		return problem("invalid_argument", "route must cover every decision Message")
	}
	return nil
}

func (s *Store) Route(ctx context.Context, b Binding, r RouteRequest) (json.RawMessage, error) {
	if b.Role != "router" {
		return nil, problem("forbidden", "only Router can decide routing")
	}
	if err := validateRoute(r); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, storageError()
	}
	defer func() { _ = tx.Rollback() }()
	if err := checkBinding(ctx, tx, b); err != nil {
		return nil, err
	}
	saved, fp, err := roleReplay(ctx, tx, b.ID, "route_decide", r.OperationID, r)
	if err != nil || saved != nil {
		return saved, err
	}
	for _, id := range r.MessageIDs {
		var round int
		var state string
		err = tx.QueryRowContext(ctx, "SELECT round,state FROM message_routes WHERE message_id=?", id).Scan(&round, &state)
		if errors.Is(err, sql.ErrNoRows) {
			return nil, problem("not_found", "source Message not found")
		}
		if err != nil {
			return nil, storageError()
		}
		if round != r.Rounds[id] || state != "pending" {
			return nil, problem("conflict", "Message routing round is stale or already decided")
		}
	}
	r.Targets = append([]RouteTarget(nil), r.Targets...)
	decision := RouteDecision{ID: newID("route"), RouteRequest: r, CreatedAt: now()}
	// All source rounds are checked in the same transaction before any targets are created.
	for i, target := range decision.Targets {
		if target.Create != nil {
			topic, createErr := s.newTopic(ctx, tx, *target.Create, decision.CreatedAt)
			if createErr != nil {
				return nil, createErr
			}
			target.TopicID = topic.ID
			decision.Targets[i].TopicID = topic.ID
		} else {
			var t Topic
			if err := readJSON(ctx, tx, "SELECT body FROM topics WHERE id=?", &t, target.TopicID); err != nil {
				return nil, err
			}
		}
		var binding string
		if err = tx.QueryRowContext(ctx, "SELECT id FROM bindings WHERE role='topic' AND owner_id=? AND state<>'revoked'", target.TopicID).Scan(&binding); err != nil && !errors.Is(err, sql.ErrNoRows) {
			return nil, storageError()
		}
		for _, id := range target.MessageIDs {
			var returned string
			err = tx.QueryRowContext(ctx, "SELECT body FROM relations WHERE topic_id=? AND message_id=? AND action='returned' ORDER BY seq DESC LIMIT 1", target.TopicID, id).Scan(&returned)
			if err == nil {
				var previous Relation
				if json.Unmarshal([]byte(returned), &previous) != nil {
					return nil, storageError()
				}
				if strings.TrimSpace(target.RedeliveryReason) == "" || strings.TrimSpace(target.RedeliveryReason) == strings.TrimSpace(previous.Reason) {
					return nil, problem("failed_precondition", "redelivery to a returning Topic requires a new explicit reason")
				}
			} else if !errors.Is(err, sql.ErrNoRows) {
				return nil, storageError()
			}
			reason := r.Reason
			if target.RedeliveryReason != "" {
				reason = target.RedeliveryReason
			}
			if err := attachMessage(ctx, tx, id, target.TopicID, r.Rounds[id], reason, decision.ID, decision.CreatedAt); err != nil {
				return nil, err
			}
			var key string
			if err = tx.QueryRowContext(ctx, "SELECT thread_key FROM messages WHERE id=?", id).Scan(&key); err != nil {
				return nil, storageError()
			}
			e := Event{ID: newID("evt"), Type: "message.routed", At: decision.CreatedAt, MessageID: id, ThreadKey: key, TargetRole: "topic", TargetID: target.TopicID, BindingID: binding, RouteRound: r.Rounds[id], Intent: "act", Status: "pending"}
			if err := EnqueueEvent(ctx, tx, e); err != nil {
				return nil, err
			}
		}
	}
	state := "routed"
	if r.Kind == "ignore" {
		state = "ignored"
	}
	for _, id := range r.MessageIDs {
		if _, err = tx.ExecContext(ctx, "UPDATE message_routes SET state=? WHERE message_id=?", state, id); err != nil {
			return nil, storageError()
		}
	}
	if err := putJSON(ctx, tx, "INSERT INTO decisions(id,created_at,body) VALUES(?,?,?)", decision, decision.ID, decision.CreatedAt); err != nil {
		return nil, err
	}
	if err := roleReceipt(ctx, tx, b.ID, "route_decide", r.OperationID, fp, decision); err != nil {
		return nil, err
	}
	commitGate("route_before_commit")
	if err = tx.Commit(); err != nil {
		return nil, storageError()
	}
	commitGate("route_after_commit")
	result, err := json.Marshal(decision)
	return result, err
}

func (s *Store) Return(ctx context.Context, b Binding, r ReturnRequest) (json.RawMessage, error) {
	if b.Role != "topic" {
		return nil, problem("forbidden", "only a Topic can return its own Message")
	}
	if r.Round < 1 || strings.TrimSpace(r.Reason) == "" {
		return nil, problem("invalid_argument", "round and reason are required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, storageError()
	}
	defer func() { _ = tx.Rollback() }()
	if err := checkBinding(ctx, tx, b); err != nil {
		return nil, err
	}
	saved, fp, err := roleReplay(ctx, tx, b.ID, "message_return", r.OperationID, r)
	if err != nil || saved != nil {
		return saved, err
	}
	var round, active int
	err = tx.QueryRowContext(ctx, "SELECT round,active FROM message_topics WHERE message_id=? AND topic_id=?", r.MessageID, b.OwnerID).Scan(&round, &active)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, problem("forbidden", "Message is outside current Topic")
	}
	if err != nil {
		return nil, storageError()
	}
	if round != r.Round || active != 1 {
		return nil, problem("conflict", "Message reception is stale or already returned")
	}
	var current int
	if err = tx.QueryRowContext(ctx, "SELECT round FROM message_routes WHERE message_id=?", r.MessageID).Scan(&current); err != nil {
		return nil, storageError()
	}
	current++
	if _, err = tx.ExecContext(ctx, "UPDATE message_routes SET round=?,state='pending' WHERE message_id=?", current, r.MessageID); err != nil {
		return nil, storageError()
	}
	if _, err = tx.ExecContext(ctx, "UPDATE message_topics SET active=0 WHERE message_id=? AND topic_id=?", r.MessageID, b.OwnerID); err != nil {
		return nil, storageError()
	}
	relation := Relation{ID: newID("rel"), MessageID: r.MessageID, TopicID: b.OwnerID, Action: "returned", Round: current, Reason: r.Reason, CreatedAt: now()}
	if err := putJSON(ctx, tx, "INSERT INTO relations(id,message_id,topic_id,action,created_at,body) VALUES(?,?,?,?,?,?)", relation, relation.ID, r.MessageID, b.OwnerID, relation.Action, relation.CreatedAt); err != nil {
		return nil, err
	}
	var key, binding string
	if err = tx.QueryRowContext(ctx, "SELECT thread_key FROM messages WHERE id=?", r.MessageID).Scan(&key); err != nil {
		return nil, storageError()
	}
	if err = tx.QueryRowContext(ctx, "SELECT id FROM bindings WHERE role='router' AND state<>'revoked'").Scan(&binding); err != nil {
		return nil, storageError()
	}
	payload, err := json.Marshal(relation)
	if err != nil {
		return nil, storageError()
	}
	e := Event{ID: newID("evt"), Type: "message.returned", At: relation.CreatedAt, MessageID: r.MessageID, ThreadKey: key, TargetRole: "router", BindingID: binding, RouteRound: current, Intent: "act", Status: "pending", Payload: payload}
	if err := EnqueueEvent(ctx, tx, e); err != nil {
		return nil, err
	}
	if err := roleReceipt(ctx, tx, b.ID, "message_return", r.OperationID, fp, relation); err != nil {
		return nil, err
	}
	commitGate("return_before_commit")
	if err = tx.Commit(); err != nil {
		return nil, storageError()
	}
	commitGate("return_after_commit")
	return payload, nil
}
