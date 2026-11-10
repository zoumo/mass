package events

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
)

// ErrLegacyEventSchema identifies persisted events that cannot be represented
// by the SDK's current live event union without losing meaning.
var ErrLegacyEventSchema = errors.New("legacy_event_schema")

// LegacyEventSchemaError names the unsupported persisted event type while
// keeping the stable ErrLegacyEventSchema sentinel available to callers.
type LegacyEventSchemaError struct {
	EventType string
}

func (e *LegacyEventSchemaError) Error() string {
	return fmt.Sprintf("%s: %q predates the SDK event schema", ErrLegacyEventSchema, e.EventType)
}

func (e *LegacyEventSchemaError) Unwrap() error { return ErrLegacyEventSchema }

// DecodeSessionEvent checks for known legacy shapes before the strict SDK
// union decode. The original bytes remain the only persisted authority.
func DecodeSessionEvent(data []byte, event *SessionEvent) error {
	if event == nil {
		return errors.New("nil SessionEvent destination")
	}
	if err := CheckLegacyEventSchema(data); err != nil {
		return err
	}
	return json.Unmarshal(data, event)
}

// CheckLegacyEventSchema recognizes only the removed plan event and tool
// records that lack the SDK's required toolName/provider identity.
func CheckLegacyEventSchema(data []byte) error {
	var envelope struct {
		Type    string          `json:"type"`
		Payload json.RawMessage `json:"payload"`
	}
	if err := json.Unmarshal(data, &envelope); err != nil {
		return nil
	}
	if envelope.Type == "agent.plan" {
		return &LegacyEventSchemaError{EventType: envelope.Type}
	}
	if envelope.Type != "agent.tool_call" && envelope.Type != "agent.tool_result" {
		return nil
	}
	var payload map[string]json.RawMessage
	if err := json.Unmarshal(envelope.Payload, &payload); err != nil {
		return nil
	}
	for _, field := range []string{"provider", "toolName"} {
		if !hasNonEmptyJSONString(payload[field]) {
			return &LegacyEventSchemaError{EventType: envelope.Type}
		}
	}
	return nil
}

func hasNonEmptyJSONString(value json.RawMessage) bool {
	if len(value) == 0 || bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
		return false
	}
	var text string
	return json.Unmarshal(value, &text) == nil && text != ""
}
