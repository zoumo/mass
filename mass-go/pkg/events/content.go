package events

import "github.com/zoumo/agent-sdk-go/agent"

// ContentBlock and its concrete branches are the SDK's public content union.
// MASS does not maintain a second parser or validator.
type (
	ContentBlock             = agent.ContentBlock
	ContentBlockText         = agent.ContentBlockText
	ContentBlockImage        = agent.ContentBlockImage
	ContentBlockAudio        = agent.ContentBlockAudio
	EmbeddedResource         = agent.EmbeddedResource
	ContentBlockResource     = agent.ContentBlockResource
	ContentBlockResourceLink = agent.ContentBlockResourceLink
	Annotations              = agent.Annotations
)

func Text(text string) ContentBlock {
	return agent.ContentBlock{Text: &agent.ContentBlockText{Text: text}}
}

// TextValue returns the text for the SDK text block branch.
func TextValue(block ContentBlock) (string, bool) {
	if block.Text != nil {
		return block.Text.Text, true
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
