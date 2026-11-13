package recordingtest

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

func auditCodexCapture(material, model, sessionID string) error {
	file, err := os.Open(filepath.Join(material, "raw", "raw.jsonl"))
	if err != nil {
		return err
	}
	defer file.Close()
	decoder := json.NewDecoder(file)
	identity, completed := false, false
	for {
		var frame struct {
			Direction string `json:"direction"`
			Wire      struct {
				Method string `json:"method"`
				Result struct {
					Model  string `json:"model"`
					Thread struct {
						ID    string `json:"id"`
						Model string `json:"model"`
					} `json:"thread"`
				} `json:"result"`
				Params struct {
					ThreadID string `json:"threadId"`
					Turn     struct {
						Status string `json:"status"`
					} `json:"turn"`
				} `json:"params"`
			} `json:"wire"`
		}
		if err := decoder.Decode(&frame); err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return err
		}
		if frame.Direction != "s2c" {
			continue
		}
		if frame.Wire.Result.Thread.ID != "" {
			nativeModel := frame.Wire.Result.Model
			if nativeModel == "" {
				nativeModel = frame.Wire.Result.Thread.Model
			}
			if frame.Wire.Result.Thread.ID != sessionID || nativeModel != model {
				return fmt.Errorf("native thread/model differs from captured facts: %q/%q", frame.Wire.Result.Thread.ID, frame.Wire.Result.Model)
			}
			identity = true
		}
		if frame.Wire.Method == "turn/completed" {
			if frame.Wire.Params.ThreadID != sessionID || frame.Wire.Params.Turn.Status != "completed" {
				return errors.New("native turn did not complete for the captured session")
			}
			completed = true
		}
	}
	if !identity || !completed {
		return errors.New("native model identity or completed turn is missing")
	}
	return nil
}
