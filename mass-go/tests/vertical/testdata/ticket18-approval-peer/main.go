// Authored offline native protocol peer. No command, tool or model is executed.
package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

func main() {
	dir := os.Getenv("EVIDENCE")
	if err := os.WriteFile(filepath.Join(dir, "pid"), fmt.Appendf(nil, "%d", os.Getpid()), 0o600); err != nil {
		panic(err)
	}
	out := json.NewEncoder(os.Stdout)
	send := func(v any) {
		if err := out.Encode(v); err != nil {
			panic(err)
		}
	}
	notify := func(method string, params any) { send(map[string]any{"method": method, "params": params}) }
	var cwd string
	thread := func() any {
		return map[string]any{"id": "approval-session", "sessionId": "approval-session", "cliVersion": "synthetic", "createdAt": 1, "updatedAt": 2, "cwd": cwd, "ephemeral": false, "modelProvider": "synthetic", "preview": "", "projectId": nil, "source": "appServer", "status": map[string]any{"type": "idle"}, "turns": []any{}}
	}
	request := func(id, threadID, turnID string) {
		send(map[string]any{"id": id, "method": "item/commandExecution/requestApproval", "params": map[string]any{"threadId": threadID, "turnId": turnID, "itemId": id, "command": "never executed", "cwd": cwd, "startedAtMs": 3, "availableDecisions": json.RawMessage(os.Getenv("CHOICES"))}})
	}
	scan := bufio.NewScanner(os.Stdin)
	for scan.Scan() {
		var v struct {
			ID     any             `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
			Result json.RawMessage `json:"result"`
		}
		if err := json.Unmarshal(scan.Bytes(), &v); err != nil {
			panic(err)
		}
		reply := func(result any) { send(map[string]any{"id": v.ID, "result": result}) }
		switch v.Method {
		case "initialize":
			reply(map[string]any{"userAgent": "synthetic", "codexHome": "/synthetic", "platformFamily": "unix", "platformOs": "linux"})
		case "initialized":
		case "thread/start":
			var params struct{ Cwd string }
			if err := json.Unmarshal(v.Params, &params); err != nil {
				panic(err)
			}
			cwd = params.Cwd
			reply(map[string]any{"thread": thread(), "approvalPolicy": "on-request", "approvalsReviewer": "user", "cwd": cwd, "model": "synthetic", "modelProvider": "synthetic", "sandbox": map[string]any{"type": "readOnly"}})
		case "turn/start":
			turn := map[string]any{"id": "approval-turn", "status": "inProgress", "items": []any{}}
			reply(map[string]any{"turn": turn})
			notify("turn/started", map[string]any{"threadId": "approval-session", "turn": turn})
			child := thread().(map[string]any)
			child["id"], child["sessionId"], child["parentThreadId"] = "approval-child", "approval-child", "approval-session"
			notify("thread/started", map[string]any{"thread": child})
			request("child-request", "approval-child", "child-turn")
			request("foreign-request", "foreign-session", "approval-turn")
			request("wrong-work-request", "approval-session", "wrong-turn")
			request("approval-request", "approval-session", "approval-turn")
		case "thread/unsubscribe":
			reply(map[string]any{"status": "unsubscribed"})
			notify("thread/closed", map[string]any{"threadId": "approval-session"})
		case "turn/interrupt":
			reply(map[string]any{})
			notify("turn/completed", map[string]any{"threadId": "approval-session", "turn": map[string]any{"id": "approval-turn", "status": "interrupted", "items": []any{}}})
		case "":
			if v.ID != "approval-request" || len(v.Result) == 0 {
				panic("answer did not use original native request")
			}
			if err := os.WriteFile(filepath.Join(dir, "answer.json"), v.Result, 0o600); err != nil {
				panic(err)
			}
			notify("turn/completed", map[string]any{"threadId": "approval-session", "turn": map[string]any{"id": "approval-turn", "status": "completed", "items": []any{}}})
			request("retired-request", "approval-session", "approval-turn")
			notify("item/completed", map[string]any{"threadId": "approval-session", "turnId": "approval-turn", "completedAtMs": 4, "item": map[string]any{"id": "post-terminal-fence", "type": "agentMessage", "text": "all original and retired requests observed"}})
		default:
			panic("unexpected method " + v.Method)
		}
	}
	if err := scan.Err(); err != nil {
		panic(err)
	}
}
