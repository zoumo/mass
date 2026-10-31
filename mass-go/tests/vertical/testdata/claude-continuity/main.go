// Synthetic native boundary: no model, SDK replacement, or credential access.
package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
)

func main() {
	enc := json.NewEncoder(os.Stdout)
	send := func(v map[string]any) {
		if err := enc.Encode(v); err != nil {
			panic(err)
		}
	}
	log, err := os.OpenFile(os.Getenv("PEER_EVIDENCE"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		panic(err)
	}
	defer log.Close()
	record := json.NewEncoder(log)
	cwd, _ := os.Getwd()
	n := 0
	var pending string
	var previous string
	result := func(link string) {
		v := map[string]any{"type": "result", "subtype": "success", "session_id": "claude-continuity", "uuid": fmt.Sprintf("result-%d", n)}
		if link != "" {
			v["user_message_uuid"] = link
		}
		send(v)
	}
	scan := bufio.NewScanner(os.Stdin)
	for scan.Scan() {
		var v map[string]any
		if err := json.Unmarshal(scan.Bytes(), &v); err != nil {
			panic(err)
		}
		if v["type"] == "control_request" {
			r := v["request"].(map[string]any)
			send(map[string]any{"type": "control_response", "response": map[string]any{"subtype": "success", "request_id": v["request_id"], "response": map[string]any{}}})
			if r["subtype"] == "initialize" {
				send(map[string]any{"type": "system", "subtype": "init", "session_id": "claude-continuity", "cwd": cwd})
			} else if r["subtype"] == "interrupt" {
				result(pending)
			}
			continue
		}
		if v["type"] != "user" {
			panic("unexpected frame")
		}
		n++
		link, _ := v["uuid"].(string)
		pending = link
		text := v["message"].(map[string]any)["content"].(string)
		if err := record.Encode(map[string]any{"pid": os.Getpid(), "input": n, "uuid": link, "text": text}); err != nil {
			panic(err)
		}
		start := map[string]any{"type": "stream_event", "session_id": "claude-continuity", "event": map[string]any{"type": "message_start", "message": map[string]any{"id": fmt.Sprintf("message-%d", n), "role": "assistant", "content": []any{}}}}
		if link != "" {
			start["user_message_uuid"] = link
		}
		send(start)
		if text == "hold" && n > 1 {
			old := map[string]any{"type": "result", "subtype": "success", "session_id": "claude-continuity", "uuid": fmt.Sprintf("result-%d", n-1)}
			if previous != "" {
				old["user_message_uuid"] = previous
			}
			send(old)
			send(map[string]any{"type": "control_request", "request_id": "old-permission", "user_message_uuid": previous, "request": map[string]any{"subtype": "can_use_tool", "tool_name": "Bash", "tool_use_id": "old-tool", "input": map[string]any{"command": "never execute"}}})
			send(map[string]any{"type": "result", "subtype": "success", "session_id": "claude-continuity", "parent_tool_use_id": "old-child"})
			send(map[string]any{"type": "assistant", "session_id": "claude-continuity", "message": map[string]any{"id": "unlinked-late", "role": "assistant", "content": []any{map[string]any{"type": "text", "text": "unlinked old or new"}}}})
		}
		message := map[string]any{"type": "assistant", "session_id": "claude-continuity", "message": map[string]any{"id": fmt.Sprintf("message-%d", n), "role": "assistant", "content": []any{map[string]any{"type": "text", "text": text}}}}
		if link != "" {
			message["user_message_uuid"] = link
		}
		send(message)
		previous = link
		if text == "hold" {
			continue
		}
		if text == "ambiguous" {
			send(map[string]any{"type": "result", "subtype": "success", "session_id": "claude-continuity"})
			continue
		}
		if text == "conflicting" {
			send(map[string]any{"type": "result", "subtype": "success", "session_id": "claude-continuity", "uuid": "result-1", "user_message_uuid": link})
			continue
		}
		result(link)
	}
}
