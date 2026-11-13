package claude

import (
	"bytes"
	"encoding/json"
	"errors"
	"strconv"
)

type blockKey struct {
	parent   string
	response string
	index    int
}

func blockItemID(response string, index int) string {
	return response + ":" + strconv.Itoa(index)
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

type coordinateState uint8

const (
	coordinateMissing coordinateState = iota
	coordinateValid
	coordinateInvalid
)

func nonNegativeIndex(number json.Number) (int, bool) {
	index, err := strconv.Atoi(number.String())
	if err != nil || index < 0 {
		return 0, false
	}
	return index, true
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

func decode(raw json.RawMessage) (any, error) {
	var value any
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	err := dec.Decode(&value)
	return value, err
}
