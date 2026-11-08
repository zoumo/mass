package codex

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/zoumo/mass/mass-go/pkg/runtime"
)

// TestCodexDriverTestPeer is a synthetic app-server peer. It is inert unless
// the parent test launches this test binary with MASS_CODEX_DRIVER_PEER=1.
func TestCodexDriverTestPeer(t *testing.T) {
	if os.Getenv("MASS_CODEX_DRIVER_PEER") != "1" {
		return
	}
	trace := os.Getenv("MASS_CODEX_DRIVER_TRACE")
	cwd := os.Getenv("MASS_CODEX_DRIVER_CWD")
	enc := json.NewEncoder(os.Stdout)
	scan := bufio.NewScanner(os.Stdin)
	scan.Buffer(make([]byte, 4096), 1<<20)
	for scan.Scan() {
		var envelope struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Params map[string]any  `json:"params"`
		}
		if err := json.Unmarshal(scan.Bytes(), &envelope); err != nil {
			os.Exit(2)
		}
		switch envelope.Method {
		case "initialize":
			if err := enc.Encode(map[string]any{"id": envelope.ID, "result": map[string]any{
				"userAgent": "synthetic", "codexHome": "/synthetic", "platformFamily": "unix", "platformOs": "linux",
			}}); err != nil {
				os.Exit(3)
			}
		case "initialized":
		case "thread/start", "thread/resume":
			if err := appendCodexPeerTrace(trace, envelope.Method, envelope.Params); err != nil {
				os.Exit(4)
			}
			result := map[string]any{
				"thread": map[string]any{
					"id": "native-thread", "sessionId": "native-session", "cliVersion": "synthetic",
					"createdAt": 1, "updatedAt": 2, "cwd": cwd, "ephemeral": false,
					"modelProvider": "synthetic", "preview": "", "projectId": nil, "source": "appServer",
					"status": map[string]any{"type": "idle"}, "turns": []any{},
				},
				"approvalPolicy": "on-request", "approvalsReviewer": "user", "cwd": cwd,
				"model": "synthetic", "modelProvider": "synthetic", "sandbox": map[string]any{"type": "readOnly"},
			}
			if err := enc.Encode(map[string]any{"id": envelope.ID, "result": result}); err != nil {
				os.Exit(5)
			}
		case "thread/unsubscribe":
			if err := enc.Encode(map[string]any{"id": envelope.ID, "result": map[string]any{"status": "unsubscribed"}}); err != nil {
				os.Exit(6)
			}
		}
	}
	os.Exit(0)
}

func appendCodexPeerTrace(path, method string, params map[string]any) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	return json.NewEncoder(f).Encode(map[string]any{"method": method, "params": params})
}

func TestCodexDriverAppliesMCPConfigOnOpenAndResume(t *testing.T) {
	executable, err := os.Executable()
	require.NoError(t, err)
	cwd := t.TempDir()
	trace := filepath.Join(t.TempDir(), "peer.jsonl")
	driver, err := (Factory{RecoveryDir: t.TempDir()}).New("agent", runtime.Config{
		"executable": executable,
		"args":       []string{"-test.run=^TestCodexDriverTestPeer$"},
		"envPolicy":  "replace",
		"env": map[string]any{
			"MASS_CODEX_DRIVER_PEER":  "1",
			"MASS_CODEX_DRIVER_TRACE": trace,
			"MASS_CODEX_DRIVER_CWD":   cwd,
			"CODEX_HOME":              t.TempDir(),
			"HOME":                    t.TempDir(),
			"TMPDIR":                  t.TempDir(),
			"TOKEN":                   "fixture-secret",
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

	address := runtime.Address{Namespace: "one", SessionName: "demo"}
	remote := []runtime.MCPServer{{
		Name:    "remote",
		Type:    runtime.MCPTransportHTTP,
		URL:     "https://example.invalid/mcp",
		Headers: map[string]string{"Authorization": "Bearer ${TOKEN}"},
	}}
	session, err := driver.OpenSession(t.Context(), address, runtime.SessionSpec{Cwd: cwd, MCPServers: &remote})
	require.NoError(t, err)
	require.Equal(t, "native-thread", session.SessionID)
	start := waitCodexPeerTrace(t, trace, "thread/start")
	startConfig := codexPeerConfig(t, start)
	require.Equal(t, map[string]any{
		"url":                  "https://example.invalid/mcp",
		"bearer_token_env_var": "TOKEN",
	}, startConfig["mcp_servers.remote"])
	require.NoError(t, driver.CloseSession(t.Context(), session.SessionID))

	local := []runtime.MCPServer{{
		Name:    "local",
		Type:    runtime.MCPTransportStdio,
		Command: "/mcp-server",
		Args:    []string{"--stdio"},
		Env:     map[string]string{"TOKEN": "${TOKEN}"},
	}}
	_, err = driver.ResumeSession(t.Context(), session.SessionID, address, runtime.SessionSpec{Cwd: cwd, MCPServers: &local})
	require.NoError(t, err)
	resume := waitCodexPeerTrace(t, trace, "thread/resume")
	resumeConfig := codexPeerConfig(t, resume)
	require.Equal(t, map[string]any{
		"command": "/mcp-server",
		"args":    []any{"--stdio"},
		"env":     map[string]any{"TOKEN": "fixture-secret"},
	}, resumeConfig["mcp_servers.local"])
	require.NoError(t, driver.CloseSession(t.Context(), session.SessionID))
}

func TestCodexDriverRejectsConflictingAuthorizationBeforeOpen(t *testing.T) {
	executable, err := os.Executable()
	require.NoError(t, err)
	driver, err := (Factory{RecoveryDir: t.TempDir()}).New("agent", runtime.Config{
		"executable": executable,
		"envPolicy":  "replace",
		"env": map[string]any{
			"CODEX_HOME": t.TempDir(),
			"HOME":       t.TempDir(),
		},
	})
	require.NoError(t, err)
	servers := []runtime.MCPServer{{
		Name:              "remote",
		Type:              runtime.MCPTransportHTTP,
		URL:               "https://example.invalid/mcp",
		EnvHeaders:        map[string]string{"Authorization": "TOKEN"},
		BearerTokenEnvVar: "OTHER_TOKEN",
	}}
	_, err = driver.OpenSession(t.Context(), runtime.Address{Namespace: "one", SessionName: "demo"}, runtime.SessionSpec{Cwd: t.TempDir(), MCPServers: &servers})
	require.ErrorIs(t, err, runtime.ErrInvalidArgument)
	require.ErrorContains(t, err, "authorization header conflicts with bearerTokenEnvVar")
}

func waitCodexPeerTrace(t *testing.T, path, method string) map[string]any {
	t.Helper()
	var found map[string]any
	require.Eventually(t, func() bool {
		data, err := os.ReadFile(path)
		if err != nil {
			return false
		}
		for line := range bytes.SplitSeq(data, []byte{'\n'}) {
			var entry map[string]any
			if json.Unmarshal(line, &entry) == nil && entry["method"] == method {
				found = entry
				return true
			}
		}
		return false
	}, 5*time.Second, 10*time.Millisecond)
	return found
}

func codexPeerConfig(t *testing.T, entry map[string]any) map[string]any {
	t.Helper()
	require.NotNil(t, entry)
	params, ok := entry["params"].(map[string]any)
	require.True(t, ok, "trace entry must include params")
	config, ok := params["config"].(map[string]any)
	require.True(t, ok, "native thread request must include config")
	return config
}
