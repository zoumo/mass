package claude

import (
	"encoding/json"
	"errors"
	"strconv"
	"time"

	"github.com/zoumo/mass/mass-go/pkg/events"
)

const maxLiveBlockState = 4096

type liveLane struct {
	responseID string
}

type blockKey struct {
	parent   string
	response string
	index    int
}

type liveBlock struct {
	kind      string
	stopped   bool
	completed bool
}

func (s *session) lane(parent string) *liveLane {
	if s.lanes == nil {
		s.lanes = map[string]*liveLane{}
	}
	lane := s.lanes[parent]
	if lane == nil {
		if len(s.lanes) >= maxLiveBlockState {
			for old := range s.lanes {
				delete(s.lanes, old)
				break
			}
		}
		lane = &liveLane{}
		s.lanes[parent] = lane
	}
	return lane
}

func (s *session) beginResponse(parent, response string) {
	s.lane(parent).responseID = response
}

func (s *session) trackedBlock(key blockKey, kind string) *liveBlock {
	if s.blocks == nil {
		s.blocks = map[blockKey]*liveBlock{}
	}
	block := s.blocks[key]
	if block == nil {
		if len(s.blocks) >= maxLiveBlockState && !s.evictCompletedBlock() {
			return nil
		}
		block = &liveBlock{}
		s.blocks[key] = block
	}
	if block.kind == "" {
		block.kind = kind
	}
	return block
}

func (s *session) evictCompletedBlock() bool {
	for key, block := range s.blocks {
		if block.completed {
			delete(s.blocks, key)
			return true
		}
	}
	return false
}

func (s *session) noteBlockStart(parent string, index int, kind string) {
	response := s.lane(parent).responseID
	if response == "" {
		return
	}
	s.trackedBlock(blockKey{parent: parent, response: response, index: index}, kind)
}

func (s *session) noteBlockStop(parent string, index int) {
	response := s.lane(parent).responseID
	block := s.blocks[blockKey{parent: parent, response: response, index: index}]
	if block != nil && !block.completed {
		block.stopped = true
	}
}

func (s *session) deltaBlock(key blockKey, kind string) (*liveBlock, bool) {
	block := s.trackedBlock(key, kind)
	if block == nil {
		return nil, false
	}
	return block, block.kind == kind
}

func (s *session) uniqueBlock(parent, response, kind string) (int, bool) {
	active := make([]int, 0, 1)
	stopped := make([]int, 0, 1)
	for key, block := range s.blocks {
		if key.parent != parent || key.response != response || block.kind != kind || block.completed {
			continue
		}
		if block.stopped {
			stopped = append(stopped, key.index)
		} else {
			active = append(active, key.index)
		}
	}
	if len(active) == 1 {
		return active[0], true
	}
	if len(active) != 0 || len(stopped) != 1 {
		return 0, false
	}
	return stopped[0], true
}

func (s *session) markAssistantFrame(parent, uuid string) bool {
	if uuid == "" {
		return false
	}
	if s.seenAssistant == nil {
		s.seenAssistant = map[string]struct{}{}
	}
	key := parent + "\x00" + uuid
	if _, seen := s.seenAssistant[key]; seen {
		return true
	}
	if len(s.seenAssistant) >= maxLiveBlockState {
		for old := range s.seenAssistant {
			delete(s.seenAssistant, old)
			break
		}
	}
	s.seenAssistant[key] = struct{}{}
	return false
}

func (key blockKey) itemID() string {
	if key.parent == "" {
		return key.response + ":" + strconv.Itoa(key.index)
	}
	return key.parent + ":" + key.response + ":" + strconv.Itoa(key.index)
}

func (key blockKey) meta() map[string]any {
	var parent any
	if key.parent != "" {
		parent = key.parent
	}
	return map[string]any{
		"responseId":      key.response,
		"blockIndex":      key.index,
		"parentToolUseId": parent,
	}
}

