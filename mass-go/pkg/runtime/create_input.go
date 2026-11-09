package runtime

import (
	"encoding/json"

	"github.com/zoumo/agent-sdk-go/agent"
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
		switch value := block.(type) {
		case agent.TextBlock:
			if value.Annotations != nil {
				return Unsupported()
			}
		case *agent.TextBlock:
			if value == nil || value.Annotations != nil {
				return Unsupported()
			}
		default:
			return Unsupported()
		}
	}
	return nil
}
