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
	if err = decoder.Decode(&raw); err != nil {
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
	// Every other native branch is rejected until it has an explicit lossless
	// persistent mapping. A valid unknown content item is not empty history.
	case "reasoning", "commandExecution", "mcpToolCall", "dynamicToolCall", "functionCallOutput", "fileChange", "collabAgentToolCall", "subAgentActivity", "webSearch", "imageView", "sleep", "imageGeneration", "hookPrompt", "plan", "enteredReviewMode", "exitedReviewMode", "contextCompaction":
		return errors.New("unsupported retained native content")
	default:
		return errors.New("unknown retained native content")
	}
}
