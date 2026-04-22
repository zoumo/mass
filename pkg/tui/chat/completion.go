package chat

import (
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/zoumo/mass/third_party/charmbracelet/crush/ui/list"
	"github.com/zoumo/mass/third_party/charmbracelet/crush/ui/styles"
)

const completionMaxHeight = 8

// completionCategory classifies a command for display in the completion list.
type completionCategory string

const (
	categorySystem  completionCategory = "system"
	categorySession completionCategory = "session"
	categoryAgent   completionCategory = "agent"
)

// completionEntry is a unified representation of any command shown in the completion list.
type completionEntry struct {
	Name        string
	Description string
	Category    completionCategory
	// IsArg marks an argument completion entry (e.g. a model ID).
	// When true the leading "/" prefix is omitted from the display.
	IsArg bool
}

// completionItem wraps a completionEntry for the FilterableList.
type completionItem struct {
	entry        completionEntry
	nameColWidth int // fixed left-column width shared across all items in this list
	focused      bool
	cache        map[int]string
	sty          *styles.Styles
}

var (
	_ list.FilterableItem = (*completionItem)(nil)
	_ list.Focusable      = (*completionItem)(nil)
)

func newCompletionItem(sty *styles.Styles, entry completionEntry, nameColWidth int) *completionItem {
	return &completionItem{sty: sty, entry: entry, nameColWidth: nameColWidth}
}

func (c *completionItem) Filter() string { return c.entry.Name }

func (c *completionItem) SetFocused(focused bool) {
	if c.focused != focused {
		c.cache = nil
	}
	c.focused = focused
}

func (c *completionItem) Render(width int) string {
	if c.cache == nil {
		c.cache = make(map[int]string)
	}
	if cached, ok := c.cache[width]; ok {
		return cached
	}

	itemStyle := c.sty.Dialog.NormalItem
	if c.focused {
		itemStyle = c.sty.Dialog.SelectedItem
	}
	catStyle := lipgloss.NewStyle().Faint(true)

	// Arg entries (e.g. model IDs) are shown without the leading "/" prefix.
	var name string
	var prefixLen int
	if c.entry.IsArg {
		name = c.entry.Name
		prefixLen = 0
	} else {
		name = "/" + c.entry.Name
		prefixLen = 1
	}
	cat := catStyle.Render(string(c.entry.Category))
	catWidth := lipgloss.Width(cat)

	// Left column: name padded to shared column width.
	// Subtract 2 for the style's horizontal padding (Padding(0,1) = 1 char each side).
	const colGap = 2
	const hPad = 2
	nameColWidth := c.nameColWidth + prefixLen
	namePadded := name + strings.Repeat(" ", max(0, nameColWidth-lipgloss.Width(name)+colGap))

	// Right column: description + category, truncated to fit remaining width.
	nameAreaWidth := nameColWidth + colGap
	availForRight := width - nameAreaWidth - hPad
	if availForRight < 4 {
		availForRight = 4
	}
	// Category on the right of the description column.
	descAvail := availForRight - catWidth - 1
	if descAvail < 0 {
		descAvail = 0
	}
	desc := ansi.Truncate(c.entry.Description, descAvail, "…")
	descWidth := lipgloss.Width(desc)
	innerGap := strings.Repeat(" ", max(0, availForRight-descWidth-catWidth))
	right := desc + innerGap + cat

	line := namePadded + right
	result := itemStyle.Render(line)
	c.cache[width] = result
	return result
}

// completionState manages the inline command completion popup.
type completionState struct {
	sty    *styles.Styles
	list   *list.FilterableList
	active bool
}

func newCompletionState(sty *styles.Styles) *completionState {
	return &completionState{sty: sty}
}

// Active returns whether the completion popup is currently shown.
func (c *completionState) Active() bool { return c.active }

// Height returns the number of lines the completion area occupies (0 if inactive).
func (c *completionState) Height() int {
	if !c.active || c.list == nil {
		return 0
	}
	n := len(c.list.FilteredItems())
	if n > completionMaxHeight {
		n = completionMaxHeight
	}
	if n < 1 {
		n = 1 // at least 1 line for "no matches"
	}
	return n
}

// Activate sets up the list with all available entries and shows the popup.
func (c *completionState) Activate(entries []completionEntry) {
	// Compute the max name length so all items share the same left column width.
	maxNameLen := 0
	for _, e := range entries {
		if n := len(e.Name); n > maxNameLen {
			maxNameLen = n
		}
	}
	items := make([]list.FilterableItem, len(entries))
	for i, e := range entries {
		items[i] = newCompletionItem(c.sty, e, maxNameLen)
	}
	fl := list.NewFilterableList(items...)
	fl.Focus()
	fl.SetSelected(0)
	c.list = fl
	c.active = true
}

// Deactivate hides the completion popup and discards its state.
func (c *completionState) Deactivate() {
	c.active = false
	c.list = nil
}

// UpdateFilter re-filters the list based on the command name typed after "/".
func (c *completionState) UpdateFilter(query string) {
	if !c.active || c.list == nil {
		return
	}
	c.list.SetFilter(query)
	c.list.ScrollToTop()
	c.list.SetSelected(0)
}

// SelectNext moves selection down, wrapping around.
func (c *completionState) SelectNext() {
	if !c.active || c.list == nil {
		return
	}
	if c.list.IsSelectedLast() {
		c.list.SelectFirst()
	} else {
		c.list.SelectNext()
	}
	c.list.ScrollToSelected()
}

// SelectPrev moves selection up, wrapping around.
func (c *completionState) SelectPrev() {
	if !c.active || c.list == nil {
		return
	}
	if c.list.IsSelectedFirst() {
		c.list.SelectLast()
	} else {
		c.list.SelectPrev()
	}
	c.list.ScrollToSelected()
}

// Selected returns the currently highlighted entry, or nil if empty.
func (c *completionState) Selected() *completionEntry {
	if !c.active || c.list == nil {
		return nil
	}
	item := c.list.SelectedItem()
	if item == nil {
		return nil
	}
	ci, ok := item.(*completionItem)
	if !ok {
		return nil
	}
	return &ci.entry
}

// Render draws the completion list at the given width. Returns "" if inactive.
func (c *completionState) Render(width int) string {
	if !c.active || c.list == nil {
		return ""
	}
	h := c.Height()
	c.list.SetSize(width, h)
	return c.list.Render()
}
