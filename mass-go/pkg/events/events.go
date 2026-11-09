// Package events defines the MASS session envelope around the SDK's live event
// and payload model.
package events

import (
	"bytes"
	"encoding/json"
	"errors"
	"maps"
	"slices"
	"time"

	"github.com/google/uuid"
	"github.com/zoumo/agent-sdk-go/agent"
)

type EventBase struct {
	Replay      *ReplaySource  `json:"replay,omitempty"`
	EventID     string         `json:"eventId"`
	Namespace   string         `json:"namespace"`
	SessionName string         `json:"sessionName"`
	SessionID   string         `json:"sessionId"`
	Time        time.Time      `json:"time"`
	ItemID      string         `json:"itemId,omitempty"`
	Meta        map[string]any `json:"_meta,omitempty"`
}

type ReplaySource struct {
	LoadID         string `json:"loadId"`
	NativeRecordID string `json:"nativeRecordId,omitempty"`
	NativeParentID string `json:"nativeParentId,omitempty"`
}

type ResumedSession struct {
	SessionID         string                 `json:"sessionId"`
	Capabilities      map[string]bool        `json:"capabilities"`
	EffectiveFeatures ResumedSessionFeatures `json:"effectiveFeatures"`
}

type ResumedSessionFeatures struct {
	AgentQuestions bool `json:"agentQuestions"`
}

type HistoryReplay struct {
	LoadID         string          `json:"loadId"`
	Phase          string          `json:"phase"`
	RetainedScope  string          `json:"retainedScope"`
	ResumedSession *ResumedSession `json:"resumedSession,omitempty"`
	CurrentState   **SessionState  `json:"currentState,omitempty"`
	Error          map[string]any  `json:"error,omitempty"`
}

func (HistoryReplay) eventType() string { return "session.history_replay" }

// Payload is the envelope's payload slot. Live values must implement the SDK
// agent.Payload closed union; HistoryReplay is MASS's only control branch.
type Payload any

// SessionEvent is the only event model. Source stays private and unserialized.
//
//nolint:recvcheck // JSON unmarshaling mutates an event; reads and marshaling support event values.
type SessionEvent struct {
	EventBase
	payload Payload
	source  Source
}

func NewEvent(base EventBase, payload Payload, source Source) (SessionEvent, error) {
	e := SessionEvent{EventBase: base, payload: payload, source: source}
	return e, e.Validate()
}

func (e SessionEvent) Type() string {
	if replay, ok := historyPayload(e.payload); ok {
		return replay.eventType()
	}
	live, ok := e.payload.(agent.Payload)
	if !ok {
		return ""
	}
	return string(agent.Event{Payload: live}.Type())
}

func (e SessionEvent) Payload() Payload { return e.payload }
func (e SessionEvent) Source() Source   { return e.source }
func (e SessionEvent) Persistent() bool {
	switch agent.EventType(e.Type()) {
	case agent.EventUserMessage,
		agent.EventAgentMessage,
		agent.EventAgentThinking,
		agent.EventAgentToolCall,
		agent.EventAgentToolResult:
		return true
	default:
		return false
	}
}

func (e SessionEvent) Validate() error {
	if len(e.EventID) != 36 ||
		uuid.Validate(e.EventID) != nil ||
		e.Namespace == "" ||
		e.SessionName == "" ||
		e.SessionID == "" ||
		e.Time.IsZero() ||
		e.payload == nil {
		return errors.New("invalid event envelope")
	}
	if e.Replay != nil && e.Replay.LoadID == "" {
		return errors.New("missing replay loadId")
	}
	if err := CheckDynamicJSON(e.Meta); err != nil {
		return err
	}
	if replay, ok := historyPayload(e.payload); ok {
		if replay.LoadID == "" ||
			replay.RetainedScope == "" ||
			!oneOf(replay.Phase, "started", "native_completed", "failed", "canceled") {
			return errors.New("invalid history replay")
		}
		if (replay.Phase == "started" || replay.Phase == "native_completed") &&
			(replay.ResumedSession == nil ||
				replay.ResumedSession.SessionID != e.SessionID ||
				replay.ResumedSession.Capabilities == nil) {
			return errors.New("missing or mismatched resumed session")
		}
		if replay.CurrentState != nil &&
			*replay.CurrentState != nil &&
			!oneOf(string(**replay.CurrentState), string(Idle), string(Running), string(RequiresAction)) {
			return errors.New("invalid replay current state")
		}
		if err := CheckDynamicJSON(replay.Error); err != nil {
			return err
		}
		return nil
	}
	live, ok := e.payload.(agent.Payload)
	if !ok {
		return errors.New("unsupported payload branch")
	}
	return (agent.Event{
		EventID:   e.EventID,
		SessionID: e.SessionID,
		Time:      e.Time,
		ItemID:    e.ItemID,
		Payload:   live,
		Meta:      e.Meta,
	}).Validate()
}

