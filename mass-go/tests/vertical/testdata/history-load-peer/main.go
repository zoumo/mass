// Owned synthetic JSONL server. No agent, credentials, or model calls.
package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
)

func main() {
	enc := json.NewEncoder(os.Stdout)
	send := func(v any) {
		if err := enc.Encode(v); err != nil {
			panic(err)
		}
	}
	passes, resumes := 0, 0
	mode := os.Getenv("LOAD_MODE")
	scan := bufio.NewScanner(os.Stdin)
	thread := func() any {
		return map[string]any{"id": "native-thread", "sessionId": "native-session", "cliVersion": "synthetic", "createdAt": 1, "updatedAt": 2, "cwd": os.Getenv("LOAD_CWD"), "ephemeral": false, "modelProvider": "synthetic", "preview": "not transcript", "projectId": nil, "source": "appServer", "status": map[string]any{"type": "idle"}, "turns": []any{}}
	}
	for scan.Scan() {
		var v struct {
			ID     any            `json:"id"`
			Method string         `json:"method"`
			Params map[string]any `json:"params"`
		}
		if err := json.Unmarshal(scan.Bytes(), &v); err != nil {
			panic(err)
		}
		respond := func(r any) { send(map[string]any{"id": v.ID, "result": r}) }
		switch v.Method {
		case "initialize":
			respond(map[string]any{"userAgent": "synthetic", "codexHome": "/synthetic", "platformFamily": "unix", "platformOs": "linux"})
		case "initialized":
		case "thread/read":
			respond(map[string]any{"thread": thread()})
		case "thread/resume":
			resumes++
			if resumes > 1 {
				panic("load retried resume")
			}
			respond(map[string]any{"thread": thread(), "approvalPolicy": "on-request", "approvalsReviewer": "user", "cwd": os.Getenv("LOAD_CWD"), "model": "synthetic", "modelProvider": "synthetic", "sandbox": map[string]any{"type": "readOnly"}})
		case "thread/turns/list":
			if v.Params["cursor"] == nil {
				passes++
			}
			if v.Params["sortDirection"] != "asc" || v.Params["itemsView"] != "full" {
				panic("must request full ascending turns")
			}
			data := []any{}
			var next any
			if v.Params["cursor"] == nil {
				data = []any{map[string]any{"id": "turn-z", "status": "completed", "items": []any{}, "itemsView": "full"}}
				next = "turn/+:next"
			} else if v.Params["cursor"] == "turn/+:next" {
				data = []any{map[string]any{"id": "turn-a", "status": "completed", "items": []any{}, "itemsView": "full"}}
			} else {
				panic("cursor changed")
			}
			respond(map[string]any{"data": data, "nextCursor": next})
		case "thread/items/list":
			if v.Params["sortDirection"] != "asc" {
				panic("must request ascending items")
			}
			tid := v.Params["turnId"].(string)
			data := []any{}
			var next any
			if tid == "turn-z" {
				if v.Params["cursor"] == nil {
					data = []any{map[string]any{"turnId": tid, "item": map[string]any{"id": "first", "type": "userMessage", "content": []any{map[string]any{"type": "text", "text": "saved first", "text_elements": []any{}}}}}}
					next = "item/+:next"
				} else if v.Params["cursor"] == "item/+:next" {
					data = []any{map[string]any{"turnId": tid, "item": map[string]any{"id": "answer", "type": "agentMessage", "text": "saved answer"}}}
				} else {
					panic("item cursor changed")
				}
			} else {
				data = []any{map[string]any{"turnId": tid, "item": map[string]any{"id": "last", "type": "agentMessage", "text": "saved last"}}}
			}

			if mode == "rich" && tid == "turn-a" {
				data = []any{}
				for _, item := range []map[string]any{
					{"id": "thought", "type": "reasoning", "content": []any{"retained thought"}, "summary": []any{"separate summary"}},
					{"id": "shell", "type": "commandExecution", "command": "printf ok", "cwd": os.Getenv("LOAD_CWD"), "commandActions": []any{}, "status": "completed", "aggregatedOutput": "ok", "exitCode": 0, "durationMs": 1},
					{"id": "no-output", "type": "commandExecution", "command": "true", "cwd": os.Getenv("LOAD_CWD"), "commandActions": []any{}, "status": "completed"},
					{"id": "null-output", "type": "commandExecution", "command": "true", "cwd": os.Getenv("LOAD_CWD"), "commandActions": []any{}, "status": "completed", "aggregatedOutput": nil},
					{"id": "empty-output", "type": "commandExecution", "command": "true", "cwd": os.Getenv("LOAD_CWD"), "commandActions": []any{}, "status": "completed", "aggregatedOutput": ""},
					{"id": "mcp", "type": "mcpToolCall", "server": "synthetic", "tool": "lookup", "arguments": map[string]any{"number": 9007199254740993}, "status": "completed", "result": map[string]any{"content": []any{map[string]any{"type": "text", "text": "mcp saved"}}}},
					{"id": "dynamic", "type": "dynamicToolCall", "tool": "lookup", "arguments": nil, "status": "completed", "success": true, "contentItems": []any{map[string]any{"type": "inputText", "text": "dynamic saved"}}},
				} {
					data = append(data, map[string]any{"turnId": tid, "item": item})
				}
			}

			if tid == "turn-a" {
				switch mode {
				case "unsupported":
					data = []any{map[string]any{"turnId": tid, "item": map[string]any{"id": "unsupported", "type": "futureContent", "text": "must not disappear"}}}
				case "parent":
					data = []any{map[string]any{"turnId": "wrong-parent", "item": map[string]any{"id": "last", "type": "agentMessage", "text": "wrong scope"}}}
				case "drift":
					if passes > 1 {
						data = []any{map[string]any{"turnId": tid, "item": map[string]any{"id": "last", "type": "agentMessage", "text": "changed within same second"}}}
					}
				case "omitted":
					respond(map[string]any{"data": data})
					continue
				case "loop":
					next = "item/+:again"
				}
			}
			respond(map[string]any{"data": data, "nextCursor": next})
		case "thread/unsubscribe":
			respond(map[string]any{"status": "unsubscribed"})
		case "turn/start":
			if v.Params["threadId"] != "native-thread" {
				panic("replaced connection")
			}
			turn := map[string]any{"id": "continued", "status": "inProgress", "items": []any{}}
			respond(map[string]any{"turn": turn})
			send(map[string]any{"method": "item/completed", "params": map[string]any{"threadId": "native-thread", "turnId": "continued", "completedAtMs": 3, "item": map[string]any{"id": "live", "type": "agentMessage", "text": "same resumed connection"}}})
			send(map[string]any{"method": "turn/completed", "params": map[string]any{"threadId": "native-thread", "turn": map[string]any{"id": "continued", "status": "completed", "items": []any{}}}})
		default:
			panic(fmt.Sprintf("unexpected method %s", v.Method))
		}
	}
}
