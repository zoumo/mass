// Package chat provides a chat UI component built on top of the list package
// ported from charmbracelet/crush. It manages a scrollable list of message
// items with follow mode (auto-scroll to bottom on new messages).
package chat

import (
	"github.com/open-agent-d/open-agent-d/third_party/charmbracelet/crush/ui/list"
)

// MessageItem extends [list.Item] with a unique identifier for lookup and
// update operations.
type MessageItem interface {
	list.Item
	// ID returns the unique identifier for this message item.
	ID() string
}

// maxTextWidth is the maximum width text messages can be for readability.
// Matches crush's cappedMessageWidth constant.
const maxTextWidth = 120

// cappedWidth returns the content width capped to maxTextWidth.
func cappedWidth(available int) int {
	if available > maxTextWidth {
		return maxTextWidth
	}
	return available
}

// cachedItem provides width-sensitive render caching. When the width changes,
// the cache is invalidated and the item is re-rendered.
//
// This mirrors crush's cachedMessageItem pattern.
type cachedItem struct {
	rendered string
	width    int
	height   int
}

// get returns the cached render if width matches.
func (c *cachedItem) get(width int) (string, int, bool) {
	if c.width == width && c.rendered != "" {
		return c.rendered, c.height, true
	}
	return "", 0, false
}

// set stores a rendered result for the given width.
func (c *cachedItem) set(rendered string, width, height int) {
	c.rendered = rendered
	c.width = width
	c.height = height
}

// clear invalidates the cache.
func (c *cachedItem) clear() {
	c.rendered = ""
	c.width = 0
	c.height = 0
}
