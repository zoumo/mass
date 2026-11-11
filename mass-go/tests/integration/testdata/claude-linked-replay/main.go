// Synthetic linked replay of the retained baseline; never launches a model.
package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"reflect"
	"time"
)

func main() {
	if len(os.Args) == 2 && os.Args[1] == "--version" {
		fmt.Println("2.1.258 (Claude Code)")
		return
	}
	if err := replay(); err != nil {
		panic(err)
	}
}

func replay() error {
	f, err := os.Open(os.Getenv("CLAUDE_MOCK_SCENARIO"))
	if err != nil {
		return err
	}
	defer f.Close()
	var scenario struct {
		AnswerGate string
		Frames     []struct {
			Direction string
			Data      map[string]any
		}
	}
	dec := json.NewDecoder(f)
	dec.UseNumber()
	if err := dec.Decode(&scenario); err != nil {
		return err
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
				if req, ok := actual["request"].(map[string]any); ok && req["subtype"] == "initialize" {
					req["hooks"] = nil
				}
			}
			if data["type"] == "user" {
				link, _ = actual["uuid"].(string)

				delete(actual, "uuid")
			}
			if !reflect.DeepEqual(data, actual) {
				return fmt.Errorf("unexpected client frame: %v", actual)
			}
			if data["type"] == "control_response" && scenario.AnswerGate != "" {
				// Keep native completion behind an explicit test-owned gate, so
				// concurrent answers race for live authority, not stale refs.
				deadline := time.Now().Add(10 * time.Second)
				for {
					if _, err := os.Stat(scenario.AnswerGate); err == nil {
						break
					}
					if time.Now().After(deadline) {
						return errors.New("answer gate timed out")
					}
					time.Sleep(time.Millisecond)
				}
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
