package core

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strconv"
	"strings"

	"go.opentelemetry.io/otel/trace"
)

const (
	routeRequestKind  = "route_request"
	routedMessageKind = "routed_message"
	messageRefKind    = "trigger_message"
	returnedRefKind   = "internal_event"
	routingMessageID  = "json_extract(refs,'$[0].id')"
	returnedEventID   = "json_extract(refs,'$[1].id')"
)

const routingViews = `
CREATE INDEX internal_events_route_message ON internal_events(` + routingMessageID + `)
 WHERE kind IN ('routed_message','route_request');
CREATE UNIQUE INDEX internal_events_return ON internal_events(` + returnedEventID + `)
 WHERE kind='route_request' AND ` + returnedEventID + ` IS NOT NULL;

CREATE VIEW thread_subscriptions AS
 SELECT t.seq, t.key AS thread_key, json_extract(sub.value,'$.id') AS id,
 json_extract(sub.value,'$.topicId') AS topic_id,
 json_extract(sub.value,'$.createdAt') AS created_at, sub.value AS body
 FROM threads t,json_each(t.subscriptions) sub;

CREATE VIEW routing_events AS
 SELECT rowid AS seq, event_id, kind, created_at, status, sent_at, sender, recipient, content, body, refs,
 ` + routingMessageID + ` AS message_id, ` + returnedEventID + ` AS returned_event_id,
 coalesce(json_extract(recipient,'$.topic_id'),json_extract(sender,'$.topic_id')) AS topic_id
 FROM internal_events WHERE kind IN ('routed_message','route_request');

CREATE VIEW routing_relations AS
 SELECT d.seq,d.event_id AS id,d.message_id,d.topic_id,d.created_at,
 json_object('id',d.event_id,'messageId',d.message_id,'topicId',d.topic_id,'createdAt',d.created_at,
 'action',CASE d.kind WHEN 'routed_message' THEN 'received' ELSE 'returned' END,
 'status',d.status,'eventId',coalesce(d.returned_event_id,d.event_id),'reason',json_extract(d.body,'$'),
 'returned',json(CASE WHEN EXISTS(SELECT 1 FROM internal_events r WHERE r.kind='route_request'
 AND ` + returnedEventID + `=d.event_id) THEN 'true' ELSE 'false' END)) AS body
 FROM routing_events d WHERE d.topic_id IS NOT NULL;

CREATE VIEW topic_message_history AS
 SELECT message_id,topic_id FROM routing_events WHERE kind='routed_message';`

const routingRound = `(1
 + (SELECT count(*) FROM routing_events
 WHERE kind='route_request' AND message_id=trigger_messages.id AND returned_event_id IS NOT NULL))`

const routingState = "routing_state"

func ensureSubscription(ctx context.Context, tx *sql.Tx, sub Subscription) (string, error) {
	var id string
	err := tx.QueryRowContext(ctx, "SELECT id FROM thread_subscriptions WHERE thread_key=? AND topic_id=?", sub.ThreadKey, sub.TopicID).Scan(&id)
	if err == nil {
		return id, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return "", storageError()
	}
	sub.ID = newID("sub")
	// Check logical references before embedding them in the Thread.
	if err := checkEventIdentity(ctx, tx, eventIdentity{Kind: "topic", TopicID: sub.TopicID}, true); err != nil {
		return "", err
	}
	if err := putJSON(ctx, tx, `UPDATE threads SET subscriptions=json_insert(subscriptions,'$[#]',json(?2))
 WHERE key=?1`, sub, sub.ThreadKey); err != nil {
		return "", err
	}
	return sub.ID, nil
}

func acceptRoute(ctx context.Context, tx *sql.Tx, m Message, eventID, at string) error {
	var count int
	var topicID string
	err := tx.QueryRowContext(ctx, `SELECT count(*),coalesce(min(topic_id),'') FROM thread_subscriptions WHERE thread_key=?`, m.ThreadKey).Scan(&count, &topicID)
	if err != nil {
		return storageError()
	}
	if count == 1 {
		if _, err := recordRoutedMessage(ctx, tx, m.ID, m.ThreadKey, topicID, eventID, "single subscription", at, "core"); err != nil {
			return err
		}
		return setRoutingState(ctx, tx, m.ID, "routed")
	}
	d := internalEvent{
		ID: eventID, Kind: routeRequestKind,
		Sender: eventIdentity{Kind: "core"}, Recipient: eventIdentity{Kind: "router"},
		Refs: []eventReference{{Kind: messageRefKind, ID: m.ID}},
	}
	if err := enqueueInternalEvent(ctx, tx, d); err != nil {
		return err
	}
	return setRoutingState(ctx, tx, m.ID, "pending")
}

