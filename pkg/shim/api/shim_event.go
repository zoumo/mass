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
//	  "streamSeq": 3,              // omitempty — same scope as turnId
//	  "phase":     "acting",       // omitempty — same scope as turnId
//	  "content":   { "text": "..." }
//	}
type ShimEvent struct {
	RunID     string    `json:"runId"`
	SessionID string    `json:"sessionId,omitempty"`
	Seq       int       `json:"seq"`
	Time      time.Time `json:"time"`
	Category  string    `json:"category"`
	Type      string    `json:"type"`

	// Turn-aware ordering fields (session events only, within active turn).
	TurnID    string `json:"turnId,omitempty"`
	StreamSeq int    `json:"streamSeq,omitempty"`
	Phase     string `json:"phase,omitempty"`

	// Content is the typed event payload (TextEvent, StateChangeEvent, etc.).
	Content Event `json:"-"`
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
		StreamSeq int             `json:"streamSeq,omitempty"`
		Phase     string          `json:"phase,omitempty"`
		Content   json.RawMessage `json:"content"`
	}

	var contentBytes json.RawMessage
	if e.Content != nil {
		b, err := json.Marshal(e.Content)
		if err != nil {
			return nil, fmt.Errorf("events: marshal shim event content: %w", err)
		}
		contentBytes = b
	} else {
		contentBytes = json.RawMessage("{}")
	}

	return json.Marshal(wire{
		RunID:     e.RunID,
		SessionID: e.SessionID,
		Seq:       e.Seq,
		Time:      e.Time,
		Category:  e.Category,
		Type:      e.Type,
		TurnID:    e.TurnID,
		StreamSeq: e.StreamSeq,
		Phase:     e.Phase,
		Content:   contentBytes,
	})
}

