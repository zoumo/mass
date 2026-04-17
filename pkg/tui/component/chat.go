package component

import (
	tea "charm.land/bubbletea/v2"
	lru "github.com/hashicorp/golang-lru/v2"

	"github.com/zoumo/mass/third_party/charmbracelet/crush/ui/anim"
	"github.com/zoumo/mass/third_party/charmbracelet/crush/ui/list"
)

// renderCacheCapacity is the maximum number of items whose render cache is
// kept in memory. When the LRU evicts an item, its per-item render cache is
// cleared so memory does not grow unbounded in long sessions.
const renderCacheCapacity = 100

// Chat represents the chat UI model that handles chat interactions and
// messages. It mirrors crush's model/chat.go but without mouse selection.
type Chat struct {
	list     *list.List
	idInxMap map[string]int // Map of message IDs to their indices in the list

	// Animation visibility optimization: track animations paused due to items
	// being scrolled out of view. When items become visible again, their
	// animations are restarted.
	pausedAnimations map[string]struct{}

	// renderLRU tracks recently rendered (visible) item IDs. When an item is
	// evicted from the LRU (capacity exceeded), its per-item render cache is
	// cleared to bound memory in long sessions.
	renderLRU *lru.Cache[string, struct{}]

	// follow is a flag to indicate whether the view should auto-scroll to
	// bottom on new messages.
	follow bool
}

// NewChat creates a new instance of [Chat].
func NewChat() *Chat {
	c := &Chat{
		idInxMap:         make(map[string]int),
		pausedAnimations: make(map[string]struct{}),
		follow:           true,
	}
	c.renderLRU, _ = lru.NewWithEvict(renderCacheCapacity, func(key string, _ struct{}) {
		// When an item is evicted from the LRU, clear its render cache to
		// free the ANSI string memory. The cache will be rebuilt on-demand if
		// the user scrolls back to this item.
		if idx, ok := c.idInxMap[key]; ok {
			if cc, ok := c.list.ItemAt(idx).(cacheClearable); ok {
				cc.clearCache()
			}
		}
	})
	l := list.NewList()
	l.SetGap(1)
	l.RegisterRenderCallback(list.FocusedRenderCallback(l))
	c.list = l
	return c
}

// cacheClearable is implemented by items that have a render cache that can be
// cleared (e.g. cachedMessageItem). Used by the LRU eviction callback.
type cacheClearable interface {
	clearCache()
}

// touchVisibleCaches marks all currently visible items as recently used in the
// render LRU. This should be called after any scroll or append operation so
// that off-screen items eventually get evicted and their render caches freed.
func (m *Chat) touchVisibleCaches() {
	if m.renderLRU == nil || m.list.Len() == 0 {
		return
	}
	startIdx, endIdx := m.list.VisibleItemIndices()
	for i := startIdx; i <= endIdx; i++ {
		if item, ok := m.list.ItemAt(i).(Identifiable); ok {
			m.renderLRU.Add(item.ID(), struct{}{})
		}
	}
}

// Height returns the height of the chat view port.
func (m *Chat) Height() int {
	return m.list.Height()
}

// Render returns the visible portion of the chat as a string.
func (m *Chat) Render() string {
	return m.list.Render()
}

// SetSize sets the size of the chat view port.
func (m *Chat) SetSize(width, height int) {
	m.list.SetSize(width, height)
	// Anchor to bottom if we were already at the bottom.
	// Skip when the list is empty: AtBottom() returns true for an empty list,
	// but calling ScrollToBottom() would set follow=true and auto-scroll
	// future AppendMessages calls.
	if m.list.Len() > 0 && m.AtBottom() {
		m.ScrollToBottom()
	}
	m.touchVisibleCaches()
}

// Len returns the number of items in the chat list.
func (m *Chat) Len() int {
	return m.list.Len()
}

// SetMessages sets the chat messages to the provided list of message items.
func (m *Chat) SetMessages(msgs ...MessageItem) {
	m.idInxMap = make(map[string]int)
	m.pausedAnimations = make(map[string]struct{})

	items := make([]list.Item, len(msgs))
	for i, msg := range msgs {
		m.idInxMap[msg.ID()] = i
		items[i] = msg
	}
	m.list.SetItems(items...)
	m.ScrollToBottom()
}

// AppendMessages appends new message items to the chat list.
// When follow mode is active, the view auto-scrolls to show new items.
func (m *Chat) AppendMessages(msgs ...MessageItem) {
	items := make([]list.Item, len(msgs))
	indexOffset := m.list.Len()
	for i, msg := range msgs {
		m.idInxMap[msg.ID()] = indexOffset + i
		items[i] = msg
	}
	m.list.AppendItems(items...)
	if m.follow {
		m.ScrollToBottom()
	}
	m.touchVisibleCaches()
}

