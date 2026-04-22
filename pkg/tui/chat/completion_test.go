package chat

import (
	"testing"

	"github.com/zoumo/mass/third_party/charmbracelet/crush/ui/styles"
)

func testCompletionState() *completionState {
	sty := styles.DefaultStyles()
	return newCompletionState(&sty)
}

func testEntries() []completionEntry {
	return []completionEntry{
		{Name: "help", Description: "Show available commands", Category: categorySystem},
		{Name: "clear", Description: "Clear chat messages", Category: categorySystem},
		{Name: "model", Description: "Switch model", Category: categorySession},
		{Name: "exit", Description: "Exit chat", Category: categorySystem},
	}
}

func TestCompletionState_InitiallyInactive(t *testing.T) {
	c := testCompletionState()
	if c.Active() {
		t.Fatal("new completionState should be inactive")
	}
	if c.Height() != 0 {
		t.Fatalf("inactive height should be 0, got %d", c.Height())
	}
	if c.Render(80) != "" {
		t.Fatal("inactive Render should return empty string")
	}
	if c.Selected() != nil {
		t.Fatal("inactive Selected should return nil")
	}
}

func TestCompletionState_ActivateDeactivate(t *testing.T) {
	c := testCompletionState()
	c.Activate(testEntries())

	if !c.Active() {
		t.Fatal("should be active after Activate")
	}
	if c.Height() == 0 {
		t.Fatal("height should be > 0 when active")
	}

	c.Deactivate()
	if c.Active() {
		t.Fatal("should be inactive after Deactivate")
	}
	if c.Height() != 0 {
		t.Fatal("height should be 0 after Deactivate")
	}
}

func TestCompletionState_UpdateFilter(t *testing.T) {
	c := testCompletionState()
	c.Activate(testEntries())

	// Filter to only items matching "mod".
	c.UpdateFilter("mod")
	items := c.list.FilteredItems()
	if len(items) != 1 {
		t.Fatalf("filter 'mod' should match 1 item, got %d", len(items))
	}

	// Clear filter shows all.
	c.UpdateFilter("")
	all := c.list.FilteredItems()
	if len(all) != len(testEntries()) {
		t.Fatalf("empty filter should show %d items, got %d", len(testEntries()), len(all))
	}
}

func TestCompletionState_Selected(t *testing.T) {
	c := testCompletionState()
	c.Activate(testEntries())

	entry := c.Selected()
	if entry == nil {
		t.Fatal("Selected should not be nil after Activate")
	}
	// First entry should be "help".
	if entry.Name != "help" {
		t.Fatalf("first selected should be 'help', got %q", entry.Name)
	}
}

func TestCompletionState_SelectNextPrev(t *testing.T) {
	c := testCompletionState()
	c.Activate(testEntries())

	c.SelectNext()
	entry := c.Selected()
	if entry == nil || entry.Name != "clear" {
		t.Fatalf("after SelectNext should be 'clear', got %v", entry)
	}

	c.SelectPrev()
	entry = c.Selected()
	if entry == nil || entry.Name != "help" {
		t.Fatalf("after SelectPrev should be 'help', got %v", entry)
	}
}

func TestCompletionState_SelectWrapsAround(t *testing.T) {
	c := testCompletionState()
	c.Activate(testEntries())

	// Wrap backwards from first.
	c.SelectPrev()
	entry := c.Selected()
	if entry == nil || entry.Name != "exit" {
		t.Fatalf("wrap prev from first should select last 'exit', got %v", entry)
	}

	// Wrap forward from last.
	c.SelectNext()
	entry = c.Selected()
	if entry == nil || entry.Name != "help" {
		t.Fatalf("wrap next from last should select first 'help', got %v", entry)
	}
}

func TestCompletionState_HeightCappedAtMax(t *testing.T) {
	c := testCompletionState()
	// Build more entries than the max.
	entries := make([]completionEntry, completionMaxHeight+3)
	for i := range entries {
		entries[i] = completionEntry{Name: "cmd", Description: "desc", Category: categorySystem}
	}
	c.Activate(entries)
	if h := c.Height(); h > completionMaxHeight {
		t.Fatalf("height %d exceeds max %d", h, completionMaxHeight)
	}
}

func TestCompletionState_RenderNotEmpty(t *testing.T) {
	c := testCompletionState()
	c.Activate(testEntries())
	if c.Render(80) == "" {
		t.Fatal("Render should not be empty when active")
	}
}

func TestCompletionState_FilterNoMatchHeight(t *testing.T) {
	c := testCompletionState()
	c.Activate(testEntries())
	c.UpdateFilter("zzzzzz")
	// No matches — height should be 1 (the "no matches" placeholder line).
	if h := c.Height(); h != 1 {
		t.Fatalf("height with no matches should be 1, got %d", h)
	}
}
