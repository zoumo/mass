// claudereplay binds the generated UUID of a single live Claude user frame to
// the UUID expected by a strict Testify replay of a consumer recording.
package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"sync"
	"sync/atomic"

	"github.com/zoumo/mass/internal/recordingtest/replaybridge"
	"github.com/zoumo/mass/internal/testpeer/rawjson"
	"github.com/zoumo/mass/internal/testpeer/replay"
	"github.com/zoumo/mass/internal/testpeer/startup"
)

func main() {
	if err := run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, "Claude consumer replay:", err)
		os.Exit(1)
	}
}

func run(args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	legacyPermissions := len(args) > 0 && args[0] == "--legacy-permissions"
	if legacyPermissions {
		args = args[1:]
	}
	if len(args) < 3 {
		return errors.New("expected captured UUID, receipt path and Testify launcher")
	}
	binding := submissionBinding{captured: args[0]}
	if binding.captured != "" && !replay.ValidClaudeUserUUID(binding.captured) {
		return errors.New("capture requires a canonical v4 UUID")
	}

	launcher := args[2:]
	if legacyPermissions {
		if err := startup.ValidateClaudePermissionArgs(launcher[1:]); err != nil {
			return err
		}
		client := &serializedWriter{destination: stdout}
		stdout = client
		binding.settings = json.NewEncoder(client)
	}
	return replaybridge.Relay(launcher, args[1], stderr,
		func(destination io.Writer) error { return binding.forwardInput(stdin, destination) },
		func(source io.Reader) error { return binding.forwardOutput(source, stdout) },
	)
}

type serializedWriter struct {
	mu          sync.Mutex
	destination io.Writer
}

func (w *serializedWriter) Write(line []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.destination.Write(line)
}

type submissionBinding struct {
	captured string
	live     atomic.Pointer[rawjson.Value]
	settings *json.Encoder
}

func (b *submissionBinding) forwardInput(source io.Reader, destination io.Writer) error {
	input := replaybridge.ProtocolLines(source)
	settingsPending := b.settings != nil
	for input.Scan() {
		frame, err := parseFrame(input.Bytes())
		if err != nil {
			return err
		}
		line := input.Bytes()
		if memberString(frame, "type") == "control_request" {
			request, _ := frame.Member("request")
			if b.settings != nil && memberString(request, "subtype") == "get_settings" {
				id := memberString(frame, "request_id")
				if !settingsPending || id == "" || len(frame.Keys()) != 3 || len(request.Keys()) != 1 {
					return errors.New("expected one legacy Claude startup settings request")
				}
				if err := b.settings.Encode(map[string]any{
					"type":     "control_response",
					"response": map[string]any{"subtype": "success", "request_id": id, "response": startup.ClaudeSettings()},
				}); err != nil {
					return err
				}
				settingsPending = false
				continue
			}
		}
		if memberString(frame, "type") == "user" {
			if settingsPending {
				return errors.New("missing Claude startup settings request before user input")
			}
			if b.live.Load() != nil {
				return errors.New("consumer recording expects one user input")
			}
			line, err = b.bindUser(frame)
			if err != nil {
				return err
			}
		}
		if _, err := fmt.Fprintf(destination, "%s\n", line); err != nil {
			return err
		}
	}
	if err := input.Err(); err != nil {
		return err
	}
	if b.live.Load() == nil {
		return errors.New("missing Claude user input")
	}
	return nil
}

func (b *submissionBinding) bindUser(frame rawjson.Value) ([]byte, error) {
	live, _ := frame.Member("uuid")
	value, _ := live.String()
	if !replay.ValidClaudeUserUUID(value) {
		return nil, errors.New("user input requires a canonical v4 UUID")
	}

	var line []byte
	var err error
	if b.captured == "" {
		line, err = withoutUUID(frame)
	} else {
		captured, parseErr := rawjson.Parse([]byte(strconv.Quote(b.captured)))
		if parseErr != nil {
			return nil, parseErr
		}
		var mapped rawjson.Value
		mapped, err = frame.Replace([]string{"uuid"}, captured)
		line = mapped.Raw()
	}
	if err != nil {
		return nil, err
	}
	// Publish the binding before the child can receive and answer the user frame.
	b.live.Store(&live)
	return line, nil
}

func withoutUUID(frame rawjson.Value) ([]byte, error) {
	fields := make(map[string]json.RawMessage, len(frame.Keys())-1)
	for _, key := range frame.Keys() {
		if key != "uuid" {
			member, _ := frame.Member(key)
			fields[key] = member.Raw()
		}
	}
	return json.Marshal(fields)
}

func (b *submissionBinding) forwardOutput(source io.Reader, destination io.Writer) error {
	if b.captured == "" && b.settings == nil {
		_, err := io.Copy(destination, source)
		return err
	}
	output := replaybridge.ProtocolLines(source)
	for output.Scan() {
		frame, err := parseFrame(output.Bytes())
		if err != nil {
			return err
		}
		if b.captured != "" {
			frame, err = b.mapServerUUID(frame, "user_message_uuid")
			if err != nil {
				return err
			}
			// Claude may echo the submitted user message with the same envelope UUID.
			// Assistant and result envelope UUIDs identify their own native events.
			if memberString(frame, "type") == "user" {
				frame, err = b.mapServerUUID(frame, "uuid")
				if err != nil {
					return err
				}
			}
		}
		if _, err := fmt.Fprintf(destination, "%s\n", frame.Raw()); err != nil {
			return err
		}
	}
	return output.Err()
}

func (b *submissionBinding) mapServerUUID(frame rawjson.Value, field string) (rawjson.Value, error) {
	if memberString(frame, field) != b.captured {
		return frame, nil
	}
	live := b.live.Load()
	if live == nil {
		return rawjson.Value{}, errors.New("server refers to captured user UUID before user input")
	}
	return frame.Replace([]string{field}, *live)
}

func memberString(frame rawjson.Value, field string) string {
	member, _ := frame.Member(field)
	value, _ := member.String()
	return value
}

func parseFrame(line []byte) (rawjson.Value, error) {
	frame, err := rawjson.Parse(line)
	if err != nil || frame.Kind() != rawjson.Object {
		return rawjson.Value{}, errors.New("expected a Claude JSON object")
	}
	return frame, nil
}
