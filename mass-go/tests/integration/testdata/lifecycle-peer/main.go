// Synthetic native protocol boundary. No tools, models, or user data are used.
package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
)

func main() {
	pidPath := os.Getenv("PEER_PID")
	if os.Getenv("PEER_CONTROL") == "overload" {
		pidPath += "." + strconv.Itoa(os.Getpid())
	}
	if err := os.WriteFile(pidPath, []byte(strconv.Itoa(os.Getpid())), 0o600); err != nil {
		panic(err)
	}
	enc := json.NewEncoder(os.Stdout)
	send := func(v any) {
		if err := enc.Encode(v); err != nil {
			panic(err)
		}
	}
	notify := func(method string, params any) { send(map[string]any{"method": method, "params": params}) }
	if os.Getenv("PEER_KIND") == "claude" {
		claude(send)
		return
	}
	threads, interrupts := 0, 0
	threadCwds := map[string]any{}
	scan := bufio.NewScanner(os.Stdin)
	for scan.Scan() {
		var v struct {
			ID     any            `json:"id"`
			Method string         `json:"method"`
			Params map[string]any `json:"params"`
		}
		if err := json.Unmarshal(scan.Bytes(), &v); err != nil {
			panic(err)
		}
		// The SDK owns answers to server requests. A response has no method and
		// must not be mistaken for an unexpected native request.
		if v.Method == "" {
			continue
		}
		respond := func(result any) { send(map[string]any{"id": v.ID, "result": result}) }
		switch v.Method {
		case "initialize":
			respond(map[string]any{"userAgent": "synthetic", "codexHome": "/synthetic", "platformFamily": "unix", "platformOs": "linux"})
		case "initialized":
		case "thread/start":
			threads++
			id := "healthy"
			if threads == 2 {
				id = "damaged"
			}
			threadCwds[id] = v.Params["cwd"]
			respond(map[string]any{"thread": map[string]any{"id": id, "sessionId": id, "turns": []any{}, "cliVersion": "synthetic", "createdAt": 1, "updatedAt": 1, "cwd": v.Params["cwd"], "ephemeral": false, "modelProvider": "synthetic", "preview": "", "projectId": nil, "source": "appServer", "status": map[string]any{"type": "idle"}}, "approvalPolicy": "on-request", "approvalsReviewer": "user", "cwd": v.Params["cwd"], "model": "synthetic", "modelProvider": "synthetic", "sandbox": map[string]any{"type": "readOnly"}})
		case "thread/read":
			id := v.Params["threadId"].(string)
			respond(map[string]any{"thread": map[string]any{"id": id, "sessionId": id, "turns": []any{}, "cliVersion": "synthetic", "createdAt": 1, "updatedAt": 1, "cwd": threadCwds[id], "ephemeral": false, "modelProvider": "synthetic", "preview": "", "projectId": nil, "source": "appServer", "status": map[string]any{"type": "idle"}}})
		case "thread/unsubscribe":
			status := "unsubscribed"
			if v.Params["threadId"] == "damaged" && os.Getenv("PEER_CONTROL") != "overload" {
				status = "unverified-release"
			}
			respond(map[string]any{"status": status})
			if os.Getenv("PEER_CONTROL") == "overload" {
				notify("thread/closed", map[string]any{"threadId": v.Params["threadId"]})
			}
		case "turn/start":
			id := v.Params["threadId"].(string)
			turnID := "native-turn-" + id
			turn := map[string]any{"id": turnID, "status": "inProgress", "items": []any{}}
			respond(map[string]any{"turn": turn})
			notify("turn/started", map[string]any{"threadId": id, "turn": turn})
			if os.Getenv("PEER_CONTROL") == "write-failure" {
				closedInput(func() {
					notify("item/completed", map[string]any{"threadId": id, "turnId": turnID, "completedAtMs": 1, "item": map[string]any{"id": "write-fault-ready", "type": "agentMessage", "text": "native input pipe closed"}})
				})
			}
			if os.Getenv("PEER_CONTROL") == "overload" {
				n := 1
				if id == "damaged" {
					n = 160
				}
				for i := range n {
					notify("item/completed", map[string]any{"threadId": id, "turnId": turnID, "item": map[string]any{"id": fmt.Sprintf("final-%d", i), "type": "agentMessage", "text": "complete synthetic fact"}, "completedAtMs": 1})
				}
				notify("turn/completed", map[string]any{"threadId": id, "turn": map[string]any{"id": turnID, "status": "completed", "items": []any{}}})
				continue
			}
			if id == "damaged" {
				for i := range 160 {
					notify("item/completed", map[string]any{"threadId": id, "turnId": turnID, "item": map[string]any{"id": fmt.Sprintf("final-%d", i), "type": "agentMessage", "text": "accepted before close"}, "completedAtMs": 1})
				}
				notify("turn/completed", map[string]any{"threadId": id, "turn": map[string]any{"id": turnID, "status": "completed", "items": []any{}}})
				send(map[string]any{"id": "queued-question", "method": "item/tool/requestUserInput", "params": map[string]any{"threadId": id, "turnId": turnID, "itemId": "late-question", "questions": []any{map[string]any{"id": "format", "header": "Format", "question": "Which format?", "isOther": false, "isSecret": false, "options": []any{map[string]any{"label": "json", "description": "JSON"}}}}}})
				// Malformed supported notification: the original protocol failure
				// must remain separate from the unknown unsubscribe result.
				notify("item/completed", map[string]any{"threadId": id, "turnId": turnID, "completedAtMs": 1})
			}
		case "turn/interrupt":
			if v.Params["threadId"] != "healthy" || v.Params["turnId"] != "native-turn-healthy" {
				panic("wrong native cancellation target")
			}
			interrupts++
			if interrupts == 1 && os.Getenv("PEER_CONTROL") == "unknown" {
				notify("item/completed", map[string]any{"threadId": "healthy", "turnId": "native-turn-healthy", "completedAtMs": 1, "item": map[string]any{"id": "interrupt-admitted", "type": "agentMessage", "text": "interrupt admitted; no acknowledgment"}})
			} else if interrupts > 1 || os.Getenv("PEER_CONTROL") == "rejected" {
				send(map[string]any{"id": v.ID, "error": map[string]any{"code": -32000, "message": "duplicate interrupt"}})
			} else {
				respond(map[string]any{})
			}
		default:
			panic("unexpected method: " + v.Method)
		}
	}
}