func (d *Driver) emitChunk(s *session, key blockKey, kind, text string, at time.Time) error {
	item := key.itemID()
	meta := key.meta()
	switch kind {
	case "thinking":
		return d.emitWithClaude(s, item, events.ThinkingChunk{MessageID: item, Content: []events.ContentBlock{events.Text(text)}}, at, meta)
	default:
		return d.emitWithClaude(s, item, events.MessageChunk{MessageID: item, Content: []events.ContentBlock{events.Text(text)}}, at, meta)
	}
}

type coordinateState uint8

const (
	coordinateMissing coordinateState = iota
	coordinateValid
	coordinateInvalid
)

func blockCoordinate(value map[string]json.RawMessage) (int, coordinateState) {
	index := -1
	for _, name := range []string{"apiBlockIndex", "index"} {
		raw, present := value[name]
		if !present {
			continue
		}
		var parsed *int
		if err := json.Unmarshal(raw, &parsed); err != nil || parsed == nil || *parsed < 0 || (index >= 0 && *parsed != index) {
			return 0, coordinateInvalid
		}
		index = *parsed
	}
	if index < 0 {
		return 0, coordinateMissing
	}
	return index, coordinateValid
}

func visibleContentKind(value string) bool {
	return value == "thinking" || value == "text"
}

type assistantContent struct {
	kind     string
	text     string
	index    int
	hasIndex bool
}

func (d *Driver) completeAssistant(s *session, parent, response string, blocks []map[string]json.RawMessage, at time.Time) error {
	if response == "" {
		return nil
	}
	content := make([]assistantContent, 0, len(blocks))
	for _, value := range blocks {
		kind := rawString(value, "type")
		if !visibleContentKind(kind) {
			continue
		}
		index, coordinate := blockCoordinate(value)
		if coordinate == coordinateInvalid {
			return nil
		}
		text := ""
		switch kind {
		case "thinking":
			ptr, ok := rawStringPointer(value, "thinking")
			if !ok {
				return errors.New("missing or invalid thinking")
			}
			text = stringValue(ptr)
		case "text":
			ptr, ok := rawStringPointer(value, "text")
			if !ok {
				return errors.New("missing or invalid text")
			}
			text = stringValue(ptr)
		}
		content = append(content, assistantContent{
			kind:     kind,
			text:     text,
			index:    index,
			hasIndex: coordinate == coordinateValid,
		})
	}
	if len(content) == 0 {
		return nil
	}
	multi := len(content) > 1
	if multi {
		seen := map[int]struct{}{}
		for _, block := range content {
			if !block.hasIndex {
				return nil
			}
			if _, duplicate := seen[block.index]; duplicate {
				return nil
			}
			seen[block.index] = struct{}{}
		}
	}
	if !multi && !content[0].hasIndex {
		index, ok := s.uniqueBlock(parent, response, content[0].kind)
		if !ok {
			return nil
		}
		content[0].index = index
		content[0].hasIndex = true
	}
	keys := make([]blockKey, len(content))
	states := make([]liveBlock, len(content))
	for index, block := range content {
		keys[index] = blockKey{parent: parent, response: response, index: block.index}
		state, ok := s.deltaBlock(keys[index], block.kind)
		if !ok {
			return nil
		}
		states[index] = *state
	}
	for index, block := range content {
		state := states[index]
		if state.completed {
			continue
		}
		key := keys[index]
		s.blocks[key].completed = true
		if block.text == "" {
			continue
		}
		item := key.itemID()
		var payload events.Payload
		if block.kind == "thinking" {
			payload = events.AgentThinking{MessageID: item, Content: []events.ContentBlock{events.Text(block.text)}}
		} else {
			payload = events.AgentMessage{MessageID: item, Content: []events.ContentBlock{events.Text(block.text)}}
		}
		if err := d.emitWithClaude(s, item, payload, at, key.meta()); err != nil {
			return err
		}
	}
	return nil
}

func rawString(value map[string]json.RawMessage, name string) string {
	var text string
	_ = json.Unmarshal(value[name], &text)
	return text
}

func rawStringPointer(value map[string]json.RawMessage, name string) (*string, bool) {
	raw, ok := value[name]
	if !ok {
		return nil, false
	}
	var result *string
	if err := json.Unmarshal(raw, &result); err != nil || result == nil {
		return nil, false
	}
	return result, true
}

func stringValue(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}
