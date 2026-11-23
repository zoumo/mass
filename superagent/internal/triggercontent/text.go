package triggercontent

import (
	"time"

	"github.com/zoumo/mass/massd/pkg/events"
)

// FromLegacyText projects the original text and URL for queued and accepted legacy messages.
// Both sides of a retry must produce the same content to preserve the receipt.
func FromLegacyText(text, actor, at, url string) ([]events.ContentBlock, error) {
	var when time.Time
	if at != "" {
		var err error
		when, err = time.Parse(time.RFC3339Nano, at)
		if err != nil {
			return nil, err
		}
	}
	content := []events.ContentBlock{sourceText(text, actor, when, at != "")}
	if url != "" {
		content = append(content, WebLink(url))
	}
	return content, nil
}

// SourceText puts the source facts that the agent needs in one text block.
func SourceText(text, actor string, at time.Time) events.ContentBlock {
	return sourceText(text, actor, at, !at.IsZero())
}

func sourceText(text, actor string, at time.Time, hasTime bool) events.ContentBlock {
	if actor != "" {
		text += "\n\nAuthor: " + actor
	}
	if hasTime {
		text += "\n\nSource time: " + at.UTC().Format(time.RFC3339Nano)
	}
	return events.Text(text)
}

// WebLink keeps the original URI and leaves the MIME type unset.
func WebLink(uri string) events.ContentBlock {
	return events.ContentBlock{ResourceLink: &events.ContentBlockResourceLink{URI: uri, Name: "URL"}}
}