// Animate animates items in the chat list. Only propagates animation messages
// to visible items to save CPU. When items are not visible, their animation ID
// is tracked so it can be restarted when they become visible again.
func (m *Chat) Animate(msg anim.StepMsg) tea.Cmd {
	idx, ok := m.idInxMap[msg.ID]
	if !ok {
		return nil
	}

	item := m.list.ItemAt(idx)
	if item == nil {
		return nil
	}

	type animatable interface {
		Animate(msg anim.StepMsg) tea.Cmd
	}
	a, ok := item.(animatable)
	if !ok {
		return nil
	}

	// Check if item is currently visible.
	startIdx, endIdx := m.list.VisibleItemIndices()
	isVisible := idx >= startIdx && idx <= endIdx

	if !isVisible {
		// Item not visible - pause animation by not propagating.
		m.pausedAnimations[msg.ID] = struct{}{}
		return nil
	}

	// Item is visible - remove from paused set and animate.
	delete(m.pausedAnimations, msg.ID)
	return a.Animate(msg)
}

// RestartPausedVisibleAnimations restarts animations for items that were paused
// due to being scrolled out of view but are now visible again.
func (m *Chat) RestartPausedVisibleAnimations() tea.Cmd {
	if len(m.pausedAnimations) == 0 {
		return nil
	}

	startIdx, endIdx := m.list.VisibleItemIndices()
	var cmds []tea.Cmd

	type startable interface {
		StartAnimation() tea.Cmd
	}

	for id := range m.pausedAnimations {
		idx, ok := m.idInxMap[id]
		if !ok {
			// Item no longer exists.
			delete(m.pausedAnimations, id)
			continue
		}

		if idx >= startIdx && idx <= endIdx {
			// Item is now visible - restart its animation.
			if s, ok := m.list.ItemAt(idx).(startable); ok {
				if cmd := s.StartAnimation(); cmd != nil {
					cmds = append(cmds, cmd)
				}
			}
			delete(m.pausedAnimations, id)
		}
	}

	if len(cmds) == 0 {
		return nil
	}
	return tea.Batch(cmds...)
}

// Focus sets the focus state of the chat component.
func (m *Chat) Focus() {
	m.list.Focus()
}

// Blur removes the focus state from the chat component.
func (m *Chat) Blur() {
	m.list.Blur()
}

// AtBottom returns whether the chat list is currently scrolled to the bottom.
func (m *Chat) AtBottom() bool {
	return m.list.AtBottom()
}

// Follow returns whether the chat view is in follow mode (auto-scroll to
// bottom on new messages).
func (m *Chat) Follow() bool {
	return m.follow
}

// ScrollToBottom scrolls the chat view to the bottom.
func (m *Chat) ScrollToBottom() {
	m.list.ScrollToBottom()
	m.follow = true
	m.touchVisibleCaches()
}

// ScrollToTop scrolls the chat view to the top.
func (m *Chat) ScrollToTop() {
	m.list.ScrollToTop()
	m.follow = false
	m.touchVisibleCaches()
}

// ScrollBy scrolls the chat view by the given number of line deltas.
func (m *Chat) ScrollBy(lines int) {
	m.list.ScrollBy(lines)
	m.follow = lines > 0 && m.AtBottom()
	m.touchVisibleCaches()
}

// ScrollToSelected scrolls the chat view to the selected item.
func (m *Chat) ScrollToSelected() {
	m.list.ScrollToSelected()
	m.follow = m.AtBottom()
	m.touchVisibleCaches()
}

// ScrollToIndex scrolls the chat view to the item at the given index.
func (m *Chat) ScrollToIndex(index int) {
	m.list.ScrollToIndex(index)
	m.follow = m.AtBottom()
	m.touchVisibleCaches()
}

// ScrollToTopAndAnimate scrolls the chat view to the top and returns a command to restart
// any paused animations that are now visible.
func (m *Chat) ScrollToTopAndAnimate() tea.Cmd {
	m.ScrollToTop()
	return m.RestartPausedVisibleAnimations()
}

// ScrollToBottomAndAnimate scrolls the chat view to the bottom and returns a command to
// restart any paused animations that are now visible.
func (m *Chat) ScrollToBottomAndAnimate() tea.Cmd {
	m.ScrollToBottom()
	return m.RestartPausedVisibleAnimations()
}

// ScrollByAndAnimate scrolls the chat view by the given number of line deltas and returns
// a command to restart any paused animations that are now visible.
func (m *Chat) ScrollByAndAnimate(lines int) tea.Cmd {
	m.ScrollBy(lines)
	return m.RestartPausedVisibleAnimations()
}

// ScrollToSelectedAndAnimate scrolls the chat view to the selected item and returns a
// command to restart any paused animations that are now visible.
func (m *Chat) ScrollToSelectedAndAnimate() tea.Cmd {
	m.ScrollToSelected()
	return m.RestartPausedVisibleAnimations()
}

// SelectedItemInView returns whether the selected item is currently in view.
func (m *Chat) SelectedItemInView() bool {
	return m.list.SelectedItemInView()
}

func (m *Chat) isSelectable(index int) bool {
	item := m.list.ItemAt(index)
	if item == nil {
		return false
	}
	_, ok := item.(list.Focusable)
	return ok
}

