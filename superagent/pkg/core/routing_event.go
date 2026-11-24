package core

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strconv"
	"strings"

	"go.opentelemetry.io/otel/trace"

	"github.com/zoumo/mass/superagent/pkg/core/internal/ent/triggermessage"
)

const (
	routeRequestKind  = "route_request"
	routedMessageKind = "routed_message"
	messageRefKind    = "trigger_message"
	returnedRefKind   = "internal_event"
	routingMessageID  = "json_extract(refs,'$[0].id')"
	returnedEventID   = "json_extract(refs,'$[1].id')"
)

const routingRound = `(1
 + (SELECT count(*) FROM routing_events
 WHERE kind='route_request' AND message_id=trigger_messages.id AND returned_event_id IS NOT NULL))`

const routingState = "routing_state"

func ensureSubscription(ctx context.Context, tx *sql.Tx, sub Subscription) (string, error) {
	var id string
	err := tx.QueryRowContext(ctx, "SELECT id FROM thread_subscriptions WHERE thread_id=? AND topic_id=?", sub.ThreadID, sub.TopicID).Scan(&id)
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
 WHERE id=?1`, sub, sub.ThreadID); err != nil {
		return "", err
	}
	return sub.ID, nil
}

func acceptRoute(ctx context.Context, tx *sql.Tx, m Message, eventID, at string) error {
	var count int
	var topicID string
	if m.scheduled != nil && m.scheduled.topicID != "" {
		return routeAcceptedMessage(ctx, tx, m, m.scheduled.topicID, eventID, "scheduled task owner", at)
	}
	subscriptions := `SELECT count(*),coalesce(min(topic_id),'') FROM thread_subscriptions WHERE thread_id=?`
	if m.scheduled != nil {
		subscriptions += ` AND EXISTS (SELECT 1 FROM topics WHERE id=thread_subscriptions.topic_id AND body->>'$.status'='active')`
	}
	err := tx.QueryRowContext(ctx, subscriptions, m.ThreadID).Scan(&count, &topicID)
	if err != nil {
		return storageError()
	}
	if count == 1 {
		return routeAcceptedMessage(ctx, tx, m, topicID, eventID, "single subscription", at)
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

func routeAcceptedMessage(ctx context.Context, tx *sql.Tx, m Message, topicID, eventID, reason, at string) error {
	if _, err := recordRoutedMessage(ctx, tx, m.ID, m.ThreadID, topicID, eventID, reason, at, "core"); err != nil {
		return err
	}
	return setRoutingState(ctx, tx, m.ID, "routed")
}

func setRoutingState(ctx context.Context, tx *sql.Tx, messageID, state string) error {
	if err := entClientForTx(tx).TriggerMessage.Update().Where(triggermessage.BusinessIDEQ(messageID)).SetRoutingState(state).Exec(ctx); err != nil {
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

func recordRoutedMessage(ctx context.Context, tx *sql.Tx, messageID, threadID, topicID, eventID, reason, at, sender string) (string, error) {
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
	_, err = ensureSubscription(ctx, tx, Subscription{TopicID: topicID, ThreadID: threadID, MessageID: messageID, CreatedAt: at})
	return eventID, err
}

type routeMessage struct {
	threadID, state string
}

func (s *Store) Route(ctx context.Context, b SessionBinding, r RouteRequest) (_ json.RawMessage, retErr error) {
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
	for _, id := range r.MessageIDs {
		var m routeMessage
		var round int
		err := tx.QueryRowContext(ctx, `SELECT thread_id,coalesce(`+routingState+`,''),`+routingRound+`
 FROM trigger_messages WHERE id=?`, id).Scan(&m.threadID, &m.state, &round)
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
	commitGate("route_after_commit")
	return json.Marshal(result)
}

func (s *Store) routeTargets(ctx context.Context, tx *sql.Tx, b SessionBinding, result *RouteDecision, messages map[string]routeMessage) error {
	decision := result.RouteRequest
	decision.Targets = append([]RouteTarget(nil), result.Targets...)
	for i := range result.Targets {
		target := &result.Targets[i]
		if target.Create != nil {
			scope := b.ID + "/" + strconv.Itoa(i)
			key := creationKey("topic", scope, result.OperationID)
			digest := creationDigest(decision)
			id, err := topicCreationReplay(ctx, entClientForTx(tx).Topic, key, digest)
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
			eventID, err := recordRoutedMessage(ctx, tx, id, m.threadID, target.TopicID, newID("evt"), result.Reason, result.CreatedAt, "router")
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

func (s *Store) DeclineMessage(ctx context.Context, b SessionBinding, r DeclineMessageRequest) (_ json.RawMessage, retErr error) {
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
	if u := mcpCallTelemetryFrom(ctx); u != nil {
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
	commitGate("return_after_commit")
	return result, nil
}
