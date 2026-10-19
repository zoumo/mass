package runtime

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"time"
)

// PublicEvent is the legacy schemaVersion 2 envelope. The development target is
// harness/modules/runtime/session-events.tsp; this representation is not yet aligned. Event is
// an adapter observation (including control authority), not a public envelope.
type PublicEvent struct {
	SchemaVersion int       `json:"schemaVersion"`
	SessionName   string    `json:"sessionName"`
	SessionID     string    `json:"sessionId"`
	Type          string    `json:"type"`
	Time          time.Time `json:"time"`
	ItemID        string    `json:"itemId,omitempty"`
	Seq           uint64    `json:"seq,omitempty"`
	Payload       any       `json:"payload"`
}

type PublicText struct {
	Type    string `json:"type"`
	Text    string `json:"text"`
	Channel string `json:"channel,omitempty"`
	Index   *int   `json:"index,omitempty"`
}
type PublicMessage struct {
	MessageID string       `json:"messageId"`
	Content   []PublicText `json:"content"`
	Phase     string       `json:"phase,omitempty"`
}
type ExecutionInput struct {
	Command string `json:"command"`
	Cwd     string `json:"cwd"`
}
type PublicOutput struct {
	Availability string `json:"availability"`
	Completeness string `json:"completeness"`
}
type PublicTool struct {
	ID                  string          `json:"id"`
	Name                string          `json:"name,omitempty"`
	Kind                string          `json:"kind,omitempty"`
	Title               string          `json:"title,omitempty"`
	Phase               string          `json:"phase,omitempty"`
	InputRepresentation string          `json:"inputRepresentation,omitempty"`
	RawInput            *ExecutionInput `json:"rawInput,omitempty"`
	Status              string          `json:"status,omitempty"`
	ExitCode            *int32          `json:"exitCode,omitempty"`
	DurationMS          *int64          `json:"durationMs,omitempty"`
	RawOutput           *string         `json:"rawOutput,omitempty"`
	Content             []PublicText    `json:"content,omitempty"`
	Output              *PublicOutput   `json:"output,omitempty"`
	CallContext         any             `json:"callContext,omitempty"`
}

// EventProjector belongs to one named session and has one caller. It never
// accumulates preview content. Only complete facts advance its public cursor.
type EventProjector struct {
	name      string
	cwd       string
	seq       uint64
	state     any
	calls     map[string]PublicTool
	callBytes int
}

func NewEventProjector(sessionName, workingDirectory string) *EventProjector {
	return &EventProjector{name: sessionName, cwd: workingDirectory, calls: map[string]PublicTool{}}
}

func (p *EventProjector) itemID(e Event, kind, native string) string {
	// The source identity is private; public IDs are stable across attachments.
	key := []any{e.RuntimeName, e.SessionID, native}
	if e.Subject != nil && (e.Origin.Attribution == "child" || e.Origin.Attribution == "unresolved") {
		key = append(key, e.Subject)
	}
	b, _ := json.Marshal(key)
	return fmt.Sprintf("%s_%x", kind, sha256.Sum256(b))
}

