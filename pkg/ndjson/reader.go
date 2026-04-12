// Package ndjson provides a fault-tolerant NDJSON (newline-delimited JSON)
// reader. Unlike json.Decoder, it uses line boundaries for framing so a
// single corrupt line does not poison the stream — the next Decode call
// simply reads the next line.
package ndjson

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"io"
)

// ErrInvalidJSON is a sentinel that matches *InvalidLineError via errors.Is.
var ErrInvalidJSON = errors.New("ndjson: invalid JSON line")

// InvalidLineError is returned when a non-empty line cannot be parsed as JSON.
// The reader has already advanced past it; the next Decode reads the next line.
type InvalidLineError struct {
	Line []byte // the trimmed line content
	Err  error  // the underlying json parse error
}

func (e *InvalidLineError) Error() string {
	const maxPreview = 200
	preview := string(e.Line)
	if len(preview) > maxPreview {
		preview = preview[:maxPreview] + "...(truncated)"
	}
	return "ndjson: invalid JSON line: " + e.Err.Error() + ": " + preview
}

func (e *InvalidLineError) Unwrap() error { return e.Err }

func (e *InvalidLineError) Is(target error) bool {
	return target == ErrInvalidJSON
}

// Reader reads newline-delimited JSON objects from an io.Reader.
type Reader struct {
	r *bufio.Reader
}

// NewReader creates a Reader. The internal buffer starts at 64 KB and
// grows as needed — there is no upper bound on line size.
func NewReader(r io.Reader) *Reader {
	return &Reader{r: bufio.NewReaderSize(r, 64*1024)}
}

// Decode reads the next non-empty line and unmarshals it into v.
//
// Returns:
//   - nil on success
//   - *InvalidLineError if the line is not valid JSON (stream still usable)
//   - io.EOF when the stream ends
//   - other errors for underlying read failures
func (r *Reader) Decode(v any) error {
	for {
		line, readErr := r.r.ReadBytes('\n')
		trimmed := bytes.TrimSpace(line)
		if len(trimmed) > 0 {
			if err := json.Unmarshal(trimmed, v); err != nil {
				return &InvalidLineError{Line: trimmed, Err: err}
			}
			return nil
		}
		if readErr != nil {
			return readErr
		}
	}
}
