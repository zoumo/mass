// Synthetic native boundary: no model, SDK replacement, or credential access.
package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

func main() {
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
	resumed := false
	for _, arg := range os.Args[1:] {
		if strings.HasPrefix(arg, "--resume=") {
			resumed = true
		}
	}
	init := func() {
		send(map[string]any{"type": "system", "subtype": "init", "session_id": "claude-continuity", "cwd": cwd, "claude_code_version": "2.1.258"})
	}
	n := 0
	var pending string
	var previous string
	result := func(link string) {
		v := map[string]any{"type": "result", "subtype": "success", "session_id": "claude-continuity", "uuid": fmt.Sprintf("result-%d", n)}
		if link != "" {
			v["user_message_uuid"] = link
		}
		send(v)
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
				if !explicit || resumed {
					init()
				}
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
		if err := record.Encode(map[string]any{"pid": os.Getpid(), "input": n, "uuid": link, "text": text, "before_init": explicit && !resumed && n == 1}); err != nil {
			panic(err)
		}
		if strings.HasPrefix(text, "linkage-") {
			linkage(send, text, n, link, previous)
			previous = link
			result(link)
			continue
		}
		start := map[string]any{"type": "stream_event", "session_id": "claude-continuity", "event": map[string]any{"type": "message_start", "message": map[string]any{"id": fmt.Sprintf("message-%d", n), "role": "assistant", "content": []any{}}}}
		if link != "" {
			start["user_message_uuid"] = link
		}
		send(start)
		if explicit && !resumed && n == 1 {
			switch text {
			case "initial-overflow":
				for i := 0; i < 129; i++ {
					send(map[string]any{"type": "assistant", "session_id": "claude-continuity", "message": map[string]any{"id": fmt.Sprintf("early-%d", i), "role": "assistant", "content": []any{map[string]any{"type": "text", "text": "early fact"}}}})
				}
				continue
			case "initial-foreign", "initial-child":
				unrelated := map[string]any{"type": "result", "subtype": "success", "session_id": "claude-continuity"}
				if text == "initial-foreign" {
					unrelated["user_message_uuid"] = "foreign-submission"
				} else {
					unrelated["parent_tool_use_id"] = "foreign-child"
				}
				send(unrelated)
				send(map[string]any{"type": "assistant", "session_id": "claude-continuity", "message": map[string]any{"id": "still-unsettled", "role": "assistant", "content": []any{map[string]any{"type": "text", "text": "not a root completion"}}}})
				init()
				continue
			}
		}
		if text == "child-provenance" {
			send(map[string]any{"type": "assistant", "session_id": "claude-continuity", "message": map[string]any{"id": "root-tool", "role": "assistant", "content": []any{map[string]any{"type": "tool_use", "id": "child-tool", "name": "Agent", "input": map[string]any{}}}}})
			send(map[string]any{"type": "system", "subtype": "task_started", "session_id": "claude-continuity", "task_id": "child-task", "task_type": "local_agent", "tool_use_id": "child-tool"})
			send(map[string]any{"type": "assistant", "session_id": "claude-continuity", "parent_tool_use_id": "child-tool", "message": map[string]any{"id": "confirmed-child", "role": "assistant", "content": []any{map[string]any{"type": "text", "text": "confirmed child fact"}}}})
			send(map[string]any{"type": "result", "subtype": "success", "session_id": "claude-continuity", "parent_tool_use_id": "child-tool"})
		}
		if text == "resume-unlinked" {
			send(map[string]any{"type": "control_request", "request_id": "unlinked-old", "request": map[string]any{"subtype": "can_use_tool", "tool_name": "Bash", "tool_use_id": "old-tool", "input": map[string]any{"command": "never execute"}}})
			send(map[string]any{"type": "result", "subtype": "success", "session_id": "claude-continuity", "uuid": "unlinked-old-result"})
			continue
		}
		if text == "hold" && n > 1 {
			old := map[string]any{"type": "result", "subtype": "success", "session_id": "claude-continuity", "uuid": fmt.Sprintf("result-%d", n-1)}
			if previous != "" {
				old["user_message_uuid"] = previous
			}
			send(old)
			send(map[string]any{"type": "control_request", "request_id": "old-permission", "user_message_uuid": previous, "request": map[string]any{"subtype": "can_use_tool", "tool_name": "Bash", "tool_use_id": "old-tool", "input": map[string]any{"command": "never execute"}}})
			send(map[string]any{"type": "result", "subtype": "success", "session_id": "claude-continuity", "parent_tool_use_id": "old-child"})
			send(map[string]any{"type": "assistant", "session_id": "claude-continuity", "message": map[string]any{"id": "unlinked-late", "role": "assistant", "content": []any{map[string]any{"type": "text", "text": "unlinked old or new"}}}})
		}
		message := map[string]any{"type": "assistant", "session_id": "claude-continuity", "message": map[string]any{"id": fmt.Sprintf("message-%d", n), "role": "assistant", "content": []any{map[string]any{"type": "text", "text": text}}}}
		if link != "" {
			message["user_message_uuid"] = link
		}
		send(message)
		previous = link
		if text == "hold" || text == "child-provenance" {
			continue
		}
		if text == "ambiguous" {
			send(map[string]any{"type": "result", "subtype": "success", "session_id": "claude-continuity"})
			continue
		}
		if text == "conflicting" {
			send(map[string]any{"type": "result", "subtype": "success", "session_id": "claude-continuity", "uuid": "result-1", "user_message_uuid": link})
			continue
		}
		result(link)
		if explicit && !resumed && n == 1 {
			init()
		}
	}
}

// The native start proves ownership; complete assistant frames omit the caller UUID.
func linkage(send func(map[string]any), mode string, n int, link, previous string) {
	id := fmt.Sprintf("message-%d", n)
	start := func(id, owner string) {
		v := map[string]any{"type": "stream_event", "session_id": "claude-continuity", "event": map[string]any{"type": "message_start", "message": map[string]any{"id": id, "role": "assistant", "content": []any{}}}}
		if owner != "" {
			v["user_message_uuid"] = owner
		}
		send(v)
	}
	message := func(id, owner, parent string) {
		for i, block := range []any{map[string]any{"type": "thinking", "thinking": "native thought"}, map[string]any{"type": "text", "text": "native answer"}} {
			v := map[string]any{"type": "assistant", "session_id": "claude-continuity", "uuid": fmt.Sprintf("envelope-%d-%d", n, i), "message": map[string]any{"id": id, "role": "assistant", "content": []any{block}}}
			if owner != "" {
				v["user_message_uuid"] = owner
			}
			if parent != "" {
				v["parent_tool_use_id"] = parent
			}
			send(v)
		}
	}
	switch mode {
	case "linkage-retired":
		id = "message-1"
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
		v := map[string]any{"type": "stream_event", "session_id": "claude-continuity", "user_message_uuid": link, "event": map[string]any{"type": "message_start", "message": map[string]any{"id": id, "role": "assistant", "content": []any{}}}}
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
		send(map[string]any{"type": "stream_event", "session_id": "claude-continuity", "event": map[string]any{"type": "message_stop"}})
	}
	for _, delta := range []map[string]any{{"type": "thinking_delta", "thinking": "preview thought"}, {"type": "text_delta", "text": "preview answer"}} {
		// Deltas have no message ID; a different-ID complete frame must not borrow it.
		if mode == "linkage-proof" || mode == "linkage-stopped-preview" {
			send(map[string]any{"type": "stream_event", "session_id": "claude-continuity", "event": map[string]any{"type": "content_block_delta", "index": 0, "delta": delta}})
		}
	}
	message(id, "", "")
}
