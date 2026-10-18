package claude

import (
	"encoding/json"
	"fmt"

	"github.com/zoumo/agent-sdk-go/claude/types"
	rt "github.com/zoumo/mass/mass-go/pkg/runtime"
)

// Only message_start establishes the current native message. Frame UUIDs are
// never message references. We retain no accumulated text and deduplicate none.
type messageMapper struct {
	messageID, role string
	tool            *streamTool
}

// The API streams one active block at a time. Retain only immutable identity,
// not the payload buffers handed to callers, and reset it at every boundary.
type streamTool struct {
	callRef, name string
	index         int
}

func (m *messageMapper) toolPayload() *rt.ToolPayload {
	index := m.tool.index
	return &rt.ToolPayload{MessageRef: m.messageID, BlockIndex: &index, CallRef: m.tool.callRef, Name: m.tool.name, Category: "unknown", State: "announced", Input: rt.ContentSet{Availability: "missing", Completeness: "unknown"}, Output: rt.ContentSet{Availability: "missing", Completeness: "unknown"}, OutcomeEvidence: "call_observed"}
}

func (m *messageMapper) convert(n types.Event, base rt.Event) ([]rt.Event, bool) {
	fail := func() ([]rt.Event, bool) {
		base.Kind = rt.Failure
		base.Error = &rt.SafeError{Category: "decode_error", Scope: "session"}
		return []rt.Event{base}, true
	}
	if n.System != nil {
		switch n.Subtype {
		case "init":
			raw := n.System.Raw
			patch, err := metadata(raw)
			if err != nil {
				return fail()
			}
			var events []rt.Event
			if n.Subtype == "init" {
				ready := base
				ready.Kind = rt.SessionReady
				events = append(events, ready)
			}
			if patch != nil {
				e := base
				e.Kind = rt.Metadata
				e.Metadata = patch
				events = append(events, e)
			}
			if len(events) == 0 {
				base.Kind = rt.Metadata
				base.Metadata = &rt.MetadataPayload{}
				events = append(events, base)
			}
			return events, true
		}
	}
	if n.Stream != nil {
		var stream struct {
			Type    string
			Index   *int
			Message struct {
				ID, Role string
				Content  json.RawMessage
			}
			Delta struct {
				Type        string
				Text        *string
				Thinking    *string
				PartialJSON *string `json:"partial_json"`
			}
			ContentBlock json.RawMessage
		}
		// content_block uses snake_case while the rest are direct API members.
		var raw struct {
			ContentBlock json.RawMessage `json:"content_block"`
		}
		if json.Unmarshal(n.Stream.Event, &stream) != nil || json.Unmarshal(n.Stream.Event, &raw) != nil {
			return fail()
		}
		stream.ContentBlock = raw.ContentBlock
		if stream.Index != nil && *stream.Index < 0 {
			return fail()
		}
		switch stream.Type {
		case "message_start":
			if stream.Message.ID == "" {
				return fail()
			}
			m.tool = nil
			m.messageID = stream.Message.ID
			m.role = role(stream.Message.Role)
			c, err := content(stream.Message.Content, "started", "partial")
			if err != nil {
				return fail()
			}
			base.Kind = rt.MessageCompleted
			base.Message = &rt.MessagePayload{MessageRef: m.messageID, Role: m.role, Stage: "started", Content: c}
			return []rt.Event{base}, true
		case "content_block_start":
			var header struct{ Type string }
			if json.Unmarshal(stream.ContentBlock, &header) != nil || header.Type == "" {
				return fail()
			}
			m.tool = nil
			if header.Type == "tool_use" {
				var call types.ToolUseBlock
				if json.Unmarshal(stream.ContentBlock, &call) != nil || call.ID == "" || call.Name == "" || stream.Index == nil {
					return fail()
				}
				m.tool = &streamTool{callRef: call.ID, name: call.Name, index: *stream.Index}
				base.Kind = rt.ToolAnnounced
				base.Tool = m.toolPayload()
				base.Tool.Input = rt.JSONContent(call.Input, "started", "partial")
				return []rt.Event{base}, true
			}
			c, err := content(json.RawMessage("["+string(stream.ContentBlock)+"]"), "started", "partial")
			if err != nil {
				return fail()
			}
			if len(c.Blocks) == 1 {
				c.Blocks[0].Index = stream.Index
			}
			return m.contentEvents(base, c, "started"), true
		case "content_block_delta":
			var text *string
			kind := rt.MessageCompleted
			switch stream.Delta.Type {
			case "input_json_delta":
				if stream.Delta.PartialJSON == nil || m.tool == nil || stream.Index == nil || m.tool.index != *stream.Index {
					return fail()
				}
				t := m.toolPayload()
				t.InputJSONDelta = stream.Delta.PartialJSON
				t.Input = rt.ContentSet{Availability: "missing", Completeness: "partial"}
				base.Kind = rt.ToolUpdated
				base.Tool = t
				return []rt.Event{base}, true
			case "text_delta":
				text = stream.Delta.Text
			case "thinking_delta":
				text = stream.Delta.Thinking
				kind = rt.ReasoningCompleted
			case "signature_delta":
				base.Kind = rt.ReasoningCompleted
				base.Reasoning = &rt.ReasoningPayload{MessageRef: m.messageID, Stage: "delta", BlockIndex: stream.Index, Content: rt.ContentSet{Availability: "missing", Completeness: "partial"}}
				return []rt.Event{base}, true
			default:
				return nil, false
			}
			if text == nil {
				return fail()
			}
			c := rt.TextContent(*text, "delta", "partial", stream.Index)
			base.Kind = kind
			if kind == rt.MessageCompleted {
				base.Message = &rt.MessagePayload{MessageRef: m.messageID, Role: m.currentRole(), Stage: "delta", Content: c}
			} else {
				base.Reasoning = &rt.ReasoningPayload{MessageRef: m.messageID, Stage: "delta", BlockIndex: stream.Index, Content: c}
			}
			return []rt.Event{base}, true
		case "content_block_stop":
			if stream.Index == nil || *stream.Index < 0 {
				return fail()
			}
			if m.tool != nil && m.tool.index == *stream.Index {
				t := m.toolPayload()
				t.Input = rt.ContentSet{Availability: "missing", Completeness: "unknown"}
				base.Kind = rt.ToolUpdated
				base.Tool = t
				m.tool = nil
				return []rt.Event{base}, true
			}
			base.Kind = rt.MessageCompleted
			base.Message = &rt.MessagePayload{MessageRef: m.messageID, Role: m.currentRole(), Stage: "delta", BlockIndex: stream.Index, BlockStage: "completed", Content: rt.ContentSet{Availability: "missing", Completeness: "partial"}}
			return []rt.Event{base}, true
		case "message_delta":
			base.Kind = rt.MessageCompleted
			base.Message = &rt.MessagePayload{MessageRef: m.messageID, Role: m.currentRole(), Stage: "delta", Content: rt.ContentSet{Availability: "missing", Completeness: "partial"}}
			return []rt.Event{base}, true
		case "message_stop":
			// API stream end has no complete text snapshot. The full assistant frame
			// supplies completion; do not manufacture one by concatenating previews.
			ref := m.messageID
			currentRole := m.currentRole()
			m.tool = nil
			m.messageID = ""
			m.role = ""
			base.Kind = rt.MessageCompleted
			base.Message = &rt.MessagePayload{MessageRef: ref, Role: currentRole, Stage: "completed", Content: rt.ContentSet{Availability: "missing", Completeness: "unknown"}}
			return []rt.Event{base}, true
		}
		return nil, false
	}
	message := n.Assistant
	if message == nil {
		message = n.User
	}
	if message == nil {
		return nil, false
	}
	c, err := content(message.Content.Raw, "completed", "complete")
	if err != nil {
		return fail()
	}
	base.Kind = rt.MessageCompleted
	base.Message = &rt.MessagePayload{MessageRef: message.ID, Role: role(message.Role), Stage: "completed", Content: c}
	if base.Message.Role == "unknown" {
		base.Message.Role = role(n.Type)
	}
	events := []rt.Event{base}
	for i, b := range message.Content.Blocks {
		e := base
		e.Message = nil
		switch {
		case b.Thinking != nil:
			e.Kind = rt.ReasoningCompleted
			e.Reasoning = &rt.ReasoningPayload{MessageRef: message.ID, Stage: "completed", Content: rt.TextContent(b.Thinking.Thinking, "completed", "complete", &i)}
		case b.RedactedThinking != nil:
			e.Kind = rt.ReasoningCompleted
			e.Reasoning = &rt.ReasoningPayload{MessageRef: message.ID, Stage: "completed", Content: rt.ContentSet{Availability: "redacted", Completeness: "complete", Blocks: []rt.ContentBlock{{Index: &i, Kind: "redacted", Stage: "completed", Completeness: "complete"}}}}
		case b.ToolUse != nil:
			if b.ToolUse.ID == "" || b.ToolUse.Name == "" {
				return fail()
			}
			e.Kind = rt.ToolAnnounced
			e.Tool = &rt.ToolPayload{MessageRef: message.ID, BlockIndex: &i, CallRef: b.ToolUse.ID, Name: b.ToolUse.Name, Category: "unknown", State: "announced", Input: rt.JSONContent(b.ToolUse.Input, "completed", "complete"), Output: rt.ContentSet{Availability: "missing", Completeness: "unknown"}, OutcomeEvidence: "call_observed"}
		case b.ToolResult != nil:
			if b.ToolResult.ToolUseID == "" {
				return fail()
			}
			output, err := content(b.ToolResult.Content, "completed", "complete")
			if err != nil {
				return fail()
			}
			state := "completed"
			evidence := "native_result"
			if b.ToolResult.IsError {
				state = "failed"
				evidence = "native_is_error"
			}
			e.Kind = rt.ToolCompleted
			e.Tool = &rt.ToolPayload{MessageRef: message.ID, BlockIndex: &i, CallRef: b.ToolResult.ToolUseID, Category: "unknown", State: state, Input: rt.ContentSet{Availability: "missing", Completeness: "unknown"}, Output: output, OutcomeEvidence: evidence}
		default:
			continue
		}
		events = append(events, e)
	}
	return events, true
}
func (m *messageMapper) currentRole() string {
	if m.role == "" {
		return "unknown"
	}
	return m.role
}
func role(v string) string {
	switch v {
	case "user", "assistant", "system", "tool":
		return v
	}
	return "unknown"
}
func (m *messageMapper) contentEvents(e rt.Event, c rt.ContentSet, stage string) []rt.Event {
	if len(c.Blocks) == 1 && (c.Blocks[0].Kind == "redacted" || c.Blocks[0].Namespace == "reasoning") {
		e.Kind = rt.ReasoningCompleted
		e.Reasoning = &rt.ReasoningPayload{MessageRef: m.messageID, Stage: "delta", BlockIndex: c.Blocks[0].Index, Content: c}
	} else {
		e.Kind = rt.MessageCompleted
		e.Message = &rt.MessagePayload{MessageRef: m.messageID, Role: m.currentRole(), Stage: stage, Content: c}
	}
	return []rt.Event{e}
}
func content(raw json.RawMessage, stage, completeness string) (rt.ContentSet, error) {
	c := rt.ContentSet{Availability: "missing", Completeness: completeness}
	if len(raw) == 0 {
		return c, nil
	}
	if string(raw) == "null" {
		c.Availability = "null"
		return c, nil
	}
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return rt.TextContent(text, stage, completeness, nil), nil
	}
	var blocks []struct {
		Type     string
		Text     *string
		Thinking *string
		JSON     json.RawMessage `json:"json"`
	}
	if err := json.Unmarshal(raw, &blocks); err != nil {
		return c, err
	}
	c.Availability = "provided"
	if len(blocks) == 0 {
		c.Availability = "provided_empty"
	}
	for i, b := range blocks {
		if b.Type == "" {
			return c, fmt.Errorf("missing block type")
		}
		block := rt.ContentBlock{Index: &i, Kind: "unsupported", Descriptor: b.Type, Stage: stage, Completeness: completeness}
		switch b.Type {
		case "text":
			if b.Text == nil {
				return c, fmt.Errorf("missing text")
			}
			block.Kind = "text"
			block.Text = b.Text
			block.Descriptor = ""
		case "json":
			if len(b.JSON) == 0 {
				return c, fmt.Errorf("missing JSON value")
			}
			block.Kind = "json"
			block.JSON = b.JSON
			block.Descriptor = ""
		case "thinking":
			if b.Thinking == nil {
				return c, fmt.Errorf("missing thinking")
			}
			block.Kind = "text"
			block.Text = b.Thinking
			block.Namespace = "reasoning"
			block.Descriptor = ""
		case "redacted_thinking":
			block.Kind = "redacted"
			block.Descriptor = ""
			c.Completeness = "partial"
		case "tool_use", "tool_result":
			// The companion ToolPayload carries call identity. A call ID must never
			// become a content-block ID. The message retains an ordered placeholder.
			c.Completeness = "partial"

		default:
			block.Completeness = "unknown"
			c.Completeness = "partial"
		}
		c.Blocks = append(c.Blocks, block)
	}
	if len(c.Blocks) == 1 && c.Blocks[0].Kind == "redacted" {
		c.Availability = "redacted"
	}
	if len(c.Blocks) == 1 && c.Blocks[0].Kind == "unsupported" {
		c.Availability = "unsupported"
	}
	return c, nil
}
func metadata(raw json.RawMessage) (*rt.MetadataPayload, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return nil, err
	}
	p := &rt.MetadataPayload{}
	present := false
	for _, f := range []struct {
		name   string
		target **[]string
	}{{"capabilities", &p.Capabilities}, {"slash_commands", &p.Commands}, {"tools", &p.Tools}} {
		if v, ok := fields[f.name]; ok {
			if string(v) == "null" {
				return nil, fmt.Errorf("null metadata collection")
			}
			var values []string
			if err := json.Unmarshal(v, &values); err != nil {
				return nil, err
			}
			*f.target = &values
			present = true
		}
	}
	for _, f := range []struct {
		name   string
		target **string
	}{{"model", &p.Model}, {"cwd", &p.WorkingDirectory}, {"title", &p.Title}} {
		if v, ok := fields[f.name]; ok {
			var value string
			if string(v) == "null" {
				return nil, fmt.Errorf("null metadata value")
			}
			if err := json.Unmarshal(v, &value); err != nil {
				return nil, err
			}
			*f.target = &value
			present = true
		}
	}
	if !present {
		return nil, nil
	}
	return p, nil
}

func (m *messageMapper) retainedBytes() int {
	size := len(m.messageID) + len(m.role)
	if m.tool != nil {
		size += len(m.tool.callRef) + len(m.tool.name)
	}
	return size
}
