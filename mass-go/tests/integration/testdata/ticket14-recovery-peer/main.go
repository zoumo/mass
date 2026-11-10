// Authored native protocol and saved-history peer; no models or SDK replacement.
package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const sessionID = "14141414-1414-4141-8141-141414141414"

func main() {
	if len(os.Args) == 2 && os.Args[1] == "--version" {
		fmt.Println("2.1.258 (Claude Code)")
		return
	}
	dir := os.Getenv("PEER_DIR")
	cwd, err := os.Getwd()
	must(err)
	if os.Getenv("PEER_KIND") == "codex" {
		cwd = os.Getenv("WORK_DIR")
	}
	wire, err := os.OpenFile(filepath.Join(dir, "wire.jsonl"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	must(err)
	defer wire.Close()
	record := json.NewEncoder(wire)
	send := func(v any) { must(json.NewEncoder(os.Stdout).Encode(v)) }
	log := func(method string, params any) {
		must(record.Encode(map[string]any{"method": method, "params": params, "pid": os.Getpid()}))
	}
	log("opened", nil)
	defer log("released", nil)
	var saved []string
	if data, e := os.ReadFile(filepath.Join(dir, "saved.json")); e == nil {
		must(json.Unmarshal(data, &saved))
	} else if !os.IsNotExist(e) {
		must(e)
	}
	save := func(text string) int {
		saved = append(saved, text)
		data, e := json.Marshal(saved)
		must(e)
		must(os.WriteFile(filepath.Join(dir, "saved.json"), data, 0o600))
		return len(saved)
	}
	resumed := false
	for _, arg := range os.Args[1:] {
		resumed = resumed || strings.HasPrefix(arg, "--resume=")
	}
	mode := os.Getenv("RESUME_INPUT_MODE")
	init := func() {
		id, work := sessionID, cwd
		if resumed && mode == "wrong-id" {
			id = "20202020-2020-4020-8020-202020202020"
		}
		if resumed && mode == "wrong-cwd" {
			work = filepath.Dir(cwd)
		}
		log("init", map[string]any{"sessionId": id, "cwd": work})
		send(map[string]any{"type": "system", "subtype": "init", "session_id": id, "cwd": work})
		send(map[string]any{"type": "system", "subtype": "session_state_changed", "session_id": id, "state": "idle"})
	}
	thread := func() map[string]any {
		return map[string]any{"id": sessionID, "sessionId": sessionID, "cliVersion": "synthetic", "createdAt": 1, "updatedAt": 2, "cwd": cwd, "ephemeral": false, "modelProvider": "synthetic", "preview": "", "projectId": nil, "source": "appServer", "status": map[string]any{"type": "idle"}, "turns": []any{}}
	}
	scan := bufio.NewScanner(os.Stdin)
	for scan.Scan() {
		var v map[string]any
		must(json.Unmarshal(scan.Bytes(), &v))
		if os.Getenv("PEER_KIND") == "claude" {
			if v["type"] == "control_request" {
				r := v["request"].(map[string]any)
				log(r["subtype"].(string), nil)
				send(map[string]any{"type": "control_response", "response": map[string]any{"subtype": "success", "request_id": v["request_id"], "response": map[string]any{}}})
				if r["subtype"] == "initialize" && !(resumed && mode == "hold-init") {
					init()
				}
				continue
			}
			if v["type"] != "user" {
				panic("unexpected Claude frame")
			}
			content := v["message"].(map[string]any)["content"]
			text, scalar := content.(string)
			if !scalar {
				for _, block := range content.([]any) {
					text += block.(map[string]any)["text"].(string)
				}
			}
			log("input", map[string]any{"text": text, "content": content, "sessionId": sessionID, "submission": v["uuid"]})
			n := save(text)
			uid, aid := fmt.Sprintf("user-%d", n), fmt.Sprintf("answer-%d", n)
			path := os.Getenv("SAVED_TRANSCRIPT")
			f, e := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
			must(e)
			var parent any
			if n > 1 {
				parent = fmt.Sprintf("answer-%d", n-1)
			}
			rows := []map[string]any{
				{"type": "user", "uuid": uid, "parentUuid": parent, "message": map[string]any{"role": "user", "content": text}},
				{"type": "assistant", "uuid": aid, "parentUuid": uid, "message": map[string]any{"id": aid, "role": "assistant", "content": []any{map[string]any{"type": "text", "text": text, "apiBlockIndex": 0}}}},
			}
			for _, row := range rows {
				row["sessionId"], row["cwd"] = sessionID, cwd
				must(json.NewEncoder(f).Encode(row))
			}
			must(f.Close())
			send(map[string]any{"type": "system", "subtype": "session_state_changed", "session_id": sessionID, "state": "running"})
			send(map[string]any{"type": "assistant", "session_id": sessionID, "user_message_uuid": v["uuid"], "message": rows[1]["message"]})
			send(map[string]any{"type": "result", "subtype": "success", "session_id": sessionID, "uuid": fmt.Sprintf("result-%d", n), "user_message_uuid": v["uuid"]})
			send(map[string]any{"type": "system", "subtype": "session_state_changed", "session_id": sessionID, "state": "idle"})
			continue
		}
		method, _ := v["method"].(string)
		p, _ := v["params"].(map[string]any)
		log(method, p)
		reply := func(r any) { send(map[string]any{"id": v["id"], "result": r}) }
		notify := func(m string, p any) { send(map[string]any{"method": m, "params": p}) }
		switch method {
		case "initialize":
			reply(map[string]any{"userAgent": "synthetic", "codexHome": dir, "platformFamily": "unix", "platformOs": "linux"})
		case "initialized":
		case "thread/start", "thread/resume":
			if method == "thread/resume" && p["threadId"] != sessionID {
				panic("replacement thread")
			}
			reply(map[string]any{"thread": thread(), "approvalPolicy": "on-request", "approvalsReviewer": "user", "cwd": cwd, "model": "synthetic", "modelProvider": "synthetic", "sandbox": map[string]any{"type": "readOnly"}})
			notify("thread/status/changed", map[string]any{"threadId": sessionID, "status": map[string]any{"type": "idle"}})
		case "thread/read":
			reply(map[string]any{"thread": thread()})
		case "thread/turns/list":
			turns := []any{}
			for i := range saved {
				turns = append(turns, map[string]any{"id": fmt.Sprintf("turn-%d", i+1), "status": "completed", "items": []any{}, "itemsView": "full"})
			}
			reply(map[string]any{"data": turns, "nextCursor": nil})
		case "thread/items/list":
			var n int
			_, e := fmt.Sscanf(p["turnId"].(string), "turn-%d", &n)
			must(e)
			items := []any{
				map[string]any{"turnId": p["turnId"], "item": map[string]any{"id": fmt.Sprintf("user-%d", n), "type": "userMessage", "content": []any{map[string]any{"type": "text", "text": saved[n-1], "text_elements": []any{}}}}},
				map[string]any{"turnId": p["turnId"], "item": map[string]any{"id": fmt.Sprintf("answer-%d", n), "type": "agentMessage", "text": saved[n-1]}},
			}
			reply(map[string]any{"data": items, "nextCursor": nil})
		case "turn/start":
			if p["threadId"] != sessionID {
				panic("replacement thread input")
			}
			text := ""
			for _, block := range p["input"].([]any) {
				text += block.(map[string]any)["text"].(string)
			}
			log("input", map[string]any{"text": text, "content": p["input"], "sessionId": sessionID})
			n := save(text)
			tid := fmt.Sprintf("turn-%d", n)
			turn := map[string]any{"id": tid, "status": "inProgress", "items": []any{}}
			early := n == 2 && os.Getenv("RESUME_INPUT_MODE") == "terminal-before-receipt"
			if !early {
				reply(map[string]any{"turn": turn})
			}
			notify("turn/started", map[string]any{"threadId": sessionID, "turn": turn})
			notify("item/completed", map[string]any{"threadId": sessionID, "turnId": tid, "completedAtMs": n, "item": map[string]any{"id": fmt.Sprintf("answer-%d", n), "type": "agentMessage", "text": text}})
			notify("turn/completed", map[string]any{"threadId": sessionID, "turn": map[string]any{"id": tid, "status": "completed", "items": []any{}}})
			if early {
				must(os.WriteFile(filepath.Join(dir, "receipt-gated"), nil, 0o600))
				// Only the test's public runtime stream consumer releases this gate
				// after observing completion, not an elapsed scheduling interval.
				for {
					if _, err := os.Stat(filepath.Join(dir, "release-receipt")); err == nil {
						break
					}
					time.Sleep(time.Millisecond)
				}
				reply(map[string]any{"turn": turn})
			}
		case "thread/unsubscribe":
			reply(map[string]any{"status": "unsubscribed"})
		default:
			panic("unexpected native method " + method)
		}
	}
	must(scan.Err())
}

func must(err error) {
	if err != nil {
		panic(err)
	}
}
