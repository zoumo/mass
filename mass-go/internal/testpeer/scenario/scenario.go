// Package scenario parses MASS-authored strict protocol scenarios. It owns the
// versioned envelope only; native protocol semantics stay in the replay package.
package scenario

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/zoumo/mass/mass-go/internal/testpeer/rawjson"
)

const FormatVersion = 1

type Direction string

const (
	ClientToServer Direction = "c2s"
	ServerToClient Direction = "s2c"
)

// Frame is one strict protocol step. Wire is absent for synthetic disconnect
// actions; Delay is explicit injection, never inferred from elapsed time.
type Frame struct {
	Ordinal    int64
	Direction  Direction
	Channel    string
	Epoch      int64
	ElapsedMS  int64
	Wire       rawjson.Value
	Delay      time.Duration
	Disconnect bool
}

type Document struct {
	Provider   string
	Provenance string
	Timeout    time.Duration
	Frames     []Frame
	Original   []byte
	SHA256     string
}

// Limits are inclusive source/count bounds and an exclusive wire byte bound.
// Zero selects the provider default when reading.
type Limits struct {
	MaxBytes      int64
	MaxFrameBytes int64
	MaxFrames     int
}

func DefaultLimits(provider string) (Limits, error) {
	switch provider {
	case "codex":
		return Limits{8 << 20, 2 << 20, 10000}, nil
	case "claude":
		return Limits{16 << 20, 2 << 20, 10000}, nil
	default:
		return Limits{}, fmt.Errorf("unsupported native protocol adapter %q", provider)
	}
}

func (l Limits) Resolve(provider string) (Limits, error) {
	d, err := DefaultLimits(provider)
	if err != nil {
		return l, err
	}
	if l.MaxBytes < 0 || l.MaxFrameBytes < 0 || l.MaxFrames < 0 || l.MaxFrames > 10000 {
		return l, errors.New("invalid scenario limits")
	}
	if l.MaxBytes == 0 {
		l.MaxBytes = d.MaxBytes
	}
	if l.MaxFrameBytes == 0 {
		l.MaxFrameBytes = d.MaxFrameBytes
	}
	if l.MaxFrames == 0 {
		l.MaxFrames = d.MaxFrames
	}
	if l.MaxBytes == 1<<63-1 {
		return l, errors.New("scenario byte limit too large")
	}
	return l, nil
}

