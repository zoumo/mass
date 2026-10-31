package codex

import (
	"bytes"
	"encoding/json"
	"errors"

	ct "github.com/zoumo/agent-sdk-go/codex/types"

	"github.com/zoumo/mass/mass-go/pkg/events"
)

func (d *Driver) loadItem(h *nativeLoad, id string, entry ct.ThreadItemEntry) error {
	b, err := json.Marshal(entry)
	if err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(b))
	decoder.UseNumber()
	var raw map[string]any
	if err := decoder.Decode(&raw); err != nil {
		return err
	}
	item, ok := raw["item"].(map[string]any)
	if !ok {
		return errors.New("missing retained item")
	}
	itemID, _ := item["id"].(string)
	kind, _ := item["type"].(string)
	if itemID == "" {
		return errors.New("missing retained item identity")
	}
	meta := map[string]any{"native": raw, "timeSource": "import_observation", "atomic": false, "scope": "all_retained_main_history"}
	emit := func(p events.Payload) error { return d.loadEmit(h, id, itemID, entry.TurnID, meta, p) }
	switch kind {
	case "userMessage":
		content, ok := item["content"].([]any)
		if !ok {
			return errors.New("missing user content")
		}
		blocks := []events.ContentBlock{}
		for _, v := range content {
			m, ok := v.(map[string]any)
			if !ok || m["type"] != "text" {
				return errors.New("unsupported retained user content")
			}
			text, ok := m["text"].(string)
			if !ok {
				return errors.New("invalid retained text")
			}
			blocks = append(blocks, events.Text(text))
		}
		return emit(events.UserMessage{MessageID: itemID, Content: blocks})
	case "agentMessage":
		text, ok := item["text"].(string)
		if !ok {
			return errors.New("invalid retained agent text")
		}
		phase, _ := item["phase"].(string)
		return emit(events.AgentMessage{MessageID: itemID, Content: []events.ContentBlock{events.Text(text)}, Phase: phase})
	case "reasoning":
		blocks := []events.ContentBlock{}
		// Content and summary are different representations; raw retains both.
		field := "content"
		v, present := item[field]
		if !present {
			field = "summary"
			v = item[field]
		}
		if v != nil {
			values, ok := v.([]any)
			if !ok {
				return errors.New("invalid retained reasoning")
			}
			for _, v := range values {
				text, ok := v.(string)
				if !ok {
					return errors.New("invalid retained reasoning part")
				}
				blocks = append(blocks, events.Text(text))
			}
		}
		meta["reasoningRepresentation"] = field
		return emit(events.AgentThinking{MessageID: itemID, Content: blocks})
	case "commandExecution", "mcpToolCall", "dynamicToolCall":
		name, _ := item["tool"].(string)
		input := item["arguments"]
		outputKey := "result"
		toolKind := "other"
		if kind == "commandExecution" {
			name = "shell"
			toolKind = "execute"
			input = map[string]any{"command": item["command"], "cwd": item["cwd"]}
			outputKey = "aggregatedOutput"
		}
		if kind == "dynamicToolCall" {
			outputKey = "contentItems"
		}
		status := "unknown"
		switch item["status"] {
		case "completed":
			status = "completed"
		case "failed":
			status = "failed"
		case "declined":
			status = "declined"
		case "inProgress":
			status = "in_progress"
		}
		value, present := item[outputKey]
		if kind == "mcpToolCall" {
			if v, ok := item["error"]; ok && v != nil {
				value = v
				present = true
				status = "failed"
			}
		}
		var output *any
		availability := "unavailable"
		if present {
			output = &value
			if value != nil {
				availability = "provided"
			}
		}
		// Retained native output is not a guarantee that the original tool emitted
		// no other bytes. Preserve explicit truncation evidence in native raw.
		completeness := "unknown"
		if item["truncated"] == true {
			completeness = "truncated"
		}
		coverage := &events.Output{Availability: availability, Completeness: completeness}
		var exit *int32
		if n, ok := item["exitCode"].(json.Number); ok {
			v, e := n.Int64()
			if e != nil {
				return e
			}
			x := int32(v)
			exit = &x
		}
		var duration *float64
		if n, ok := item["durationMs"].(json.Number); ok {
			v, e := n.Float64()
			if e != nil {
				return e
			}
			duration = &v
		}
		call := events.ToolCall{ID: itemID, Name: name, Kind: toolKind, RawInput: input, RawOutput: output, Status: status, ExitCode: exit, DurationMs: duration, Output: coverage}
		if err := emit(call); err != nil {
			return err
		}
		if !present || value == nil {
			return nil
		}
		result := events.ToolResult{ID: itemID, Name: name, Kind: toolKind, Status: status, RawOutput: output, ExitCode: exit, DurationMs: duration, Output: coverage}
		if text, ok := value.(string); ok {
			result.Content = []events.ContentBlock{events.Text(text)}
		}
		return emit(result)

	// Every other native branch is rejected until it has an explicit lossless
	// persistent mapping. A valid unknown content item is not empty history.
	case "functionCallOutput", "fileChange", "collabAgentToolCall", "subAgentActivity", "webSearch", "imageView", "sleep", "imageGeneration", "hookPrompt", "plan", "enteredReviewMode", "exitedReviewMode", "contextCompaction":
		return errors.New("unsupported retained native content")
	default:
		return errors.New("unknown retained native content")
	}
}
