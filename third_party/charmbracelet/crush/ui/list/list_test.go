package list

import (
	"fmt"
	"strings"
	"testing"
)

// testItem is a minimal Item for testing.
type testItem struct {
	text string
}

func (t testItem) Render(width int) string { return t.text }

func items(texts ...string) []Item {
	out := make([]Item, len(texts))
	for i, t := range texts {
		out[i] = testItem{text: t}
	}
	return out
}

func multiLineItem(lines int) testItem {
	parts := make([]string, lines)
	for i := range lines {
		parts[i] = fmt.Sprintf("line-%d", i)
	}
	return testItem{text: strings.Join(parts, "\n")}
}

// ── NewList ──────────────────────────────────────────────────────────────────

func TestNewList(t *testing.T) {
	l := NewList(items("a", "b", "c")...)
	if l.Len() != 3 {
		t.Fatalf("Len: got %d, want 3", l.Len())
	}
}

// ── Render ───────────────────────────────────────────────────────────────────

func TestRender_AllFit(t *testing.T) {
	l := NewList(items("a", "b", "c")...)
	l.SetSize(80, 10)

	got := l.Render()
	want := "a\nb\nc"
	if got != want {
		t.Fatalf("Render:\ngot:  %q\nwant: %q", got, want)
	}
}

func TestRender_Truncated(t *testing.T) {
	l := NewList(items("a", "b", "c", "d", "e")...)
	l.SetSize(80, 3)

	got := l.Render()
	lines := strings.Split(got, "\n")
	if len(lines) != 3 {
		t.Fatalf("expected 3 visible lines, got %d: %q", len(lines), got)
	}
	if lines[0] != "a" {
		t.Fatalf("first line: got %q, want %q", lines[0], "a")
	}
}

func TestRender_MultiLineItem(t *testing.T) {
	l := NewList(multiLineItem(3), testItem{text: "single"})
	l.SetSize(80, 10)

	got := l.Render()
	lines := strings.Split(got, "\n")
	if len(lines) != 4 {
		t.Fatalf("expected 4 lines, got %d: %q", len(lines), got)
	}
}

func TestRender_WithGap(t *testing.T) {
	l := NewList(items("a", "b", "c")...)
	l.SetSize(80, 10)
	l.SetGap(1)

	got := l.Render()
	lines := strings.Split(got, "\n")
	// Crush's list adds gap after each item (including between items),
	// so: a, "", b, "", c, "" = 6 lines (trailing gap gets included
	// because the viewport has room).
	if len(lines) < 5 {
		t.Fatalf("expected >=5 lines with gap=1, got %d: %v", len(lines), lines)
	}
	if lines[1] != "" {
		t.Fatalf("gap line should be empty, got %q", lines[1])
	}
}

func TestRender_Empty(t *testing.T) {
	l := NewList()
	l.SetSize(80, 10)
	if got := l.Render(); got != "" {
		t.Fatalf("empty list should render empty, got %q", got)
	}
}

// ── AtBottom / ScrollToBottom ────────────────────────────────────────────────

func TestAtBottom_AllFit(t *testing.T) {
	l := NewList(items("a", "b")...)
	l.SetSize(80, 10)
	if !l.AtBottom() {
		t.Fatal("should be at bottom when all items fit")
	}
}

func TestAtBottom_NotAtBottom(t *testing.T) {
	l := NewList(items("a", "b", "c", "d", "e")...)
	l.SetSize(80, 3)
	if l.AtBottom() {
		t.Fatal("should NOT be at bottom when items overflow viewport")
	}
}

func TestScrollToBottom(t *testing.T) {
	l := NewList(items("a", "b", "c", "d", "e")...)
	l.SetSize(80, 3)

	l.ScrollToBottom()
	if !l.AtBottom() {
		t.Fatal("should be at bottom after ScrollToBottom")
	}

	got := l.Render()
	lines := strings.Split(got, "\n")
	if lines[len(lines)-1] != "e" {
		t.Fatalf("last visible line should be 'e', got %q", lines[len(lines)-1])
	}
}

// ── ScrollBy ─────────────────────────────────────────────────────────────────

func TestScrollBy_Down(t *testing.T) {
	l := NewList(items("a", "b", "c", "d", "e")...)
	l.SetSize(80, 3)

	l.ScrollBy(2)

	got := l.Render()
	lines := strings.Split(got, "\n")
	if lines[0] != "c" {
		t.Fatalf("after ScrollBy(2), first line should be 'c', got %q", lines[0])
	}
}

func TestScrollBy_Up(t *testing.T) {
	l := NewList(items("a", "b", "c", "d", "e")...)
	l.SetSize(80, 3)

	l.ScrollToBottom()
	l.ScrollBy(-2)

	got := l.Render()
	lines := strings.Split(got, "\n")
	// Should have scrolled up from bottom.
	if lines[len(lines)-1] == "e" {
		t.Fatal("should have scrolled up from bottom")
	}
}

