package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
)

const screenshotPNG = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAIAAACQd1PeAAAADElEQVR4nGP4z8AAAAMBAQDJ/pLvAAAAAElFTkSuQmCC"

func runDesktop(stateDir string, args []string) error {
	if len(args) != 12 {
		return errors.New("unexpected Driver command length")
	}
	id := strings.TrimPrefix(args[6], "https://relay.invalid:5943/cua-relay/m/")
	token := os.Getenv("CUA_ENV_TOKEN")
	if id == args[6] || token != "fixture-only-access."+id ||
		!slices.Equal(args[2:6], []string{"-i", "--embedded", "--state-dir", stateDir}) ||
		!slices.Equal(args[7:], []string{"--", "/target/Cua Driver/bin/cua-driver", "mcp", "--socket", "/target/Library/Caches/driver.sock"}) {
		return errors.New("unexpected Driver command or credential")
	}
	fmt.Fprintln(os.Stderr, token)
	if id == "broken-driver" {
		return errors.New("cannot connect to the Driver socket")
	}

	scanner := bufio.NewScanner(os.Stdin)
	encoder := json.NewEncoder(os.Stdout)
	driver := desktopFixture{url: args[6], token: token}
	initialized := false
	for scanner.Scan() {
		var req desktopRequest
		if err := json.Unmarshal(scanner.Bytes(), &req); err != nil {
			return err
		}
		reply := map[string]any{"jsonrpc": "2.0", "id": req.ID}
		switch req.Method {
		case "initialize":
			reply["result"] = map[string]any{
				"protocolVersion": "2025-11-25",
				"serverInfo":      map[string]any{"name": "cua-driver-fixture", "version": "0.63.1"},
				"capabilities":    map[string]any{"tools": map[string]any{}},
			}
		case "notifications/initialized":
			initialized = true
			continue
		case "tools/call":
			if !initialized || req.Params.Args["computer_id"] != nil {
				return errors.New("unexpected Driver tool call")
			}
			key, value := driver.call(req)
			reply[key] = value
		default:
			return errors.New("unexpected Driver MCP method")
		}
		if err := encoder.Encode(reply); err != nil {
			return err
		}
	}
	return scanner.Err()
}

type desktopRequest struct {
	ID     json.RawMessage `json:"id"`
	Method string          `json:"method"`
	Params struct {
		Name string                     `json:"name"`
		Args map[string]json.RawMessage `json:"arguments"`
	} `json:"params"`
}

type desktopFixture struct {
	url          string
	token        string
	elementToken string
	snapshot     int
	checked      bool
}

func (d *desktopFixture) call(req desktopRequest) (string, any) {
	args := req.Params.Args
	data := map[string]any{
		"url": d.url, "child_pid": os.Getpid(), "arguments": args, "pid": 501, "window_id": 724,
	}
	text := req.Params.Name
	isError := false
	switch req.Params.Name {
	case "list_windows":
		switch string(args["app"]) {
		case `"tool-error"`:
			text, isError = "Driver cannot list windows", true
			data["code"] = "desktop_unavailable"
		case `"rpc-error"`:
			return "error", map[string]any{
				"code": -32010, "message": "Driver RPC failed", "data": map[string]any{"retryable": true},
			}
		case `"credential-in-tool-result"`:
			data["secret"] = d.token
		}
	case "get_window_state":
		d.snapshot++
		snapshotID := fmt.Sprintf("s%08x", os.Getpid()+d.snapshot)
		d.elementToken = snapshotID + ":1"
		data["snapshot_id"] = snapshotID
		data["elements"] = []any{map[string]any{"element_token": d.elementToken, "checked": d.checked}}
	case "click", "type_text", "press_key":
		if quoted := string(args["element_token"]); d.elementToken == "" || quoted != `"`+d.elementToken+`"` {
			text, isError = "Element token belongs to another snapshot or session", true
			data["code"] = "stale_element_token"
		} else {
			data["effect"] = "confirmed"
			data["revision_json"] = string(args["ui_tree_revision"])
			if req.Params.Name == "click" {
				d.checked = !d.checked
			}
		}
	default:
		text, isError = "Unrecognized Driver tool", true
	}
	content := []any{map[string]any{"type": "text", "text": text}}
	if req.Params.Name == "get_window_state" {
		content = append(content, map[string]any{"type": "image", "mimeType": "image/png", "data": screenshotPNG})
	}
	return "result", map[string]any{
		"content": content, "structuredContent": data, "isError": isError,
		"_meta": map[string]any{"fixture": "driver"},
	}
}
