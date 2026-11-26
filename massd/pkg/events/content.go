package events

import "github.com/zoumo/agent-sdk-go/agent"

// ContentBlock and its concrete branches are the SDK's public content union.
// MASS does not maintain a second parser or validator.
type (
	ContentBlock                 = agent.ContentBlock
	ContentBlockText             = agent.ContentBlockText
	ContentBlockImage            = agent.ContentBlockImage
	ContentBlockAudio            = agent.ContentBlockAudio
	EmbeddedResource             = agent.EmbeddedResource
	ContentBlockEmbeddedResource = agent.ContentBlockEmbeddedResource
	ContentBlockResourceLink     = agent.ContentBlockResourceLink
	ResourceIcon                 = agent.ResourceIcon
	Annotations                  = agent.Annotations
)

func Text(text string) ContentBlock {
	return agent.ContentBlock{Text: &agent.ContentBlockText{Text: text}}
}

// ValidateContentBlock delegates annotations and media validation to the SDK
// content union.
func ValidateContentBlock(block ContentBlock) error {
	return agent.Input{Content: []agent.ContentBlock{block}}.Validate()
}