func (e SessionEvent) MarshalJSON() ([]byte, error) {
	if err := e.Validate(); err != nil {
		return nil, err
	}
	if replay, ok := historyPayload(e.payload); ok {
		return json.Marshal(struct {
			EventBase
			Type    string        `json:"type"`
			Payload HistoryReplay `json:"payload"`
		}{EventBase: e.EventBase, Type: replay.eventType(), Payload: replay})
	}
	live := e.payload.(agent.Payload)
	encoded, err := json.Marshal(agent.Event{
		EventID:   e.EventID,
		SessionID: e.SessionID,
		Time:      e.Time,
		ItemID:    e.ItemID,
		Payload:   live,
		Meta:      e.Meta,
	})
	if err != nil {
		return nil, err
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &object); err != nil {
		return nil, err
	}
	baseJSON, err := json.Marshal(e.EventBase)
	if err != nil {
		return nil, err
	}
	var base map[string]json.RawMessage
	if err := json.Unmarshal(baseJSON, &base); err != nil {
		return nil, err
	}
	maps.Copy(object, base)
	return json.Marshal(object)
}

func (e *SessionEvent) UnmarshalJSON(data []byte) error {
	var raw struct {
		EventBase
		Type    string          `json:"type"`
		Payload json.RawMessage `json:"payload"`
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	decoder.UseNumber()
	if err := decoder.Decode(&raw); err != nil {
		return err
	}
	if len(raw.Payload) == 0 || bytes.Equal(bytes.TrimSpace(raw.Payload), []byte("null")) {
		return errors.New("missing event payload")
	}
	if raw.Type == "session.history_replay" {
		var replay HistoryReplay
		payloadDecoder := json.NewDecoder(bytes.NewReader(raw.Payload))
		payloadDecoder.DisallowUnknownFields()
		payloadDecoder.UseNumber()
		if err := payloadDecoder.Decode(&replay); err != nil {
			return err
		}
		var members map[string]json.RawMessage
		if err := json.Unmarshal(raw.Payload, &members); err != nil {
			return err
		}
		if value, ok := members["currentState"]; ok &&
			bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			var unknown *SessionState
			replay.CurrentState = &unknown
		}
		event := SessionEvent{EventBase: raw.EventBase, payload: replay}
		if err := event.Validate(); err != nil {
			return err
		}
		*e = event
		return nil
	}
	liveJSON, err := json.Marshal(struct {
		EventID   string          `json:"eventId"`
		SessionID string          `json:"sessionId"`
		Time      time.Time       `json:"time"`
		ItemID    string          `json:"itemId"`
		Type      agent.EventType `json:"type"`
		Payload   json.RawMessage `json:"payload"`
		Meta      map[string]any  `json:"_meta"`
	}{
		EventID:   raw.EventID,
		SessionID: raw.SessionID,
		Time:      raw.Time,
		ItemID:    raw.ItemID,
		Type:      agent.EventType(raw.Type),
		Payload:   raw.Payload,
		Meta:      raw.Meta,
	})
	if err != nil {
		return err
	}
	var live agent.Event
	if err := json.Unmarshal(liveJSON, &live); err != nil {
		return err
	}
	event := SessionEvent{EventBase: raw.EventBase, payload: live.Payload}
	if err := event.Validate(); err != nil {
		return err
	}
	*e = event
	return nil
}

// Clone detaches slices/maps without changing the source association.
func (e SessionEvent) Clone() (SessionEvent, error) {
	encoded, err := json.Marshal(e)
	if err != nil {
		return SessionEvent{}, err
	}
	var out SessionEvent
	err = json.Unmarshal(encoded, &out)
	out.source = e.source
	return out, err
}

// ValidType recognizes MASS history replay and the SDK's live event union.
func ValidType(kind string) bool {
	switch agent.EventType(kind) {
	case agent.EventUserMessage,
		agent.EventAgentMessage,
		agent.EventAgentThinking,
		agent.EventAgentToolCall,
		agent.EventAgentToolResult,
		agent.EventAgentMessageChunk,
		agent.EventAgentThinkingChunk,
		agent.EventAgentToolCallChunk,
		agent.EventSessionStateUpdate,
		agent.EventSessionError,
		agent.EventSessionInfoUpdate,
		agent.EventSessionConfigOptionUpdate,
		agent.EventSessionCommandsUpdate,
		agent.EventSessionUsageUpdate,
		agent.EventInteractionRequested,
		agent.EventInteractionClosed:
		return true
	}
	return kind == "session.history_replay"
}

func historyPayload(payload Payload) (HistoryReplay, bool) {
	switch value := payload.(type) {
	case HistoryReplay:
		return value, true
	case *HistoryReplay:
		if value == nil {
			return HistoryReplay{}, false
		}
		return *value, true
	default:
		return HistoryReplay{}, false
	}
}

func oneOf(value string, values ...string) bool {
	return slices.Contains(values, value)
}
