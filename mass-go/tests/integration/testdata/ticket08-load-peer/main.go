// Deterministic native protocol peer, not a real provider or recording.
package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"

	"github.com/zoumo/mass/mass-go/tests/integration/testdata/clipeer"
)

func main() {
	if clipeer.Version() {
		return
	}
	if os.Getenv("LOAD_PROVIDER") == "claude" {
		claude()
		return
	}
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
	record("opened", map[string]any{})
	defer record("released", map[string]any{})
	enc := json.NewEncoder(os.Stdout)
	send := func(v any) {
		if err := enc.Encode(v); err != nil {
			panic(err)
		}
	}
	thread := func() any {
		return map[string]any{"id": "native-thread", "sessionId": "native-session", "cliVersion": "synthetic", "createdAt": 1, "updatedAt": 2, "cwd": os.Getenv("LOAD_CWD"), "ephemeral": false, "modelProvider": "synthetic", "preview": "", "projectId": nil, "source": "appServer", "status": map[string]any{"type": "idle"}, "turns": []any{}}
	}
	n, resumes := 0, 0
	scan := bufio.NewScanner(os.Stdin)
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
		respond := func(r any) { send(map[string]any{"id": v.ID, "result": r}) }
		notify := func(method string, p map[string]any) {
			p["threadId"] = "native-thread"
			send(map[string]any{"method": method, "params": p})
		}
		switch v.Method {
		case "initialize":
			respond(map[string]any{"userAgent": "synthetic", "codexHome": "/synthetic", "platformFamily": "unix", "platformOs": "linux"})
		case "initialized":
		case "thread/read":
			respond(map[string]any{"thread": thread()})
		case "thread/resume":
			resumes++
			if resumes != 1 {
				panic("second resume")
			}
			record("resumed", map[string]any{"id": v.Params["threadId"]})
			respond(map[string]any{"thread": thread(), "approvalPolicy": "on-request", "approvalsReviewer": "user", "cwd": os.Getenv("LOAD_CWD"), "model": "synthetic", "modelProvider": "synthetic", "sandbox": map[string]any{"type": "readOnly"}})
			notify("thread/status/changed", map[string]any{"threadId": "native-thread", "status": map[string]any{"type": "idle"}})
		case "thread/turns/list":
			respond(map[string]any{"data": []any{map[string]any{"id": "saved", "status": "completed", "items": []any{}, "itemsView": "full"}}, "nextCursor": nil})
		case "thread/items/list":
			respond(map[string]any{"data": []any{
				map[string]any{"turnId": "saved", "item": map[string]any{"id": "first", "type": "userMessage", "content": []any{map[string]any{"type": "text", "text": "saved input", "text_elements": []any{}}}}},
				map[string]any{"turnId": "saved", "item": map[string]any{"id": "last", "type": "agentMessage", "text": "saved answer"}},
			}, "nextCursor": nil})
		case "turn/start":
			if resumes != 1 || v.Params["threadId"] != "native-thread" {
				panic("not retained resume")
			}
			n++
			turn := fmt.Sprintf("live-%d", n)
			record("input", map[string]any{"turn": turn})
			respond(map[string]any{"turn": map[string]any{"id": turn, "status": "inProgress", "items": []any{}}})
			notify("turn/started", map[string]any{"turn": map[string]any{"id": turn, "status": "inProgress", "items": []any{}}})
			send(map[string]any{"id": "approval-" + turn, "method": "item/commandExecution/requestApproval", "params": map[string]any{"threadId": "native-thread", "turnId": turn, "itemId": "tool-" + turn, "startedAtMs": 3}})
		case "":
			turn := fmt.Sprintf("live-%d", n)
			if v.ID != "approval-"+turn || v.Result["decision"] != "accept" {
				panic("wrong live answer")
			}
			record("answer", map[string]any{"turn": turn, "decision": v.Result["decision"]})
			notify("item/completed", map[string]any{"turnId": turn, "completedAtMs": 3, "item": map[string]any{"id": turn, "type": "agentMessage", "text": "same retained connection"}})
			notify("turn/completed", map[string]any{"turn": map[string]any{"id": turn, "status": "completed", "items": []any{}}})
		case "thread/unsubscribe":
			record("unsubscribed", map[string]any{})
			respond(map[string]any{"status": "unsubscribed"})
		default:
			panic("unexpected method " + v.Method)
		}
	}
	if err := scan.Err(); err != nil {
		panic(err)
	}
}
