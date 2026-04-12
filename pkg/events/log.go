package events

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"sync"

	"github.com/zoumo/oar/pkg/ndjson"
)

// EventLog appends replayable notification envelopes to a JSONL file.
// It is safe for concurrent use.
type EventLog struct {
	mu      sync.Mutex
	f       *os.File
	enc     *json.Encoder
	nextSeq int
}

// OpenEventLog opens (or creates) the JSONL log file at path.
// Existing content is preserved; new entries are appended.
// The caller owns the EventLog and must call Close when done.
func OpenEventLog(path string) (*EventLog, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o640)
	if err != nil {
		return nil, fmt.Errorf("events: open log %s: %w", path, err)
	}

	nextSeq, err := countLines(path)
	if err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("events: count lines in %s: %w", path, err)
	}

	return &EventLog{f: f, enc: json.NewEncoder(f), nextSeq: nextSeq}, nil
}

// Append writes env to the log.
func (l *EventLog) Append(env Envelope) error {
	seq, err := env.Seq()
	if err != nil {
		return fmt.Errorf("events: invalid envelope: %w", err)
	}

	l.mu.Lock()
	defer l.mu.Unlock()

	if seq != l.nextSeq {
		return fmt.Errorf("events: append expected seq %d, got %d", l.nextSeq, seq)
	}
	if err := l.enc.Encode(env); err != nil {
		return fmt.Errorf("events: write log entry: %w", err)
	}
	l.nextSeq++
	return nil
}

// NextSeq returns the next sequence number that will be assigned.
func (l *EventLog) NextSeq() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.nextSeq
}

// LastSeq returns the last assigned sequence number, or -1 when the log is empty.
func (l *EventLog) LastSeq() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.nextSeq - 1
}

// Close flushes and closes the underlying file.
func (l *EventLog) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.f.Close()
}

// ReadEventLog reads all Envelope records from path starting at fromSeq.
// Returns an empty slice (not an error) if the file does not exist yet.
//
// Damaged-tail tolerance: if the last non-empty line(s) in the file fail to
// unmarshal as JSON, they are treated as a partial write from a crash —
// the successfully decoded entries are returned without error.  Mid-file
// corruption (corrupt lines followed by valid lines) still returns an error.
func ReadEventLog(path string, fromSeq int) ([]Envelope, error) {
	f, err := os.Open(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("events: open log for read %s: %w", path, err)
	}
	defer f.Close()

	dec := ndjson.NewReader(f)

	// First pass: collect all non-empty lines.
	type lineRecord struct {
		valid bool
		env   Envelope
		err   error // non-nil for invalid lines
	}
	var lines []lineRecord
	for {
		var e Envelope
		err := dec.Decode(&e)
		if errors.Is(err, ndjson.ErrInvalidJSON) {
			lines = append(lines, lineRecord{valid: false, err: err})
			continue
		}
		if err != nil {
			if err != io.EOF {
				return nil, fmt.Errorf("events: read log %s: %w", path, err)
			}
			break
		}
		lines = append(lines, lineRecord{valid: true, env: e})
	}

	// Walk through collected lines. If a corrupt line is followed by any
	// valid line later, that is mid-file corruption — return an error.
	// If corrupt lines only appear at the tail, skip them (damaged tail).
	var entries []Envelope
	for i, lr := range lines {
		if lr.valid {
			seq, err := lr.env.Seq()
			if err != nil {
				return nil, fmt.Errorf("events: decode log entry: %w", err)
			}
			if seq >= fromSeq {
				entries = append(entries, lr.env)
			}
			continue
		}
		// Corrupt line — check if any valid line follows.
		for j := i + 1; j < len(lines); j++ {
			if lines[j].valid {
				return nil, fmt.Errorf("events: mid-file corruption at line %d: %v", i+1, lr.err)
			}
		}
		// No valid lines follow — damaged tail. Log and break.
		slog.Warn("skipping damaged tail lines", "count", len(lines)-i, "path", path, "first_error", lr.err)
		break
	}

	return entries, nil
}

// countLines counts non-empty lines in path without decoding them. This keeps
// the live append path usable even when older history rows are corrupt.
// Uses chunk-based reading with O(1) memory regardless of line size.
func countLines(path string) (int, error) {
	f, err := os.Open(path)
	if os.IsNotExist(err) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	defer f.Close()

	buf := make([]byte, 32*1024)
	count := 0
	hasContent := false // tracks whether current line has non-whitespace
	for {
		n, err := f.Read(buf)
		for i := 0; i < n; i++ {
			switch {
			case buf[i] == '\n':
				if hasContent {
					count++
				}
				hasContent = false
			case buf[i] != ' ' && buf[i] != '\t' && buf[i] != '\r':
				hasContent = true
			}
		}
		if err != nil {
			break
		}
	}
	// Handle last line without trailing newline.
	if hasContent {
		count++
	}
	return count, nil
}
