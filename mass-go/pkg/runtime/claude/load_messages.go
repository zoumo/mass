package claude

import (
	"encoding/json"
	"errors"
	"maps"
	"time"

	sdk "github.com/zoumo/agent-sdk-go/claude"

	"github.com/zoumo/mass/mass-go/pkg/events"
)

type retainedTool struct {
	name      string
	completed bool
}

func (d *Driver) loadMessage(h *nativeLoad, id string, m sdk.SessionMessage, tools map[string]retainedTool, offset int) error {
	value, err := decode(m.Raw)
	if err != nil {
		return err
	}
	parent := ""
	if m.ParentUUID != nil {
		parent = *m.ParentUUID
	}
	meta := map[string]any{"native": value, "nativeRaw": string(m.Raw), "atomic": false, "timeSource": "import_observation", "pagination": map[string]any{"offset": offset, "limit": 100, "scan": "full_file_per_call"}}
	meta["query"] = claudeReadEvidence(h)
	meta["nativeProjectEvidence"] = "unavailable"
	if raw, ok := value.(map[string]any); ok {
		if _, present := raw["cwd"]; present {
			meta["nativeProjectEvidence"] = "matched"
		}
	}
	if m.Timestamp != nil {
		at, err := time.Parse(time.RFC3339Nano, *m.Timestamp)
		if err != nil || at.IsZero() {
			return errors.New("invalid retained native timestamp")
		}
		meta["nativeTime"], meta["timeSource"] = at.Format(time.RFC3339Nano), "native_record"
	}
	msgValue, err := decode(m.Message)
	if err != nil {
		return err
	}
	msg, ok := msgValue.(map[string]any)
	if !ok || msg["role"] != m.Type {
		return errors.New("invalid retained message role")
	}
	var blocks []any
	switch content := msg["content"].(type) {
	case string:
		blocks = []any{map[string]any{"type": "text", "text": content}}
	case []any:
		blocks = content
		if len(blocks) == 0 {
			blocks = []any{map[string]any{"type": "text", "text": ""}}
		}
	default:
		return errors.New("unsupported retained message content")
	}
	responseID, _ := msg["id"].(string)
	apiBlockIndex, blockCoordinate := retainedBlockIndex(value)
	hasAPIBlockIndex := blockCoordinate == coordinateValid
	for ordinal, v := range blocks {
		b, ok := v.(map[string]any)
		if !ok {
			return errors.New("invalid retained block")
		}
		var payload events.Payload
		item := blockItemID(m.UUID, ordinal)
		eventMeta := meta
		switch b["type"] {
		case "text", "thinking":
			if m.Type == "assistant" && blockCoordinate == coordinateInvalid {
				return errors.New("invalid retained Claude block coordinate")
			}
			field := "text"
			if b["type"] == "thinking" {
				field = "thinking"
			}
			if _, present := b[field]; !present && field == "thinking" {
				if _, signatureOnly := b["signature"].(string); signatureOnly {
					continue
				}
			}
			text, ok := b[field].(string)
			if !ok {
				return errors.New("invalid retained text")
			}
			if text == "" {
				continue
			}
			if m.Type == "assistant" && responseID != "" && hasAPIBlockIndex {
				blockIndex := apiBlockIndex + ordinal
				if blockIndex < apiBlockIndex {
					return errors.New("invalid retained Claude block coordinate")
				}
				item = blockItemID(responseID, blockIndex)
				eventMeta = maps.Clone(meta)
				eventMeta["claude"] = blockKey{response: responseID, index: blockIndex}.meta()
			}
			p := events.MessagePayload{MessageID: item, Content: []events.ContentBlock{events.Text(text)}}
			if field == "thinking" {
				if m.Type != "assistant" {
					return errors.New("invalid thinking role")
				}
				payload = events.AgentThinking(p)
			} else if m.Type == "user" {
				payload = events.UserMessage(p)
			} else {
				payload = events.AgentMessage(p)
			}
		case "redacted_thinking":
			if m.Type != "assistant" {
				return errors.New("invalid redacted thinking role")
			}
			continue
		case "tool_use":
			key, _ := b["id"].(string)
			name, _ := b["name"].(string)
			input, present := b["input"]
			_, exists := tools[key]
			if m.Type != "assistant" || key == "" || name == "" || !present || exists {
				return errors.New("invalid or conflicting retained tool call")
			}
			tools[key] = retainedTool{name: name}
			item = key
			payload = events.ToolCall{ID: key, Name: name, Kind: "other", RawInput: input, Status: "unknown"}
		case "tool_result":
			key, _ := b["tool_use_id"].(string)
			tool, exists := tools[key]
			if m.Type != "user" || key == "" || !exists {
				return errors.New("missing or conflicting retained tool relationship")
			}
			if tool.completed {
				return errors.New("duplicate retained tool result")
			}
			tool.completed = true
			tools[key] = tool
			value, present := b["content"]
			p := events.ToolResult{ID: key, Name: tool.name, Kind: "other", Status: "completed", Output: &events.Output{Availability: "unavailable", Completeness: "unknown"}}
			if bad, exists := b["is_error"]; exists {
				flag, ok := bad.(bool)
				if !ok {
					return errors.New("invalid retained error flag")
				}
				if flag {
					p.Status = "failed"
				}
			}
			if present {
				switch output := value.(type) {
				case nil, string:
				case []any:
					for _, v := range output {
						block, ok := v.(map[string]any)
						if !ok || block["type"] != "text" {
							return errors.New("unsupported retained tool output")
						}
						text, ok := block["text"].(string)
						if !ok {
							return errors.New("invalid retained tool output text")
						}
						p.Content = append(p.Content, events.Text(text))
					}
				default:
					return errors.New("invalid retained tool output")
				}
				p.RawOutput = &value
				if value != nil {
					p.Output.Availability = "provided"
					p.Output.Completeness = "retained"
				}
				if text, ok := value.(string); ok {
					p.Content = []events.ContentBlock{events.Text(text)}
				}
			}
			if truncated, exists := b["truncated"]; exists {
				flag, ok := truncated.(bool)
				if !ok {
					return errors.New("invalid retained truncation flag")
				}
				if flag {
					p.Output.Completeness = "truncated"
					if text, ok := value.(string); ok {
						p.Output.Truncation = &events.Truncation{RetainedBytes: int64(len(text))}
					}
				}
			}
			payload = p
			item = key
		default:
			return errors.New("unsupported retained Claude content")
		}
		if err := d.loadEmit(h, loadEventIdentity{sessionID: id, itemID: item, nativeRecordID: m.UUID, nativeParentID: parent}, eventMeta, payload); err != nil {
			return err
		}
	}
	return nil
}

func retainedBlockIndex(value any) (int, coordinateState) {
	record, ok := value.(map[string]any)
	if !ok {
		return 0, coordinateInvalid
	}
	raw, present := record["apiBlockIndex"]
	if !present {
		return 0, coordinateMissing
	}
	number, ok := raw.(json.Number)
	if !ok {
		return 0, coordinateInvalid
	}
	index, ok := nonNegativeIndex(number)
	if !ok {
		return 0, coordinateInvalid
	}
	return index, coordinateValid
}
