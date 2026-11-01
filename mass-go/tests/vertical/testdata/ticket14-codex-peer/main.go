// Authored native protocol peer: no SDK replacement, model, or tool execution.
package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"time"
)

func main() {
	dir := os.Getenv("EVIDENCE")
	must(os.WriteFile(filepath.Join(dir, "pid"), []byte(strconv.Itoa(os.Getpid())), 0600))
	defer func() { must(os.WriteFile(filepath.Join(dir, "stdin-closed"), []byte("EOF"), 0600)) }()
	enc := json.NewEncoder(os.Stdout)
	send := func(v any) { must(enc.Encode(v)) }
	log, err := os.OpenFile(filepath.Join(dir, "wire.jsonl"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	must(err)
	defer log.Close()
	record := json.NewEncoder(log)
	scan := bufio.NewScanner(os.Stdin)
	opened := 0
	for scan.Scan() {
		var v struct {
			ID     any            `json:"id"`
			Method string         `json:"method"`
			Params map[string]any `json:"params"`
		}
		must(json.Unmarshal(scan.Bytes(), &v))
		must(record.Encode(v))
		reply := func(result any) { send(map[string]any{"id": v.ID, "result": result}) }
		notify := func(method string, p any) { send(map[string]any{"method": method, "params": p}) }
		switch v.Method {
		case "initialize":
			reply(map[string]any{"userAgent": "synthetic", "codexHome": "/synthetic", "platformFamily": "unix", "platformOs": "linux"})
		case "initialized":
		case "thread/start":
			opened++
			id := fmt.Sprintf("native-%d", opened)
			reply(map[string]any{"thread": map[string]any{"id": id, "sessionId": id, "turns": []any{}, "cliVersion": "synthetic", "createdAt": 1, "updatedAt": 1, "cwd": v.Params["cwd"], "ephemeral": false, "modelProvider": "synthetic", "preview": "", "projectId": nil, "source": "appServer", "status": map[string]any{"type": "idle"}}, "approvalPolicy": "on-request", "approvalsReviewer": "user", "cwd": v.Params["cwd"], "model": "synthetic", "modelProvider": "synthetic", "sandbox": map[string]any{"type": "readOnly"}})
		case "turn/start":
			id := v.Params["threadId"].(string)
			if id == "native-1" {
				must(os.WriteFile(filepath.Join(dir, "dispatched"), []byte("turn/start received; response held"), 0600))
				for {
					if _, err := os.Stat(filepath.Join(dir, "release")); err == nil {
						break
					}
					time.Sleep(10 * time.Millisecond)
				}
			}
			// Facts deliberately precede the acceptance response on the same connection.
			notify("turn/started", map[string]any{"threadId": id, "turn": map[string]any{"id": "turn-" + id, "status": "inProgress", "items": []any{}}})
			notify("item/completed", map[string]any{"threadId": id, "turnId": "turn-" + id, "completedAtMs": 1, "item": map[string]any{"id": "answer-" + id, "type": "agentMessage", "text": "owned late answer"}})
			notify("turn/completed", map[string]any{"threadId": id, "turn": map[string]any{"id": "turn-" + id, "status": "completed", "items": []any{}}})
			reply(map[string]any{"turn": map[string]any{"id": "turn-" + id, "status": "completed", "items": []any{}}})
		case "thread/unsubscribe":
			reply(map[string]any{"status": "unsubscribed"})
			notify("thread/closed", map[string]any{"threadId": v.Params["threadId"]})
		default:
			panic("unexpected native method " + v.Method)
		}
	}
	must(scan.Err())
}
func must(err error) {
	if err != nil {
		panic(err)
	}
}
