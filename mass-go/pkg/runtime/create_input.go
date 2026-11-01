package runtime

import (
	"encoding/json"

	"github.com/zoumo/mass/mass-go/pkg/events"
)

// CopyInput validates and snapshots input before asynchronous ownership begins.
func CopyInput(input Input) (Input, error) {
	if len(input.Content) == 0 {
		return Input{}, InvalidArgument("empty input")
	}
	content := make([]events.ContentBlock, len(input.Content))
	for i, block := range input.Content {
		if err := block.Validate(); err != nil {
			return Input{}, InvalidArgument("invalid content block")
		}
		raw, err := json.Marshal(block)
		if err != nil {
			return Input{}, InvalidArgument("invalid content block")
		}
		if err = json.Unmarshal(raw, &content[i]); err != nil {
			return Input{}, InvalidArgument("invalid content block")
		}
	}
	return Input{Content: content}, nil
}

// ValidateTextInput rejects unsupported kinds and annotations, rather than
// silently removing fields that neither native input mapping can represent.
func ValidateTextInput(input Input) error {
	if _, err := CopyInput(input); err != nil {
		return err
	}
	for _, block := range input.Content {
		if block.Type != "text" {
			return Unsupported()
		}
		raw, err := json.Marshal(block)
		if err != nil {
			return InvalidArgument("invalid content block")
		}
		var fields map[string]json.RawMessage
		if json.Unmarshal(raw, &fields) != nil || len(fields) != 2 {
			return Unsupported()
		}
	}
	return nil
}