func setRoutingState(ctx context.Context, tx *sql.Tx, messageID, state string) error {
	if _, err := tx.ExecContext(ctx, "UPDATE trigger_messages SET routing_state=? WHERE id=?", state, messageID); err != nil {
		return storageError()
	}
	return nil
}

const activeRoute = `d.kind='routed_message' AND d.status<>'superseded'
 AND NOT EXISTS(SELECT 1 FROM internal_events WHERE kind='route_request' AND ` + returnedEventID + `=d.event_id)`

func activeRouteID(ctx context.Context, tx *sql.Tx, messageID, topicID string) (string, error) {
	var id string
	err := tx.QueryRowContext(ctx, `SELECT event_id FROM routing_events d WHERE message_id=? AND topic_id=? AND `+activeRoute,
		messageID, topicID).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", storageError()
	}
	return id, nil
}

func recordRoutedMessage(ctx context.Context, tx *sql.Tx, messageID, threadKey, topicID, eventID, reason, at, sender string) (string, error) {
	existing, err := activeRouteID(ctx, tx, messageID, topicID)
	if err != nil || existing != "" {
		return existing, err
	}
	body, err := json.Marshal(reason)
	if err != nil {
		return "", storageError()
	}
	d := internalEvent{
		ID: eventID, Kind: routedMessageKind,
		Sender: eventIdentity{Kind: sender}, Recipient: eventIdentity{Kind: "topic", TopicID: topicID},
		Body: body, Refs: []eventReference{{Kind: messageRefKind, ID: messageID}},
	}
	if err := enqueueInternalEvent(ctx, tx, d); err != nil {
		return "", err
	}
	_, err = ensureSubscription(ctx, tx, Subscription{TopicID: topicID, ThreadKey: threadKey, MessageID: messageID, CreatedAt: at})
	return eventID, err
}

type routeMessage struct {
	key, state string
}

func (s *Store) Route(ctx context.Context, b Binding, r RouteRequest) (_ json.RawMessage, retErr error) {
	ctx, finish := s.telemetry.startOperation(ctx, "routing.decide", "router")
	outcome := "routed"
	defer func() { finish(operationOutcome(retErr, outcome)) }()
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

	messages := make(map[string]routeMessage, len(r.MessageIDs))
	changed := false
	for _, id := range r.MessageIDs {
		var m routeMessage
		var round int
		err := tx.QueryRowContext(ctx, `SELECT thread_key,coalesce(`+routingState+`,''),`+routingRound+`
 FROM trigger_messages WHERE id=?`, id).Scan(&m.key, &m.state, &round)
		if errors.Is(err, sql.ErrNoRows) {
			return nil, problem("not_found", "source Message not found")
		}
		if err != nil {
			return nil, storageError()
		}
		if round != r.Rounds[id] || m.state == "" {
			return nil, problem("conflict", "Message routing round is stale or unavailable")
		}
		messages[id] = m
		changed = changed || m.state == "pending"
	}
	if s.telemetry.tracesEnabled {
		var links []trace.Link
		for _, id := range r.MessageIDs[:min(len(r.MessageIDs), traceAssociationLimit)] {
			links = messageAdmission.links(ctx, tx, links, id, 0)
		}
		addTraceLinks(ctx, links)
	}

	result := RouteDecision{ID: creationID(creationKey("route", b.ID, r.OperationID), r), RouteRequest: r, CreatedAt: now()}
	if r.Kind == "ignore" {
		outcome = "ignored"
		for _, id := range r.MessageIDs {
			if err := ignoreMessage(ctx, tx, id); err != nil {
				return nil, err
			}
		}
	} else {
		result.Targets = append([]RouteTarget(nil), r.Targets...)
		if err := s.routeTargets(ctx, tx, b, &result, messages); err != nil {
			return nil, err
		}
		for _, id := range r.MessageIDs {
			if err := setRoutingState(ctx, tx, id, "routed"); err != nil {
				return nil, err
			}
		}
	}
	commitGate("route_before_commit")
	if err := tx.Commit(); err != nil {
		return nil, storageError()
	}
	markMCPMutation(ctx, !changed, changed)
	commitGate("route_after_commit")
	return json.Marshal(result)
}