// Read supports the original unified JSON object, legacy data/direction JSONL
// and v1 scenarios. It does not discover a checkout, manifest or executable.
func Read(r io.Reader, provider string, limits Limits) (*Document, error) {
	l, err := limits.Resolve(provider)
	if err != nil {
		return nil, err
	}
	data, err := io.ReadAll(io.LimitReader(r, l.MaxBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > l.MaxBytes {
		return nil, errors.New("scenario byte limit exceeded")
	}
	d := &Document{Provider: provider, Provenance: "recording", Original: data, SHA256: Hash(data)}
	var rows []rawjson.Value
	whole, wholeErr := rawjson.Parse(data)
	if _, ok := whole.Member("frames"); wholeErr == nil && ok {
		if err := allowed(whole, "version", "provenance", "source", "timeoutMs", "frames"); err != nil {
			return nil, err
		}
		version, e := integer(whole, "version", false)
		if e != nil || version != FormatVersion {
			return nil, errors.New("unsupported scenario version")
		}
		d.Provenance, err = stringField(whole, "provenance")
		if err != nil || (d.Provenance != "synthetic" && d.Provenance != "recording") {
			return nil, errors.New("explicit scenario provenance required")
		}
		if d.Provenance == "recording" && !nonemptySource(member(whole, "source")) {
			return nil, errors.New("recording source required")
		}
		ms, e := integer(whole, "timeoutMs", true)
		if e != nil || ms < 0 || ms > 300000 {
			return nil, errors.New("scenario timeout out of range")
		}
		d.Timeout = time.Duration(ms) * time.Millisecond
		frames := member(whole, "frames")
		if frames.Kind() != rawjson.Array {
			return nil, errors.New("frames must be an array")
		}
		rows = frames.Elements()
	} else {
		lines := bytes.Split(data, []byte{'\n'})
		if len(lines) > 0 && len(lines[len(lines)-1]) == 0 {
			lines = lines[:len(lines)-1]
		}
		for i, line := range lines {
			row, e := rawjson.Parse(line)
			if e != nil {
				return nil, fmt.Errorf("line %d: %w", i+1, e)
			}
			rows = append(rows, row)
		}
	}
	if len(rows) == 0 || len(rows) > l.MaxFrames {
		return nil, errors.New("scenario frame count out of range")
	}
	var last int64
	for i, row := range rows {
		frame, e := decodeFrame(row, provider, d.Provenance, i == len(rows)-1, l)
		if e != nil {
			return nil, fmt.Errorf("frame %d: %w", i+1, e)
		}
		if frame.Ordinal <= last || (provider == "claude" && frame.Ordinal != int64(i+1)) {
			return nil, fmt.Errorf("frame %d: invalid ordinal order", i+1)
		}
		last = frame.Ordinal
		d.Frames = append(d.Frames, frame)
	}
	return d, nil
}

func decodeFrame(v rawjson.Value, provider, provenance string, final bool, l Limits) (Frame, error) {
	f := Frame{Epoch: 1}
	_, wire := v.Member("wire")
	_, epoch := v.Member("epoch")
	unified := wire || epoch
	payloadKey, delayKey := "data", "delay_ms"
	var err error
	if unified {
		err = allowed(v, "ordinal", "direction", "channel", "epoch", "elapsedMs", "wire", "delayMs", "disconnect")
		payloadKey, delayKey = "wire", "delayMs"
	} else {
		err = allowed(v, "ordinal", "direction", "channel", "data", "delay_ms")
	}
	if err != nil {
		return f, err
	}
	f.Ordinal, err = integer(v, "ordinal", false)
	if err != nil || f.Ordinal <= 0 {
		return f, errors.New("positive integer ordinal required")
	}
	if unified {
		f.Epoch, err = integer(v, "epoch", false)
		if err != nil || f.Epoch != 1 {
			return f, errors.New("invalid capture epoch")
		}
	}
	f.ElapsedMS, err = integer(v, "elapsedMs", true)
	if err != nil || f.ElapsedMS < 0 {
		return f, errors.New("invalid elapsedMs")
	}
	f.Channel, err = stringField(v, "channel")
	if err != nil || (f.Channel != "protocol" && f.Channel != "control") {
		return f, errors.New("invalid frame channel")
	}
	direction, err := stringField(v, "direction")
	if err != nil {
		return f, err
	}
	switch direction {
	case "c2s", "client":
		f.Direction = ClientToServer
	case "s2c", "server":
		f.Direction = ServerToClient
	case "disconnect":
		f.Direction = ServerToClient
		f.Disconnect = true
	default:
		return f, errors.New("invalid frame direction")
	}
	if flag, ok := v.Member("disconnect"); ok {
		flagValue, e := flag.Bool()
		if e != nil {
			return f, e
		}
		f.Disconnect = f.Disconnect || flagValue
	}
	if f.Disconnect && f.Direction != ServerToClient {
		return f, errors.New("disconnect must be a server action")
	}
	delay, err := integer(v, delayKey, true)
	maxDelay := int64(300000)
	if provider == "claude" {
		maxDelay = 30000
	}
	if err != nil || delay < 0 || delay > maxDelay {
		return f, errors.New("invalid frame delay")
	}
	f.Delay = time.Duration(delay) * time.Millisecond
	if provenance != "synthetic" && (f.Disconnect || delay != 0) {
		return f, errors.New("injected faults require synthetic provenance")
	}
	f.Wire = member(v, payloadKey)
	if f.Disconnect {
		if !final || (f.Wire.Kind() != rawjson.Invalid && f.Wire.Kind() != rawjson.Null) {
			return f, errors.New("disconnect must be final without payload")
		}
		f.Wire = rawjson.Value{}
	} else if f.Wire.Kind() != rawjson.Object {
		return f, errors.New("native frame must be an object")
	}
	if int64(len(f.Wire.Raw())) >= l.MaxFrameBytes {
		return f, errors.New("frame byte limit exceeded")
	}
	return f, nil
}

func Hash(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func allowed(v rawjson.Value, keys ...string) error {
	if v.Kind() != rawjson.Object {
		return errors.New("object required")
	}
	valid := make(map[string]bool, len(keys))
	for _, key := range keys {
		valid[key] = true
	}
	for _, key := range v.Keys() {
		if !valid[key] {
			return fmt.Errorf("unknown envelope field %q", key)
		}
	}
	return nil
}

func member(v rawjson.Value, key string) rawjson.Value {
	x, _ := v.Member(key)
	return x
}

func integer(v rawjson.Value, key string, optional bool) (int64, error) {
	x, ok := v.Member(key)
	if !ok && optional {
		return 0, nil
	}
	n, err := x.Int64()
	if err != nil {
		return 0, fmt.Errorf("%s: %w", key, err)
	}
	return n, nil
}

func stringField(v rawjson.Value, key string) (string, error) {
	x, _ := v.Member(key)
	s, err := x.String()
	if err != nil {
		return "", fmt.Errorf("%s: %w", key, err)
	}
	return s, nil
}

func nonemptySource(v rawjson.Value) bool {
	switch v.Kind() {
	case rawjson.String:
		s, _ := v.String()
		return s != ""
	case rawjson.Object:
		return len(v.Keys()) > 0
	}
	return false
}