// Project returns no event for control-only observations or content boundaries.
// Unsupported complete data returns an error rather than a fabricated fact.
func (p *EventProjector) Project(e Event) (*PublicEvent, error) {
	if p.name == "" || e.SessionID == "" {
		return nil, fmt.Errorf("missing public session identity")
	}
	var kind, id string
	var payload any
	durable := false
	switch {
	case e.Message != nil || e.Reasoning != nil:
		var ref, stage, role, phase string
		var c ContentSet
		if m := e.Message; m != nil {
			ref, stage, role, phase, c = m.MessageRef, m.Stage, m.Role, m.Phase, m.Content
			if role != "assistant" && role != "user" {
				return nil, nil
			}
			kind = "agent.message"
			if role == "user" {
				kind = "user.message"
			}
		} else {
			r := e.Reasoning
			ref, stage, c = r.MessageRef, r.Stage, r.Content
			kind = "agent.thinking"
		}
		blocks := []PublicText{}
		for _, b := range c.Blocks {
			if b.Kind != "text" || b.Text == nil || (e.Message != nil && b.Namespace == "reasoning") {
				continue
			}
			blocks = append(blocks, PublicText{Type: "text", Text: *b.Text, Channel: b.Namespace, Index: b.Index})
		}
		if len(blocks) == 0 && c.Availability != "provided_empty" {
			return nil, nil
		}
		idKind := "message"
		if e.Reasoning != nil {
			idKind = "thinking"
		}
		if ref != "" {
			id = p.itemID(e, idKind, ref)
		}
		durable = stage == "completed" && c.Completeness == "complete"
		// Tool placeholders and redacted blocks do not invalidate the independently
		// observed text snapshot in a full Claude message.
		if stage == "completed" && c.Completeness == "partial" {
			durable = true
			for _, b := range c.Blocks {
				if b.Kind == "text" && b.Completeness != "complete" {
					durable = false
				}
				if b.Kind == "unsupported" && b.Descriptor != "tool_use" && b.Descriptor != "tool_result" {
					durable = false
				}
			}
		}
		if durable && id == "" {
			return nil, fmt.Errorf("complete message missing identity")
		}
		if stage == "completed" && !durable {
			return nil, fmt.Errorf("incomplete public message snapshot")
		}
		if !durable {
			if role == "user" {
				return nil, nil
			}
			kind += "_chunk"
		}
		payload = PublicMessage{MessageID: id, Content: blocks, Phase: phase}
	case e.Tool != nil:
		t := e.Tool
		if t.CallRef == "" {
			return nil, fmt.Errorf("tool missing identity")
		}
		id = p.itemID(e, "tool", t.CallRef)
		call, exists := p.calls[id]
		tool := PublicTool{ID: id, Name: "shell", Kind: "execute"}
		if exists {
			input := *call.RawInput
			tool.RawInput, tool.Title, tool.InputRepresentation = &input, call.Title, call.InputRepresentation
		}
		if t.Name != "" && t.Name != "commandExecution" && t.Name != "Bash" {
			return nil, fmt.Errorf("tool has no unified public projection")
		}
		if t.Input.Availability == "provided" && t.Input.Completeness == "complete" && len(t.Input.Blocks) == 1 {
			var input struct {
				Command *string `json:"command"`
				Cwd     *string `json:"cwd"`
			}
			if json.Unmarshal(t.Input.Blocks[0].JSON, &input) != nil || input.Command == nil {
				return nil, fmt.Errorf("invalid execution input")
			}
			// Claude Bash inherits the explicitly configured runtime cwd.
			if input.Cwd == nil && t.Name == "Bash" && p.cwd != "" {
				input.Cwd = &p.cwd
			}
			if input.Cwd == nil {
				return nil, fmt.Errorf("execution input missing cwd")
			}
			tool.RawInput = &ExecutionInput{Command: *input.Command, Cwd: *input.Cwd}
			tool.Title, tool.InputRepresentation = *input.Command, "execution"
		}
		if e.Kind == ToolAnnounced && tool.RawInput != nil {
			tool.Phase = "parameters_ready"
			if exists {
				if *tool.RawInput != *call.RawInput {
					return nil, fmt.Errorf("conflicting tool input")
				}
				return nil, nil
			} else {
				data, _ := json.Marshal(tool)
				if len(p.calls) >= 128 || p.callBytes+len(data) > 1<<20 {
					return nil, ErrBackpressure
				}
				cached := tool
				input := *tool.RawInput
				cached.RawInput = &input
				p.calls[id] = cached
				p.callBytes += len(data)
			}
			kind, payload, durable = "agent.tool_call", tool, true
		} else if e.Kind == ToolCompleted {
			if tool.RawInput == nil {
				return nil, fmt.Errorf("tool result missing execution input")
			}
			kind, durable = "agent.tool_result", true
			tool.Status = "unknown"
			if t.State == "completed" || t.State == "failed" {
				tool.Status = t.State
			}
			if t.Status == "declined" {
				tool.Status = "declined"
			}
			tool.ExitCode, tool.DurationMS = t.ExitCode, t.DurationMS
			tool.Output = &PublicOutput{Availability: "unavailable", Completeness: "unknown"}
			if t.Output.Availability == "provided" || t.Output.Availability == "provided_empty" {
				if t.Output.Completeness == "truncated" {
					return nil, fmt.Errorf("truncated output missing public byte evidence")
				}
				if len(t.Output.Blocks) != 1 || t.Output.Blocks[0].Kind != "text" || t.Output.Blocks[0].Text == nil {
					return nil, fmt.Errorf("unsupported public tool output")
				}
				text := *t.Output.Blocks[0].Text
				tool.RawOutput = &text
				tool.Content = []PublicText{{Type: "text", Text: text}}
				tool.Output = &PublicOutput{Availability: "provided", Completeness: "retained"}
			}
			tool.CallContext = "missing"
			if exists {
				tool.CallContext = call
				data, _ := json.Marshal(call)
				p.callBytes -= len(data)
				delete(p.calls, id)
			}
			payload = tool
		} else {
			kind = "agent.tool_call_chunk"
			tool.Name, tool.Kind = "", ""
			for _, b := range t.Output.Blocks {
				if b.Text != nil {
					tool.Content = append(tool.Content, PublicText{Type: "text", Text: *b.Text})
				}
			}
			if len(tool.Content) == 0 {
				return nil, nil
			}
			payload = tool
		}
	case e.Work != nil:
		if e.Origin.Attribution != "root" {
			return nil, nil
		}
		var state any
		if e.Kind == TurnStarted {
			state = "running"
		}
		if e.Kind == TurnCompleted && (e.Work.Status == "completed" || e.Work.Status == "failed" || e.Work.Status == "cancelled" || e.Work.Status == "interrupted") {
			state = "idle"
		}
		kind, payload = "session.state_update", map[string]any{"previousState": p.state, "state": state}
		p.state = state
	case e.Error != nil:
		kind, payload = "session.error", map[string]string{"reason": e.Error.Category, "message": "runtime operation failed"}
	default:
		return nil, nil
	}
	when := e.ObservedAt
	if when.IsZero() {
		when = time.Now().UTC()
	}
	result := &PublicEvent{SchemaVersion: 2, SessionName: p.name, SessionID: e.SessionID, Type: kind, Time: when, ItemID: id, Payload: payload}
	if durable {
		if p.seq == 9007199254740991 {
			return nil, ErrIncomplete
		}
		p.seq++
		result.Seq = p.seq
	}
	return result, nil
}
