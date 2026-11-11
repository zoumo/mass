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

	"github.com/stretchr/testify/assert"
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
				"thread":         codexPeerThread(cwd),
				"approvalPolicy": "on-request", "approvalsReviewer": "user", "cwd": cwd,
				"model": "synthetic", "modelProvider": "synthetic", "sandbox": map[string]any{"type": "readOnly"},
			}
			if err := enc.Encode(map[string]any{"id": envelope.ID, "result": result}); err != nil {
				os.Exit(5)
			}
		case "thread/turns/list":
			if err := enc.Encode(map[string]any{"id": envelope.ID, "result": map[string]any{"data": []any{}, "nextCursor": nil}}); err != nil {
				os.Exit(5)
			}
		case "thread/read":
			if err := enc.Encode(map[string]any{"id": envelope.ID, "result": map[string]any{"thread": codexPeerThread(cwd)}}); err != nil {
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

func codexPeerThread(cwd string) map[string]any {
	return map[string]any{
		"id": "native-thread", "sessionId": "native-session", "cliVersion": "synthetic",
		"createdAt": 1, "updatedAt": 2, "cwd": cwd, "ephemeral": false,
		"modelProvider": "synthetic", "preview": "", "projectId": nil, "source": "appServer",
		"status": map[string]any{"type": "idle"}, "turns": []any{},
	}
}

func appendCodexPeerTrace(path, method string, params map[string]any) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	return json.NewEncoder(f).Encode(map[string]any{"method": method, "params": params})
}

func TestCodexDriverMCPForwardingOpenResumeAndLoad(t *testing.T) {
	configured := []runtime.MCPServer{
		{Name: "local", Type: runtime.MCPTransportStdio, Command: "/mcp-server", Args: []string{"--stdio"}, Env: map[string]string{"TOKEN": "${TOKEN}"}, Cwd: "/mcp-work"},
		{Name: "remote", Type: runtime.MCPTransportHTTP, URL: "https://example.invalid/mcp", Headers: map[string]string{"X-Mode": "fixture", "Authorization": "Bearer ${TOKEN}"}, EnvHeaders: map[string]string{"X-Token": "TOKEN"}},
	}
	empty := []runtime.MCPServer{}
	for name, servers := range map[string]*[]runtime.MCPServer{"omitted": nil, "empty": &empty, "stdio and http": &configured} {
		t.Run(name, func(t *testing.T) {
			driver, cwd, trace, openedID := newCodexMCPDriverTestPeer(t)
			address := runtime.Address{Namespace: "one", SessionName: "mcp"}
			spec := runtime.SessionSpec{Cwd: cwd, MCPServers: servers}
			opened, err := driver.OpenSession(t.Context(), address, spec)
			*openedID = opened.SessionID
			require.NoError(t, err)
			require.NoError(t, driver.CloseSession(t.Context(), opened.SessionID))
			*openedID = ""
			resumed, err := driver.ResumeSession(t.Context(), opened.SessionID, address, spec)
			*openedID = resumed.SessionID
			require.NoError(t, err)
			require.NoError(t, driver.CloseSession(t.Context(), opened.SessionID))
			*openedID = ""
			require.NoError(t, driver.LoadSession(t.Context(), opened.SessionID, address, spec, "mcp-load"))
			*openedID = opened.SessionID
			require.NoError(t, driver.CloseSession(t.Context(), opened.SessionID))
			*openedID = ""
			require.NoError(t, driver.Stop(t.Context()))
			data, err := os.ReadFile(trace)
			require.NoError(t, err)
			methods := []string{}
			for line := range bytes.SplitSeq(data, []byte{'\n'}) {
				var record struct {
					Method string
					Params map[string]any
				}
				if json.Unmarshal(line, &record) != nil {
					continue
				}
				methods = append(methods, record.Method)
				if servers == nil || len(*servers) == 0 {
					require.NotContains(t, record.Params, "config", "omitted/empty lists do not disable native ambient servers")
					continue
				}
				require.Equal(t, map[string]any{
					"mcp_servers.local":  map[string]any{"command": "/mcp-server", "args": []any{"--stdio"}, "env": map[string]any{"TOKEN": "fixture-secret"}, "cwd": "/mcp-work"},
					"mcp_servers.remote": map[string]any{"url": "https://example.invalid/mcp", "http_headers": map[string]any{"X-Mode": "fixture"}, "env_http_headers": map[string]any{"X-Token": "TOKEN"}, "bearer_token_env_var": "TOKEN"},
				}, record.Params["config"], "SDK emits only declared per-server overrides; native ambient fields remain provider-owned")
			}
			require.Equal(t, []string{"thread/start", "thread/resume", "thread/resume"}, methods)
		})
	}
}

