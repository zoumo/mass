package api

import (
	"encoding/json"
	"fmt"
	"time"
)

// AgentRunEvent is the unified event structure for both live notifications and
// JSONL history entries.
//
// JSON wire shape:
//
//	{
//	  "runId":     "codex",
//	  "sessionId": "acp-xxx",      // omitempty — empty until ACP handshake completes
//	  "seq":       42,
//	  "time":      "2026-04-07T10:00:02Z",
//	  "type":      "agent_message",
//	  "turnId":    "turn-001",     // omitempty — non-runtime_update events inside active turn only
//	  "payload":   { ... }
//	}
type AgentRunEvent struct {
	// WatchID is a transport-only demux key assigned by the server's WatchEvent
	// handler. It allows clients to distinguish events from different watch
	// streams on the same connection. WatchID is NOT persisted to the event log
	// (events.jsonl) — it is set by the Service goroutine right before
	// peer.Notify() and stripped on the Translator/log side.
	WatchID string `json:"watchId,omitempty"`

	RunID     string    `json:"runId"`
	SessionID string    `json:"sessionId,omitempty"`
	Seq       int       `json:"seq"`
	Time      time.Time `json:"time"`
	Type      string    `json:"type"`

	// Turn-aware ordering field (non-runtime_update events inside active turn only).
	TurnID string `json:"turnId,omitempty"`

	// Payload is the typed event payload (ContentEvent, RuntimeUpdateEvent, etc.).
	Payload Event `json:"-"`
}

// MarshalJSON serializes AgentRunEvent to the flat JSON wire shape.
// WatchID is included when non-empty (transport-only, not in event log).
func (e AgentRunEvent) MarshalJSON() ([]byte, error) {
	type wire struct {
		WatchID   string          `json:"watchId,omitempty"`
		RunID     string          `json:"runId"`
		SessionID string          `json:"sessionId,omitempty"`
		Seq       int             `json:"seq"`
		Time      time.Time       `json:"time"`
		Type      string          `json:"type"`
		TurnID    string          `json:"turnId,omitempty"`
		Payload   json.RawMessage `json:"payload"`
	}

	var payloadBytes json.RawMessage
	if e.Payload != nil {
		b, err := json.Marshal(e.Payload)
		if err != nil {
			return nil, fmt.Errorf("events: marshal agent run event payload: %w", err)
		}
		payloadBytes = b
	} else {
		payloadBytes = json.RawMessage("{}")
	}

	return json.Marshal(wire{
		WatchID:   e.WatchID,
		RunID:     e.RunID,
		SessionID: e.SessionID,
		Seq:       e.Seq,
		Time:      e.Time,
		Type:      e.Type,
		TurnID:    e.TurnID,
		Payload:   payloadBytes,
	})
}

// UnmarshalJSON deserialises an AgentRunEvent from the flat JSON wire shape.
// It uses decodeEventPayload to reconstruct the typed Payload.
// WatchID is preserved when present (transport-only field from live notifications).
func (e *AgentRunEvent) UnmarshalJSON(data []byte) error {
	type wire struct {
		WatchID   string          `json:"watchId,omitempty"`
		RunID     string          `json:"runId"`
		SessionID string          `json:"sessionId,omitempty"`
		Seq       int             `json:"seq"`
		Time      time.Time       `json:"time"`
		Type      string          `json:"type"`
		TurnID    string          `json:"turnId,omitempty"`
		Payload   json.RawMessage `json:"payload"`
	}

	var w wire
	if err := json.Unmarshal(data, &w); err != nil {
		return err
	}

	e.WatchID = w.WatchID
	e.RunID = w.RunID
	e.SessionID = w.SessionID
	e.Seq = w.Seq
	e.Time = w.Time
	e.Type = w.Type
	e.TurnID = w.TurnID

	if len(w.Payload) > 0 && string(w.Payload) != "null" {
		ev, err := decodeEventPayload(w.Type, w.Payload)
		if err != nil {
			return fmt.Errorf("events: decode agent run event payload (type=%q): %w", w.Type, err)
		}
		e.Payload = ev
	}

	return nil
}

// NewAgentRunEvent constructs an AgentRunEvent with the given fields.
// turnID is set for all event types except runtime_update (which does not
// participate in turn ordering).
func NewAgentRunEvent(
	runID, sessionID string,
	seq int,
	at time.Time,
	ev Event,
	turnID string,
) AgentRunEvent {
	eventType := ev.eventType()

	ae := AgentRunEvent{
		RunID:     runID,
		SessionID: sessionID,
		Seq:       seq,
		Time:      at,
		Type:      eventType,
		Payload:   ev,
	}
	if turnID != "" && eventType != EventTypeRuntimeUpdate {
		ae.TurnID = turnID
	}
	return ae
}

// decodeEventPayload decodes a JSON payload into the appropriate typed Event
// given the event type string.
func decodeEventPayload(eventType string, payload json.RawMessage) (Event, error) {
	if len(payload) == 0 || string(payload) == "null" {
		return nil, nil
	}

	var dst Event
	switch eventType {
	case EventTypeAgentMessage, EventTypeAgentThinking, EventTypeUserMessage:
		var ce ContentEvent
		if err := json.Unmarshal(payload, &ce); err != nil {
			return nil, fmt.Errorf("events: decode %s payload: %w", eventType, err)
		}
		ce.typ = eventType
		return ce, nil
	case EventTypeToolCall:
		dst = &ToolCallEvent{}
	case EventTypeToolResult:
		dst = &ToolResultEvent{}
	case EventTypePlan:
		dst = &PlanEvent{}
	case EventTypeTurnStart:
		dst = &TurnStartEvent{}
	case EventTypeTurnEnd:
		dst = &TurnEndEvent{}
	case EventTypeError:
		dst = &ErrorEvent{}
	case EventTypeRuntimeUpdate:
		dst = &RuntimeUpdateEvent{}
	default:
		return nil, fmt.Errorf("events: unknown typed event %q", eventType)
	}

	if err := json.Unmarshal(payload, dst); err != nil {
		return nil, fmt.Errorf("events: decode %s payload: %w", eventType, err)
	}

	// Dereference pointer to return value type (Event interface implementations are value receivers).
	switch v := dst.(type) {
	case *ToolCallEvent:
		return *v, nil
	case *ToolResultEvent:
		return *v, nil
	case *PlanEvent:
		return *v, nil
	case *TurnStartEvent:
		return *v, nil
	case *TurnEndEvent:
		return *v, nil
	case *ErrorEvent:
		return *v, nil
	case *RuntimeUpdateEvent:
		return *v, nil
	default:
		return nil, fmt.Errorf("events: unknown typed event %q", eventType)
	}
}
