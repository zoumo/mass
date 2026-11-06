package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

func claude() {
	log, err := os.OpenFile(os.Getenv("LOAD_EVIDENCE"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		panic(err)
	}
	defer log.Close()
	record := func(event string, fields map[string]any) {
		fields["event"] = event
		fields["pid"] = os.Getpid()
		if err := json.NewEncoder(log).Encode(fields); err != nil {
			panic(err)
		}
	}
	record("opened", map[string]any{"args": os.Args[1:]})
	defer record("released", map[string]any{})
	id := os.Getenv("LOAD_ID")
	resumed := false
	for _, arg := range os.Args[1:] {
		if arg == "--resume="+id {
			resumed = true
		}
	}
	if !resumed {
		panic("must resume requested identity")
	}
	record("resumed", map[string]any{"id": id})
	enc := json.NewEncoder(os.Stdout)
	send := func(v any) {
		if err := enc.Encode(v); err != nil {
			panic(err)
		}
	}
	n := 0
	var link string
	complete := func() {
		message := map[string]any{"type": "assistant", "session_id": id, "user_message_uuid": link, "message": map[string]any{"id": fmt.Sprintf("live-%d", n), "role": "assistant", "content": []any{map[string]any{"type": "text", "text": "same retained connection"}}}}
		result := map[string]any{"type": "result", "subtype": "success", "session_id": id, "uuid": fmt.Sprintf("result-%d", n), "user_message_uuid": link}
		if os.Getenv("LOAD_MODE") == "unlinked" {
			delete(message, "user_message_uuid")
			delete(result, "user_message_uuid")
		}
		send(message)
		send(result)
	}

	scan := bufio.NewScanner(os.Stdin)
	for scan.Scan() {
		var v map[string]any
		if err := json.Unmarshal(scan.Bytes(), &v); err != nil {
			panic(err)
		}
		switch v["type"] {
		case "control_request":
			r := v["request"].(map[string]any)
			send(map[string]any{"type": "control_response", "response": map[string]any{"subtype": "success", "request_id": v["request_id"], "response": map[string]any{}}})
			switch r["subtype"] {
			case "initialize":
				cwd, _ := os.Getwd()
				send(map[string]any{"type": "system", "subtype": "init", "session_id": id, "cwd": cwd})
			case "interrupt":
				send(map[string]any{"type": "result", "subtype": "success", "session_id": id, "user_message_uuid": link})
			}
		case "user":
			n++
			link, _ = v["uuid"].(string)
			record("input", map[string]any{"uuid": link, "input": n})
			send(map[string]any{"type": "control_request", "user_message_uuid": link, "request_id": fmt.Sprintf("approval-%d", n), "request": map[string]any{"subtype": "can_use_tool", "tool_name": "Bash", "input": map[string]any{"command": "echo synthetic"}, "tool_use_id": fmt.Sprintf("tool-live-%d", n)}})
		case "control_response":
			response := v["response"].(map[string]any)
			if response["request_id"] != fmt.Sprintf("approval-%d", n) || response["subtype"] != "success" {
				panic("wrong current answer")
			}
			body := response["response"].(map[string]any)
			if body["behavior"] != "allow" {
				panic("not allowed")
			}
			record("answer", map[string]any{"uuid": link, "input": n, "behavior": body["behavior"]})
			complete()
		default:
			panic("unexpected frame " + strings.TrimSpace(string(scan.Bytes())))
		}
	}
	if err := scan.Err(); err != nil {
		panic(err)
	}
}
