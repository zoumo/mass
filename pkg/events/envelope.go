package events

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/zoumo/oar/api"
)

// Deprecated: use api.MethodSessionUpdate and api.MethodRuntimeStateChange.
const (
	MethodSessionUpdate      = api.MethodSessionUpdate
	MethodRuntimeStateChange = api.MethodRuntimeStateChange
)

type sequenceParams interface {
	envelopeMethod() string
	sequence() int
}

// SequenceMeta is the shared recovery metadata present on all externally
// visible live notifications and replay history entries.
type SequenceMeta struct {
	SessionID string `json:"sessionId"`
	Seq       int    `json:"seq"`
	Timestamp string `json:"timestamp"`
}

// TypedEvent is the stable typed event surface exposed inside session/update.
type TypedEvent struct {
	Type    string `json:"type"`
	Payload any    `json:"payload"`
}

func newTypedEvent(ev Event) TypedEvent {
	return TypedEvent{
		Type:    ev.eventType(),
		Payload: ev,
	}
}

func (e TypedEvent) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		Type    string `json:"type"`
		Payload any    `json:"payload"`
	}{
		Type:    e.Type,
		Payload: e.Payload,
	})
}

func (e *TypedEvent) UnmarshalJSON(data []byte) error {
	var raw struct {
		Type    string          `json:"type"`
		Payload json.RawMessage `json:"payload"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}

	payload, err := decodeEventPayload(raw.Type, raw.Payload)
	if err != nil {
		return err
	}

	e.Type = raw.Type
	e.Payload = payload
	return nil
}

// SessionUpdateParams is the params object for session/update notifications.
type SessionUpdateParams struct {
	SequenceMeta
	TurnID string     `json:"turnId,omitempty"`
	Phase  string     `json:"phase,omitempty"`
	Event  TypedEvent `json:"event"`
}

func (SessionUpdateParams) envelopeMethod() string { return api.MethodSessionUpdate }
func (p SessionUpdateParams) sequence() int        { return p.Seq }

// RuntimeStateChangeParams is the params object for runtime/state_change notifications.
type RuntimeStateChangeParams struct {
	SequenceMeta
	PreviousStatus string `json:"previousStatus"`
	Status         string `json:"status"`
	PID            int    `json:"pid,omitempty"`
	Reason         string `json:"reason,omitempty"`
}

func (RuntimeStateChangeParams) envelopeMethod() string { return api.MethodRuntimeStateChange }
func (p RuntimeStateChangeParams) sequence() int        { return p.Seq }

// Envelope is the canonical replayable notification shape shared by live
// notifications and runtime/history replay entries.
type Envelope struct {
	Method string         `json:"method"`
	Params sequenceParams `json:"params"`
}

func NewSessionUpdateEnvelope(sessionID string, seq int, at time.Time, ev Event) Envelope {
	return Envelope{
		Method: api.MethodSessionUpdate,
		Params: SessionUpdateParams{
			SequenceMeta: SequenceMeta{
				SessionID: sessionID,
				Seq:       seq,
				Timestamp: at.UTC().Format(time.RFC3339Nano),
			},
			Event: newTypedEvent(ev),
		},
	}
}

func NewRuntimeStateChangeEnvelope(sessionID string, seq int, at time.Time, previousStatus, status string, pid int, reason string) Envelope {
	return Envelope{
		Method: api.MethodRuntimeStateChange,
		Params: RuntimeStateChangeParams{
			SequenceMeta: SequenceMeta{
				SessionID: sessionID,
				Seq:       seq,
				Timestamp: at.UTC().Format(time.RFC3339Nano),
			},
			PreviousStatus: previousStatus,
			Status:         status,
			PID:            pid,
			Reason:         reason,
		},
	}
}

func (e Envelope) Seq() (int, error) {
	if e.Params == nil {
		return 0, fmt.Errorf("events: missing envelope params")
	}
	return e.Params.sequence(), nil
}

func (e Envelope) MarshalJSON() ([]byte, error) {
	if e.Params == nil {
		return nil, fmt.Errorf("events: missing envelope params")
	}

	method := e.Method
	if method == "" {
		method = e.Params.envelopeMethod()
	}
	if method != e.Params.envelopeMethod() {
		return nil, fmt.Errorf("events: envelope method %q does not match params %q", method, e.Params.envelopeMethod())
	}

	return json.Marshal(struct {
		Method string `json:"method"`
		Params any    `json:"params"`
	}{
		Method: method,
		Params: e.Params,
	})
}

func (e *Envelope) UnmarshalJSON(data []byte) error {
	var raw struct {
		Method string          `json:"method"`
		Params json.RawMessage `json:"params"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}

	var params sequenceParams
	switch raw.Method {
	case api.MethodSessionUpdate:
		var p SessionUpdateParams
		if err := json.Unmarshal(raw.Params, &p); err != nil {
			return fmt.Errorf("events: decode %s params: %w", raw.Method, err)
		}
		params = p
	case api.MethodRuntimeStateChange:
		var p RuntimeStateChangeParams
		if err := json.Unmarshal(raw.Params, &p); err != nil {
			return fmt.Errorf("events: decode %s params: %w", raw.Method, err)
		}
		params = p
	default:
		return fmt.Errorf("events: unknown envelope method %q", raw.Method)
	}

	e.Method = raw.Method
	e.Params = params
	return nil
}

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
		default:
			return nil, fmt.Errorf("events: unknown typed event %q", eventType)
		}
	}

	switch eventType {
	case api.EventTypeText:
		return unmarshal(TextEvent{})
	case api.EventTypeThinking:
		return unmarshal(ThinkingEvent{})
	case api.EventTypeUserMessage:
		return unmarshal(UserMessageEvent{})
	case api.EventTypeToolCall:
		return unmarshal(ToolCallEvent{})
	case api.EventTypeToolResult:
		return unmarshal(ToolResultEvent{})
	case api.EventTypeFileWrite:
		return unmarshal(FileWriteEvent{})
	case api.EventTypeFileRead:
		return unmarshal(FileReadEvent{})
	case api.EventTypeCommand:
		return unmarshal(CommandEvent{})
	case api.EventTypePlan:
		return unmarshal(PlanEvent{})
	case api.EventTypeTurnStart:
		return unmarshal(TurnStartEvent{})
	case api.EventTypeTurnEnd:
		return unmarshal(TurnEndEvent{})
	case api.EventTypeError:
		return unmarshal(ErrorEvent{})
	case api.EventTypeAvailableCommands:
		return unmarshal(AvailableCommandsEvent{})
	case api.EventTypeCurrentMode:
		return unmarshal(CurrentModeEvent{})
	case api.EventTypeConfigOption:
		return unmarshal(ConfigOptionEvent{})
	case api.EventTypeSessionInfo:
		return unmarshal(SessionInfoEvent{})
	case api.EventTypeUsage:
		return unmarshal(UsageEvent{})
	default:
		return nil, fmt.Errorf("events: unknown typed event %q", eventType)
	}
}
