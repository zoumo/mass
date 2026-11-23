package triggercontent

import (
	"time"

	"github.com/zoumo/mass/massd/pkg/events"
)

// FromLegacyText is the text projection for queued and accepted legacy messages.
// Both sides of a retry must produce the same block to preserve the receipt.
func FromLegacyText(text, actor, at, url string) ([]events.ContentBlock, error) {
	var when time.Time
	if at != "" {
		var err error
		when, err = time.Parse(time.RFC3339Nano, at)
		if err != nil {
			return nil, err
		}
	}
	return sourceText(text, actor, url, when, at != ""), nil
}

// SourceText puts the source facts that the agent needs in one text block.
func SourceText(text, actor, url string, at time.Time) []events.ContentBlock {
	return sourceText(text, actor, url, at, !at.IsZero())
}

func sourceText(text, actor, url string, at time.Time, hasTime bool) []events.ContentBlock {
	if actor != "" {
		text += "\n\nAuthor: " + actor
	}
	if hasTime {
		text += "\n\nSource time: " + at.UTC().Format(time.RFC3339Nano)
	}
	if url != "" {
		text += "\n\nURL: " + url
	}
	return []events.ContentBlock{events.Text(text)}
}