func (s *Store) routeTargets(ctx context.Context, tx *sql.Tx, b Binding, result *RouteDecision, messages map[string]routeMessage) error {
	decision := result.RouteRequest
	decision.Targets = append([]RouteTarget(nil), result.Targets...)
	for i := range result.Targets {
		target := &result.Targets[i]
		if target.Create != nil {
			scope := b.ID + "/" + strconv.Itoa(i)
			key := creationKey("topic", scope, result.OperationID)
			digest := creationDigest(decision)
			id, err := numberedCreation(ctx, tx, digest, "SELECT id,creation_digest FROM topics WHERE creation_key=?", key)
			if err != nil {
				return err
			}
			if id == "" {
				for _, messageID := range target.MessageIDs {
					if messages[messageID].state != "pending" {
						return problem("conflict", "Message routing already decided")
					}
				}
				id, err = s.newTopic(ctx, tx, key, digest, *target.Create, result.CreatedAt)
				if err != nil {
					return err
				}
			}
			target.TopicID = id
		}
		target.EventIDs = make(map[string]string, len(target.MessageIDs))
		for _, id := range target.MessageIDs {
			m := messages[id]
			if m.state != "pending" {
				eventID, err := activeRouteID(ctx, tx, id, target.TopicID)
				if err != nil {
					return err
				}
				if eventID == "" {
					return problem("conflict", "Message routing already decided")
				}
				target.EventIDs[id] = eventID
				continue
			}
			eventID, err := recordRoutedMessage(ctx, tx, id, m.key, target.TopicID, newID("evt"), result.Reason, result.CreatedAt, "router")
			if err != nil {
				return err
			}
			target.EventIDs[id] = eventID
		}
	}
	return nil
}

func ignoreMessage(ctx context.Context, tx *sql.Tx, messageID string) error {
	var active bool
	err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM routing_events d WHERE message_id=? AND `+activeRoute+`)`, messageID).Scan(&active)
	if err != nil {
		return storageError()
	}
	if active {
		return setRoutingState(ctx, tx, messageID, "routed")
	}
	return setRoutingState(ctx, tx, messageID, "ignored")
}

func (s *Store) DeclineMessage(ctx context.Context, b Binding, r DeclineMessageRequest) (_ json.RawMessage, retErr error) {
	ctx, finish := s.telemetry.startOperation(ctx, "routing.return", "topic")
	outcome := "returned"
	defer func() { finish(operationOutcome(retErr, outcome)) }()
	if b.Role != "topic" {
		return nil, problem("forbidden", "only a Topic can decline a message routed to it")
	}
	if r.EventID == "" || strings.TrimSpace(r.Reason) == "" {
		return nil, problem("invalid_argument", "event_id and reason are required")
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

	var messageID string
	err = tx.QueryRowContext(ctx, `SELECT m.id FROM routing_events d JOIN trigger_messages m ON m.id=d.message_id
 WHERE d.event_id=? AND d.kind='routed_message' AND d.topic_id=?`, r.EventID, b.OwnerID).Scan(&messageID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, problem("forbidden", "routed event is outside current Topic")
	}
	if err != nil {
		return nil, storageError()
	}
	if u := mcpUsageFrom(ctx); u != nil {
		u.eventID = r.EventID
	}
	if s.telemetry.tracesEnabled {
		links := internalEnqueue.links(ctx, tx, nil, r.EventID, 0)
		addTraceLinks(ctx, messageAdmission.links(ctx, tx, links, messageID, 0))
	}
	var result []byte
	err = tx.QueryRowContext(ctx, `SELECT json_remove(rr.body,'$.status') FROM routing_events d JOIN routing_relations rr ON rr.id=d.event_id
 WHERE d.kind='route_request' AND d.returned_event_id=?`, r.EventID).Scan(&result)
	if err == nil {
		outcome = "duplicate"
		markMCPMutation(ctx, true, false)
		return result, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return nil, storageError()
	}

	raw, err := json.Marshal(r.Reason)
	if err != nil {
		return nil, storageError()
	}
	d := internalEvent{
		ID: newID("evt"), Kind: routeRequestKind,
		Sender: eventIdentity{Kind: "topic", TopicID: b.OwnerID}, Recipient: eventIdentity{Kind: "router"},
		Body: raw, Refs: []eventReference{
			{Kind: messageRefKind, ID: messageID}, {Kind: returnedRefKind, ID: r.EventID},
		},
	}
	if err := enqueueInternalEvent(ctx, tx, d); err != nil {
		return nil, err
	}
	if err := supersedeInternalEvent(ctx, tx, r.EventID); err != nil {
		return nil, err
	}
	if err := setRoutingState(ctx, tx, messageID, "pending"); err != nil {
		return nil, err
	}
	if err := tx.QueryRowContext(ctx, "SELECT json_remove(body,'$.status') FROM routing_relations WHERE id=?", d.ID).Scan(&result); err != nil {
		return nil, storageError()
	}
	commitGate("return_before_commit")
	if err := tx.Commit(); err != nil {
		return nil, storageError()
	}
	markMCPMutation(ctx, false, true)
	commitGate("return_after_commit")
	return result, nil
}
