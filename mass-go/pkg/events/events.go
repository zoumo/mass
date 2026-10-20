// Package events defines the unified session event envelope and payloads.
package events

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
)

type EventBase struct {
	EventID     string         `json:"eventId"`
	Namespace   string         `json:"namespace"`
	SessionName string         `json:"sessionName"`
	SessionID   string         `json:"sessionId"`
	Time        time.Time      `json:"time"`
	ItemID      string         `json:"itemId,omitempty"`
	Meta        map[string]any `json:"_meta,omitempty"`
}

// Payload is sealed. Each concrete branch fixes its event type at compile time.
type (
	Payload        interface{ eventType() string }
	MessagePayload struct {
		MessageID string         `json:"messageId"`
		Content   []ContentBlock `json:"content"`
		Phase     string         `json:"phase,omitempty"`
	}
)
type UserMessage MessagePayload

func (UserMessage) eventType() string { return "user.message" }

type AgentMessage MessagePayload

func (AgentMessage) eventType() string { return "agent.message" }

type AgentThinking MessagePayload

func (AgentThinking) eventType() string { return "agent.thinking" }

type MessageChunk MessagePayload

func (MessageChunk) eventType() string { return "agent.message_chunk" }

type ThinkingChunk MessagePayload

func (ThinkingChunk) eventType() string { return "agent.thinking_chunk" }

type ToolCall struct {
	ID       string `json:"id"`
	Kind     string `json:"kind"`
	RawInput any    `json:"rawInput"`
}

func (ToolCall) eventType() string { return "agent.tool_call" }

type Output struct {
	Availability string `json:"availability"`
	Completeness string `json:"completeness"`
}

//nolint:recvcheck // JSON unmarshaling needs a pointer; payload values implement the event contract.
type ToolResult struct {
	ExitCode   *int32         `json:"exitCode,omitempty"`
	DurationMs *int64         `json:"durationMs,omitempty"`
	ID         string         `json:"id"`
	Kind       string         `json:"kind"`
	Status     string         `json:"status"`
	RawOutput  *any           `json:"rawOutput,omitempty"`
	Content    []ContentBlock `json:"content,omitempty"`
	Output     *Output        `json:"output,omitempty"`
}

func (ToolResult) eventType() string { return "agent.tool_result" }

type StateUpdate struct {
	PreviousState *SessionState `json:"previousState"`
	State         *SessionState `json:"state"`
	StopReason    string        `json:"stopReason,omitempty"`
}

func (StateUpdate) eventType() string { return "session.state_update" }

// SessionEvent is the only event model. Source stays private and unserialized.
//
//nolint:recvcheck // JSON unmarshaling mutates an event; reads and marshaling support event values.
type SessionEvent struct {
	EventBase
	payload Payload
	source  Source
}

func NewEvent(base EventBase, payload Payload, source Source) (SessionEvent, error) {
	e := SessionEvent{base, payload, source}
	return e, e.Validate()
}

func (e SessionEvent) Type() string {
	if e.payload == nil {
		return ""
	}
	return e.payload.eventType()
}
func (e SessionEvent) Payload() Payload { return e.payload }
func (e SessionEvent) Source() Source   { return e.source }
func (e SessionEvent) Persistent() bool {
	switch e.Type() {
	case "user.message", "agent.message", "agent.thinking", "agent.tool_call", "agent.tool_result":
		return true
	}
	return false
}

var uuidPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

func (e SessionEvent) Validate() error {
	if !uuidPattern.MatchString(e.EventID) || e.Namespace == "" || e.SessionName == "" || e.SessionID == "" || e.Time.IsZero() || e.payload == nil {
		return errors.New("invalid event envelope")
	}
	if err := CheckDynamicJSON(e.Meta); err != nil {
		return err
	}
	var m *MessagePayload
	switch p := e.payload.(type) {
	case UserMessage:
		v := MessagePayload(p)
		m = &v
	case AgentMessage:
		v := MessagePayload(p)
		m = &v
	case AgentThinking:
		v := MessagePayload(p)
		m = &v
	case MessageChunk:
		v := MessagePayload(p)
		m = &v
	case ThinkingChunk:
		v := MessagePayload(p)
		m = &v
	case ToolCall:
		if err := CheckDynamicJSON(p.RawInput); err != nil {
			return err
		}
		if p.ID == "" || p.Kind == "" {
			return errors.New("invalid tool call")
		}
	case ToolResult:
		if p.RawOutput != nil {
			if err := CheckDynamicJSON(*p.RawOutput); err != nil {
				return err
			}
		}
		switch p.Status {
		case "pending", "in_progress", "completed", "failed", "declined", "unknown":
		default:
			return errors.New("invalid tool status")
		}
		for _, b := range p.Content {
			if b.Type != "text" {
				return errors.New("unsupported tool content type")
			}
		}
		if p.DurationMs != nil && *p.DurationMs < 0 {
			return errors.New("negative tool duration")
		}
		if p.Output != nil {
			if p.Output.Availability != "provided" && p.Output.Availability != "unavailable" {
				return errors.New("invalid output availability")
			}
			if p.Output.Completeness != "retained" && p.Output.Completeness != "unknown" {
				return errors.New("unsupported output completeness")
			}
		}
		if p.ID == "" || p.Kind == "" || p.Status == "" {
			return errors.New("invalid tool result")
		}
	case InteractionRequested:
		if p.Interaction == "" || (p.Request.Kind != "permission" && p.Request.Kind != "form") {
			return errors.New("invalid interaction")
		}
		if p.Request.Kind == "form" && (p.Request.Message == "" || p.Request.RequestedSchema == nil) {
			return errors.New("invalid form request")
		}
		if p.Request.Kind == "permission" && (p.Request.ToolCallID == "" || len(p.Request.Options) == 0) {
			return errors.New("invalid permission request")
		}
		if err := CheckDynamicJSON(p.Request.RequestedSchema); err != nil {
			return err
		}
	case InteractionClosed:
		if p.Interaction == "" || p.Outcome != "answered" {
			return errors.New("invalid interaction closure")
		}
	case SessionError:
		if p.Message == "" {
			return errors.New("empty session error")
		}
	case StateUpdate:
		for _, s := range []*SessionState{p.State, p.PreviousState} {
			if s != nil && *s != Idle && *s != Running && *s != RequiresAction {
				return errors.New("invalid observed state")
			}
		}
	default:
		return errors.New("unsupported payload branch")
	}
	if !strings.HasPrefix(e.Type(), "session.") && e.ItemID == "" {
		return errors.New("itemId required")
	}
	if m != nil {
		if m.MessageID == "" || m.Content == nil {
			return errors.New("invalid message")
		}
		for _, b := range m.Content {
			if b.Type != "text" {
				return errors.New("content type unsupported in this implementation")
			}
		}
	}
	return nil
}

