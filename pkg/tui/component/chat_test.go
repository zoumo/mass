package component

import (
	"strconv"
	"testing"
)

// testMsgItem is a minimal MessageItem for testing.
type testMsgItem struct {
	id   string
	text string
}

func (t testMsgItem) ID() string           { return t.id }
func (t testMsgItem) Render(int) string    { return t.text }
func (t testMsgItem) RawRender(int) string { return t.text }

var _ MessageItem = testMsgItem{}

func makeItems(n int) []MessageItem {
	items := make([]MessageItem, n)
	for i := range n {
		items[i] = testMsgItem{
			id:   "m-" + strconv.Itoa(i),
			text: "message " + strconv.Itoa(i),
		}
	}
	return items
}

// ── NewChat ──────────────────────────────────────────────────────────────────

func TestNewChat(t *testing.T) {
	c := NewChat()
	if c.Len() != 0 {
		t.Fatalf("new chat should be empty, got %d", c.Len())
	}
	// Follow mode is false initially — only enabled by ScrollToBottom.
	if c.Follow() {
		t.Fatal("new chat should NOT start in follow mode")
	}
}

// ── AppendMessages ───────────────────────────────────────────────────────────

func TestAppendMessages(t *testing.T) {
	c := NewChat()
	c.SetSize(80, 20)

	items := makeItems(3)
	c.AppendMessages(items...)

	if c.Len() != 3 {
		t.Fatalf("Len: got %d, want 3", c.Len())
	}
}

func TestAppendMessages_IDLookup(t *testing.T) {
	c := NewChat()
	c.SetSize(80, 20)

	items := makeItems(3)
	c.AppendMessages(items...)

	for _, item := range items {
		got := c.MessageItem(item.ID())
		if got == nil {
			t.Fatalf("MessageItem(%q) returned nil", item.ID())
		}
		if got.ID() != item.ID() {
			t.Fatalf("MessageItem(%q).ID() = %q", item.ID(), got.ID())
		}
	}

	// Non-existent ID.
	if c.MessageItem("nonexistent") != nil {
		t.Fatal("expected nil for nonexistent ID")
	}
}

// ── SetMessages ──────────────────────────────────────────────────────────────

func TestSetMessages(t *testing.T) {
	c := NewChat()
	c.SetSize(80, 20)

	c.AppendMessages(makeItems(5)...)
	c.SetMessages(makeItems(2)...)

	if c.Len() != 2 {
		t.Fatalf("after SetMessages, Len: got %d, want 2", c.Len())
	}
}

// ── RemoveMessage ────────────────────────────────────────────────────────────

func TestRemoveMessage(t *testing.T) {
	c := NewChat()
	c.SetSize(80, 20)

	items := makeItems(5)
	c.AppendMessages(items...)
	c.RemoveMessage("m-2")

	if c.Len() != 4 {
		t.Fatalf("after RemoveMessage, Len: got %d, want 4", c.Len())
	}

	// Removed item should not be found.
	if c.MessageItem("m-2") != nil {
		t.Fatal("removed item should not be found")
	}

	// Items after the removed one should still be accessible.
	if c.MessageItem("m-3") == nil {
		t.Fatal("m-3 should still be accessible after removing m-2")
	}
	if c.MessageItem("m-4") == nil {
		t.Fatal("m-4 should still be accessible after removing m-2")
	}
}

func TestRemoveMessage_NonExistent(t *testing.T) {
	c := NewChat()
	c.SetSize(80, 20)
	c.AppendMessages(makeItems(2)...)

	c.RemoveMessage("nonexistent") // should be no-op
	if c.Len() != 2 {
		t.Fatal("remove nonexistent should be no-op")
	}
}

// ── ClearMessages ────────────────────────────────────────────────────────────

func TestClearMessages(t *testing.T) {
	c := NewChat()
	c.SetSize(80, 20)
	c.AppendMessages(makeItems(5)...)

	c.ClearMessages()
	if c.Len() != 0 {
		t.Fatalf("after ClearMessages, Len: got %d, want 0", c.Len())
	}
}

// ── Follow mode ──────────────────────────────────────────────────────────────

func TestFollow_ScrollUpDisables(t *testing.T) {
	c := NewChat()
	c.SetSize(80, 5)
	c.AppendMessages(makeItems(20)...)
	c.ScrollToBottom()

	if !c.Follow() {
		t.Fatal("should be in follow mode after ScrollToBottom")
	}

	c.ScrollBy(-5) // scroll up

	if c.Follow() {
		t.Fatal("scrolling up should disable follow mode")
	}
}

func TestFollow_ScrollToBottomRe_enables(t *testing.T) {
	c := NewChat()
	c.SetSize(80, 5)
	c.AppendMessages(makeItems(20)...)

	c.ScrollToBottom()
	c.ScrollBy(-5) // disable follow
	c.ScrollToBottom()

	if !c.Follow() {
		t.Fatal("ScrollToBottom should re-enable follow mode")
	}
}

func TestFollow_ScrollToTopDisables(t *testing.T) {
	c := NewChat()
	c.SetSize(80, 5)
	c.AppendMessages(makeItems(20)...)
	c.ScrollToBottom()

	c.ScrollToTop()
	if c.Follow() {
		t.Fatal("ScrollToTop should disable follow mode")
	}
}

func TestFollow_ScrollDownToBottomRe_enables(t *testing.T) {
	c := NewChat()
	c.SetSize(80, 5)
	c.AppendMessages(makeItems(20)...)

	c.ScrollToBottom()
	c.ScrollBy(-5)  // disable follow
	c.ScrollBy(100) // scroll way down past bottom

	if !c.Follow() {
		t.Fatal("scrolling down to bottom should re-enable follow")
	}
}

// ── AtBottom ─────────────────────────────────────────────────────────────────

func TestAtBottom_FewItems(t *testing.T) {
	c := NewChat()
	c.SetSize(80, 20)
	c.AppendMessages(makeItems(3)...)

	if !c.AtBottom() {
		t.Fatal("should be at bottom when all items fit")
	}
}

func TestAtBottom_ManyItems(t *testing.T) {
	c := NewChat()
	c.SetSize(80, 5)
	c.AppendMessages(makeItems(20)...)

	if c.AtBottom() {
		t.Fatal("should NOT be at bottom with 20 items in 5-line viewport")
	}
}

// ── Render ───────────────────────────────────────────────────────────────────

func TestRender_NotEmpty(t *testing.T) {
	c := NewChat()
	c.SetSize(80, 10)
	c.AppendMessages(makeItems(3)...)

	got := c.Render()
	if got == "" {
		t.Fatal("Render should not be empty with items")
	}
}

// ── SetSize ──────────────────────────────────────────────────────────────────

func TestSetSize_KeepsBottomWhenAtBottom(t *testing.T) {
	c := NewChat()
	c.SetSize(80, 5)
	c.AppendMessages(makeItems(20)...)
	c.ScrollToBottom()

	c.SetSize(80, 10) // resize

	if !c.AtBottom() {
		t.Fatal("should remain at bottom after resize when was at bottom")
	}
}
