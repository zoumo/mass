package api

import (
	"encoding/json"
	"fmt"
	"time"
)

// ShimEvent is the unified event structure for both live notifications and
// JSONL history entries. It replaces the old Envelope + TypedEvent + SessionUpdateParams
// / RuntimeStateChangeParams layering.
//
// JSON wire shape:
//
//	{
//	  "runId":     "codex",
//	  "sessionId": "acp-xxx",      // omitempty — empty until ACP handshake completes
//	  "seq":       42,
//	  "time":      "2026-04-07T10:00:02Z",
//	  "category":  "session",      // "session" | "runtime"
//	  "type":      "text",
//	  "turnId":    "turn-001",     // omitempty — session events inside active turn only
//	  "payload":   { "text": "..." }
//	}
type ShimEvent struct {
	RunID     string    `json:"runId"`
	SessionID string    `json:"sessionId,omitempty"`
	Seq       int       `json:"seq"`
	Time      time.Time `json:"time"`
	Category  string    `json:"category"`
	Type      string    `json:"type"`

	// Turn-aware ordering field (session events only, within active turn).
	TurnID string `json:"turnId,omitempty"`

	// Payload is the typed event payload (ContentEvent, StateChangeEvent, etc.).
	Payload Event `json:"-"`
}

// MarshalJSON serialises ShimEvent to the flat JSON wire shape.
func (e ShimEvent) MarshalJSON() ([]byte, error) {
	type wire struct {
		RunID     string          `json:"runId"`
		SessionID string          `json:"sessionId,omitempty"`
		Seq       int             `json:"seq"`
		Time      time.Time       `json:"time"`
		Category  string          `json:"category"`
		Type      string          `json:"type"`
		TurnID    string          `json:"turnId,omitempty"`
		Payload   json.RawMessage `json:"payload"`
	}

	var payloadBytes json.RawMessage
	if e.Payload != nil {
		b, err := json.Marshal(e.Payload)
		if err != nil {
			return nil, fmt.Errorf("events: marshal shim event payload: %w", err)
		}
		payloadBytes = b
	} else {
		payloadBytes = json.RawMessage("{}")
	}

	return json.Marshal(wire{
		RunID:     e.RunID,
		SessionID: e.SessionID,
		Seq:       e.Seq,
		Time:      e.Time,
		Category:  e.Category,
		Type:      e.Type,
		TurnID:    e.TurnID,
		Payload:   payloadBytes,
	})
}

// UnmarshalJSON deserialises a ShimEvent from the flat JSON wire shape.
// It uses decodeEventPayload to reconstruct the typed Payload.
func (e *ShimEvent) UnmarshalJSON(data []byte) error {
	type wire struct {
		RunID     string          `json:"runId"`
		SessionID string          `json:"sessionId,omitempty"`
		Seq       int             `json:"seq"`
		Time      time.Time       `json:"time"`
		Category  string          `json:"category"`
		Type      string          `json:"type"`
		TurnID    string          `json:"turnId,omitempty"`
		Payload   json.RawMessage `json:"payload"`
	}

	var w wire
	if err := json.Unmarshal(data, &w); err != nil {
		return err
	}

	e.RunID = w.RunID
	e.SessionID = w.SessionID
	e.Seq = w.Seq
	e.Time = w.Time
	e.Category = w.Category
	e.Type = w.Type
	e.TurnID = w.TurnID

	if len(w.Payload) > 0 && string(w.Payload) != "null" {
		ev, err := decodeEventPayload(w.Type, w.Payload)
		if err != nil {
			return fmt.Errorf("events: decode shim event payload (type=%q): %w", w.Type, err)
		}
		e.Payload = ev
	}

	return nil
}

// CategoryForEvent returns the category string for a given event type.
// Only state_change returns CategoryRuntime; all other event types return CategorySession.
func CategoryForEvent(eventType string) string {
	if eventType == EventTypeStateChange {
		return CategoryRuntime
	}
	return CategorySession
}

// NewShimEvent constructs a ShimEvent with the given fields.
// turnID should only be set for session category events inside an active turn.
// For runtime events or out-of-turn session events, pass empty turnID.
func NewShimEvent(
	runID, sessionID string,
	seq int,
	at time.Time,
	ev Event,
	turnID string,
) ShimEvent {
	eventType := ev.eventType()
	category := CategoryForEvent(eventType)

	se := ShimEvent{
		RunID:     runID,
		SessionID: sessionID,
		Seq:       seq,
		Time:      at,
		Category:  category,
		Type:      eventType,
		Payload:   ev,
	}
	if turnID != "" && category == CategorySession {
		se.TurnID = turnID
	}
	return se
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
	case EventTypeAvailableCommands:
		dst = &AvailableCommandsEvent{}
	case EventTypeCurrentMode:
		dst = &CurrentModeEvent{}
	case EventTypeConfigOption:
		dst = &ConfigOptionEvent{}
	case EventTypeSessionInfo:
		dst = &SessionInfoEvent{}
	case EventTypeUsage:
		dst = &UsageEvent{}
	case EventTypeStateChange:
		dst = &StateChangeEvent{}
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
	case *AvailableCommandsEvent:
		return *v, nil
	case *CurrentModeEvent:
		return *v, nil
	case *ConfigOptionEvent:
		return *v, nil
	case *SessionInfoEvent:
		return *v, nil
	case *UsageEvent:
		return *v, nil
	case *StateChangeEvent:
		return *v, nil
	default:
		return nil, fmt.Errorf("events: unknown typed event %q", eventType)
	}
}
