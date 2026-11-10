// Owned synthetic native boundary. No model, tool, or credentials are used.
package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
)

func main() {
	log, err := os.Create(os.Getenv("PEER_EVIDENCE"))
	if err != nil {
		panic(err)
	}
	defer log.Close()
	record := json.NewEncoder(log)
	enc := json.NewEncoder(os.Stdout)
	send := func(v any) {
		if err := enc.Encode(v); err != nil {
			panic(err)
		}
	}
	cwd, _ := os.Getwd()
	id, link, n := "continuous", "", 0
	claude := os.Getenv("PEER_KIND") == "claude"
	notify := func(method string, p any) { send(map[string]any{"method": method, "params": p}) }
	finish := func() {
		if claude {
			send(map[string]any{"type": "assistant", "session_id": id, "user_message_uuid": link, "message": map[string]any{"id": fmt.Sprintf("message-%d", n), "role": "assistant", "content": []any{map[string]any{"type": "text", "text": fmt.Sprintf("complete-%d", n), "apiBlockIndex": 0}}}})
			send(map[string]any{"type": "result", "subtype": "success", "session_id": id, "uuid": fmt.Sprintf("result-%d", n), "user_message_uuid": link})
				send(map[string]any{"type": "system", "subtype": "session_state_changed", "session_id": id, "state": "idle"})
		} else {
			notify("item/completed", map[string]any{"threadId": id, "turnId": fmt.Sprintf("turn-%d", n), "item": map[string]any{"id": fmt.Sprintf("message-%d", n), "type": "agentMessage", "text": fmt.Sprintf("complete-%d", n)}, "completedAtMs": 1})
			notify("turn/completed", map[string]any{"threadId": id, "turn": map[string]any{"id": fmt.Sprintf("turn-%d", n), "status": "completed", "items": []any{}}})
		}
	}
	scan := bufio.NewScanner(os.Stdin)
	for scan.Scan() {
		var v map[string]any
		if err := json.Unmarshal(scan.Bytes(), &v); err != nil {
			panic(err)
		}
		if claude {
			if v["type"] == "control_response" {
				r := v["response"].(map[string]any)
				a := r["response"].(map[string]any)
				if n != 2 || r["request_id"] != "permission-2" || a["behavior"] != "allow" || a["updatedInput"].(map[string]any)["command"] != "echo synthetic" {
					panic("unexpected Claude approval")
				}
				finish()
				continue
			}
			if v["type"] == "control_request" {
				r := v["request"].(map[string]any)
				send(map[string]any{"type": "control_response", "response": map[string]any{"subtype": "success", "request_id": v["request_id"], "response": map[string]any{}}})
				if r["subtype"] == "initialize" {
					send(map[string]any{"type": "system", "subtype": "init", "session_id": id, "cwd": cwd})
					send(map[string]any{"type": "system", "subtype": "session_state_changed", "session_id": id, "state": "idle"})
				}
				continue
			}
			if v["type"] != "user" {
				panic("unexpected Claude input")
			}
			n++
			link, _ = v["uuid"].(string)
		} else {
			if v["method"] == nil && v["id"] == "permission-2" {
				if n != 2 || v["result"].(map[string]any)["decision"] != "accept" {
					panic("unexpected Codex approval")
				}
				finish()
				continue
			}
			respond := func(r any) { send(map[string]any{"id": v["id"], "result": r}) }
			switch v["method"] {
			case "initialize":
				respond(map[string]any{"userAgent": "synthetic", "codexHome": "/synthetic", "platformFamily": "unix", "platformOs": "linux"})
				continue
			case "initialized":
				continue
			case "thread/start":
				cwd = v["params"].(map[string]any)["cwd"].(string)
				respond(map[string]any{"thread": map[string]any{"id": id, "sessionId": id, "turns": []any{}, "cliVersion": "synthetic", "createdAt": 1, "updatedAt": 1, "cwd": cwd, "ephemeral": false, "modelProvider": "synthetic", "preview": "", "projectId": nil, "source": "appServer", "status": map[string]any{"type": "idle"}}, "approvalPolicy": "on-request", "approvalsReviewer": "user", "cwd": cwd, "model": "synthetic", "modelProvider": "synthetic", "sandbox": map[string]any{"type": "readOnly"}})
				notify("thread/status/changed", map[string]any{"threadId": id, "status": map[string]any{"type": "idle"}})
				continue
			case "thread/read":
				respond(map[string]any{"thread": map[string]any{"id": id, "sessionId": id, "turns": []any{}, "cliVersion": "synthetic", "createdAt": 1, "updatedAt": 1, "cwd": cwd, "ephemeral": false, "modelProvider": "synthetic", "preview": "", "projectId": nil, "source": "appServer", "status": map[string]any{"type": "idle"}}})
				continue
			case "thread/unsubscribe":
				respond(map[string]any{"status": "unsubscribed"})
				notify("thread/closed", map[string]any{"threadId": id})
				continue
			case "turn/start":
				n++
				turn := map[string]any{"id": fmt.Sprintf("turn-%d", n), "status": "inProgress", "items": []any{}}
				respond(map[string]any{"turn": turn})
				notify("turn/started", map[string]any{"threadId": id, "turn": turn})
			default:
				panic("unexpected Codex input")
			}
		}
		if err := record.Encode(map[string]any{"pid": os.Getpid(), "input": n}); err != nil {
			panic(err)
		}
		if n == 2 {
			if claude {
				send(map[string]any{"type": "control_request", "request_id": "permission-2", "user_message_uuid": link, "request": map[string]any{"subtype": "can_use_tool", "tool_name": "Bash", "tool_use_id": "tool-2", "input": map[string]any{"command": "echo synthetic"}}})
			} else {
				send(map[string]any{"id": "permission-2", "method": "item/commandExecution/requestApproval", "params": map[string]any{"threadId": id, "turnId": "turn-2", "itemId": "tool-2", "startedAtMs": 3}})
			}
		} else {
			finish()
		}
	}
}
