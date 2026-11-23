package core

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"

	"github.com/zoumo/mass/massd/pkg/events"
)

const internalEventSchema = `
CREATE TABLE internal_events (
 event_id TEXT PRIMARY KEY NOT NULL, kind TEXT NOT NULL, sender TEXT NOT NULL, recipient TEXT NOT NULL,
 content TEXT CHECK (content IS NULL OR (` + structuredContentObjectCheck + `)),
 body TEXT, refs TEXT NOT NULL DEFAULT '[]', created_at TEXT NOT NULL,
 status TEXT NOT NULL CHECK(status IN ('pending','dispatching','sent','superseded')), sent_at TEXT
);
CREATE INDEX internal_events_pending ON internal_events(` + eventRecipientKind + `, ` + eventRecipientOwner + `)
 WHERE status IN ('pending','dispatching');
`

const contextSupplement = "context_supplement"

const (
	eventRecipientKind  = `json_extract(recipient,'$.kind')`
	eventRecipientOwner = `COALESCE(json_extract(recipient,'$.topic_id'),json_extract(recipient,'$.session_binding_id'),'')`
)

type eventIdentity struct {
	Kind             string `json:"kind"`
	TopicID          string `json:"topic_id,omitempty"`
	SessionBindingID string `json:"session_binding_id,omitempty"`
}

func (r eventIdentity) valid() bool {
	switch r.Kind {
	case "core", "router":
		return r.TopicID == "" && r.SessionBindingID == ""
	case "topic":
		return r.TopicID != "" && len(r.TopicID) <= 512 && r.SessionBindingID == ""
	case "issue":
		return r.SessionBindingID != "" && len(r.SessionBindingID) <= 512 && r.TopicID == ""
	default:
		return false
	}
}

type eventReference struct {
	Kind         string `json:"kind"`
	ID           string `json:"id"`
	IssueID      string `json:"issue_id,omitempty"`
	GoalRevision int    `json:"goal_revision,omitempty"`
}

type eventMessageContent struct {
	ID     string
	Blocks []events.ContentBlock
	size   int
}

type internalEvent struct {
	ID        string             `json:"event_id"`
	Kind      string             `json:"kind"`
	Sender    eventIdentity      `json:"sender"`
	Recipient eventIdentity      `json:"recipient"`
	Content   *StructuredContent `json:"content,omitempty"`
	Body      json.RawMessage    `json:"body,omitempty"`
	Refs      []eventReference   `json:"refs,omitempty"`
	// Optional facts on the copy used for dispatch.
	truncated      bool
	threadKey      string
	routing        json.RawMessage
	quotedReply    json.RawMessage
	topic          *Topic
	issue          *Issue
	report         *Report
	followup       json.RawMessage
	messages       []json.RawMessage
	messageContent []eventMessageContent
}

// AgentPromptView encodes the fields an agent needs to act on the source record.
type AgentPromptView interface {
	MarshalAgentPromptJSON() ([]byte, error)
}

var _ AgentPromptView = (*internalEvent)(nil)