func claude(send func(any)) {
	sessionID := "claude-session"
	if os.Getenv("PEER_CONTROL") == "overload" {
		cwd, err := os.Getwd()
		if err != nil {
			panic(err)
		}
		sessionID = "claude-" + filepath.Base(cwd)
	}
	interrupts := 0
	scan := bufio.NewScanner(os.Stdin)
	for scan.Scan() {
		var v struct {
			Type    string `json:"type"`
			ID      string `json:"request_id"`
			Request struct {
				Subtype string `json:"subtype"`
			} `json:"request"`
		}
		if err := json.Unmarshal(scan.Bytes(), &v); err != nil {
			panic(err)
		}
		if v.Type == "user" {
			send(map[string]any{"type": "stream_event", "session_id": sessionID, "event": map[string]any{"type": "message_start", "message": map[string]any{"id": "active", "role": "assistant", "content": []any{}}}})
			if os.Getenv("PEER_CONTROL") == "write-failure" {
				closedInput(func() {
					send(map[string]any{"type": "assistant", "session_id": sessionID, "message": map[string]any{"id": "write-fault-ready", "role": "assistant", "content": []any{map[string]any{"type": "text", "text": "native input pipe closed", "apiBlockIndex": 0}}}})
				})
			}
			if os.Getenv("PEER_CONTROL") == "overload" {
				n := 1
				if sessionID == "claude-damaged" {
					n = 160
				}
				for i := range n {
					send(map[string]any{"type": "assistant", "session_id": sessionID, "message": map[string]any{"id": fmt.Sprintf("final-%d", i), "role": "assistant", "content": []any{map[string]any{"type": "text", "text": "complete synthetic fact", "apiBlockIndex": 0}}}})
				}
				send(map[string]any{"type": "result", "subtype": "success", "session_id": sessionID, "is_error": false, "result": "settled"})
				continue
			}
			if os.Getenv("PEER_CONTROL") == "eof" {
				send(map[string]any{"type": "result", "subtype": "success", "session_id": sessionID, "is_error": false, "result": "settled"})
				for i := range 160 {
					send(map[string]any{"type": "assistant", "session_id": sessionID, "message": map[string]any{"id": fmt.Sprintf("final-%d", i), "role": "assistant", "content": []any{map[string]any{"type": "text", "text": "accepted before EOF", "apiBlockIndex": 0}}}})
				}
				// The SDK answers unsupported controls independently of Receive.
				// Its reply proves all preceding native frames were admitted.
				send(map[string]any{"type": "control_request", "request_id": "drain-barrier", "request": map[string]any{"subtype": "synthetic_barrier"}})
			}
			continue
		}
		if v.Type == "control_response" && os.Getenv("PEER_CONTROL") == "eof" {
			return
		}
		if v.Type != "control_request" {
			panic("unexpected Claude frame")
		}
		if v.Request.Subtype == "interrupt" {
			interrupts++
			if interrupts == 1 && os.Getenv("PEER_CONTROL") == "unknown" {
				send(map[string]any{"type": "assistant", "session_id": sessionID, "message": map[string]any{"id": "interrupt-admitted", "role": "assistant", "content": []any{map[string]any{"type": "text", "text": "interrupt admitted; no acknowledgment", "apiBlockIndex": 0}}}})
				continue
			}
			if interrupts > 1 || os.Getenv("PEER_CONTROL") == "rejected" {
				send(map[string]any{"type": "control_response", "response": map[string]any{"subtype": "error", "request_id": v.ID, "error": "native interrupt rejected"}})
				continue
			}
		}
		send(map[string]any{"type": "control_response", "response": map[string]any{"subtype": "success", "request_id": v.ID, "response": map[string]any{}}})
		if v.Request.Subtype == "initialize" {
			cwd, err := os.Getwd()
			if err != nil {
				panic(err)
			}
			send(map[string]any{"type": "system", "subtype": "init", "session_id": sessionID, "cwd": cwd, "model": "synthetic", "tools": []any{}})
		}
	}
}

func closedInput(ready func()) {
	if err := os.Stdin.Close(); err != nil {
		panic(err)
	}
	ready()
	// Keep stdout alive so the next control fails at the write boundary rather
	// than racing a native EOF. The test owns and reaps this synthetic process.
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt)
	<-sig
}
