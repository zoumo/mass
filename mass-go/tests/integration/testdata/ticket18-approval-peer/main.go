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
	childAnswered := false
	childRejected := false
	for scan.Scan() {
		var v struct {
			ID     any             `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
			Result json.RawMessage `json:"result"`
			Error  json.RawMessage `json:"error"`
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
		case "thread/read":
			var params struct{ ThreadID string }
			if err := json.Unmarshal(v.Params, &params); err != nil {
				panic(err)
			}
			reply(map[string]any{"thread": map[string]any{"id": params.ThreadID, "sessionId": params.ThreadID, "cliVersion": "synthetic", "createdAt": 1, "updatedAt": 2, "cwd": cwd, "ephemeral": false, "modelProvider": "synthetic", "preview": "", "projectId": nil, "source": "appServer", "status": map[string]any{"type": "idle"}, "turns": []any{}}})
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
			if len(v.Error) != 0 {
				if len(v.Result) != 0 {
					panic("answer cannot contain both result and error")
				}
				var rpcError struct {
					Code    int64  `json:"code"`
					Message string `json:"message"`
				}
				if err := json.Unmarshal(v.Error, &rpcError); err != nil {
					panic(err)
				}
				if rpcError.Code != -32602 || rpcError.Message == "" {
					panic(fmt.Sprintf("unexpected answer rejection: %+v", rpcError))
				}
				switch v.ID {
				case "child-request":
					if childAnswered || childRejected {
						panic("child approval was rejected more than once")
					}
					childRejected = true
				case "approval-request":
					if childAnswered || !childRejected {
						panic("unexpected root approval rejection")
					}
				case "foreign-request", "wrong-work-request", "retired-request":
				default:
					panic(fmt.Sprintf("unexpected rejected answer ID %v", v.ID))
				}
				continue
			}
			if v.ID == "child-request" {
				if childAnswered || len(v.Result) == 0 {
					panic("child approval was not answered exactly once")
				}
				childAnswered = true
				notify("serverRequest/resolved", map[string]any{"threadId": "approval-child", "requestId": "child-request"})
				continue
			}
			if v.ID != "approval-request" || len(v.Result) == 0 {
				panic("answer did not use original native request")
			}
			if !childAnswered {
				panic("root approval was answered before child approval")
			}
			if err := os.WriteFile(filepath.Join(dir, "answer.json"), v.Result, 0o600); err != nil {
				panic(err)
			}
			notify("turn/completed", map[string]any{"threadId": "approval-session", "turn": map[string]any{"id": "approval-turn", "status": "completed", "items": []any{}}})
			request("retired-request", "approval-session", "approval-turn")
		default:
			panic("unexpected method " + v.Method)
		}
	}
	if err := scan.Err(); err != nil {
		panic(err)
	}
}
