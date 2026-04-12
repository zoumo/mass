package runtime

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/coder/acp-go-sdk"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/open-agent-d/open-agent-d/api"
	apispec "github.com/open-agent-d/open-agent-d/api/spec"
)

// newTestManager builds a Manager with no agent process — just enough to
// exercise unexported helpers and acpClient methods.
func newTestManager(policy apispec.PermissionPolicy) *Manager {
	bundleDir, err := os.MkdirTemp("", "oad-bundle-")
	if err != nil {
		panic("newTestManager: MkdirTemp bundleDir: " + err.Error())
	}
	workspaceDir := filepath.Join(bundleDir, "workspace")
	if err := os.MkdirAll(workspaceDir, 0o755); err != nil {
		panic("newTestManager: MkdirAll workspace: " + err.Error())
	}
	stateDir, err := os.MkdirTemp("", "oad-state-")
	if err != nil {
		panic("newTestManager: MkdirTemp stateDir: " + err.Error())
	}
	cfg := apispec.Config{
		OarVersion:  "0.1.0",
		Metadata:    apispec.Metadata{Name: "test-agent"},
		AgentRoot:   apispec.AgentRoot{Path: "workspace"},
		Permissions: policy,
	}
	return New(cfg, bundleDir, stateDir, slog.Default())
}

// cleanupManager removes the bundleDir and stateDir created by newTestManager.
func cleanupManager(m *Manager) {
	_ = os.RemoveAll(m.bundleDir)
	_ = os.RemoveAll(m.stateDir)
}

// ── acpClient.RequestPermission ──────────────────────────────────────────────