func TestScrollBy_ClampTop(t *testing.T) {
	l := NewList(items("a", "b", "c")...)
	l.SetSize(80, 2)

	l.ScrollBy(-100) // scroll way up

	got := l.Render()
	lines := strings.Split(got, "\n")
	if lines[0] != "a" {
		t.Fatalf("should clamp to top, got first line %q", lines[0])
	}
}

func TestScrollBy_ClampBottom(t *testing.T) {
	l := NewList(items("a", "b", "c")...)
	l.SetSize(80, 2)

	l.ScrollBy(100) // scroll way down

	if !l.AtBottom() {
		t.Fatal("should clamp to bottom")
	}
}

func TestScrollBy_NoOp_WhenAtBottom(t *testing.T) {
	l := NewList(items("a", "b")...)
	l.SetSize(80, 10)

	l.ScrollBy(5) // already fits, should be no-op
	if !l.AtBottom() {
		t.Fatal("should still be at bottom")
	}
}

// ── ScrollToTop ──────────────────────────────────────────────────────────────

func TestScrollToTop(t *testing.T) {
	l := NewList(items("a", "b", "c", "d", "e")...)
	l.SetSize(80, 3)

	l.ScrollToBottom()
	l.ScrollToTop()

	got := l.Render()
	lines := strings.Split(got, "\n")
	if lines[0] != "a" {
		t.Fatalf("after ScrollToTop, first line should be 'a', got %q", lines[0])
	}
}

// ── ScrollToIndex ────────────────────────────────────────────────────────────

func TestScrollToIndex(t *testing.T) {
	l := NewList(items("a", "b", "c", "d", "e")...)
	l.SetSize(80, 2)

	l.ScrollToIndex(3)

	got := l.Render()
	lines := strings.Split(got, "\n")
	if lines[0] != "d" {
		t.Fatalf("after ScrollToIndex(3), first line should be 'd', got %q", lines[0])
	}
}

// ── VisibleItemIndices ───────────────────────────────────────────────────────

func TestVisibleItemIndices(t *testing.T) {
	l := NewList(items("a", "b", "c", "d", "e")...)
	l.SetSize(80, 3)

	start, end := l.VisibleItemIndices()
	if start != 0 || end != 2 {
		t.Fatalf("VisibleItemIndices: got (%d,%d), want (0,2)", start, end)
	}

	l.ScrollToBottom()
	start, end = l.VisibleItemIndices()
	if end != 4 {
		t.Fatalf("after ScrollToBottom, end should be 4, got %d", end)
	}
}

// ── Item management ──────────────────────────────────────────────────────────

func TestSetItems(t *testing.T) {
	l := NewList(items("a", "b")...)
	l.SetSize(80, 10)
	l.SetItems(items("x", "y", "z")...)
	if l.Len() != 3 {
		t.Fatalf("after SetItems, Len: got %d, want 3", l.Len())
	}
}

func TestAppendItems(t *testing.T) {
	l := NewList(items("a")...)
	l.SetSize(80, 10)
	l.AppendItems(items("b", "c")...)
	if l.Len() != 3 {
		t.Fatalf("after AppendItems, Len: got %d, want 3", l.Len())
	}
}

func TestRemoveItem(t *testing.T) {
	l := NewList(items("a", "b", "c")...)
	l.SetSize(80, 10)
	l.RemoveItem(1) // remove "b"

	if l.Len() != 2 {
		t.Fatalf("after RemoveItem, Len: got %d, want 2", l.Len())
	}
	got := l.Render()
	if got != "a\nc" {
		t.Fatalf("after removing index 1, got %q, want %q", got, "a\nc")
	}
}

func TestRemoveItem_OutOfBounds(t *testing.T) {
	l := NewList(items("a")...)
	l.SetSize(80, 10)
	l.RemoveItem(-1)
	l.RemoveItem(5)
	if l.Len() != 1 {
		t.Fatal("out-of-bounds remove should be no-op")
	}
}

func TestItemAt(t *testing.T) {
	l := NewList(items("a", "b")...)
	if l.ItemAt(0).(testItem).text != "a" {
		t.Fatal("ItemAt(0) should be 'a'")
	}
	if l.ItemAt(-1) != nil {
		t.Fatal("ItemAt(-1) should be nil")
	}
	if l.ItemAt(99) != nil {
		t.Fatal("ItemAt(99) should be nil")
	}
}

// ── MultiLine scrolling ─────────────────────────────────────────────────────

func TestScrollBy_MultiLineItem(t *testing.T) {
	// Item 0: 3 lines, Item 1: 1 line, Item 2: 1 line
	l := NewList(multiLineItem(3), testItem{text: "b"}, testItem{text: "c"})
	l.SetSize(80, 3)

	// Scroll down 1 line — should show line-1, line-2 of item 0, then "b".
	l.ScrollBy(1)

	got := l.Render()
	lines := strings.Split(got, "\n")
	if lines[0] != "line-1" {
		t.Fatalf("after ScrollBy(1), first line should be 'line-1', got %q", lines[0])
	}
}
