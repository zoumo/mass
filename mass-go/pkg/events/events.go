// Package events defines the unified session event envelope and payloads.
package events

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"time"

	"github.com/google/uuid"
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
	Name                string         `json:"name,omitempty"`
	Title               string         `json:"title,omitempty"`
	Phase               string         `json:"phase,omitempty"`
	InputRepresentation string         `json:"inputRepresentation,omitempty"`
	Status              string         `json:"status,omitempty"`
	RawOutput           *any           `json:"rawOutput,omitempty"`
	Content             []ContentBlock `json:"content,omitempty"`
	ExitCode            *int32         `json:"exitCode,omitempty"`
	DurationMs          *float64       `json:"durationMs,omitempty" minimum:"0"`
	Output              *Output        `json:"output,omitempty"`
	ID                  string         `json:"id"`
	Kind                string         `json:"kind"`
	RawInput            any            `json:"rawInput"`
}

func (ToolCall) eventType() string { return "agent.tool_call" }

type Output struct {
	Availability string      `json:"availability" enum:"provided,unavailable"`
	Completeness string      `json:"completeness" enum:"retained,truncated,unknown"`
	Truncation   *Truncation `json:"truncation,omitempty"`
}

//nolint:recvcheck // JSON unmarshaling needs a pointer; payload values implement the event contract.
type ToolResult struct {
	Name                string         `json:"name,omitempty"`
	Title               string         `json:"title,omitempty"`
	Phase               string         `json:"phase,omitempty"`
	InputRepresentation string         `json:"inputRepresentation,omitempty"`
	RawInput            *any           `json:"rawInput,omitempty"`
	ExitCode            *int32         `json:"exitCode,omitempty"`
	DurationMs          *float64       `json:"durationMs,omitempty" minimum:"0"`
	ID                  string         `json:"id"`
	Kind                string         `json:"kind"`
	Status              string         `json:"status"`
	RawOutput           *any           `json:"rawOutput,omitempty"`
	Content             []ContentBlock `json:"content,omitempty"`
	Output              *Output        `json:"output,omitempty"`
}

func (ToolResult) eventType() string { return "agent.tool_result" }

