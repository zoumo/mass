package events

import (
	"encoding/json"
	"fmt"
	"os"
	"sync"
	"time"
)

// LogEntry is a single line in the events.jsonl log file.
// Every event emitted by the Translator is appended here so callers
// can reconstruct the full event history after reconnecting.
type LogEntry struct {
	// Seq is a monotonically increasing sequence number (0-based) within
	// this agent session. Used as the offset for GetHistory.
	Seq int `json:"seq"`

	// Timestamp is the RFC 3339 wall-clock time when the event was recorded.
	Timestamp string `json:"ts"`

	// Type is the event discriminator, matching EventNotification.Type.
	Type string `json:"type"`

	// Payload is the event-specific data as a raw JSON object.
	Payload any `json:"payload"`
}

// EventLog appends LogEntry records to a JSONL file (one JSON object per line).
// It is safe for concurrent use.
type EventLog struct {
	mu   sync.Mutex
	f    *os.File
	enc  *json.Encoder
	seq  int
}

// OpenEventLog opens (or creates) the JSONL log file at path.
// Existing content is preserved; new entries are appended.
// The caller owns the EventLog and must call Close when done.
func OpenEventLog(path string) (*EventLog, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o640)
	if err != nil {
		return nil, fmt.Errorf("events: open log %s: %w", path, err)
	}
	// Count existing lines to initialise seq correctly so offsets are
	// stable across restarts that append to the same file.
	seq, err := countLines(path)
	if err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("events: count lines in %s: %w", path, err)
	}
	return &EventLog{f: f, enc: json.NewEncoder(f), seq: seq}, nil
}

// Append writes entry to the log. Type and Payload are provided by the caller;
// Seq and Timestamp are assigned automatically.
func (l *EventLog) Append(evType string, payload any) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	entry := LogEntry{
		Seq:       l.seq,
		Timestamp: time.Now().UTC().Format(time.RFC3339Nano),
		Type:      evType,
		Payload:   payload,
	}
	if err := l.enc.Encode(entry); err != nil {
		return fmt.Errorf("events: write log entry: %w", err)
	}
	l.seq++
	return nil
}

// Close flushes and closes the underlying file.
func (l *EventLog) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.f.Close()
}

// ReadEventLog reads all LogEntry records from path starting at fromSeq.
// Returns an empty slice (not an error) if the file does not exist yet.
func ReadEventLog(path string, fromSeq int) ([]LogEntry, error) {
	f, err := os.Open(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("events: open log for read %s: %w", path, err)
	}
	defer f.Close()

	var entries []LogEntry
	dec := json.NewDecoder(f)
	for dec.More() {
		var e LogEntry
		if err := dec.Decode(&e); err != nil {
			return nil, fmt.Errorf("events: decode log entry: %w", err)
		}
		if e.Seq >= fromSeq {
			entries = append(entries, e)
		}
	}
	return entries, nil
}

// countLines counts the number of non-empty lines in path by opening it for
// reading. Used to initialise the sequence counter when appending to an
// existing log.
func countLines(path string) (int, error) {
	f, err := os.Open(path)
	if os.IsNotExist(err) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	defer f.Close()

	entries, err := func() (int, error) {
		dec := json.NewDecoder(f)
		n := 0
		for dec.More() {
			var raw json.RawMessage
			if err := dec.Decode(&raw); err != nil {
				return n, err
			}
			n++
		}
		return n, nil
	}()
	return entries, err
}