func TestAcpClient_RequestPermission_DenyAll(t *testing.T) {
	mgr := newTestManager(apispec.DenyAll)
	defer cleanupManager(mgr)

	client := &acpClient{mgr: mgr}
	_, err := client.RequestPermission(context.Background(), acp.RequestPermissionRequest{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "deny-all")
}

func TestAcpClient_RequestPermission_ApproveReads(t *testing.T) {
	mgr := newTestManager(apispec.ApproveReads)
	defer cleanupManager(mgr)

	client := &acpClient{mgr: mgr}
	_, err := client.RequestPermission(context.Background(), acp.RequestPermissionRequest{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "approve-reads")
}

func TestAcpClient_RequestPermission_ApproveAll(t *testing.T) {
	mgr := newTestManager(apispec.ApproveAll)
	defer cleanupManager(mgr)

	client := &acpClient{mgr: mgr}
	_, err := client.RequestPermission(context.Background(), acp.RequestPermissionRequest{})
	require.NoError(t, err, "approve-all should return no error")
}

// ── Not-supported stubs ───────────────────────────────────────────────────────

func TestAcpClient_NotSupported(t *testing.T) {
	mgr := newTestManager(apispec.ApproveAll)
	defer cleanupManager(mgr)
	client := &acpClient{mgr: mgr}
	ctx := context.Background()

	_, err := client.ReadTextFile(ctx, acp.ReadTextFileRequest{})
	require.Error(t, err)
	assert.Equal(t, "not supported", err.Error())

	_, err = client.WriteTextFile(ctx, acp.WriteTextFileRequest{})
	require.Error(t, err)
	assert.Equal(t, "not supported", err.Error())

	_, err = client.CreateTerminal(ctx, acp.CreateTerminalRequest{})
	require.Error(t, err)
	assert.Equal(t, "not supported", err.Error())

	_, err = client.KillTerminalCommand(ctx, acp.KillTerminalCommandRequest{})
	require.Error(t, err)
	assert.Equal(t, "not supported", err.Error())

	_, err = client.TerminalOutput(ctx, acp.TerminalOutputRequest{})
	require.Error(t, err)
	assert.Equal(t, "not supported", err.Error())

	_, err = client.ReleaseTerminal(ctx, acp.ReleaseTerminalRequest{})
	require.Error(t, err)
	assert.Equal(t, "not supported", err.Error())

	_, err = client.WaitForTerminalExit(ctx, acp.WaitForTerminalExitRequest{})
	require.Error(t, err)
	assert.Equal(t, "not supported", err.Error())
}

// ── convertMcpServers ─────────────────────────────────────────────────────────

func TestConvertMcpServers_SSEBranch(t *testing.T) {
	servers := []apispec.McpServer{
		{Type: "sse", URL: "http://example.com/sse"},
	}
	result := convertMcpServers(servers)
	require.Len(t, result, 1)
	require.NotNil(t, result[0].Sse, "expected Sse field to be populated for type=sse")
	assert.Equal(t, "http://example.com/sse", result[0].Sse.Url)
	assert.Nil(t, result[0].Http, "expected Http field to be nil for type=sse")
}

func TestConvertMcpServers_HTTPBranch(t *testing.T) {
	servers := []apispec.McpServer{
		{Type: "http", URL: "http://example.com/mcp"},
	}
	result := convertMcpServers(servers)
	require.Len(t, result, 1)
	require.NotNil(t, result[0].Http, "expected Http field to be populated for type=http")
	assert.Equal(t, "http://example.com/mcp", result[0].Http.Url)
	assert.Nil(t, result[0].Sse, "expected Sse field to be nil for type=http")
}

func TestConvertMcpServers_StdioBranch(t *testing.T) {
	servers := []apispec.McpServer{
		{
			Type:    "stdio",
			Name:    "room-tools",
			Command: "/usr/bin/room-mcp-server",
			Args:    []string{"--verbose"},
			Env:     []api.EnvVar{{Name: "FOO", Value: "bar"}, {Name: "BAZ", Value: "qux"}},
		},
	}
	result := convertMcpServers(servers)
	require.Len(t, result, 1)
	require.NotNil(t, result[0].Stdio, "expected Stdio field to be populated for type=stdio")
	assert.Equal(t, "room-tools", result[0].Stdio.Name)
	assert.Equal(t, "/usr/bin/room-mcp-server", result[0].Stdio.Command)
	assert.Equal(t, []string{"--verbose"}, result[0].Stdio.Args)
	require.Len(t, result[0].Stdio.Env, 2)
	assert.Equal(t, "FOO", result[0].Stdio.Env[0].Name)
	assert.Equal(t, "bar", result[0].Stdio.Env[0].Value)
	assert.Equal(t, "BAZ", result[0].Stdio.Env[1].Name)
	assert.Equal(t, "qux", result[0].Stdio.Env[1].Value)
	assert.Nil(t, result[0].Http, "expected Http field to be nil for type=stdio")
	assert.Nil(t, result[0].Sse, "expected Sse field to be nil for type=stdio")
}

func TestConvertMcpServers_Empty(t *testing.T) {
	result := convertMcpServers(nil)
	assert.Empty(t, result, "nil input should produce empty slice")
}

// ── acpClient.SessionUpdate ───────────────────────────────────────────────────

func TestAcpClient_SessionUpdate_DropsWhenFull(t *testing.T) {
	mgr := newTestManager(apispec.ApproveAll)
	defer cleanupManager(mgr)
	client := &acpClient{mgr: mgr}

	// Fill the channel to capacity (64).
	for i := 0; i < cap(mgr.events); i++ {
		mgr.events <- acp.SessionNotification{}
	}

	// A SessionUpdate on a full channel must not block and must return nil.
	err := client.SessionUpdate(context.Background(), acp.SessionNotification{})
	assert.NoError(t, err, "SessionUpdate should not error even when channel is full")
}

func TestAcpClient_SessionUpdate_Delivers(t *testing.T) {
	mgr := newTestManager(apispec.ApproveAll)
	defer cleanupManager(mgr)
	client := &acpClient{mgr: mgr}

	n := acp.SessionNotification{}
	err := client.SessionUpdate(context.Background(), n)
	require.NoError(t, err)

	select {
	case got := <-mgr.events:
		_ = got // just confirm it was delivered
	default:
		t.Fatal("expected notification to be delivered to events channel")
	}
}