type StateUpdate struct {
	PreviousState *SessionState  `json:"previousState"`
	State         *SessionState  `json:"state"`
	StopReason    string         `json:"stopReason,omitempty"`
	Error         *ObservedError `json:"error,omitempty"`
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

func (e SessionEvent) Validate() error {
	if len(e.EventID) != 36 || uuid.Validate(e.EventID) != nil || e.Namespace == "" || e.SessionName == "" || e.SessionID == "" || e.Time.IsZero() || e.payload == nil {
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
		if err := validateTool(p.Status, p.RawOutput, p.Content, p.DurationMs, p.Output); err != nil {
			return err
		}
		if err := CheckDynamicJSON(p.RawInput); err != nil {
			return err
		}
		if p.ID == "" || p.Kind == "" {
			return errors.New("invalid tool call")
		}
	case ToolResult:
		if err := validateTool(p.Status, p.RawOutput, p.Content, p.DurationMs, p.Output); err != nil {
			return err
		}
		if p.RawInput != nil {
			if err := CheckDynamicJSON(*p.RawInput); err != nil {
				return err
			}
		}

		if p.ID == "" || p.Kind == "" || p.Status == "" {
			return errors.New("invalid tool result")
		}
	case InteractionRequested:
		if err := p.Request.Validate(); err != nil {
			return err
		}
		if p.Interaction == "" || (p.Request.Kind != "permission" && p.Request.Kind != "form" && p.Request.Kind != "url") {
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
		if p.Interaction == "" || !oneOf(p.Outcome, "answered", "completed", "cancelled", "expired", "failed") {
			return errors.New("invalid interaction closure")
		}
	case SessionError:
		if p.Message == "" {
			return errors.New("empty session error")
		}
	case ToolChunk:
		if p.ID == "" || p.Content == nil {
			return errors.New("invalid tool chunk")
		}
		for _, b := range p.Content {
			if err := b.Validate(); err != nil {
				return err
			}
		}
	case Plan:
		if p.PlanID == "" || p.Entries == nil {
			return errors.New("invalid plan")
		}
		for _, entry := range p.Entries {
			if !oneOf(entry.Priority, "low", "medium", "high") || !oneOf(entry.Status, "pending", "in_progress", "completed") {
				return errors.New("invalid plan entry")
			}
		}
	case InfoUpdate:
		if p.Title == nil && p.UpdatedAt == nil {
			return errors.New("empty info update")
		}
	case ConfigOptionUpdate:
		if p.ConfigOptions == nil {
			return errors.New("missing config options")
		}
		for _, option := range p.ConfigOptions {
			if err := option.Validate(); err != nil {
				return err
			}
		}
	case AvailableCommandsUpdate:
		if p.AvailableCommands == nil {
			return errors.New("missing commands")
		}
	case UsageUpdate:
		if p.Cost != nil {
			if err := CheckDynamicJSON(p.Cost.Amount); err != nil {
				return err
			}
		}
	case StateUpdate:
		if p.Error != nil {
			if p.Error.Code != nil {
				switch v := p.Error.Code.(type) {
				case string:
				case json.Number:
					if !jsonInteger(v) {
						return errors.New("invalid error code")
					}
				case int, int32, int64:
				default:
					return errors.New("invalid error code")
				}
			}
		}
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
			if err := b.Validate(); err != nil {
				return err
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
	case "agent.tool_call_chunk":
		p = &ToolChunk{}
	case "agent.plan":
		p = &Plan{}
	case "session.info_update":
		p = &InfoUpdate{}
	case "session.config_option_update":
		p = &ConfigOptionUpdate{}
	case "session.available_commands_update":
		p = &AvailableCommandsUpdate{}
	case "session.usage_update":
		p = &UsageUpdate{}
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
	// Required members must remain distinguishable from zero values and null.
	for f := range reflect.TypeOf(p).Elem().Fields() {
		name, options, _ := strings.Cut(f.Tag.Get("json"), ",")
		if name == "" || strings.Contains(options, "omitempty") {
			continue
		}
		value, ok := members[name]
		if !ok {
			return fmt.Errorf("missing required payload field %s", name)
		}
		if string(bytes.TrimSpace(value)) == "null" && raw.Type != "session.state_update" && name != "rawInput" {
			return fmt.Errorf("null payload field %s", name)
		}
	}
	d = json.NewDecoder(bytes.NewReader(raw.Payload))
	d.DisallowUnknownFields()
	d.UseNumber()
	if err := d.Decode(p); err != nil {
		return err
	}
	// Normalize pointer branches so callers see the same concrete payload on reads.
	switch v := p.(type) {
	case *ToolChunk:
		p = *v
	case *Plan:
		p = *v
	case *InfoUpdate:
		p = *v
	case *ConfigOptionUpdate:
		p = *v
	case *AvailableCommandsUpdate:
		p = *v
	case *UsageUpdate:
		p = *v
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
	if raw, ok := members["rawInput"]; ok {
		var value any
		d = json.NewDecoder(bytes.NewReader(raw))
		d.UseNumber()
		if err := d.Decode(&value); err != nil {
			return err
		}
		out.RawInput = &value
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
	return marshalToolContent(fields(p), p.Content)
}

// Preserve explicit null for an optional dynamic output and explicit [] content.
func (p *ToolCall) UnmarshalJSON(data []byte) error {
	type fields ToolCall
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
	*p = ToolCall(out)
	return nil
}
func (p ToolCall) MarshalJSON() ([]byte, error) {
	type fields ToolCall
	return marshalToolContent(fields(p), p.Content)
}
func marshalToolContent(value any, content []ContentBlock) ([]byte, error) {
	data, err := json.Marshal(value)
	if err != nil || content == nil {
		return data, err
	}
	var members map[string]json.RawMessage
	if err := json.Unmarshal(data, &members); err != nil {
		return nil, err
	}
	members["content"], err = json.Marshal(content)
	if err != nil {
		return nil, err
	}
	return json.Marshal(members)
}
