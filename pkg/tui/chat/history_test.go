package chat

import "testing"

func TestHistory_PushAndNavigate(t *testing.T) {
	h := NewHistory(100)

	h.Push("first")
	h.Push("second")
	h.Push("third")

	// Navigate back through history.
	if text, ok := h.Prev(); !ok || text != "third" {
		t.Fatalf("Prev()=%q,%v, want 'third',true", text, ok)
	}
	if text, ok := h.Prev(); !ok || text != "second" {
		t.Fatalf("Prev()=%q,%v, want 'second',true", text, ok)
	}
	if text, ok := h.Prev(); !ok || text != "first" {
		t.Fatalf("Prev()=%q,%v, want 'first',true", text, ok)
	}

	// At beginning — can't go further back.
	if _, ok := h.Prev(); ok {
		t.Fatal("Prev() at beginning should return false")
	}

	// Navigate forward.
	if text, ok := h.Next(); !ok || text != "second" {
		t.Fatalf("Next()=%q,%v, want 'second',true", text, ok)
	}
}

func TestHistory_Draft(t *testing.T) {
	h := NewHistory(100)
	h.Push("old")

	h.SaveDraft("current typing")
	text, ok := h.Prev()
	if !ok || text != "old" {
		t.Fatalf("Prev()=%q,%v, want 'old',true", text, ok)
	}

	text, ok = h.Next()
	if !ok || text != "current typing" {
		t.Fatalf("Next()=%q,%v, want 'current typing',true", text, ok)
	}
}

func TestHistory_Dedup(t *testing.T) {
	h := NewHistory(100)
	h.Push("same")
	h.Push("same")
	h.Push("same")

	count := 0
	for {
		_, ok := h.Prev()
		if !ok {
			break
		}
		count++
	}
	if count != 1 {
		t.Fatalf("deduped history should have 1 entry, navigated %d", count)
	}
}

func TestHistory_MaxSize(t *testing.T) {
	h := NewHistory(3)
	h.Push("a")
	h.Push("b")
	h.Push("c")
	h.Push("d")

	// "a" should be evicted.
	var entries []string
	for {
		text, ok := h.Prev()
		if !ok {
			break
		}
		entries = append(entries, text)
	}
	if len(entries) != 3 {
		t.Fatalf("want 3 entries, got %d: %v", len(entries), entries)
	}
	if entries[2] != "b" {
		t.Fatalf("oldest should be 'b', got %q", entries[2])
	}
}

func TestHistory_EmptyPush(t *testing.T) {
	h := NewHistory(100)
	h.Push("")
	if !h.AtEnd() {
		t.Fatal("empty push should not add entry")
	}
}

func TestHistory_AtEnd(t *testing.T) {
	h := NewHistory(100)
	if !h.AtEnd() {
		t.Fatal("new history should be at end")
	}
	h.Push("x")
	if !h.AtEnd() {
		t.Fatal("after push should be at end")
	}
	h.Prev()
	if h.AtEnd() {
		t.Fatal("after prev should not be at end")
	}
	h.Next()
	if !h.AtEnd() {
		t.Fatal("after next to end should be at end")
	}
}
