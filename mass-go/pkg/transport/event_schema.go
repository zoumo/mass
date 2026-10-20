package transport

import (
	"github.com/danielgtaylor/huma/v2"
	"github.com/zoumo/mass/mass-go/pkg/events"
	"reflect"
	"strings"
)

// SessionEvent's payload is private. Register its actual JSON union, reusing
// the public payload types rather than inventing a second event representation.
func registerEventSchemas(r huma.Registry) {
	object := func(p map[string]*huma.Schema, required ...string) *huma.Schema {
		return &huma.Schema{Type: "object", Properties: p, Required: required, AdditionalProperties: false}
	}
	text := func() *huma.Schema { return &huma.Schema{Type: "string"} }
	array := func(item *huma.Schema) *huma.Schema { return &huma.Schema{Type: "array", Items: item} }
	enum := func(values ...string) *huma.Schema {
		v := []any{}
		for _, s := range values {
			v = append(v, s)
		}
		return &huma.Schema{Type: "string", Enum: v}
	}
	// ConfigOption has two discriminated branches and homogeneous grouped/flat
	// select values. Its codec preserves these shapes without domain/Huma coupling.
	optionValue := r.Schema(reflect.TypeFor[events.ConfigOptionValue](), true, "")
	optionGroup := r.Schema(reflect.TypeFor[events.ConfigOptionGroup](), true, "")
	configBranches := []*huma.Schema{}
	for _, kind := range []string{"select", "boolean"} {
		p := map[string]*huma.Schema{"id": text(), "name": text(), "description": text(), "category": text(), "type": {Type: "string", Const: kind}}
		required := []string{"id", "name", "type", "currentValue"}
		if kind == "select" {
			p["currentValue"] = text()
			p["options"] = &huma.Schema{AnyOf: []*huma.Schema{array(optionValue), array(optionGroup)}}
			required = append(required, "options")
		} else {
			p["currentValue"] = &huma.Schema{Type: "boolean"}
		}
		configBranches = append(configBranches, object(p, required...))
	}
	config := r.Schema(reflect.TypeFor[events.ConfigOption](), false, "")
	*config = huma.Schema{OneOf: configBranches, Discriminator: &huma.Discriminator{PropertyName: "type"}}
	// InteractionRequest also custom-serializes a union.
	interactionBranches := []*huma.Schema{}
	for _, kind := range []string{"permission", "form", "url"} {
		p := map[string]*huma.Schema{"kind": {Type: "string", Const: kind}}
		required := []string{"kind"}
		switch kind {
		case "permission":
			p["toolCallId"], p["title"] = text(), text()
			p["content"] = array(r.Schema(reflect.TypeFor[events.ContentBlock](), true, ""))
			p["options"] = array(r.Schema(reflect.TypeFor[events.PermissionOption](), true, ""))
			one := 1
			p["options"].MinItems = &one
			required = append(required, "toolCallId", "options")
		case "form":
			p["message"] = text()
			p["requestedSchema"] = &huma.Schema{Type: "object", AdditionalProperties: true}
			required = append(required, "message", "requestedSchema")
		case "url":
			p["message"], p["url"] = text(), text()
			required = append(required, "message", "url")
		}
		interactionBranches = append(interactionBranches, object(p, required...))
	}
	interaction := r.Schema(reflect.TypeFor[events.InteractionRequest](), false, "")
	*interaction = huma.Schema{OneOf: interactionBranches, Discriminator: &huma.Discriminator{PropertyName: "kind"}}
	branches := []*huma.Schema{}
	payloads := []events.Payload{events.UserMessage{}, events.AgentMessage{}, events.AgentThinking{}, events.MessageChunk{}, events.ThinkingChunk{}, events.ToolCall{}, events.ToolResult{}, events.ToolChunk{}, events.Plan{}, events.StateUpdate{}, events.SessionError{}, events.InfoUpdate{}, events.ConfigOptionUpdate{}, events.AvailableCommandsUpdate{}, events.UsageUpdate{}, events.InteractionRequested{}, events.InteractionClosed{}}
	kinds := []string{"user.message", "agent.message", "agent.thinking", "agent.message_chunk", "agent.thinking_chunk", "agent.tool_call", "agent.tool_result", "agent.tool_call_chunk", "agent.plan", "session.state_update", "session.error", "session.info_update", "session.config_option_update", "session.available_commands_update", "session.usage_update", "session.interaction_requested", "session.interaction_closed"}
	base := r.Schema(reflect.TypeFor[events.EventBase](), false, "")
	for i, value := range payloads {
		kind := kinds[i]
		payload := r.Schema(reflect.TypeOf(value), false, "")
		// Slices required by the contract encode as arrays, never null.
		for _, name := range []string{"content", "entries", "configOptions", "availableCommands"} {
			if f := payload.Properties[name]; f != nil {
				f.Nullable = false
			}
		}
		switch kind {
		case "agent.tool_call", "agent.tool_result":
			if f := payload.Properties["status"]; f != nil {
				*f = *enum("pending", "in_progress", "completed", "failed", "declined", "unknown")
			}
		case "session.state_update":
			for _, name := range []string{"state", "previousState"} {
				payload.Properties[name] = enum("idle", "running", "requires_action")
				payload.Properties[name].Nullable = true
			}
			payload.Properties["error"] = object(map[string]*huma.Schema{"code": {AnyOf: []*huma.Schema{{Type: "integer"}, text()}}, "message": text()}, "message")
		case "session.info_update":
			payload.AnyOf = []*huma.Schema{{Required: []string{"title"}}, {Required: []string{"updatedAt"}}}
		case "session.interaction_closed":
			payload.Properties["outcome"] = enum("answered", "completed", "cancelled", "expired", "failed")
		case "session.usage_update":
			payload.Properties["cost"] = object(map[string]*huma.Schema{"amount": {Type: "number"}, "currency": text()}, "amount", "currency")
		}
		props := map[string]*huma.Schema{}
		for k, v := range base.Properties {
			props[k] = v
		}
		props["eventId"] = &huma.Schema{Type: "string", Format: "uuid"}
		for _, name := range []string{"namespace", "sessionName", "sessionId"} {
			props[name] = &huma.Schema{Type: "string", MinLength: ptr(1)}
		}
		props["_meta"] = &huma.Schema{Type: "object", AdditionalProperties: true}
		props["type"] = &huma.Schema{Type: "string", Const: kind}
		props["payload"] = r.Schema(reflect.TypeOf(value), true, "")
		required := []string{"eventId", "namespace", "sessionName", "sessionId", "time", "type", "payload"}
		if !strings.HasPrefix(kind, "session.") {
			required = append(required, "itemId")
		}
		branches = append(branches, object(props, required...))
	}
	schema := r.Schema(reflect.TypeFor[events.SessionEvent](), false, "")
	*schema = huma.Schema{OneOf: branches, Discriminator: &huma.Discriminator{PropertyName: "type"}}
}
func ptr[T any](v T) *T { return &v }
