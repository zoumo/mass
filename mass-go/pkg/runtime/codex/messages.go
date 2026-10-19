package codex

import (
	"encoding/json"
	"strings"

	"github.com/zoumo/agent-sdk-go/codex/types"
	rt "github.com/zoumo/mass/mass-go/pkg/runtime"
)

// The SDK leaves some supported notifications raw. Decode those named schemas
// here; no protocol method or raw payload escapes to common consumers.
func messageEvent(native types.Event, id string) (rt.Event, bool) {
	e := rt.Event{SessionID: id, Diagnostic: diagnostic(native.Method, native.Params)}
	known := native.Method == "item/agentMessage/delta" || strings.HasPrefix(native.Method, "item/reasoning/") || native.Method == "turn/plan/updated" || native.Method == "item/plan/delta" || native.Method == "thread/name/updated" || native.Method == "model/rerouted"
	if native.Method != "item/started" && native.Method != "item/completed" && !known {
		return e, false
	}
	var d struct {
		TurnID       string  `json:"turnId"`
		ItemID       string  `json:"itemId"`
		Delta        *string `json:"delta"`
		SummaryIndex *int    `json:"summaryIndex"`
		ContentIndex *int    `json:"contentIndex"`
		Plan         []struct {
			Step   string `json:"step"`
			Status string `json:"status"`
		} `json:"plan"`
		Explanation *string `json:"explanation"`
		Name        *string `json:"name"`
		ToModel     *string `json:"toModel"`
		Item        struct {
			ID, Type string
			Phase    string `json:"phase"`
			Text     json.RawMessage
			Content  json.RawMessage
			Summary  json.RawMessage
		} `json:"item"`
	}
	if json.Unmarshal(native.Params, &d) != nil || native.DecodeError != nil {
		e.Kind = rt.Failure
		e.Error = &rt.SafeError{Category: "decode_error", Scope: "session"}
		return e, true
	}
	e.WorkRef = d.TurnID
	fail := func() (rt.Event, bool) {
		e.Kind = rt.Failure
		e.Error = &rt.SafeError{Category: "decode_error", Scope: "session"}
		return e, true
	}
	if (d.ContentIndex != nil && *d.ContentIndex < 0) || (d.SummaryIndex != nil && *d.SummaryIndex < 0) {
		return fail()
	}
	switch native.Method {
	case "item/agentMessage/delta":
		if d.ItemID == "" || d.Delta == nil {
			return fail()
		}
		e.Kind = rt.MessageCompleted
		e.Message = &rt.MessagePayload{MessageRef: d.ItemID, Role: "assistant", Stage: "delta", Content: rt.TextContent(*d.Delta, "delta", "partial", nil)}
	case "item/reasoning/textDelta", "item/reasoning/summaryTextDelta", "item/reasoning/summaryPartAdded":
		if d.ItemID == "" {
			return fail()
		}
		index := d.ContentIndex
		channel := "content"
		if native.Method != "item/reasoning/textDelta" {
			index = d.SummaryIndex
			channel = "summary"
		}
		c := rt.ContentSet{Availability: "missing", Completeness: "partial"}
		if d.Delta != nil {
			c = rt.TextContent(*d.Delta, "delta", "partial", index)
		} else if native.Method != "item/reasoning/summaryPartAdded" {
			return fail()
		}
		for i := range c.Blocks {
			c.Blocks[i].Namespace = channel
		}
		e.Kind = rt.ReasoningCompleted
		e.Reasoning = &rt.ReasoningPayload{MessageRef: d.ItemID, Stage: "delta", BlockIndex: index, Channel: channel, Content: c}
	case "turn/plan/updated":
		if d.Plan == nil {
			return fail()
		}
		e.Kind = rt.Plan
		e.Plan = &rt.PlanPayload{Explanation: d.Explanation, UpdateMode: "replace", Entries: []rt.PlanEntry{}, Completeness: "complete"}
		for _, p := range d.Plan {
			status := p.Status
			if status == "inProgress" {
				status = "in_progress"
			}
			switch status {
			case "pending", "in_progress", "completed":
			default:
				status = "unknown"
			}
			e.Plan.Entries = append(e.Plan.Entries, rt.PlanEntry{Text: p.Step, Status: status})
		}
	case "item/plan/delta":
		if d.Delta == nil {
			return fail()
		}
		e.Kind = rt.Plan
		e.Plan = &rt.PlanPayload{PlanRef: d.ItemID, UpdateMode: "patch", Entries: []rt.PlanEntry{}, Text: d.Delta, Stage: "delta", Completeness: "partial"}
	case "thread/name/updated":
		if d.Name == nil {
			return fail()
		}
		e.Kind = rt.Metadata
		e.Metadata = &rt.MetadataPayload{Title: d.Name}
	case "model/rerouted":
		if d.ToModel == nil {
			return fail()
		}
		e.Kind = rt.Metadata
		e.Metadata = &rt.MetadataPayload{Model: d.ToModel}
	case "item/started", "item/completed":
		stage := "started"
		completeness := "partial"
		if native.Method == "item/completed" {
			stage = "completed"
			completeness = "complete"
		}
		switch d.Item.Type {
		case "agentMessage", "userMessage":
			if d.Item.ID == "" {
				return fail()
			}
			role := "assistant"
			raw := d.Item.Text
			if d.Item.Type == "userMessage" {
				role = "user"
				raw = d.Item.Content
			}
			content, err := codexContent(raw, stage, completeness)
			if err != nil {
				return fail()
			}
			e.Kind = rt.MessageCompleted
			e.Message = &rt.MessagePayload{MessageRef: d.Item.ID, Role: role, Stage: stage, Content: content, Phase: d.Item.Phase}
		case "reasoning":
			if d.Item.ID == "" {
				return fail()
			}
			c, err := reasoningContent(d.Item.Content, d.Item.Summary, stage, completeness)
			if err != nil {
				return fail()
			}
			e.Kind = rt.ReasoningCompleted
			reasoningStage := "delta"
			if stage == "completed" {
				reasoningStage = stage
			}
			e.Reasoning = &rt.ReasoningPayload{MessageRef: d.Item.ID, Stage: reasoningStage, Content: c}
		case "plan":
			var text string
			if json.Unmarshal(d.Item.Text, &text) != nil {
				return fail()
			}
			e.Kind = rt.Plan
			e.Plan = &rt.PlanPayload{PlanRef: d.Item.ID, UpdateMode: "replace", Entries: []rt.PlanEntry{}, Text: &text, Stage: stage, Completeness: completeness}
		default:
			return e, false
		}
	default:
		return e, false
	}
	return e, true
}
func codexContent(raw json.RawMessage, stage, completeness string) (rt.ContentSet, error) {
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
		Type string
		Text *string
	}
	if err := json.Unmarshal(raw, &blocks); err != nil {
		return c, err
	}
	c.Availability = "provided"
	if len(blocks) == 0 {
		c.Availability = "provided_empty"
	}
	for i, b := range blocks {
		block := rt.ContentBlock{Index: &i, Kind: "unsupported", Descriptor: b.Type, Stage: stage, Completeness: "unknown"}
		if b.Type == "text" && b.Text != nil {
			block.Kind = "text"
			block.Text = b.Text
			block.Descriptor = ""
			block.Completeness = completeness
		} else {
			c.Completeness = "partial"
		}
		c.Blocks = append(c.Blocks, block)
	}
	return c, nil
}
func reasoningContent(content, summary json.RawMessage, stage, completeness string) (rt.ContentSet, error) {
	c := rt.ContentSet{Availability: "missing", Completeness: completeness}
	for _, part := range []struct {
		name string
		raw  json.RawMessage
	}{{"content", content}, {"summary", summary}} {
		if len(part.raw) == 0 {
			continue
		}
		if string(part.raw) == "null" {
			if c.Availability == "missing" {
				c.Availability = "null"
			}
			continue
		}
		var texts []string
		if err := json.Unmarshal(part.raw, &texts); err != nil {
			return c, err
		}
		if c.Availability != "provided" {
			c.Availability = "provided_empty"
		}
		for i, text := range texts {
			c.Availability = "provided"
			c.Blocks = append(c.Blocks, rt.ContentBlock{Namespace: part.name, Index: &i, Kind: "text", Text: &text, Stage: stage, Completeness: completeness})
		}
	}
	return c, nil
}
