// codexreplay adapts the permission exchange for a legacy Codex recording
// while replaying the rest of the exchange through strict Testify.
package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"sync/atomic"

	"github.com/zoumo/mass/internal/recordingtest/replaybridge"
	"github.com/zoumo/mass/internal/testpeer/rawjson"
)

func main() {
	if len(os.Args) < 3 {
		fmt.Fprintln(os.Stderr, "expected receipt path and Testify launcher")
		os.Exit(1)
	}
	var startup permissionExchange
	err := replaybridge.Relay(os.Args[2:], os.Args[1], os.Stderr,
		func(destination io.Writer) error { return startup.forwardInput(os.Stdin, destination) },
		func(source io.Reader) error { return startup.forwardOutput(source, os.Stdout) },
	)
	if err != nil {
		fmt.Fprintln(os.Stderr, "Codex consumer replay:", err)
		os.Exit(1)
	}
}

type permissionExchange struct {
	requestID atomic.Pointer[rawjson.Value]
}

func (b *permissionExchange) forwardInput(source io.Reader, destination io.Writer) error {
	return forwardLines(source, destination, func(frame rawjson.Value) ([]byte, error) {
		method := memberString(frame, "method")
		if method != "thread/start" {
			return frame.Raw(), nil
		}
		id, hasID := frame.Member("id")
		params, _ := frame.Member("params")
		if !hasID || !b.requestID.CompareAndSwap(nil, &id) ||
			memberString(params, "approvalPolicy") != "on-request" ||
			memberString(params, "approvalsReviewer") != "auto_review" ||
			memberString(params, "sandbox") != "workspace-write" {
			return nil, errors.New("expected one Codex thread/start with autoReview and workspace-write")
		}
		fields := make(map[string]json.RawMessage, len(params.Keys())-3)
		for _, key := range params.Keys() {
			switch key {
			case "approvalPolicy", "approvalsReviewer", "sandbox":
			default:
				value, _ := params.Member(key)
				fields[key] = value.Raw()
			}
		}
		return replace(frame, []string{"params"}, fields)
	})
}

func (b *permissionExchange) forwardOutput(source io.Reader, destination io.Writer) error {
	confirmed := false
	err := forwardLines(source, destination, func(frame rawjson.Value) ([]byte, error) {
		id, hasID := frame.Member("id")
		threadStartID := b.requestID.Load()
		if !hasID || threadStartID == nil || !rawjson.Equal(id, *threadStartID) {
			return frame.Raw(), nil
		}
		if _, isRequest := frame.Member("method"); isRequest {
			return frame.Raw(), nil
		}
		result, _ := frame.Member("result")
		sandbox, _ := result.Member("sandbox")
		if confirmed || memberString(result, "approvalPolicy") != "on-request" ||
			memberString(result, "approvalsReviewer") != "user" || memberString(sandbox, "type") != "readOnly" {
			return nil, errors.New("expected the recorded Codex thread/start permission response")
		}
		reviewer, err := rawjson.Parse([]byte(`"auto_review"`))
		if err != nil {
			return nil, err
		}
		frame, err = frame.Replace([]string{"result", "approvalsReviewer"}, reviewer)
		if err != nil {
			return nil, err
		}
		confirmed = true
		return replace(frame, []string{"result", "sandbox"}, map[string]string{"type": "workspaceWrite"})
	})
	if err == nil && !confirmed {
		return errors.New("missing Codex thread/start permission response")
	}
	return err
}

func forwardLines(source io.Reader, destination io.Writer, transform func(rawjson.Value) ([]byte, error)) error {
	lines := replaybridge.ProtocolLines(source)
	for lines.Scan() {
		frame, err := rawjson.Parse(lines.Bytes())
		if err != nil || frame.Kind() != rawjson.Object {
			return errors.New("expected a Codex JSON object")
		}
		line, err := transform(frame)
		if err != nil {
			return err
		}
		if _, err := fmt.Fprintf(destination, "%s\n", line); err != nil {
			return err
		}
	}
	return lines.Err()
}

func memberString(frame rawjson.Value, key string) string {
	field, _ := frame.Member(key)
	value, _ := field.String()
	return value
}

func replace(frame rawjson.Value, path []string, value any) ([]byte, error) {
	body, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	replacement, err := rawjson.Parse(body)
	if err != nil {
		return nil, err
	}
	modified, err := frame.Replace(path, replacement)
	return modified.Raw(), err
}
