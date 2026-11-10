// Authored offline native protocol peer; never runs a tool or model.
package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

func main() {
	evidence := os.Getenv("EVIDENCE")
	pid := strconv.Itoa(os.Getpid())
	if err := os.WriteFile(filepath.Join(evidence, "pid-"+pid), []byte(pid), 0o600); err != nil {
		panic(err)
	}
	out := json.NewEncoder(os.Stdout)
	send := func(v any) {
		if err := out.Encode(v); err != nil {
			panic(err)
		}
	}
	cwd, err := os.Getwd()
	if err != nil {
		panic(err)
	}
	if os.Getenv("KIND") == "claude" {
		runClaude(send, cwd)
		return
	}
	runCodex(send)
}

func runCodex(send func(any)) {
	notify := func(method string, params any) { send(map[string]any{"method": method, "params": params}) }
	cwds := map[string]any{}
	thread := func(id string) map[string]any {
		cwd := cwds[id]
		if cwd == nil {
			cwd = os.Getenv("PROJECT")
		}
		return map[string]any{"id": id, "sessionId": id, "cliVersion": "synthetic", "createdAt": 1, "updatedAt": 2, "cwd": cwd, "ephemeral": false, "modelProvider": "synthetic", "preview": "", "projectId": nil, "source": "appServer", "status": map[string]any{"type": "idle"}, "turns": []any{}}
	}
	fact := func(id, item string) {
		notify("item/completed", map[string]any{"threadId": id, "turnId": "turn-" + id, "completedAtMs": 3, "item": map[string]any{"id": item, "type": "agentMessage", "text": "complete " + item}})
	}
	request := func(id, name string) {
		send(map[string]any{"id": name, "method": "item/commandExecution/requestApproval", "params": map[string]any{"threadId": id, "turnId": "turn-" + id, "itemId": name, "command": "never executed", "cwd": os.Getenv("PROJECT"), "startedAtMs": 3, "availableDecisions": []any{"accept", "decline"}}})
	}
	scan := bufio.NewScanner(os.Stdin)
	opened := 0
	for scan.Scan() {
		var v struct {
			ID     any            `json:"id"`
			Method string         `json:"method"`
			Params map[string]any `json:"params"`
			Result map[string]any `json:"result"`
		}
		if err := json.Unmarshal(scan.Bytes(), &v); err != nil {
			panic(err)
		}
		reply := func(r any) { send(map[string]any{"id": v.ID, "result": r}) }
		if v.Method == "" {
			if v.Result["decision"] == "accept" {
				panic("stale permission reached native peer")
			}
			continue
		}
		switch v.Method {
		case "initialize":
			reply(map[string]any{"userAgent": "synthetic", "codexHome": "/synthetic", "platformFamily": "unix", "platformOs": "linux"})
		case "initialized":
		case "thread/start":
			opened++
			id := "active"
			if opened > 1 {
				id = "healthy"
			}
			cwds[id] = v.Params["cwd"]
			reply(map[string]any{"thread": thread(id), "approvalPolicy": "on-request", "approvalsReviewer": "user", "cwd": os.Getenv("PROJECT"), "model": "synthetic", "modelProvider": "synthetic", "sandbox": map[string]any{"type": "readOnly"}})
			notify("thread/status/changed", map[string]any{"threadId": id, "status": map[string]any{"type": "idle"}})
		case "thread/read":
			reply(map[string]any{"thread": thread(v.Params["threadId"].(string))})
		case "thread/resume":
			id := v.Params["threadId"].(string)
			reply(map[string]any{"thread": thread(id), "approvalPolicy": "on-request", "approvalsReviewer": "user", "cwd": os.Getenv("PROJECT"), "model": "synthetic", "modelProvider": "synthetic", "sandbox": map[string]any{"type": "readOnly"}})
			notify("thread/status/changed", map[string]any{"threadId": id, "status": map[string]any{"type": "idle"}})
		case "thread/turns/list":
			reply(map[string]any{"data": []any{map[string]any{"id": "saved-turn", "status": "completed", "items": []any{}, "itemsView": "full"}}, "nextCursor": nil})
		case "thread/items/list":
			reply(map[string]any{"data": []any{map[string]any{"turnId": "saved-turn", "item": map[string]any{"id": "saved-first", "type": "agentMessage", "text": "retained first"}}, map[string]any{"turnId": "saved-turn", "item": map[string]any{"id": "saved-last", "type": "agentMessage", "text": "retained last"}}}, "nextCursor": nil})
		case "turn/start":
			id := v.Params["threadId"].(string)
			turn := map[string]any{"id": "turn-" + id, "status": "inProgress", "items": []any{}}
			reply(map[string]any{"turn": turn})
			notify("turn/started", map[string]any{"threadId": id, "turn": turn})
			if id == "active" {
				request(id, "current")
				fact(id, "before-close")
				request(id, "queued")
				fact(id, "queued-last")
			} else {
				fact(id, "healthy-fact")
				notify("turn/completed", map[string]any{"threadId": id, "turn": map[string]any{"id": "turn-" + id, "status": "completed", "items": []any{}}})
			}
		case "turn/interrupt":
			id := v.Params["threadId"].(string)
			if v.Params["turnId"] != "turn-"+id {
				panic("wrong interrupt target")
			}
			reply(map[string]any{})
			fact(id, "cancel-final")
			notify("turn/completed", map[string]any{"threadId": id, "turn": map[string]any{"id": "turn-" + id, "status": "interrupted", "items": []any{}}})
		case "thread/unsubscribe":
			reply(map[string]any{"status": "unsubscribed"})
		default:
			panic("unexpected method " + v.Method)
		}
	}
}

