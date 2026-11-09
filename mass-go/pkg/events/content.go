package events

import "github.com/zoumo/agent-sdk-go/agent"

// ContentBlock and its concrete branches are the SDK's public content union.
// MASS does not maintain a second parser or validator.
type (
	ContentBlock      = agent.ContentBlock
	TextBlock         = agent.TextBlock
	ImageBlock        = agent.ImageBlock
	AudioBlock        = agent.AudioBlock
	EmbeddedResource  = agent.EmbeddedResource
	ResourceBlock     = agent.ResourceBlock
	ResourceLinkBlock = agent.ResourceLinkBlock
	Annotations       = agent.Annotations
)

func Text(text string) ContentBlock {
	return agent.TextBlock{Text: text}
}

// TextValue returns the text for the SDK text block branch.
func TextValue(block ContentBlock) (string, bool) {
	switch value := block.(type) {
	case agent.TextBlock:
		return value.Text, true
	case *agent.TextBlock:
		if value != nil {
			return value.Text, true
		}
	}
	return "", false
}

// TextOf returns text for a text block and an empty string for other kinds.
func TextOf(block ContentBlock) string {
	text, _ := TextValue(block)
	return text
}

// ValidateContentBlock delegates annotations and media validation to the SDK
// content union.
func ValidateContentBlock(block ContentBlock) error {
	return agent.Input{Content: []agent.ContentBlock{block}}.Validate()
}
