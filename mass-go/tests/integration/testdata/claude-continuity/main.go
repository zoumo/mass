// Synthetic native boundary: no model, SDK replacement, or credential access.
package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

const nativeSessionID = "44444444-4444-4444-8444-444444444444"

func main() {
	if len(os.Args) == 2 && os.Args[1] == "--version" {
		fmt.Println("2.1.258 (Claude Code)")
		return
	}
	enc := json.NewEncoder(os.Stdout)
	send := func(v map[string]any) {
		if err := enc.Encode(v); err != nil {
			panic(err)
		}
	}
	log, err := os.OpenFile(os.Getenv("PEER_EVIDENCE"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		panic(err)
	}
	defer log.Close()
	record := json.NewEncoder(log)
	cwd, _ := os.Getwd()
	explicit := os.Getenv("EXPLICIT_CREATE") == "1"
	init := func() {
		send(map[string]any{"type": "system", "subtype": "init", "session_id": nativeSessionID, "cwd": cwd, "claude_code_version": "2.1.258"})
		send(map[string]any{"type": "system", "subtype": "session_state_changed", "session_id": nativeSessionID, "state": "idle"})
	}
	n := 0
	var pending string
	var previous string
	result := func(link string) {
		v := map[string]any{"type": "result", "subtype": "success", "session_id": nativeSessionID, "uuid": fmt.Sprintf("result-%d", n)}
		if link != "" {
			v["user_message_uuid"] = link
		}
		send(v)
		send(map[string]any{"type": "system", "subtype": "session_state_changed", "session_id": nativeSessionID, "state": "idle"})
	}
	scan := bufio.NewScanner(os.Stdin)
	for scan.Scan() {
		var v map[string]any
		if err := json.Unmarshal(scan.Bytes(), &v); err != nil {
			panic(err)
		}
		if v["type"] == "control_request" {
			r := v["request"].(map[string]any)
			send(map[string]any{"type": "control_response", "response": map[string]any{"subtype": "success", "request_id": v["request_id"], "response": map[string]any{}}})
			switch r["subtype"] {
			case "initialize":
				init()
			case "interrupt":
				result(pending)
			}
			continue
		}
		if v["type"] != "user" {
			panic("unexpected frame")
		}
		n++
		link, _ := v["uuid"].(string)
		pending = link
		text := v["message"].(map[string]any)["content"].(string)
		if err := record.Encode(map[string]any{"pid": os.Getpid(), "input": n, "uuid": link, "text": text, "after_init": true}); err != nil {
			panic(err)
		}
		send(map[string]any{"type": "system", "subtype": "session_state_changed", "session_id": nativeSessionID, "state": "running"})
		if strings.HasPrefix(text, "linkage-") {
			linkage(send, text, n, link, previous)
			previous = link
			result(link)
			continue
		}
		start := map[string]any{"type": "stream_event", "session_id": nativeSessionID, "event": map[string]any{"type": "message_start", "message": map[string]any{"id": fmt.Sprintf("message-%d", n), "role": "assistant", "content": []any{}}}}
		if link != "" {
			start["user_message_uuid"] = link
		}
		send(start)
		if explicit && n == 1 {
			switch text {
			case "initial-overflow":
				for i := 0; i < 129; i++ {
					send(map[string]any{"type": "assistant", "session_id": nativeSessionID, "message": map[string]any{"id": fmt.Sprintf("early-%d", i), "role": "assistant", "content": []any{map[string]any{"type": "text", "text": "early fact", "apiBlockIndex": 0}}}})
				}
				continue
			case "initial-foreign", "initial-child":
				unrelated := map[string]any{"type": "result", "subtype": "success", "session_id": nativeSessionID}
				if text == "initial-foreign" {
					unrelated["user_message_uuid"] = "foreign-submission"
				} else {
					unrelated["parent_tool_use_id"] = "foreign-child"
				}
				send(unrelated)
				send(map[string]any{"type": "assistant", "session_id": nativeSessionID, "message": map[string]any{"id": "still-unsettled", "role": "assistant", "content": []any{map[string]any{"type": "text", "text": "not a root completion", "apiBlockIndex": 0}}}})
				continue
			}
		}
		if text == "child-provenance" {
			send(map[string]any{"type": "assistant", "session_id": nativeSessionID, "message": map[string]any{"id": "root-tool", "role": "assistant", "content": []any{map[string]any{"type": "tool_use", "id": "child-tool", "name": "Agent", "input": map[string]any{}}}}})
			send(map[string]any{"type": "system", "subtype": "task_started", "session_id": nativeSessionID, "task_id": "child-task", "task_type": "local_agent", "tool_use_id": "child-tool"})
			send(map[string]any{"type": "assistant", "session_id": nativeSessionID, "parent_tool_use_id": "child-tool", "message": map[string]any{"id": "confirmed-child", "role": "assistant", "content": []any{map[string]any{"type": "text", "text": "confirmed child fact", "apiBlockIndex": 0}}}})
			send(map[string]any{"type": "result", "subtype": "success", "session_id": nativeSessionID, "parent_tool_use_id": "child-tool"})
		}
		if text == "resume-unlinked" {
			send(map[string]any{"type": "control_request", "request_id": "unlinked-old", "request": map[string]any{"subtype": "can_use_tool", "tool_name": "Bash", "tool_use_id": "old-tool", "input": map[string]any{"command": "never execute"}}})
			send(map[string]any{"type": "result", "subtype": "success", "session_id": nativeSessionID, "uuid": "unlinked-old-result"})
			continue
		}
		if text == "hold" && n > 1 {
			old := map[string]any{"type": "result", "subtype": "success", "session_id": nativeSessionID, "uuid": fmt.Sprintf("result-%d", n-1)}
			if previous != "" {
				old["user_message_uuid"] = previous
			}
			send(old)
			send(map[string]any{"type": "control_request", "request_id": "old-permission", "user_message_uuid": previous, "request": map[string]any{"subtype": "can_use_tool", "tool_name": "Bash", "tool_use_id": "old-tool", "input": map[string]any{"command": "never execute"}}})
			send(map[string]any{"type": "result", "subtype": "success", "session_id": nativeSessionID, "parent_tool_use_id": "old-child"})
			send(map[string]any{"type": "assistant", "session_id": nativeSessionID, "message": map[string]any{"id": "unlinked-late", "role": "assistant", "content": []any{map[string]any{"type": "text", "text": "unlinked old or new", "apiBlockIndex": 0}}}})
		}
		message := map[string]any{"type": "assistant", "session_id": nativeSessionID, "message": map[string]any{"id": fmt.Sprintf("message-%d", n), "role": "assistant", "content": []any{map[string]any{"type": "text", "text": text, "apiBlockIndex": 0}}}}
		if link != "" {
			message["user_message_uuid"] = link
		}
		send(message)
		previous = link
		if text == "hold" || text == "child-provenance" {
			continue
		}
		if text == "ambiguous" {
			send(map[string]any{"type": "result", "subtype": "success", "session_id": nativeSessionID})
			continue
		}
		if text == "conflicting" {
			send(map[string]any{"type": "result", "subtype": "success", "session_id": nativeSessionID, "uuid": "result-1", "user_message_uuid": link})
			continue
		}
		result(link)
	}
}

// The native start proves ownership; complete assistant frames omit the caller UUID.
func linkage(send func(map[string]any), mode string, n int, link, previous string) {
	id := fmt.Sprintf("message-%d", n)
	baseIndex := 0
	start := func(id, owner string) {
		v := map[string]any{"type": "stream_event", "session_id": nativeSessionID, "event": map[string]any{"type": "message_start", "message": map[string]any{"id": id, "role": "assistant", "content": []any{}}}}
		if owner != "" {
			v["user_message_uuid"] = owner
		}
		send(v)
	}
	message := func(id, owner, parent string) {
		for i, block := range []any{map[string]any{"type": "thinking", "thinking": "native thought"}, map[string]any{"type": "text", "text": "native answer"}} {
			block.(map[string]any)["apiBlockIndex"] = baseIndex + i
			v := map[string]any{"type": "assistant", "session_id": nativeSessionID, "uuid": fmt.Sprintf("envelope-%d-%s-%d", n, id, i), "message": map[string]any{"id": id, "role": "assistant", "content": []any{block}}}
			if owner != "" {
				v["user_message_uuid"] = owner
			}
			if parent != "" {
				v["parent_tool_use_id"] = parent
			}
			send(v)
		}
	}
	if mode == "linkage-saturation" {
		for i := range 16 {
			message(fmt.Sprintf("saturated-%d-%d", n, i), "", "")
		}
		return
	}
	switch mode {
	case "linkage-retired":
		id = "message-1"
		baseIndex = 1
	case "linkage-unproven":
		message(id, "", "")
	case "linkage-child-first":
		message(id, "", "child-tool")
	}
	switch mode {
	case "linkage-unlinked-start":
		start(id, "")
		start(id, link)
	case "linkage-child-start", "linkage-foreign-start":
		v := map[string]any{"type": "stream_event", "session_id": nativeSessionID, "user_message_uuid": link, "event": map[string]any{"type": "message_start", "message": map[string]any{"id": id, "role": "assistant", "content": []any{}}}}
		if mode == "linkage-child-start" {
			v["parent_tool_use_id"] = "child-tool"
		} else {
			v["session_id"] = "foreign-session"
		}
		send(v)
	case "linkage-no-start":
	default:
		start(id, link)
	}
	switch mode {
	case "linkage-explicit":
		message(id, link, "")
	case "linkage-repeated-start":
		start(id, link)
	case "linkage-different":
		id = "different-message"
	case "linkage-conflict":
		message(id, "foreign-submission", "")
	case "linkage-prior-work":
		message(id, previous, "")
	case "linkage-child":
		message(id, "", "child-tool")
	}
	if mode == "linkage-stopped-preview" {
		send(map[string]any{"type": "stream_event", "session_id": nativeSessionID, "event": map[string]any{"type": "message_stop"}})
	}
	for i, delta := range []map[string]any{{"type": "thinking_delta", "thinking": "preview thought"}, {"type": "text_delta", "text": "preview answer"}} {
		// Deltas have no message ID; a different-ID complete frame must not borrow it.
		if mode == "linkage-proof" || mode == "linkage-stopped-preview" {
			send(map[string]any{"type": "stream_event", "session_id": nativeSessionID, "event": map[string]any{"type": "content_block_delta", "index": i, "delta": delta}})
		}
	}
	message(id, "", "")
}
