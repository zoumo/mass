// Synthetic linked replay of the retained baseline; never launches a model.
package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"reflect"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/zoumo/agent-sdk-go/agent"

	"github.com/zoumo/mass/internal/testpeer/replay"
	"github.com/zoumo/mass/massd/tests/integration/testdata/clipeer"
)

func main() {
	if clipeer.Version() {
		return
	}
	if err := runReplay(); err != nil {
		panic(err)
	}
}

func runReplay() error {
	claimedSessionID := uuid.NewString()
	assignedSessionID := ""
	claiming := clipeer.AwaitingClaim()
	for _, arg := range os.Args[1:] {
		if value, ok := strings.CutPrefix(arg, "--session-id="); ok {
			assignedSessionID = value
		}
		if value, ok := strings.CutPrefix(arg, "--resume="); ok {
			assignedSessionID = value
		}
	}
	if claiming {
		assignedSessionID = claimedSessionID
	}
	f, err := os.Open(os.Getenv("CLAUDE_MOCK_SCENARIO"))
	if err != nil {
		return err
	}
	defer f.Close()
	var scenario struct {
		AnswerGate        string
		ExpectPromptTrace bool
		Frames            []struct {
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
	var initializeRequestID any
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
			cwd, err := os.Getwd()
			if err != nil {
				return nil, err
			}
			if reply, ok := clipeer.StatusReply(actual, assignedSessionID, cwd); ok {
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
				actualRequestID := actual["request_id"]
				ids[data["request_id"].(string)] = actualRequestID
				actual["request_id"] = data["request_id"]
				if req, ok := actual["request"].(map[string]any); ok && req["subtype"] == "initialize" {
					initializeRequestID = actualRequestID
					req["hooks"] = nil
				}
			}
			if data["type"] == "user" {
				if scenario.ExpectPromptTrace {
					if err := acknowledgePromptTrace(actual, output); err != nil {
						return err
					}
					actual, err = readActual()
					if err != nil {
						return err
					}
				}
				link, _ = actual["uuid"].(string)
				if data["uuid"] == replay.GeneratedClaudeUserUUID {
					if !replay.ValidClaudeUserUUID(link) {
						return errors.New("expected a generated Claude user UUID")
					}
					data["uuid"] = link
				}
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
		if claiming && data["type"] == "control_response" {
			response, _ := data["response"].(map[string]any)
			if response["request_id"] == initializeRequestID {
				var claim map[string]any
				if err := input.Decode(&claim); err != nil {
					return err
				}
				reply, ok := clipeer.ClaimReply(claim, claimedSessionID)
				if !ok {
					return fmt.Errorf("expected claim_session after initialize response: %v", claim)
				}
				if err := output.Encode(reply); err != nil {
					return err
				}
				claiming = false
			}
		}
	}
	// Retain the native connection until its owner closes it.
	if _, err := readActual(); err == nil {
		return errors.New("unexpected extra input")
	} else if !errors.Is(err, io.EOF) {
		return err
	}
	return nil
}

func acknowledgePromptTrace(frame map[string]any, output *json.Encoder) error {
	request, _ := frame["request"].(map[string]any)
	settings, _ := request["settings"].(map[string]any)
	env, _ := settings["env"].(map[string]any)
	parent, _ := env["TRACEPARENT"].(string)
	id, _ := frame["request_id"].(string)
	want := map[string]any{
		"type": "control_request", "request_id": id,
		"request": map[string]any{
			"subtype": "apply_flag_settings",
			"settings": map[string]any{"env": map[string]any{"TRACEPARENT": parent, "TRACESTATE": ""}},
		},
	}
	if id == "" || !reflect.DeepEqual(frame, want) ||
		(agent.TraceContext{Traceparent: parent}).Validate() != nil || !strings.HasSuffix(parent, "-01") {
		return errors.New("expected sampled prompt trace settings")
	}
	return output.Encode(map[string]any{
		"type": "control_response",
		"response": map[string]any{"request_id": id, "subtype": "success", "response": map[string]any{}},
	})
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
