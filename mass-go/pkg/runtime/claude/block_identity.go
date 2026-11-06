package claude

import (
	"encoding/json"
	"errors"
	"strconv"
	"time"

	"github.com/zoumo/mass/mass-go/pkg/events"
	"github.com/zoumo/mass/mass-go/pkg/runtime"
)

// Identity and completion tombstones live for the session. Saturation terminates
// observation instead of evicting state that could later re-open an item.
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
	toolUseID string
}

func checkLiveBlockCapacity(size int, reason string) error {
	if size >= maxLiveBlockState {
		return runtime.Exhausted(reason)
	}
	return nil
}

func (s *session) lane(parent string) (*liveLane, error) {
	if s.lanes == nil {
		s.lanes = map[string]*liveLane{}
	}
	lane := s.lanes[parent]
	if lane == nil {
		if err := checkLiveBlockCapacity(len(s.lanes), "live_block_lane_capacity"); err != nil {
			return nil, err
		}
		lane = &liveLane{}
		s.lanes[parent] = lane
	}
	return lane, nil
}

func (s *session) response(parent string) string {
	lane := s.lanes[parent]
	if lane == nil {
		return ""
	}
	return lane.responseID
}

func (s *session) beginResponse(parent, response string) error {
	lane, err := s.lane(parent)
	if err != nil {
		return err
	}
	lane.responseID = response
	return nil
}

func (s *session) trackedBlock(key blockKey, kind string) (*liveBlock, error) {
	if s.blocks == nil {
		s.blocks = map[blockKey]*liveBlock{}
	}
	block := s.blocks[key]
	if block == nil {
		if err := checkLiveBlockCapacity(len(s.blocks), "live_block_capacity"); err != nil {
			return nil, err
		}
		block = &liveBlock{}
		s.blocks[key] = block
	}
	if block.kind == "" {
		block.kind = kind
	}
	return block, nil
}

func (s *session) noteBlockStart(parent string, index int, kind, toolUseID string) error {
	response := s.response(parent)
	if response == "" {
		return nil
	}
	block, err := s.trackedBlock(blockKey{parent: parent, response: response, index: index}, kind)
	if err != nil {
		return err
	}
	if kind == "tool_use" && toolUseID != "" {
		if block.toolUseID != "" && block.toolUseID != toolUseID {
			return errors.New("conflicting Claude tool block identity")
		}
		block.toolUseID = toolUseID
	}
	return nil
}

func (s *session) noteBlockStop(parent string, index int) {
	response := s.response(parent)
	block := s.blocks[blockKey{parent: parent, response: response, index: index}]
	if block != nil && !block.completed {
		block.stopped = true
	}
}

func (s *session) deltaBlock(key blockKey, kind string) (*liveBlock, bool, error) {
	block, err := s.trackedBlock(key, kind)
	if err != nil {
		return nil, false, err
	}
	return block, block.kind == kind, nil
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

func (s *session) markAssistantFrame(parent, uuid string) (bool, error) {
	if uuid == "" {
		return false, nil
	}
	if s.seenAssistant == nil {
		s.seenAssistant = map[string]struct{}{}
	}
	key := parent + "\x00" + uuid
	if _, seen := s.seenAssistant[key]; seen {
		return true, nil
	}
	if err := checkLiveBlockCapacity(len(s.seenAssistant), "live_assistant_frame_capacity"); err != nil {
		return false, err
	}
	s.seenAssistant[key] = struct{}{}
	return false, nil
}

func blockItemID(response string, index int) string {
	return response + ":" + strconv.Itoa(index)
}

func (key blockKey) itemID() string {
	if key.parent == "" {
		return blockItemID(key.response, key.index)
	}
	return key.parent + ":" + blockItemID(key.response, key.index)
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
		return d.emitWithClaudeMeta(s, item, events.ThinkingChunk{MessageID: item, Content: []events.ContentBlock{events.Text(text)}}, at, meta)
	default:
		return d.emitWithClaudeMeta(s, item, events.MessageChunk{MessageID: item, Content: []events.ContentBlock{events.Text(text)}}, at, meta)
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
		var parsed *json.Number
		if err := json.Unmarshal(raw, &parsed); err != nil || parsed == nil {
			return 0, coordinateInvalid
		}
		parsedIndex, ok := nonNegativeIndex(*parsed)
		if !ok || (index >= 0 && parsedIndex != index) {
			return 0, coordinateInvalid
		}
		index = parsedIndex
	}
	if index < 0 {
		return 0, coordinateMissing
	}
	return index, coordinateValid
}

func nonNegativeIndex(number json.Number) (int, bool) {
	index, err := strconv.Atoi(number.String())
	if err != nil || index < 0 {
		return 0, false
	}
	return index, true
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
			var err error
			text, _, err = thinkingBlockText(value)
			if err != nil {
				return err
			}
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
		state, ok, err := s.deltaBlock(keys[index], block.kind)
		if err != nil {
			return err
		}
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
		if err := d.emitWithClaudeMeta(s, item, payload, at, key.meta()); err != nil {
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

func thinkingBlockText(value map[string]json.RawMessage) (string, bool, error) {
	var thinking any
	present := false
	if raw, exists := value["thinking"]; exists {
		if err := json.Unmarshal(raw, &thinking); err != nil {
			return "", false, errors.New("invalid thinking")
		}
		present = true
	}
	var signature any
	if raw, exists := value["signature"]; exists {
		if err := json.Unmarshal(raw, &signature); err != nil {
			return "", false, errors.New("invalid thinking")
		}
	}
	text, signatureOnly, err := thinkingText(thinking, present, signature)
	return text, signatureOnly, err
}

func thinkingText(value any, present bool, signature any) (string, bool, error) {
	if present && value != nil {
		text, ok := value.(string)
		if !ok {
			return "", false, errors.New("invalid thinking")
		}
		return text, false, nil
	}
	if _, signatureOnly := signature.(string); signatureOnly {
		return "", true, nil
	}
	return "", false, errors.New("missing or invalid thinking")
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
