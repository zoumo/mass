package runtime

import (
	"encoding/json"
)

// CopyInput validates and snapshots input with the SDK input codec before
// asynchronous ownership begins.
func CopyInput(input Input) (Input, error) {
	if err := input.Validate(); err != nil {
		return Input{}, InvalidArgument("invalid content block")
	}
	raw, err := json.Marshal(input)
	if err != nil {
		return Input{}, InvalidArgument("invalid content block")
	}
	var copied Input
	if err := json.Unmarshal(raw, &copied); err != nil {
		return Input{}, InvalidArgument("invalid content block")
	}
	return copied, nil
}

// ValidateTextInput rejects non-text blocks and text annotations, rather than
// silently removing fields neither native provider can represent.
func ValidateTextInput(input Input) error {
	if _, err := CopyInput(input); err != nil {
		return err
	}
	for _, block := range input.Content {
		if block.Text == nil || block.Text.Annotations != nil {
			return Unsupported()
		}
	}
	return nil
}
