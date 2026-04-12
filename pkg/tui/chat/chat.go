package chat

import (
	"github.com/open-agent-d/open-agent-d/third_party/charmbracelet/crush/ui/list"
)

// Chat is a scrollable chat view built on top of [list.List]. It manages
// message items by ID and implements follow mode (auto-scroll to bottom when
// new messages arrive).
//
// The design mirrors charmbracelet/crush's Chat wrapper (model/chat.go),
// simplified for our use case.
type Chat struct {
	list     *list.List
	idMap    map[string]int // message ID → list index
	follow   bool
	maxWidth int
}

// New creates a new Chat with default settings.
func New() *Chat {
	c := &Chat{
		list:     list.NewList(),
		idMap:    make(map[string]int),
		follow:   true,
		maxWidth: maxTextWidth,
	}
	c.list.SetGap(0)
	return c
}

// SetSize sets the chat viewport dimensions.
func (c *Chat) SetSize(width, height int) {
	c.list.SetSize(width, height)
	if c.AtBottom() {
		c.ScrollToBottom()
	}
}

// Width returns the viewport width.
func (c *Chat) Width() int {
	return c.list.Width()
}

// Height returns the viewport height.
func (c *Chat) Height() int {
	return c.list.Height()
}

// ContentWidth returns the effective content width (capped to maxWidth).
func (c *Chat) ContentWidth() int {
	w := c.list.Width()
	if c.maxWidth > 0 && w > c.maxWidth {
		return c.maxWidth
	}
	return w
}

// SetMaxWidth sets the maximum content width for readability.
func (c *Chat) SetMaxWidth(w int) {
	c.maxWidth = w
}

// Len returns the number of items.
func (c *Chat) Len() int {
	return c.list.Len()
}

// ── Item management ──────────────────────────────────────────────────────────

// Append adds message items to the end of the chat. In follow mode, the view
// auto-scrolls to the bottom.
func (c *Chat) Append(items ...MessageItem) {
	offset := c.list.Len()
	listItems := make([]list.Item, len(items))
	for i, item := range items {
		c.idMap[item.ID()] = offset + i
		listItems[i] = item
	}
	c.list.AppendItems(listItems...)
	if c.follow {
		c.ScrollToBottom()
	}
}

// Item returns the message item with the given ID, or nil if not found.
func (c *Chat) Item(id string) MessageItem {
	idx, ok := c.idMap[id]
	if !ok {
		return nil
	}
	item, ok := c.list.ItemAt(idx).(MessageItem)
	if !ok {
		return nil
	}
	return item
}

// Update looks up the item by id and calls fn to modify it in place.
// The caller is responsible for invalidating the item's render cache.
func (c *Chat) Update(id string, fn func(MessageItem)) {
	item := c.Item(id)
	if item == nil {
		return
	}
	fn(item)
	if c.follow {
		c.ScrollToBottom()
	}
}

// Remove removes a message by ID and rebuilds the index map.
func (c *Chat) Remove(id string) {
	idx, ok := c.idMap[id]
	if !ok {
		return
	}
	c.list.RemoveItem(idx)
	delete(c.idMap, id)
	// Rebuild indices for items after the removed one.
	for i := idx; i < c.list.Len(); i++ {
		if item, ok := c.list.ItemAt(i).(MessageItem); ok {
			c.idMap[item.ID()] = i
		}
	}
}

// Clear removes all messages.
func (c *Chat) Clear() {
	c.idMap = make(map[string]int)
	c.list.SetItems()
}

// ── Scrolling ────────────────────────────────────────────────────────────────

// AtBottom returns whether the chat is scrolled to the bottom.
func (c *Chat) AtBottom() bool {
	return c.list.AtBottom()
}

// Follow returns whether follow mode is active.
func (c *Chat) Follow() bool {
	return c.follow
}

// ScrollToBottom scrolls to the bottom and enables follow mode.
func (c *Chat) ScrollToBottom() {
	c.list.ScrollToBottom()
	c.follow = true
}

// ScrollToTop scrolls to the top and disables follow mode.
func (c *Chat) ScrollToTop() {
	c.list.ScrollToTop()
	c.follow = false
}

// ScrollBy scrolls by the given number of lines. Scrolling up disables follow
// mode; scrolling down to the bottom re-enables it.
func (c *Chat) ScrollBy(lines int) {
	c.list.ScrollBy(lines)
	c.follow = lines > 0 && c.AtBottom()
}

// ── Rendering ────────────────────────────────────────────────────────────────

// Render returns the visible portion of the chat as a string.
func (c *Chat) Render() string {
	return c.list.Render()
}
