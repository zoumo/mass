// Owned synthetic JSONL server. No agent, credentials, or model calls.
package main

import (
	"bufio"
	"encoding/json"
	"os"
	"strings"
)

func main() {
	enc := json.NewEncoder(os.Stdout)
	send := func(v any) {
		if err := enc.Encode(v); err != nil {
			panic(err)
		}
	}
	passes, resumes, reads := 0, 0, 0
	mode := os.Getenv("LOAD_MODE")
	evidence := func(method string, params any) {
		if path := os.Getenv("LOAD_EVIDENCE"); path != "" {
			f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
			if err != nil {
				panic(err)
			}
			defer f.Close()
			if err := json.NewEncoder(f).Encode(map[string]any{"method": method, "params": params, "pid": os.Getpid()}); err != nil {
				panic(err)
			}
		}
	}
	defer evidence("released", nil)
	scan := bufio.NewScanner(os.Stdin)
	thread := func() any {
		cwd := os.Getenv("LOAD_CWD")
		if mode == "identity" {
			cwd += "/wrong"
		}
		nativeSession := "native-session"
		if mode == "resume-drift" && resumes > 0 {
			nativeSession = "different-session"
		}
		status := "idle"
		if strings.HasPrefix(mode, "cold") && resumes == 0 {
			status = "notLoaded"
		}
		nativeStatus := map[string]any{"type": status}
		if strings.HasPrefix(mode, "cold") {
			nativeStatus["retainedExtension"] = "unchanged"
		}
		t := map[string]any{"id": "native-thread", "sessionId": nativeSession, "cliVersion": "synthetic", "createdAt": 1, "updatedAt": 2, "cwd": cwd, "ephemeral": false, "modelProvider": "synthetic", "preview": "not transcript", "projectId": nil, "source": "appServer", "status": nativeStatus, "turns": []any{}}
		if strings.HasPrefix(mode, "cold") {
			t["path"] = "/synthetic/rollouts/native-thread.jsonl"
		}
		if strings.HasPrefix(mode, "cold-materialized") {
			t["environments"] = nil
			if resumes > 0 {
				t["updatedAt"] = 3
				t["environments"] = []any{map[string]any{"cwd": cwd, "environmentId": "owned-local", "runtimeWorkspaceRoots": []any{cwd}}}
				if strings.HasPrefix(mode, "cold-materialized-empty") {
					t["environments"] = []any{}
				}
			}
		}
		if mode == "cold-timestamp" && resumes > 0 {
			t["updatedAt"] = 3
		}
		if strings.HasPrefix(mode, "loaded:") {
			t["environments"] = nil
		}
		stage := "pre"
		if resumes > 0 {
			stage = "resume"
		}
		if reads > 1 {
			stage = "after"
		}
		if parts := strings.Split(mode, ":"); len(parts) == 3 && parts[1] == stage {
			switch parts[2] {
			case "active":
				nativeStatus["type"], nativeStatus["activeFlags"] = "active", []any{}
			case "idle", "unknown", "notLoaded", "systemError":
				nativeStatus["type"] = parts[2]
			case "statusMetadata":
				nativeStatus["retainedExtension"] = "changed"
			case "id", "sessionId", "cwd", "modelProvider", "preview":
				t[parts[2]] = "different"
			case "source":
				t["source"] = "cli"
				if stage == "pre" {
					t["source"] = "unknown"
				}
			case "createdAt", "updatedAt":
				t[parts[2]] = 3
			case "updatedAtRegression":
				t["updatedAt"] = 1
			case "updatedAtNegative":
				t["updatedAt"] = -1
			case "updatedAtBeforeCreated":
				t["updatedAt"] = 0
			case "updatedAtAdvance":
				t["updatedAt"] = 4
			case "environmentsMissing":
				delete(t, "environments")
			case "environmentsNull":
				t["environments"] = nil
			case "environmentsEmpty":
				t["environments"] = []any{}
			case "environmentUnknown":
				t["environments"] = []any{map[string]any{"cwd": cwd, "environmentId": "owned-local", "runtimeWorkspaceRoots": []any{cwd}, "futureMetadata": "must remain checked"}}
			case "ephemeral":
				t["ephemeral"] = true
			case "path":
				t["path"] = "/synthetic/rollouts/different.jsonl"
			case "outsideHome":
				t["path"] = "/different-home/native-thread.jsonl"
			case "relativePath":
				t["path"] = "relative/native-thread.jsonl"
			case "metadata":
				t["futureMetadata"] = "must remain checked"
			default:
				panic("unknown cold fault")
			}
		}
		return t
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
		evidence(v.Method, v.Params)
		respond := func(r any) {
			if v.Method == "thread/read" || v.Method == "thread/resume" {
				evidence(v.Method+"/result", r)
			}
			send(map[string]any{"id": v.ID, "result": r})
		}
		switch v.Method {
		case "initialize":
			respond(map[string]any{"userAgent": "synthetic", "codexHome": "/synthetic", "platformFamily": "unix", "platformOs": "linux"})
		case "initialized":
		case "thread/read":
			reads++
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
			var data []any
			var next any
			switch v.Params["cursor"] {
			case nil:
				view := "full"
				if mode == "summary" {
					view = "summary"
				}
				data = []any{map[string]any{"id": "turn-z", "status": "completed", "items": []any{}, "itemsView": view}}
				next = "turn/+:next"
			case "turn/+:next":
				data = []any{map[string]any{"id": "turn-a", "status": "completed", "items": []any{}, "itemsView": "full"}}
			default:
				panic("cursor changed")
			}
			respond(map[string]any{"data": data, "nextCursor": next})
		case "thread/items/list":
			if v.Params["sortDirection"] != "asc" {
				panic("must request ascending items")
			}
			tid := v.Params["turnId"].(string)
			var data []any
			var next any
			if tid == "turn-z" {
				switch v.Params["cursor"] {
				case nil:
					data = []any{map[string]any{"turnId": tid, "item": map[string]any{"id": "first", "type": "userMessage", "content": []any{map[string]any{"type": "text", "text": "saved first", "text_elements": []any{}}}}}}
					next = "item/+:next"
				case "item/+:next":
					data = []any{map[string]any{"turnId": tid, "item": map[string]any{"id": "answer", "type": "agentMessage", "text": "saved answer"}}}
				default:
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
					{"id": "truncated-output", "type": "commandExecution", "command": "no-run", "cwd": os.Getenv("LOAD_CWD"), "commandActions": []any{}, "status": "completed", "aggregatedOutput": "partial", "truncated": true},
					{"id": "mcp", "type": "mcpToolCall", "server": "synthetic", "tool": "lookup", "arguments": map[string]any{"number": 9007199254740993}, "status": "completed", "result": map[string]any{"content": []any{map[string]any{"type": "text", "text": "mcp saved"}}}},
					{"id": "dynamic", "type": "dynamicToolCall", "tool": "lookup", "arguments": nil, "status": "completed", "success": true, "contentItems": []any{map[string]any{"type": "inputText", "text": "dynamic saved"}}},
				} {
					data = append(data, map[string]any{"turnId": tid, "item": item})
				}
			}

			if tid == "turn-a" {
				switch mode {
				case "repeat":
					data = []any{map[string]any{"turnId": tid, "item": map[string]any{"id": "first", "type": "agentMessage", "text": "legitimate repeated native ID"}}}
				case "valid-unsupported":
					data = []any{map[string]any{"turnId": tid, "item": map[string]any{"id": "sleep", "type": "sleep", "durationMs": 1}}}
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
			panic("unexpected method " + v.Method)
		}
	}
}