func TestCodexDriverRejectsInvalidMCPBeforeNativeOpenResumeAndLoad(t *testing.T) {
	address := runtime.Address{Namespace: "one", SessionName: "mcp"}
	for name, server := range map[string]runtime.MCPServer{
		"unsupported SSE": {Name: "remote", Type: runtime.MCPTransportSSE, URL: "https://example.invalid/mcp"},
		"missing secret":  {Name: "local", Type: runtime.MCPTransportStdio, Command: "/mcp-server", Env: map[string]string{"TOKEN": "${MISSING_TOKEN}"}},
	} {
		t.Run(name, func(t *testing.T) {
			driver, cwd, trace, _ := newCodexMCPDriverTestPeer(t)
			servers := []runtime.MCPServer{server}
			spec := runtime.SessionSpec{Cwd: cwd, MCPServers: &servers}
			_, openErr := driver.OpenSession(t.Context(), address, spec)
			_, resumeErr := driver.ResumeSession(t.Context(), "native-thread", address, spec)
			loadErr := driver.LoadSession(t.Context(), "native-thread", address, spec, "invalid-mcp-load")
			for _, err := range []error{openErr, resumeErr, loadErr} {
				if server.Type == runtime.MCPTransportSSE {
					var public *runtime.OperationError
					require.ErrorAs(t, err, &public)
					require.Equal(t, "unsupported", public.Code)
					require.Equal(t, "mcp_transport_unsupported", public.Reason)
				} else {
					require.ErrorIs(t, err, runtime.ErrInvalidArgument)
					require.ErrorContains(t, err, "MISSING_TOKEN")
				}
			}
			require.NoFileExists(t, trace, "MCP validation must precede native thread/start or thread/resume")
		})
	}
}

func newCodexMCPDriverTestPeer(t *testing.T) (*Driver, string, string, *string) {
	t.Helper()
	executable, err := os.Executable()
	require.NoError(t, err)
	cwd := t.TempDir()
	trace := filepath.Join(t.TempDir(), "peer.jsonl")
	home := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(home, "config.toml"), []byte("[mcp_servers.ambient]\ncommand = \"/ambient\"\n"), 0o600))
	created, err := (Factory{RecoveryDir: t.TempDir()}).New("agent", runtime.Config{
		"executable": executable, "args": []string{"-test.run=^TestCodexDriverTestPeer$"}, "envPolicy": "replace",
		"env": map[string]any{"MASS_CODEX_DRIVER_PEER": "1", "MASS_CODEX_DRIVER_TRACE": trace, "MASS_CODEX_DRIVER_CWD": cwd, "CODEX_HOME": home, "HOME": t.TempDir(), "TMPDIR": t.TempDir(), "TOKEN": "fixture-secret"},
	})
	require.NoError(t, err)
	driver := created.(*Driver)
	stream, err := driver.Events()
	require.NoError(t, err)
	readCtx, stopRead := context.WithCancel(context.Background())
	readDone := make(chan struct{})
	var openedID string
	t.Cleanup(func() {
		if openedID != "" {
			cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			assert.NoError(t, driver.CloseSession(cleanup, openedID), "cleanup must release the native session")
			cancel()
		}
		cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		assert.NoError(t, driver.Stop(cleanup), "cleanup must release the provider even if session close failed")
		cancel()
		stopRead()
		<-readDone
	})
	go func() {
		defer close(readDone)
		for {
			if _, err := stream.Next(readCtx); err != nil {
				return
			}
		}
	}()
	_, err = driver.Start(t.Context())
	require.NoError(t, err)
	return driver, cwd, trace, &openedID
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
