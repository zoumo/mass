package runtime

import (
	"encoding/json"
)

// ValidateInput checks the shared contract. The SDK adapter checks whether its
// provider can carry the requested blocks.
func ValidateInput(input Input) error {
	if err := input.Validate(); err != nil {
		return InvalidArgument("invalid content block")
	}
	return nil
}

// CopyInput validates and snapshots input with the SDK input codec before
// asynchronous ownership begins.
func CopyInput(input Input) (Input, error) {
	if err := ValidateInput(input); err != nil {
		return Input{}, err
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

// ValidateTextInput applies the text-only admission for initial Session input.
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
