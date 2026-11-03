// Synthetic linked replay of the retained baseline; never launches a model.
package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
)

func main() {
	if err := replay(); err != nil {
		panic(err)
	}
}

func replay() error {
	path := os.Getenv("CLAUDE_LINKED_REPLAY")
	if info, err := os.Stat(path); err == nil && info.IsDir() {
		path = filepath.Join(path, "raw", "raw.jsonl")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	type frame struct {
		Direction string
		Data      map[string]any
	}
	var scenario struct {
		Frames []frame
	}
	var frames []frame
	if err := json.Unmarshal(data, &scenario); err == nil && scenario.Frames != nil {
		frames = scenario.Frames
	} else {
		dec := json.NewDecoder(bytes.NewReader(data))
		dec.UseNumber()
		for {
			var value frame
			if err := dec.Decode(&value); errors.Is(err, io.EOF) {
				break
			} else if err != nil {
				return err
			}
			frames = append(frames, value)
		}
	}
	scenario.Frames = frames
	if len(frames) == 0 {
		return errors.New("linked replay has no frames")
	}
	input := json.NewDecoder(os.Stdin)
	input.UseNumber()
	output := json.NewEncoder(os.Stdout)
	var link string
	ids := map[string]any{}
	for _, frame := range scenario.Frames {
		data := frame.Data
		if frame.Direction == "client" {
			var actual map[string]any
			if err := input.Decode(&actual); err != nil {
				return err
			}
			if data["type"] == "control_request" {
				ids[data["request_id"].(string)] = actual["request_id"]
				actual["request_id"] = data["request_id"]
			}
			if data["type"] == "user" {
				link, _ = actual["uuid"].(string)
				if data["message"].(map[string]any)["content"] == "continue" && link == "" {
					return errors.New("resumed input lacks caller UUID")
				}
				delete(actual, "uuid")
			}
			if !reflect.DeepEqual(data, actual) {
				return fmt.Errorf("unexpected client frame: %v", actual)
			}
			continue
		}
		if data["type"] == "control_response" {
			response := data["response"].(map[string]any)
			response["request_id"] = ids[response["request_id"].(string)]
		}
		// The synthetic native peer explicitly attests every post-input frame.
		if link != "" {
			data["user_message_uuid"] = link
		}
		if err := output.Encode(data); err != nil {
			return err
		}
	}
	// Retain the native connection until its owner closes it.
	var extra any
	if err := input.Decode(&extra); err == nil {
		return errors.New("unexpected extra input")
	}
	return nil
}
