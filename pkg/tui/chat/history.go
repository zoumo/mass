package chat

// History stores prompt history with cursor navigation.
type History struct {
	entries []string
	cursor  int
	draft   string
	maxSize int
}

// NewHistory creates a history with the given max size.
func NewHistory(maxSize int) *History {
	return &History{maxSize: maxSize}
}

// Push appends an entry and resets the cursor to the end.
func (h *History) Push(text string) {
	if text == "" {
		return
	}
	// Deduplicate against the last entry.
	if len(h.entries) > 0 && h.entries[len(h.entries)-1] == text {
		h.cursor = len(h.entries)
		return
	}
	h.entries = append(h.entries, text)
	if len(h.entries) > h.maxSize {
		h.entries = h.entries[1:]
	}
	h.cursor = len(h.entries)
}

// SaveDraft saves the current editor content before navigating.
func (h *History) SaveDraft(text string) {
	h.draft = text
}

// Prev moves the cursor back and returns the previous entry.
// Returns ("", false) if already at the beginning.
func (h *History) Prev() (string, bool) {
	if h.cursor <= 0 {
		return "", false
	}
	h.cursor--
	return h.entries[h.cursor], true
}

// Next moves the cursor forward and returns the next entry.
// When reaching the end, returns the saved draft.
func (h *History) Next() (string, bool) {
	if h.cursor >= len(h.entries) {
		return "", false
	}
	h.cursor++
	if h.cursor >= len(h.entries) {
		return h.draft, true
	}
	return h.entries[h.cursor], true
}

// AtEnd returns true if the cursor is at the end (no navigation active).
func (h *History) AtEnd() bool {
	return h.cursor >= len(h.entries)
}