func runClaude(send func(any), cwd string) {
	id := "claude-" + filepath.Base(cwd)
	for _, arg := range os.Args[1:] {
		if resumed, ok := strings.CutPrefix(arg, "--resume="); ok {
			id = resumed
		}
	}
	var link string
	fact := func(item string) {
		send(map[string]any{"type": "assistant", "session_id": id, "user_message_uuid": link, "message": map[string]any{"id": item, "role": "assistant", "content": []any{map[string]any{"type": "text", "text": "complete " + item, "apiBlockIndex": 0}}}})
	}
	result := func() {
		send(map[string]any{"type": "result", "subtype": "success", "session_id": id, "user_message_uuid": link, "uuid": "result-" + link})
	}
	request := func(name string) {
		send(map[string]any{"type": "control_request", "request_id": name, "request": map[string]any{"subtype": "can_use_tool", "tool_name": "Bash", "tool_use_id": name, "input": map[string]any{"command": "never executed"}}})
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
			if r["subtype"] == "initialize" {
				send(map[string]any{"type": "system", "subtype": "init", "session_id": id, "cwd": cwd, "tools": []any{}})
				send(map[string]any{"type": "system", "subtype": "session_state_changed", "session_id": id, "state": "idle"})
			}
			if r["subtype"] == "interrupt" {
				fact("cancel-final")
				result()
			}
		case "user":
			link, _ = v["uuid"].(string)
			send(map[string]any{"type": "stream_event", "session_id": id, "user_message_uuid": link, "event": map[string]any{"type": "message_start", "message": map[string]any{"id": "active", "role": "assistant", "content": []any{}}}})
			if filepath.Base(cwd) == "active" {
				request("current")
				fact("before-close")
				request("queued")
				fact("queued-last")
			} else {
				fact("healthy-fact")
				result()
			}
		case "control_response":
			raw, err := json.Marshal(v)
			if err != nil {
				panic(err)
			}
			if strings.Contains(string(raw), `"behavior":"allow"`) {
				panic("stale permission reached native peer")
			}
		default:
			panic(fmt.Sprintf("unexpected frame %v", v["type"]))
		}
	}
}
