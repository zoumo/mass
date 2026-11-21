// Runs the existing controlled Claude wire peer and its configured HTTP MCP.
package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

func check(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func main() {
	args := os.Args[1:]
	native := exec.Command(os.Getenv("CLAUDE_WIRE_PEER"), args...)
	native.Stdin, native.Stdout, native.Stderr = os.Stdin, os.Stdout, os.Stderr
	if slices.Contains(args, "--version") {
		check(native.Run())
		return
	}
	var config struct {
		Servers map[string]struct {
			Type    string
			URL     string
			Headers map[string]string
		} `json:"mcpServers"`
	}
	questionsDisabled := false
	for _, arg := range args {
		if raw, ok := strings.CutPrefix(arg, "--mcp-config="); ok {
			check(json.Unmarshal([]byte(raw), &config))
		}
		if tools, ok := strings.CutPrefix(arg, "--disallowedTools="); ok {
			questionsDisabled = slices.Contains(strings.Split(tools, ","), "AskUserQuestion")
		}
	}
	server := config.Servers["superagent"]
	if server.Type != "http" || server.Headers["Authorization"] == "" {
		check(fmt.Errorf("native HTTP MCP configuration missing"))
	}
	request, err := http.NewRequest(http.MethodPost, server.URL, bytes.NewBufferString(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-11-25","capabilities":{},"clientInfo":{"name":"controlled-native","version":"1"}}}`))
	check(err)
	for name, value := range server.Headers {
		request.Header.Set(name, os.ExpandEnv(value))
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json, text/event-stream")
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Do(request)
	check(err)
	if resp.StatusCode != http.StatusOK {
		check(fmt.Errorf("native HTTP MCP initialization returned %d", resp.StatusCode))
	}
	var response struct {
		Result struct {
			Meta map[string]json.RawMessage `json:"_meta"`
		}
	}
	check(json.NewDecoder(resp.Body).Decode(&response))
	check(resp.Body.Close())
	binding := response.Result.Meta["mass.dev/session-binding"]
	if len(binding) == 0 {
		check(fmt.Errorf("native HTTP MCP initialization returned no binding"))
	}
	record, err := os.OpenFile(filepath.Join(os.Getenv("PEER_DIR"), "mcp.jsonl"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	check(err)
	check(native.Start())
	log := json.NewEncoder(record)
	check(log.Encode(map[string]any{"method": "opened", "nativePid": native.Process.Pid, "binding": binding, "questionsDisabled": questionsDisabled}))
	nativeErr := native.Wait()
	check(log.Encode(map[string]any{"method": "released", "nativePid": native.Process.Pid, "binding": binding}))
	check(record.Close())
	check(nativeErr)
}
