package core

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
)

const internalDeliverySchema = `
CREATE TABLE internal_deliveries (
 id TEXT PRIMARY KEY NOT NULL, kind TEXT NOT NULL, sender TEXT NOT NULL, recipient TEXT NOT NULL,
 content TEXT CHECK (content IS NULL OR (` + structuredContentObjectCheck + `)),
 body TEXT, refs TEXT NOT NULL DEFAULT '[]', created_at TEXT NOT NULL,
 status TEXT NOT NULL CHECK(status IN ('pending','dispatching','sent','superseded')), sent_at TEXT
);
CREATE INDEX internal_deliveries_pending ON internal_deliveries(` + deliveryRecipientKind + `, ` + deliveryRecipientOwner + `)
 WHERE status IN ('pending','dispatching');
`

const contextSupplement = "context_supplement"

const (
	deliveryRecipientKind  = `json_extract(recipient,'$.kind')`
	deliveryRecipientOwner = `COALESCE(json_extract(recipient,'$.topic_id'),json_extract(recipient,'$.execution_id'),'')`
)

type deliveryIdentity struct {
	Kind        string `json:"kind"`
	TopicID     string `json:"topic_id,omitempty"`
	ExecutionID string `json:"execution_id,omitempty"`
}

func (r deliveryIdentity) valid() bool {
	switch r.Kind {
	case "core", "router":
		return r.TopicID == "" && r.ExecutionID == ""
	case "topic":
		return r.TopicID != "" && len(r.TopicID) <= 512 && r.ExecutionID == ""
	case "worker":
		return r.ExecutionID != "" && len(r.ExecutionID) <= 512 && r.TopicID == ""
	default:
		return false
	}
}

type deliveryReference struct {
	Kind         string `json:"kind"`
	ID           string `json:"id"`
	IssueID      string `json:"issue_id,omitempty"`
	GoalRevision int    `json:"goal_revision,omitempty"`
}

type internalDelivery struct {
	ID        string              `json:"delivery_id"`
	Kind      string              `json:"kind"`
	Sender    deliveryIdentity    `json:"sender"`
	Recipient deliveryIdentity    `json:"recipient"`
	Content   *StructuredContent  `json:"content,omitempty"`
	Body      json.RawMessage     `json:"body,omitempty"`
	Refs      []deliveryReference `json:"refs,omitempty"`
	// Optional facts on the copy used for dispatch.
	truncated     bool
	promptVersion int
	threadKey     string
	routing       json.RawMessage
	quotedReply   json.RawMessage
	issue         *Issue
	report        *Report
	messages      []json.RawMessage
}

// AgentPromptView encodes the fields an agent needs to act on the source record.
type AgentPromptView interface {
	MarshalAgentPromptJSON() ([]byte, error)
}

var _ AgentPromptView = internalDelivery{}

func (d internalDelivery) MarshalAgentPromptJSON() ([]byte, error) {
	event := map[string]any{"delivery_id": d.ID, "kind": d.Kind}
	var refKind string
	var bodyKeys []string
	switch d.Kind {
	case routeRequestKind, routedMessageKind:
		refKind = messageRefKind
	case reportSubmittedKind:
		refKind = "report"
	case "outbox_failed":
		refKind = "outbox"
	case workerUpdateKind:
		bodyKeys = []string{"materials"}
	case contextSupplement:
		switch d.Recipient.Kind {
		case "worker":
			bodyKeys = []string{"materials"}
		case "topic":
			refKind = "report"
			bodyKeys = []string{"type", "dependencyIssueId", "dependentIssueIds", "recovery"}
			if d.promptVersion >= agentPromptVersion {
				bodyKeys = append(bodyKeys, "previousStatus", "status")
			}
		}
	}
	if refKind != "" {
		var refs []deliveryReference
		for _, ref := range d.Refs {
			if ref.Kind == refKind || d.Kind == routeRequestKind && ref.Kind == returnedRefKind {
				refs = append(refs, deliveryReference{Kind: ref.Kind, ID: ref.ID})
			}
		}
		if len(refs) != 0 {
			event["refs"] = refs
		}
	}
	if d.truncated {
		event["truncated"] = true
		return json.Marshal(event)
	}
	if d.Content != nil && (d.Kind == routedMessageKind || d.Kind == workerUpdateKind || d.Kind == contextSupplement ||
		d.Kind == routeRequestKind && d.promptVersion >= agentPromptVersion) {
		event["content"] = d.Content
	}
	if d.Kind == routeRequestKind && len(d.routing) != 0 {
		event["threadKey"] = d.threadKey
		event["routing"] = d.routing
	}
	if len(d.quotedReply) != 0 {
		event["quotedReply"] = d.quotedReply
	}
	if d.issue != nil {
		issue := *d.issue
		issue.promptEventKind = d.Kind
		raw, err := issue.MarshalAgentPromptJSON()
		if err != nil {
			return nil, err
		}
		event["issue"] = json.RawMessage(raw)
	}
	if d.report != nil {
		raw, err := d.report.MarshalAgentPromptJSON()
		if err != nil {
			return nil, err
		}
		event["report"] = json.RawMessage(raw)
	}
	if len(d.messages) != 0 {
		event["messages"] = d.messages
	}
	if len(bodyKeys) != 0 && len(d.Body) != 0 {
		var body map[string]json.RawMessage
		if err := json.Unmarshal(d.Body, &body); err != nil {
			return nil, err
		}
		selected := make(map[string]json.RawMessage, len(bodyKeys))
		for _, key := range bodyKeys {
			if value, ok := body[key]; ok {
				selected[key] = value
			}
		}
		if len(selected) != 0 {
			event["body"] = selected
		}
	}
	return json.Marshal(event)
}