// UnmarshalJSON deserialises a ShimEvent from the flat JSON wire shape.
// It uses decodeEventPayload to reconstruct the typed Content.
func (e *ShimEvent) UnmarshalJSON(data []byte) error {
	type wire struct {
		RunID     string          `json:"runId"`
		SessionID string          `json:"sessionId,omitempty"`
		Seq       int             `json:"seq"`
		Time      time.Time       `json:"time"`
		Category  string          `json:"category"`
		Type      string          `json:"type"`
		TurnID    string          `json:"turnId,omitempty"`
		StreamSeq int             `json:"streamSeq,omitempty"`
		Phase     string          `json:"phase,omitempty"`
		Content   json.RawMessage `json:"content"`
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
	e.StreamSeq = w.StreamSeq
	e.Phase = w.Phase

	if len(w.Content) > 0 && string(w.Content) != "null" {
		ev, err := decodeEventPayload(w.Type, w.Content)
		if err != nil {
			return fmt.Errorf("events: decode shim event content (type=%q): %w", w.Type, err)
		}
		e.Content = ev
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

// PhaseForEvent returns the phase string for a given session event type.
// thinking → "thinking", tool_call/tool_result → "tool_call", others → "acting".
// runtime category events (state_change) should not call this function.
func PhaseForEvent(eventType string) string {
	switch eventType {
	case EventTypeThinking:
		return "thinking"
	case EventTypeToolCall, EventTypeToolResult:
		return "tool_call"
	default:
		return "acting"
	}
}

// NewShimEvent constructs a ShimEvent with the given fields.
// turnID, streamSeq, and phase should only be set for session category events
// inside an active turn. For runtime events or out-of-turn session events,
// pass empty turnID (streamSeq and phase will be ignored when turnID is empty).
func NewShimEvent(
	runID, sessionID string,
	seq int,
	at time.Time,
	ev Event,
	turnID string,
	streamSeq int,
) ShimEvent {
	eventType := ev.eventType()
	category := CategoryForEvent(eventType)

	var phase string
	if turnID != "" && category == CategorySession {
		phase = PhaseForEvent(eventType)
	}

	se := ShimEvent{
		RunID:     runID,
		SessionID: sessionID,
		Seq:       seq,
		Time:      at,
		Category:  category,
		Type:      eventType,
		Content:   ev,
	}
	if turnID != "" && category == CategorySession {
		se.TurnID = turnID
		se.StreamSeq = streamSeq
		se.Phase = phase
	}
	return se
}

// decodeEventPayload decodes a JSON payload into the appropriate typed Event
// given the event type string.
func decodeEventPayload(eventType string, payload json.RawMessage) (Event, error) {
	unmarshal := func(dst Event) (Event, error) {
		if len(payload) == 0 || string(payload) == "null" {
			return dst, nil
		}
		switch v := dst.(type) {
		case TextEvent:
			if err := json.Unmarshal(payload, &v); err != nil {
				return nil, fmt.Errorf("events: decode %s payload: %w", eventType, err)
			}
			return v, nil
		case ThinkingEvent:
			if err := json.Unmarshal(payload, &v); err != nil {
				return nil, fmt.Errorf("events: decode %s payload: %w", eventType, err)
			}
			return v, nil
		case UserMessageEvent:
			if err := json.Unmarshal(payload, &v); err != nil {
				return nil, fmt.Errorf("events: decode %s payload: %w", eventType, err)
			}
			return v, nil
		case ToolCallEvent:
			if err := json.Unmarshal(payload, &v); err != nil {
				return nil, fmt.Errorf("events: decode %s payload: %w", eventType, err)
			}
			return v, nil
		case ToolResultEvent:
			if err := json.Unmarshal(payload, &v); err != nil {
				return nil, fmt.Errorf("events: decode %s payload: %w", eventType, err)
			}
			return v, nil
		case FileWriteEvent:
			if err := json.Unmarshal(payload, &v); err != nil {
				return nil, fmt.Errorf("events: decode %s payload: %w", eventType, err)
			}
			return v, nil
		case FileReadEvent:
			if err := json.Unmarshal(payload, &v); err != nil {
				return nil, fmt.Errorf("events: decode %s payload: %w", eventType, err)
			}
			return v, nil
		case CommandEvent:
			if err := json.Unmarshal(payload, &v); err != nil {
				return nil, fmt.Errorf("events: decode %s payload: %w", eventType, err)
			}
			return v, nil
		case PlanEvent:
			if err := json.Unmarshal(payload, &v); err != nil {
				return nil, fmt.Errorf("events: decode %s payload: %w", eventType, err)
			}
			return v, nil
		case TurnStartEvent:
			if err := json.Unmarshal(payload, &v); err != nil {
				return nil, fmt.Errorf("events: decode %s payload: %w", eventType, err)
			}
			return v, nil
		case TurnEndEvent:
			if err := json.Unmarshal(payload, &v); err != nil {
				return nil, fmt.Errorf("events: decode %s payload: %w", eventType, err)
			}
			return v, nil
		case ErrorEvent:
			if err := json.Unmarshal(payload, &v); err != nil {
				return nil, fmt.Errorf("events: decode %s payload: %w", eventType, err)
			}
			return v, nil
		case AvailableCommandsEvent:
			if err := json.Unmarshal(payload, &v); err != nil {
				return nil, fmt.Errorf("events: decode %s payload: %w", eventType, err)
			}
			return v, nil
		case CurrentModeEvent:
			if err := json.Unmarshal(payload, &v); err != nil {
				return nil, fmt.Errorf("events: decode %s payload: %w", eventType, err)
			}
			return v, nil
		case ConfigOptionEvent:
			if err := json.Unmarshal(payload, &v); err != nil {
				return nil, fmt.Errorf("events: decode %s payload: %w", eventType, err)
			}
			return v, nil
		case SessionInfoEvent:
			if err := json.Unmarshal(payload, &v); err != nil {
				return nil, fmt.Errorf("events: decode %s payload: %w", eventType, err)
			}
			return v, nil
		case UsageEvent:
			if err := json.Unmarshal(payload, &v); err != nil {
				return nil, fmt.Errorf("events: decode %s payload: %w", eventType, err)
			}
			return v, nil
		case StateChangeEvent:
			if err := json.Unmarshal(payload, &v); err != nil {
				return nil, fmt.Errorf("events: decode %s payload: %w", eventType, err)
			}
			return v, nil
		default:
			return nil, fmt.Errorf("events: unknown typed event %q", eventType)
		}
	}

	switch eventType {
	case EventTypeText:
		return unmarshal(TextEvent{})
	case EventTypeThinking:
		return unmarshal(ThinkingEvent{})
	case EventTypeUserMessage:
		return unmarshal(UserMessageEvent{})
	case EventTypeToolCall:
		return unmarshal(ToolCallEvent{})
	case EventTypeToolResult:
		return unmarshal(ToolResultEvent{})
	case EventTypeFileWrite:
		return unmarshal(FileWriteEvent{})
	case EventTypeFileRead:
		return unmarshal(FileReadEvent{})
	case EventTypeCommand:
		return unmarshal(CommandEvent{})
	case EventTypePlan:
		return unmarshal(PlanEvent{})
	case EventTypeTurnStart:
		return unmarshal(TurnStartEvent{})
	case EventTypeTurnEnd:
		return unmarshal(TurnEndEvent{})
	case EventTypeError:
		return unmarshal(ErrorEvent{})
	case EventTypeAvailableCommands:
		return unmarshal(AvailableCommandsEvent{})
	case EventTypeCurrentMode:
		return unmarshal(CurrentModeEvent{})
	case EventTypeConfigOption:
		return unmarshal(ConfigOptionEvent{})
	case EventTypeSessionInfo:
		return unmarshal(SessionInfoEvent{})
	case EventTypeUsage:
		return unmarshal(UsageEvent{})
	case EventTypeStateChange:
		return unmarshal(StateChangeEvent{})
	default:
		return nil, fmt.Errorf("events: unknown typed event %q", eventType)
	}
}