func (e SessionEvent) MarshalJSON() ([]byte, error) {
	if err := e.Validate(); err != nil {
		return nil, err
	}
	return json.Marshal(struct {
		EventBase
		Type    string  `json:"type"`
		Payload Payload `json:"payload"`
	}{e.EventBase, e.Type(), e.payload})
}

func (e *SessionEvent) UnmarshalJSON(data []byte) error {
	var raw struct {
		EventBase
		Type    string          `json:"type"`
		Payload json.RawMessage `json:"payload"`
	}
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	d.UseNumber()
	if err := d.Decode(&raw); err != nil {
		return err
	}
	var members map[string]json.RawMessage
	if err := json.Unmarshal(raw.Payload, &members); err != nil {
		return err
	}
	required := []string{}
	switch raw.Type {
	case "agent.tool_call":
		required = []string{"rawInput"}
	case "session.state_update":
		required = []string{"state", "previousState"}
	}
	for _, name := range required {
		if _, ok := members[name]; !ok {
			return fmt.Errorf("missing required payload field %s", name)
		}
	}
	var p Payload
	switch raw.Type {
	case "user.message":
		p = &UserMessage{}
	case "agent.message":
		p = &AgentMessage{}
	case "agent.thinking":
		p = &AgentThinking{}
	case "agent.message_chunk":
		p = &MessageChunk{}
	case "agent.thinking_chunk":
		p = &ThinkingChunk{}
	case "agent.tool_call":
		p = &ToolCall{}
	case "agent.tool_result":
		p = &ToolResult{}
	case "session.interaction_requested":
		p = &InteractionRequested{}
	case "session.interaction_closed":
		p = &InteractionClosed{}
	case "session.error":
		p = &SessionError{}
	case "session.state_update":
		p = &StateUpdate{}
	default:
		return fmt.Errorf("unsupported event type %q", raw.Type)
	}
	d = json.NewDecoder(bytes.NewReader(raw.Payload))
	d.DisallowUnknownFields()
	d.UseNumber()
	if err := d.Decode(p); err != nil {
		return err
	}
	// Normalize pointer branches so callers see the same concrete payload on reads.
	switch v := p.(type) {
	case *UserMessage:
		p = *v
	case *AgentMessage:
		p = *v
	case *AgentThinking:
		p = *v
	case *MessageChunk:
		p = *v
	case *ThinkingChunk:
		p = *v
	case *ToolCall:
		p = *v
	case *ToolResult:
		p = *v
	case *InteractionRequested:
		p = *v
	case *InteractionClosed:
		p = *v
	case *SessionError:
		p = *v
	case *StateUpdate:
		p = *v
	}
	result, err := NewEvent(raw.EventBase, p, Source{})
	if err == nil {
		*e = result
	}
	return err
}

// Clone detaches slices/maps without changing the source association.
func (e SessionEvent) Clone() (SessionEvent, error) {
	b, err := json.Marshal(e)
	if err != nil {
		return SessionEvent{}, err
	}
	var out SessionEvent
	err = json.Unmarshal(b, &out)
	out.source = e.source
	return out, err
}

// A pointer field alone cannot distinguish omitted rawOutput from explicit null
// during encoding/json decoding. Preserve the member's actual presence.
func (p *ToolResult) UnmarshalJSON(data []byte) error {
	type fields ToolResult
	var out fields
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	d.UseNumber()
	if err := d.Decode(&out); err != nil {
		return err
	}
	var members map[string]json.RawMessage
	if err := json.Unmarshal(data, &members); err != nil {
		return err
	}
	if raw, ok := members["rawOutput"]; ok {
		var value any
		d = json.NewDecoder(bytes.NewReader(raw))
		d.UseNumber()
		if err := d.Decode(&value); err != nil {
			return err
		}
		out.RawOutput = &value
	}
	*p = ToolResult(out)
	return nil
}

// An explicitly supplied empty content array is distinct from omitted content.
func (p ToolResult) MarshalJSON() ([]byte, error) {
	type fields ToolResult
	data, err := json.Marshal(fields(p))
	if err != nil {
		return nil, err
	}
	if p.Content == nil {
		return data, nil
	}
	var members map[string]json.RawMessage
	if err := json.Unmarshal(data, &members); err != nil {
		return nil, err
	}
	members["content"], err = json.Marshal(p.Content)
	if err != nil {
		return nil, err
	}
	return json.Marshal(members)
}