// SetSelected sets the selected message index in the chat list.
func (m *Chat) SetSelected(index int) {
	m.list.SetSelected(index)
	if index < 0 || index >= m.list.Len() {
		return
	}
	for {
		if m.isSelectable(m.list.Selected()) {
			return
		}
		if m.list.SelectNext() {
			continue
		}
		// If we're at the end and the last item isn't selectable, walk backwards.
		for {
			if !m.list.SelectPrev() {
				return
			}
			if m.isSelectable(m.list.Selected()) {
				return
			}
		}
	}
}

// SelectPrev selects the previous message in the chat list.
func (m *Chat) SelectPrev() {
	for {
		if !m.list.SelectPrev() {
			return
		}
		if m.isSelectable(m.list.Selected()) {
			return
		}
	}
}

// SelectNext selects the next message in the chat list.
func (m *Chat) SelectNext() {
	for {
		if !m.list.SelectNext() {
			return
		}
		if m.isSelectable(m.list.Selected()) {
			return
		}
	}
}

// SelectFirst selects the first message in the chat list.
func (m *Chat) SelectFirst() {
	if !m.list.SelectFirst() {
		return
	}
	if m.isSelectable(m.list.Selected()) {
		return
	}
	for {
		if !m.list.SelectNext() {
			return
		}
		if m.isSelectable(m.list.Selected()) {
			return
		}
	}
}

// SelectLast selects the last message in the chat list.
func (m *Chat) SelectLast() {
	if !m.list.SelectLast() {
		return
	}
	if m.isSelectable(m.list.Selected()) {
		return
	}
	for {
		if !m.list.SelectPrev() {
			return
		}
		if m.isSelectable(m.list.Selected()) {
			return
		}
	}
}

// SelectFirstInView selects the first message currently in view.
func (m *Chat) SelectFirstInView() {
	startIdx, endIdx := m.list.VisibleItemIndices()
	for i := startIdx; i <= endIdx; i++ {
		if m.isSelectable(i) {
			m.list.SetSelected(i)
			return
		}
	}
}

// SelectLastInView selects the last message currently in view.
func (m *Chat) SelectLastInView() {
	startIdx, endIdx := m.list.VisibleItemIndices()
	for i := endIdx; i >= startIdx; i-- {
		if m.isSelectable(i) {
			m.list.SetSelected(i)
			return
		}
	}
}

// ClearMessages removes all messages from the chat list.
func (m *Chat) ClearMessages() {
	m.idInxMap = make(map[string]int)
	m.pausedAnimations = make(map[string]struct{})
	m.list.SetItems()
}

// RemoveMessage removes a message from the chat list by its ID.
func (m *Chat) RemoveMessage(id string) {
	idx, ok := m.idInxMap[id]
	if !ok {
		return
	}

	// Remove from list
	m.list.RemoveItem(idx)

	// Remove from index map
	delete(m.idInxMap, id)

	// Rebuild index map for all items after the removed one
	for i := idx; i < m.list.Len(); i++ {
		if item, ok := m.list.ItemAt(i).(MessageItem); ok {
			m.idInxMap[item.ID()] = i
		}
	}

	// Clean up any paused animations for this message
	delete(m.pausedAnimations, id)
}

// MessageItem returns the message item with the given ID, or nil if not found.
func (m *Chat) MessageItem(id string) MessageItem {
	idx, ok := m.idInxMap[id]
	if !ok {
		return nil
	}
	item, ok := m.list.ItemAt(idx).(MessageItem)
	if !ok {
		return nil
	}
	return item
}

// ToggleExpandedSelectedItem expands the selected message item if it is expandable.
func (m *Chat) ToggleExpandedSelectedItem() {
	if expandable, ok := m.list.SelectedItem().(Expandable); ok {
		if !expandable.ToggleExpanded() {
			m.ScrollToIndex(m.list.Selected())
		}
		if m.AtBottom() {
			m.ScrollToBottom()
		}
	}
}

// HandleMouseClick handles a mouse click at the given viewport-relative
// coordinates. If the click lands on an expandable tool item, it selects
// the item and toggles its expanded state.
func (m *Chat) HandleMouseClick(x, y int) {
	idx, itemY := m.list.ItemIndexAtPosition(x, y)
	if idx < 0 {
		return
	}
	m.list.SetSelected(idx)

	item := m.list.ItemAt(idx)
	if item == nil {
		return
	}

	// Toggle expansion on click.
	if expandable, ok := item.(Expandable); ok {
		expandable.ToggleExpanded()
		if m.AtBottom() {
			m.ScrollToBottom()
		}
	}
	_ = itemY
}

// HandleKeyMsg handles key events for the chat component.
func (m *Chat) HandleKeyMsg(key tea.KeyMsg) (bool, tea.Cmd) {
	if m.list.Focused() {
		type keyHandler interface {
			HandleKeyEvent(key tea.KeyMsg) (bool, tea.Cmd)
		}
		if handler, ok := m.list.SelectedItem().(keyHandler); ok {
			return handler.HandleKeyEvent(key)
		}
	}
	return false, nil
}