func (d *internalEvent) MarshalAgentPromptJSON() ([]byte, error) {
	event := map[string]any{"event_id": d.ID, "kind": d.Kind}
	var refKind string
	var bodyKeys []string
	switch d.Kind {
	case routeRequestKind, routedMessageKind:
		refKind = messageRefKind
	case reportSubmittedKind:
		refKind = "report"
	case "outbox_failed":
		refKind = "outbox"
	case issueSessionUpdateKind:
		bodyKeys = []string{"materials"}
	case contextSupplement:
		switch d.Recipient.Kind {
		case "issue":
			bodyKeys = []string{"materials"}
		case "topic":
			refKind = "report"
			bodyKeys = []string{"type", "dependencyIssueId", "dependentIssueIds", "recovery", "previousStatus", "status"}
		}
	}
	if refKind != "" {
		var refs []eventReference
		for _, ref := range d.Refs {
			if ref.Kind == refKind || d.Kind == routeRequestKind && ref.Kind == returnedRefKind {
				refs = append(refs, eventReference{Kind: ref.Kind, ID: ref.ID})
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
	if d.Content != nil && (d.Kind == routedMessageKind || d.Kind == issueSessionUpdateKind || d.Kind == contextSupplement ||
		d.Kind == routeRequestKind) {
		event["content"] = d.Content
	}
	if d.Kind == routeRequestKind && len(d.routing) != 0 {
		event["threadKey"] = d.threadKey
		event["routing"] = d.routing
	}
	if len(d.messageContent) != 0 {
		type attachment struct {
			MessageID  string `json:"messageId"`
			BlockCount int    `json:"blockCount"`
		}
		list := make([]attachment, 0, len(d.messageContent))
		for _, content := range d.messageContent {
			list = append(list, attachment{MessageID: content.ID, BlockCount: len(content.Blocks)})
		}
		event["messageContent"] = list
	}
	if len(d.followup) != 0 {
		event["followup"] = d.followup
	}
	if len(d.quotedReply) != 0 {
		event["quotedReply"] = d.quotedReply
	}
	if d.topic != nil {
		event["topic"] = map[string]any{
			"id": d.topic.ID, "title": d.topic.Title, "workspace": d.topic.Workspace,
			"status": d.topic.Status, "revision": d.topic.Revision,
		}
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

// enqueueInternalEvent writes a pending item in the producer's transaction.
// The producer owns its stable ID, verifies business references and commits
// their changes together with this insert.
func enqueueInternalEvent(ctx context.Context, tx *sql.Tx, d internalEvent) error {
	if d.ID == "" || len(d.ID) > 128 || strings.TrimSpace(d.Kind) == "" || len(d.Kind) > 128 ||
		!d.Sender.valid() || !d.Recipient.valid() || d.Recipient.Kind == "core" ||
		(d.Content == nil && len(d.Body) == 0 && len(d.Refs) == 0) {
		return problem("invalid_argument", "invalid internal event identity or content")
	}
	for _, ref := range d.Refs {
		if ref.Kind == "" || ref.ID == "" || ref.GoalRevision < 0 {
			return problem("invalid_argument", "invalid internal event reference")
		}
	}
	if err := checkEventIdentity(ctx, tx, d.Sender, false); err != nil {
		return err
	}
	if err := checkEventIdentity(ctx, tx, d.Recipient, true); err != nil {
		return err
	}
	raw, err := json.Marshal(d)
	if err != nil {
		return problem("invalid_argument", "invalid internal event JSON content")
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO internal_events(event_id,kind,sender,recipient,content,body,refs,created_at,status)
 VALUES(?1,?2,?3->'$.sender',?3->'$.recipient',?3->'$.content',?3->'$.body',coalesce(?3->'$.refs','[]'),?4,'pending')`,
		d.ID, d.Kind, string(raw), now())
	if err != nil {
		return storageError()
	}
	return internalEnqueue.save(ctx, tx, d.ID, 0)
}

func checkEventIdentity(ctx context.Context, tx *sql.Tx, r eventIdentity, recipient bool) error {
	switch r.Kind {
	case "topic":
		var topic Topic
		return readJSON(ctx, tx, "SELECT body FROM topics WHERE id=?", &topic, r.TopicID)
	case "issue":
		binding, err := loadIssueBinding(ctx, tx, r.SessionBindingID)
		if err != nil {
			return err
		}
		if recipient {
			_, err = issueForBinding(ctx, tx, binding)
			return err
		}
	}
	return nil
}

const internalEventColumns = `event_id,kind,sender,recipient,content,body,refs,created_at`

func scanInternalEvent(row interface{ Scan(...any) error }, d *internalEvent, createdAt *string) error {
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

func (s *Store) getInternalEvent(ctx context.Context, b SessionBinding, id string) (internalEvent, error) {
	var d internalEvent
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return d, storageError()
	}
	defer func() { _ = tx.Rollback() }()
	if err := checkBinding(ctx, tx, b); err != nil {
		return d, err
	}
	var at string
	err = scanInternalEvent(tx.QueryRowContext(ctx, `SELECT `+internalEventColumns+`
 FROM internal_events WHERE event_id=? AND `+eventRecipientKind+`=? AND `+eventRecipientOwner+`=?
 AND status IN ('dispatching','sent')`, id, b.Role, b.eventRecipientID()), &d, &at)
	if errors.Is(err, sql.ErrNoRows) {
		return d, problem("not_found", "event is unavailable to this role")
	}
	if err != nil {
		return d, storageError()
	}
	if s.telemetry.tracesEnabled {
		addTraceLinks(ctx, internalEnqueue.links(ctx, tx, nil, d.ID, 0))
	}
	return d, nil
}

// supersedeInternalEvent fences an unconfirmed item. A late receipt cannot
// change its state and a confirmed item retains its acceptance history.
func supersedeInternalEvent(ctx context.Context, tx *sql.Tx, id string) error {
	_, err := tx.ExecContext(ctx, `UPDATE internal_events SET status='superseded'
 WHERE event_id=? AND status IN ('pending','dispatching')`, id)
	return err
}

func supersedeIssueBindingEvents(ctx context.Context, tx *sql.Tx, sessionBindingID string) error {
	_, err := tx.ExecContext(ctx, `UPDATE internal_events SET status='superseded'
 WHERE `+eventRecipientKind+`='issue' AND `+eventRecipientOwner+`=? AND status IN ('pending','dispatching')`, sessionBindingID)
	return err
}

func recoverInternalEvents(ctx context.Context, tx *sql.Tx) error {
	rows, err := tx.QueryContext(ctx, `SELECT DISTINCT `+eventRecipientOwner+` FROM internal_events
 WHERE `+eventRecipientKind+`='issue' AND status IN ('pending','dispatching')`)
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
		err := checkEventIdentity(ctx, tx, eventIdentity{Kind: "issue", SessionBindingID: id}, true)
		switch errorCode(err) {
		case "":
		case "not_found", "stale_binding":
			if err := supersedeIssueBindingEvents(ctx, tx, id); err != nil {
				return err
			}
		default:
			return err
		}
	}
	return nil
}
