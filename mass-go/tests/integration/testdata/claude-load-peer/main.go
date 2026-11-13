// Synthetic native process. This is not evidence of real Claude/model behavior.
package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/zoumo/mass/mass-go/tests/integration/testdata/clipeer"
)

func main() {
	if clipeer.Version() {
		return
	}
	log, err := os.OpenFile(os.Getenv("LOAD_EVIDENCE"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		panic(err)
	}
	defer log.Close()
	record := json.NewEncoder(log)
	write := json.NewEncoder(os.Stdout)
	id := os.Getenv("LOAD_ID")
	if id == "" {
		id = "88888888-8888-4888-8888-888888888888"
	}
	if clipeer.AwaitingClaim() {
		id = uuid.NewString()
	}
	for _, arg := range os.Args[1:] {
		if value, ok := strings.CutPrefix(arg, "--resume="); ok {
			id = value
		}
		if value, ok := strings.CutPrefix(arg, "--session-id="); ok {
			id = value
		}
	}
	cwd, _ := os.Getwd()
	evidence := func(v any) {
		if err := record.Encode(v); err != nil {
			panic(err)
		}
	}
	evidence(map[string]any{"event": "opened", "pid": os.Getpid(), "args": os.Args[1:]})
	defer func() { evidence(map[string]any{"event": "released", "pid": os.Getpid()}) }()
	send := func(v any) {
		if err := write.Encode(v); err != nil {
			panic(err)
		}
	}
	var link string
	n := 0
	result := func() {
		send(map[string]any{"type": "result", "subtype": "success", "session_id": id, "uuid": fmt.Sprintf("result-%d", n), "user_message_uuid": link})
		send(map[string]any{"type": "system", "subtype": "session_state_changed", "session_id": id, "state": "idle"})
	}
	frames := make(chan map[string]any)
	eof := make(chan struct{})
	go func() {
		defer close(eof)
		defer close(frames)
		scan := bufio.NewScanner(os.Stdin)
		for scan.Scan() {
			var v map[string]any
			if err := json.Unmarshal(scan.Bytes(), &v); err != nil {
				panic(err)
			}
			frames <- v
		}
	}()
	await := func() bool {
		dir := os.Getenv("LOAD_GATE")
		if dir == "" {
			return true
		}
		if err := os.WriteFile(dir+"/ready", []byte("ready"), 0o600); err != nil {
			panic(err)
		}
		ticker := time.NewTicker(5 * time.Millisecond)
		defer ticker.Stop()
		for {
			if _, err := os.Stat(dir + "/release"); err == nil {
				return true
			}
			select {
			case <-eof:
				return false
			case <-ticker.C:
			}
		}
	}
	for v := range frames {
		if v["type"] == "control_request" {
			r := v["request"].(map[string]any)
			if reply, ok := clipeer.ClaimReply(v, id); ok {
				send(reply)
				continue
			}
			if r["subtype"] == "initialize" && os.Getenv("LOAD_MODE") == "startup" {
				if !await() {
					return
				}
			}
			send(map[string]any{"type": "control_response", "response": map[string]any{"subtype": "success", "request_id": v["request_id"], "response": map[string]any{}}})
			switch r["subtype"] {
			case "initialize":
				if os.Getenv("LOAD_MODE") == "init" {
					if !await() {
						return
					}
				}
				observed := id
				if os.Getenv("LOAD_MODE") == "identity" {
					observed = "22222222-2222-4222-8222-222222222222"
				}
				send(map[string]any{"type": "system", "subtype": "init", "session_id": observed, "cwd": cwd, "claude_code_version": clipeer.VersionNumber})
				send(map[string]any{"type": "system", "subtype": "session_state_changed", "session_id": observed, "state": "idle"})
			case "interrupt":
				result()
			}
			continue
		}
		if v["type"] != "user" {
			panic("unexpected native frame")
		}
		n++
		link, _ = v["uuid"].(string)
		evidence(map[string]any{"event": "input", "pid": os.Getpid(), "uuid": link, "input": n, "priority": v["priority"], "shouldQuery": v["shouldQuery"]})
		m := map[string]any{"type": "assistant", "session_id": id, "user_message_uuid": link, "message": map[string]any{"id": fmt.Sprintf("live-%d", n), "role": "assistant", "content": []any{map[string]any{"type": "text", "text": "same retained process", "apiBlockIndex": 0}}}}
		if os.Getenv("LOAD_MODE") == "unlinked" {
			delete(m, "user_message_uuid")
		}
		send(m)
		if os.Getenv("LOAD_MODE") == "unlinked" {
			send(map[string]any{"type": "result", "subtype": "success", "session_id": id})
			send(map[string]any{"type": "system", "subtype": "session_state_changed", "session_id": id, "state": "idle"})
		} else {
			result()
		}
	}
}
