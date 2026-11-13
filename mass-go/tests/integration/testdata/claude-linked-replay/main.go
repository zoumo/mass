// Synthetic linked replay of the retained baseline; never launches a model.
package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"reflect"
	"strings"
	"time"

	"github.com/zoumo/mass/mass-go/tests/integration/testdata/clipeer"
)

const claimedSessionID = "66666666-6666-4666-8666-666666666666"

func main() {
	if clipeer.Version() {
		return
	}
	if err := replay(); err != nil {
		panic(err)
	}
}

func replay() error {
	assignedSessionID := ""
	for _, arg := range os.Args[1:] {
		if value, ok := strings.CutPrefix(arg, "--session-id="); ok {
			assignedSessionID = value
		}
		if value, ok := strings.CutPrefix(arg, "--resume="); ok {
			assignedSessionID = value
		}
	}
	if clipeer.AwaitingClaim() {
		assignedSessionID = claimedSessionID
	}
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
	readActual := func() (map[string]any, error) {
		for {
			var actual map[string]any
			if err := input.Decode(&actual); err != nil {
				return nil, err
			}
			if reply, ok := clipeer.ClaimReply(actual, claimedSessionID); ok {
				if err := output.Encode(reply); err != nil {
					return nil, err
				}
				continue
			}
			return actual, nil
		}
	}
	recordedSessionID := ""
	for _, frame := range scenario.Frames {
		if frame.Direction == "server" {
			if value, _ := frame.Data["session_id"].(string); value != "" {
				recordedSessionID = value
				break
			}
		}
	}
	for _, frame := range scenario.Frames {
		data := frame.Data
		if frame.Direction == "client" {
			actual, err := readActual()
			if err != nil {
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
		replaceSessionID(data, recordedSessionID, assignedSessionID)
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

func replaceSessionID(value any, recorded, assigned string) {
	if recorded == "" || assigned == "" {
		return
	}
	switch value := value.(type) {
	case map[string]any:
		if sessionID, _ := value["session_id"].(string); sessionID == recorded {
			value["session_id"] = assigned
		}
		for _, child := range value {
			replaceSessionID(child, recorded, assigned)
		}
	case []any:
		for _, child := range value {
			replaceSessionID(child, recorded, assigned)
		}
	}
}
