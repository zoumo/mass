// Runs the existing controlled Claude wire peer and its configured stdio MCP.
package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
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
			Command string
			Args    []string
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
	mcp := exec.Command(server.Command, server.Args...)
	// The race-enabled test Core otherwise waits an extra second after EOF.
	mcp.Env = append(os.Environ(), "GORACE=atexit_sleep_ms=0")
	mcp.Stderr = os.Stderr
	in, err := mcp.StdinPipe()
	check(err)
	out, err := mcp.StdoutPipe()
	check(err)
	check(mcp.Start())
	_, err = fmt.Fprintln(in, `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-11-25","capabilities":{},"clientInfo":{"name":"controlled-native","version":"1"}}}`)
	check(err)
	scan := bufio.NewScanner(out)
	if !scan.Scan() {
		check(fmt.Errorf("native MCP process did not initialize"))
	}
	var response struct {
		Result json.RawMessage
	}
	check(json.Unmarshal(scan.Bytes(), &response))
	if len(response.Result) == 0 {
		check(fmt.Errorf("native MCP process rejected initialization"))
	}
	record, err := os.OpenFile(filepath.Join(os.Getenv("PEER_DIR"), "mcp.jsonl"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	check(err)
	check(native.Start())
	log := json.NewEncoder(record)
	check(log.Encode(map[string]any{"method": "opened", "nativePid": native.Process.Pid, "mcpPid": mcp.Process.Pid, "questionsDisabled": questionsDisabled}))
	nativeErr := native.Wait()
	check(in.Close())
	check(mcp.Wait())
	check(log.Encode(map[string]any{"method": "released", "nativePid": native.Process.Pid, "mcpPid": mcp.Process.Pid}))
	check(record.Close())
	check(nativeErr)
}
