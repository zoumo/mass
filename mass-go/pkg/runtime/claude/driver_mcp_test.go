package claude

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/zoumo/mass/mass-go/pkg/runtime"
)

// TestClaudeDriverTestPeer is a synthetic CLI. The launcher records argv; this
// peer completes initialize, publishes one system/init identity, and never
// calls a model.
func TestClaudeDriverTestPeer(t *testing.T) {
	if os.Getenv("MASS_CLAUDE_DRIVER_PEER") != "1" {
		return
	}
	reader := bufio.NewReader(os.Stdin)
	line, err := reader.ReadBytes('\n')
	if err != nil {
		os.Exit(2)
	}
	var initialize struct {
		Type      string `json:"type"`
		RequestID string `json:"request_id"`
		Request   struct {
			Subtype string `json:"subtype"`
		} `json:"request"`
	}
	if json.Unmarshal(line, &initialize) != nil || initialize.Type != "control_request" || initialize.Request.Subtype != "initialize" || initialize.RequestID == "" {
		os.Exit(3)
	}
	enc := json.NewEncoder(os.Stdout)
	if err := enc.Encode(map[string]any{
		"type": "control_response",
		"response": map[string]any{
			"subtype":    "success",
			"request_id": initialize.RequestID,
			"response":   map[string]any{},
		},
	}); err != nil {
		os.Exit(6)
	}
	if err := enc.Encode(map[string]any{
		"type": "system", "subtype": "init", "session_id": "native-session",
		"cwd": os.Getenv("MASS_CLAUDE_DRIVER_CWD"), "model": "synthetic", "tools": []any{},
	}); err != nil {
		os.Exit(7)
	}
	_, _ = io.Copy(io.Discard, reader)
	os.Exit(0)
}

func TestClaudeDriverPassesMCPConfigToNativeCLI(t *testing.T) {
	executable, err := os.Executable()
	require.NoError(t, err)
	cwd := t.TempDir()
	capture := filepath.Join(t.TempDir(), "argv")
	script := filepath.Join(t.TempDir(), "claude")
	require.NoError(t, os.WriteFile(script, []byte(
		"#!/bin/sh\n"+
			": > \"$MASS_CLAUDE_DRIVER_CAPTURE\"\n"+
			"for arg in \"$@\"; do printf '%s\\0' \"$arg\" >> \"$MASS_CLAUDE_DRIVER_CAPTURE\"; done\n"+
			"exec "+shellQuote(executable)+" -test.run '^TestClaudeDriverTestPeer$'\n",
	), 0o700))

	driver, err := (Factory{RecoveryDir: t.TempDir()}).New("agent", runtime.Config{
		"executable": script,
		"envPolicy":  "replace",
		"env": map[string]any{
			"MASS_CLAUDE_DRIVER_PEER":    "1",
			"MASS_CLAUDE_DRIVER_CAPTURE": capture,
			"MASS_CLAUDE_DRIVER_CWD":     cwd,
			"HOME":                       t.TempDir(),
			"TMPDIR":                     t.TempDir(),
			"TOKEN":                      "fixture-secret",
		},
	})
	require.NoError(t, err)
	_, err = driver.Start(t.Context())
	require.NoError(t, err)
	t.Cleanup(func() { _ = driver.Stop(context.Background()) })
	stream, err := driver.Events()
	require.NoError(t, err)
	go func() {
		for {
			if _, err := stream.Next(t.Context()); err != nil {
				return
			}
		}
	}()

	servers := []runtime.MCPServer{
		{
			Name:    "local",
			Type:    runtime.MCPTransportStdio,
			Command: "/mcp-server",
			Args:    []string{"--stdio"},
			Env:     map[string]string{"TOKEN": "${TOKEN}"},
		},
		{
			Name:    "remote",
			Type:    runtime.MCPTransportHTTP,
			URL:     "https://example.invalid/mcp",
			Headers: map[string]string{"Authorization": "Bearer ${TOKEN}"},
		},
	}
	session, err := driver.OpenSession(t.Context(), runtime.Address{Namespace: "one", SessionName: "demo"}, runtime.SessionSpec{Cwd: cwd, MCPServers: &servers})
	require.NoError(t, err)
	require.Equal(t, "native-session", session.SessionID)
	t.Cleanup(func() { _ = driver.CloseSession(context.Background(), session.SessionID) })

	data, err := os.ReadFile(capture)
	require.NoError(t, err)
	argv := strings.Split(strings.TrimSuffix(string(data), "\x00"), "\x00")
	require.NotContains(t, argv, "--strict-mcp-config")
	var raw string
	for _, arg := range argv {
		if value, ok := strings.CutPrefix(arg, "--mcp-config="); ok {
			raw = value
			break
		}
	}
	require.NotEmpty(t, raw, "native CLI argv must include --mcp-config")
	var config struct {
		MCPServers map[string]any `json:"mcpServers"`
	}
	require.NoError(t, json.Unmarshal([]byte(raw), &config))
	require.Equal(t, map[string]any{
		"type":    "stdio",
		"command": "/mcp-server",
		"args":    []any{"--stdio"},
		"env":     map[string]any{"TOKEN": "${TOKEN}"},
	}, config.MCPServers["local"])
	require.Equal(t, map[string]any{
		"type":    "http",
		"url":     "https://example.invalid/mcp",
		"headers": map[string]any{"Authorization": "Bearer ${TOKEN}"},
	}, config.MCPServers["remote"])
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
}
