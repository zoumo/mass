package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
)

type registration struct {
	ID    string
	URL   string
	Token string
}

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(args []string) error {
	home := os.Getenv("CUA_HOME")
	info, err := os.Stat(home)
	if err != nil || info.Mode().Perm() != 0o700 || os.Getenv("HOME") != home ||
		os.Getenv("CUA_CREDENTIAL_STORE") != "file" || os.Getenv("CUA_TELEMETRY") != "off" ||
		os.Getenv("CUA_MESH") != "off" || filepath.Base(os.Getenv("SSL_CERT_FILE")) != "test-ca.pem" ||
		os.Getenv("CUA_DAEMON") != "" {
		return errors.New("isolated Cua environment is required")
	}
	state := filepath.Join(home, "sandboxes")
	registry := filepath.Join(home, "fake-registry.json")
	switch {
	case len(args) >= 2 && slices.Equal(args[:2], []string{"spacesd", "exec"}):
		return runDesktop(state, args)

	case len(args) == 8 && slices.Equal(args[:2], []string{"spaces", "add"}):
		id := args[4]
		token := os.Getenv("CUA_ENV_TOKEN")
		if !slices.Equal(args[3:], []string{"--name", id, "--embedded", "--state-dir", state}) ||
			!strings.HasSuffix(args[2], "/m/"+id) || token != "fixture-only-access."+id {
			return errors.New("unexpected registration arguments or credential")
		}
		fmt.Fprintln(os.Stderr, token)
		if id == "unavailable-mac" {
			return errors.New("computer unavailable")
		}
		raw, err := json.Marshal(registration{ID: id, URL: args[2], Token: token})
		if err != nil {
			return err
		}
		return os.WriteFile(registry, raw, 0o600)

	case len(args) == 8 && args[0] == "mcp":
		var reg registration
		raw, err := os.ReadFile(registry)
		if err != nil {
			return err
		}
		if err := json.Unmarshal(raw, &reg); err != nil {
			return err
		}
		if !slices.Equal(args[1:], []string{"--embedded", "--sandbox", reg.ID, "--state-dir", state, "--permissions", "spaces:space_bash"}) ||
			os.Getenv("CUA_ENV_TOKEN") != "" {
			return errors.New("unexpected MCP command or credential in environment")
		}
		fmt.Fprintln(os.Stderr, reg.Token)
		if reg.ID == "broken-mcp" {
			return errors.New("MCP startup failed")
		}
		return serve(reg)
	default:
		return errors.New("unexpected Cua command")
	}
}

type request struct {
	ID     json.RawMessage `json:"id"`
	Method string          `json:"method"`
	Params struct {
		Name string `json:"name"`
		Args struct {
			Space   string  `json:"space"`
			Command string  `json:"command"`
			Timeout *uint64 `json:"timeout"`
		} `json:"arguments"`
	} `json:"params"`
}

func serve(reg registration) error {
	scanner := bufio.NewScanner(os.Stdin)
	encoder := json.NewEncoder(os.Stdout)
	initialized, calls := false, 0
	for scanner.Scan() {
		var req request
		if err := json.Unmarshal(scanner.Bytes(), &req); err != nil {
			return err
		}
		reply := map[string]any{"jsonrpc": "2.0", "id": req.ID}
		switch req.Method {
		case "initialize":
			reply["result"] = map[string]any{
				"protocolVersion": "2025-11-25",
				"serverInfo":      map[string]any{"name": "official-cua-fixture", "version": "0.31.0"},
				"capabilities":    map[string]any{"tools": map[string]any{}},
			}
		case "notifications/initialized":
			initialized = true
			continue
		case "tools/call":
			if !initialized || req.Params.Name != "space_bash" || req.Params.Args.Space != reg.ID {
				return errors.New("unexpected MCP tool call")
			}
			calls++
			key, value := toolReply(reg, req, calls)
			reply[key] = value
		default:
			return errors.New("unexpected MCP method")
		}
		if err := encoder.Encode(reply); err != nil {
			return err
		}
	}
	return scanner.Err()
}

func toolReply(reg registration, req request, calls int) (string, any) {
	args := req.Params.Args
	text := "stdout\n[stderr]\nstderr\n[exit 7]"
	toolFailure := false
	switch args.Command {
	case "tool-error":
		toolFailure = true
		text = "upstream target unavailable"
	case "rpc-error":
		return "error", map[string]any{"code": -32002, "message": "upstream failure", "data": map[string]any{"retryable": true}}
	case "credential-in-tool-result":
		text = reg.Token
	case "credential-in-rpc-data":
		return "error", map[string]any{"code": -32002, "message": "upstream failure", "data": map[string]any{"secret": reg.Token}}
	}
	result := map[string]any{
		"content": []any{map[string]any{"type": "text", "text": text}},
		"structuredContent": map[string]any{
			"cmd": args.Command, "pid": os.Getpid(), "call": calls, "url": reg.URL,
		},
		"_meta":   map[string]any{"fixture": true},
		"isError": toolFailure,
	}
	if args.Timeout != nil {
		result["structuredContent"].(map[string]any)["timeout"] = strconv.FormatUint(*args.Timeout, 10)
	}
	return "result", result
}
