package testpeer

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
)

// Synthetic identifies one authored external recording under a test-owned root.
type Synthetic struct {
	Root      string
	Recording string
}

// Author writes versioned external material accepted by the public mock
// --recording contract. It preserves native wire bytes, assigns only synthetic
// envelope ordinals and never writes into the testify source tree.
func Author(dir, provider string, scenario []byte) (Synthetic, error) {
	if provider != "codex" && provider != "claude" {
		return Synthetic{}, fmt.Errorf("unsupported synthetic provider %q", provider)
	}
	dir, err := filepath.Abs(dir)
	if err != nil {
		return Synthetic{}, err
	}
	var envelope struct {
		Version    int               `json:"version"`
		Provenance string            `json:"provenance"`
		Source     json.RawMessage   `json:"source"`
		TimeoutMS  int               `json:"timeoutMs"`
		Frames     []json.RawMessage `json:"frames"`
	}
	dec := json.NewDecoder(bytes.NewReader(scenario))
	dec.UseNumber()
	if err := dec.Decode(&envelope); err != nil {
		return Synthetic{}, fmt.Errorf("decode synthetic scenario: %w", err)
	}
	if envelope.Version != 1 || envelope.Provenance != "synthetic" || len(envelope.Frames) == 0 {
		return Synthetic{}, errors.New("synthetic scenario requires version 1, explicit synthetic provenance and at least one frame")
	}
	if envelope.TimeoutMS < 0 || envelope.TimeoutMS > 300000 {
		return Synthetic{}, errors.New("synthetic scenario timeout out of range")
	}
	frames := make([]json.RawMessage, 0, len(envelope.Frames))
	for index, frameRaw := range envelope.Frames {
		var frame map[string]json.RawMessage
		if err := json.Unmarshal(frameRaw, &frame); err != nil {
			return Synthetic{}, fmt.Errorf("decode synthetic frame %d: %w", index+1, err)
		}
		frame["ordinal"] = json.RawMessage(strconv.Itoa(index + 1))
		row, err := json.Marshal(frame)
		if err != nil {
			return Synthetic{}, err
		}
		frames = append(frames, row)
	}
	scenarioDocument := map[string]any{
		"version":    1,
		"provenance": "synthetic",
		"frames":     frames,
	}
	if len(envelope.Source) != 0 && string(envelope.Source) != "null" {
		scenarioDocument["source"] = envelope.Source
	}
	if envelope.TimeoutMS != 0 {
		scenarioDocument["timeoutMs"] = envelope.TimeoutMS
	}
	raw, err := json.Marshal(scenarioDocument)
	if err != nil {
		return Synthetic{}, err
	}
	project := ""
	var findCWD func(any)
	findCWD = func(value any) {
		if project != "" {
			return
		}
		switch value := value.(type) {
		case map[string]any:
			for key, child := range value {
				if key == "cwd" {
					if cwd, ok := child.(string); ok && filepath.IsAbs(cwd) {
						project = cwd
						return
					}
				}
				findCWD(child)
			}
		case []any:
			for _, child := range value {
				findCWD(child)
			}
		}
	}
	frameDec := json.NewDecoder(bytes.NewReader(raw))
	frameDec.UseNumber()
	for {
		var frame any
		if err := frameDec.Decode(&frame); err != nil {
			break
		}
		findCWD(frame)
		if project != "" {
			break
		}
	}
	root, err := os.MkdirTemp(dir, "synthetic-"+provider+"-")
	if err != nil {
		return Synthetic{}, err
	}
	if project == "" {
		project = filepath.Join(root, "project")
		if err := os.Mkdir(project, 0o700); err != nil {
			return Synthetic{}, err
		}
	}
	plan, err := json.Marshal(map[string]any{
		"caseId":     "MASS-AUTHORED",
		"provider":   provider,
		"provenance": "synthetic",
		"project":    project,
	})
	if err != nil {
		return Synthetic{}, err
	}
	manifest, err := json.Marshal(map[string]any{
		"provenance":    "synthetic",
		"captureStatus": "complete",
		"planSHA256":    digest(plan),
		"files":         map[string]string{"raw.jsonl": digest(raw)},
	})
	if err != nil {
		return Synthetic{}, err
	}
	recording := filepath.Join(root, "recording")
	if err := os.MkdirAll(filepath.Join(recording, "raw"), 0o700); err != nil {
		return Synthetic{}, err
	}
	for _, file := range []struct {
		name    string
		content []byte
	}{
		{"plan.json", plan},
		{"manifest.json", manifest},
		{"raw/raw.jsonl", raw},
	} {
		if err := os.WriteFile(filepath.Join(recording, file.name), file.content, 0o600); err != nil {
			return Synthetic{}, err
		}
	}
	return Synthetic{Root: root, Recording: recording}, nil
}

func digest(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
