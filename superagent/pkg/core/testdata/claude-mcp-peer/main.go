// Runs the existing controlled Claude wire peer and its configured HTTP MCP.
package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
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

func callMCP(client *http.Client, endpoint string, headers map[string]string, body string, result any) {
	request, err := http.NewRequest(http.MethodPost, endpoint, bytes.NewBufferString(body))
	check(err)
	for name, value := range headers {
		request.Header.Set(name, os.ExpandEnv(value))
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json, text/event-stream")
	resp, err := client.Do(request)
	check(err)
	if resp.StatusCode != http.StatusOK {
		check(fmt.Errorf("native HTTP MCP request returned %d", resp.StatusCode))
	}
	check(json.NewDecoder(resp.Body).Decode(result))
	check(resp.Body.Close())
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
	endpoint, err := url.Parse(server.URL)
	check(err)
	client := &http.Client{Timeout: 5 * time.Second}
	var response struct {
		Result struct {
			Meta map[string]json.RawMessage `json:"_meta"`
		}
	}
	callMCP(client, server.URL, server.Headers, `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-11-25","capabilities":{},"clientInfo":{"name":"controlled-native","version":"1"}}}`, &response)
	binding := response.Result.Meta["mass.dev/session-binding"]
	var identity struct{ Role string }
	check(json.Unmarshal(binding, &identity))
	tool := map[string]string{"router": "list_topics", "topic": "get_topic", "issue": "get_issue"}[identity.Role]
	if tool == "" {
		check(fmt.Errorf("native HTTP MCP initialization returned an unexpected role"))
	}
	var toolResponse struct {
		Error  json.RawMessage
		Result struct {
			IsError           bool
			StructuredContent json.RawMessage
		}
	}
	body := fmt.Sprintf(`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":%q,"arguments":{}}}`, tool)
	callMCP(client, server.URL, server.Headers, body, &toolResponse)
	if toolResponse.Error != nil || toolResponse.Result.IsError || len(toolResponse.Result.StructuredContent) == 0 {
		check(fmt.Errorf("native HTTP MCP tool %s returned no result", tool))
	}
	record, err := os.OpenFile(filepath.Join(os.Getenv("PEER_DIR"), "mcp.jsonl"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	check(err)
	check(native.Start())
	log := json.NewEncoder(record)
	check(log.Encode(map[string]any{
		"method": "opened", "nativePid": native.Process.Pid, "binding": binding,
		"questionsDisabled": questionsDisabled, "mcpPath": endpoint.Path, "tool": tool,
	}))
	nativeErr := native.Wait()
	check(log.Encode(map[string]any{"method": "released", "nativePid": native.Process.Pid, "binding": binding}))
	check(record.Close())
	check(nativeErr)
}