// enqueueInternalDelivery writes a pending item in the producer's transaction.
// The producer owns its stable ID, verifies business references and commits
// their changes together with this insert.
func enqueueInternalDelivery(ctx context.Context, tx *sql.Tx, d internalDelivery) error {
	if d.ID == "" || len(d.ID) > 128 || strings.TrimSpace(d.Kind) == "" || len(d.Kind) > 128 ||
		!d.Sender.valid() || !d.Recipient.valid() || d.Recipient.Kind == "core" ||
		(d.Content == nil && len(d.Body) == 0 && len(d.Refs) == 0) {
		return problem("invalid_argument", "invalid internal delivery identity or content")
	}
	for _, ref := range d.Refs {
		if ref.Kind == "" || ref.ID == "" || ref.GoalRevision < 0 {
			return problem("invalid_argument", "invalid internal delivery reference")
		}
	}
	if err := checkDeliveryIdentity(ctx, tx, d.Sender, false); err != nil {
		return err
	}
	if err := checkDeliveryIdentity(ctx, tx, d.Recipient, true); err != nil {
		return err
	}
	raw, err := json.Marshal(d)
	if err != nil {
		return problem("invalid_argument", "invalid internal delivery JSON content")
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO internal_deliveries(id,kind,sender,recipient,content,body,refs,created_at,status)
 VALUES(?1,?2,?3->'$.sender',?3->'$.recipient',?3->'$.content',?3->'$.body',coalesce(?3->'$.refs','[]'),?4,'pending')`,
		d.ID, d.Kind, string(raw), now())
	if err != nil {
		return storageError()
	}
	return internalEnqueue.save(ctx, tx, d.ID, 0)
}

func checkDeliveryIdentity(ctx context.Context, tx *sql.Tx, r deliveryIdentity, recipient bool) error {
	switch r.Kind {
	case "topic":
		var topic Topic
		return readJSON(ctx, tx, "SELECT body FROM topics WHERE id=?", &topic, r.TopicID)
	case "worker":
		var x Execution
		if err := readJSON(ctx, tx, "SELECT body FROM executions WHERE id=?", &x, r.ExecutionID); err != nil {
			return err
		}
		if recipient {
			var b Binding
			if err := readJSON(ctx, tx, "SELECT body FROM session_bindings WHERE id=? AND state<>'revoked'", &b, x.BindingID); err != nil {
				return err
			}
			return checkBinding(ctx, tx, b)
		}
	}
	return nil
}

const internalDeliveryColumns = `id,kind,sender,recipient,content,body,refs,created_at`

func scanInternalDelivery(row interface{ Scan(...any) error }, d *internalDelivery, createdAt *string) error {
	var sender, recipient, content, body, refs []byte
	if err := row.Scan(&d.ID, &d.Kind, &sender, &recipient, &content, &body, &refs, createdAt); err != nil {
		return err
	}
	d.Body = body
	if json.Unmarshal(sender, &d.Sender) != nil || json.Unmarshal(recipient, &d.Recipient) != nil ||
		(len(content) != 0 && json.Unmarshal(content, &d.Content) != nil) || json.Unmarshal(refs, &d.Refs) != nil {
		return storageError()
	}
	return nil
}

func (s *Store) getInternalDelivery(ctx context.Context, b Binding, id string) (internalDelivery, error) {
	var d internalDelivery
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return d, storageError()
	}
	defer func() { _ = tx.Rollback() }()
	if err := checkBinding(ctx, tx, b); err != nil {
		return d, err
	}
	var at string
	err = scanInternalDelivery(tx.QueryRowContext(ctx, `SELECT `+internalDeliveryColumns+`
 FROM internal_deliveries WHERE id=? AND `+deliveryRecipientKind+`=? AND `+deliveryRecipientOwner+`=?
 AND status IN ('dispatching','sent')`, id, b.Role, b.OwnerID), &d, &at)
	if errors.Is(err, sql.ErrNoRows) {
		return d, problem("not_found", "delivery is unavailable to this role")
	}
	if err != nil {
		return d, storageError()
	}
	if s.telemetry.tracesEnabled {
		addTraceLinks(ctx, internalEnqueue.links(ctx, tx, nil, d.ID, 0))
	}
	return d, nil
}

// supersedeInternalDelivery fences an unconfirmed item. A late receipt cannot
// change its state and a confirmed item retains its acceptance history.
func supersedeInternalDelivery(ctx context.Context, tx *sql.Tx, id string) error {
	_, err := tx.ExecContext(ctx, `UPDATE internal_deliveries SET status='superseded'
 WHERE id=? AND status IN ('pending','dispatching')`, id)
	return err
}

func supersedeExecutionDeliveries(ctx context.Context, tx *sql.Tx, executionID string) error {
	_, err := tx.ExecContext(ctx, `UPDATE internal_deliveries SET status='superseded'
 WHERE `+deliveryRecipientKind+`='worker' AND `+deliveryRecipientOwner+`=? AND status IN ('pending','dispatching')`, executionID)
	return err
}

func recoverInternalDeliveries(ctx context.Context, tx *sql.Tx) error {
	rows, err := tx.QueryContext(ctx, `SELECT DISTINCT `+deliveryRecipientOwner+` FROM internal_deliveries
 WHERE `+deliveryRecipientKind+`='worker' AND status IN ('pending','dispatching')`)
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for _, id := range ids {
		err := checkDeliveryIdentity(ctx, tx, deliveryIdentity{Kind: "worker", ExecutionID: id}, true)
		switch errorCode(err) {
		case "":
		case "not_found", "stale_binding":
			if err := supersedeExecutionDeliveries(ctx, tx, id); err != nil {
				return err
			}
		default:
			return err
		}
	}
	return nil
}
