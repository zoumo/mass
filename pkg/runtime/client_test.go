package runtime

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/coder/acp-go-sdk"
	"github.com/open-agent-d/open-agent-d/pkg/spec"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newTestManager builds a Manager with no agent process — just enough to
// exercise unexported helpers and acpClient methods.
func newTestManager(policy spec.PermissionPolicy) *Manager {
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
	cfg := spec.Config{
		OarVersion:  "0.1.0",
		Metadata:    spec.Metadata{Name: "test-agent"},
		AgentRoot:   spec.AgentRoot{Path: "workspace"},
		Permissions: policy,
	}
	return New(cfg, bundleDir, stateDir)
}

// cleanupManager removes the bundleDir and stateDir created by newTestManager.
func cleanupManager(m *Manager) {
	_ = os.RemoveAll(m.bundleDir)
	_ = os.RemoveAll(m.stateDir)
}

// ── readFile ─────────────────────────────────────────────────────────────────

func TestReadFile_ExistingFile(t *testing.T) {
	f, err := os.CreateTemp("", "readfile-test-*")
	require.NoError(t, err)
	defer os.Remove(f.Name())

	content := "hello, readFile"
	_, err = f.WriteString(content)
	require.NoError(t, err)
	require.NoError(t, f.Close())

	got, err := readFile(f.Name())
	require.NoError(t, err)
	assert.Equal(t, content, got)
}

func TestReadFile_NonExistent(t *testing.T) {
	_, err := readFile("/nonexistent/path/that/does/not/exist")
	assert.Error(t, err, "expected error for nonexistent path")
}

// ── writeFile ────────────────────────────────────────────────────────────────

func TestWriteFile_CreatesAndWrites(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "output.txt")
	content := "hello, writeFile"

	require.NoError(t, writeFile(path, content))

	data, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, content, string(data))
}

func TestWriteFile_Truncates(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "trunc.txt")

	require.NoError(t, writeFile(path, "long original content"))
	require.NoError(t, writeFile(path, "short"))

	data, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, "short", string(data))
}

// ── acpClient.ReadTextFile ───────────────────────────────────────────────────

func TestAcpClient_ReadTextFile_ApproveReads(t *testing.T) {
	mgr := newTestManager(spec.ApproveReads)
	defer cleanupManager(mgr)

	// Write a temp file to read.
	f, err := os.CreateTemp("", "readtextfile-*")
	require.NoError(t, err)
	defer os.Remove(f.Name())
	_, err = f.WriteString("approve-reads content")
	require.NoError(t, err)
	require.NoError(t, f.Close())

	client := &acpClient{mgr: mgr}
	resp, err := client.ReadTextFile(context.Background(), acp.ReadTextFileRequest{Path: f.Name()})
	require.NoError(t, err)
	assert.Equal(t, "approve-reads content", resp.Content)
}

func TestAcpClient_ReadTextFile_ApproveAll(t *testing.T) {
	mgr := newTestManager(spec.ApproveAll)
	defer cleanupManager(mgr)

	f, err := os.CreateTemp("", "readtextfile-aa-*")
	require.NoError(t, err)
	defer os.Remove(f.Name())
	_, err = f.WriteString("approve-all content")
	require.NoError(t, err)
	require.NoError(t, f.Close())

	client := &acpClient{mgr: mgr}
	resp, err := client.ReadTextFile(context.Background(), acp.ReadTextFileRequest{Path: f.Name()})
	require.NoError(t, err)
	assert.Equal(t, "approve-all content", resp.Content)
}

func TestAcpClient_ReadTextFile_DenyAll(t *testing.T) {
	mgr := newTestManager(spec.DenyAll)
	defer cleanupManager(mgr)

	client := &acpClient{mgr: mgr}
	_, err := client.ReadTextFile(context.Background(), acp.ReadTextFileRequest{Path: "/some/file"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "permission denied")
}

func TestAcpClient_ReadTextFile_FileReadError(t *testing.T) {
	mgr := newTestManager(spec.ApproveAll)
	defer cleanupManager(mgr)

	client := &acpClient{mgr: mgr}
	_, err := client.ReadTextFile(context.Background(), acp.ReadTextFileRequest{Path: "/nonexistent/path/file.txt"})
	require.Error(t, err, "expected error reading nonexistent file")
}

// ── acpClient.RequestPermission ──────────────────────────────────────────────

func TestAcpClient_RequestPermission_DenyAll(t *testing.T) {
	mgr := newTestManager(spec.DenyAll)
	defer cleanupManager(mgr)

	client := &acpClient{mgr: mgr}
	_, err := client.RequestPermission(context.Background(), acp.RequestPermissionRequest{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "deny-all")
}

func TestAcpClient_RequestPermission_ApproveReads(t *testing.T) {
	mgr := newTestManager(spec.ApproveReads)
	defer cleanupManager(mgr)

	client := &acpClient{mgr: mgr}
	_, err := client.RequestPermission(context.Background(), acp.RequestPermissionRequest{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "approve-reads")
}

func TestAcpClient_RequestPermission_ApproveAll(t *testing.T) {
	mgr := newTestManager(spec.ApproveAll)
	defer cleanupManager(mgr)

	client := &acpClient{mgr: mgr}
	_, err := client.RequestPermission(context.Background(), acp.RequestPermissionRequest{})
	require.NoError(t, err, "approve-all should return no error")
}

// ── Terminal stubs ────────────────────────────────────────────────────────────

func TestAcpClient_TerminalStubs(t *testing.T) {
	mgr := newTestManager(spec.ApproveAll)
	defer cleanupManager(mgr)
	client := &acpClient{mgr: mgr}
	ctx := context.Background()

	_, err := client.CreateTerminal(ctx, acp.CreateTerminalRequest{})
	require.Error(t, err)
	assert.True(t, strings.Contains(err.Error(), "terminal not supported"),
		"CreateTerminal: expected 'terminal not supported', got %q", err.Error())

	_, err = client.KillTerminalCommand(ctx, acp.KillTerminalCommandRequest{})
	require.Error(t, err)
	assert.True(t, strings.Contains(err.Error(), "terminal not supported"),
		"KillTerminalCommand: expected 'terminal not supported', got %q", err.Error())

	_, err = client.TerminalOutput(ctx, acp.TerminalOutputRequest{})
	require.Error(t, err)
	assert.True(t, strings.Contains(err.Error(), "terminal not supported"),
		"TerminalOutput: expected 'terminal not supported', got %q", err.Error())

	_, err = client.ReleaseTerminal(ctx, acp.ReleaseTerminalRequest{})
	require.Error(t, err)
	assert.True(t, strings.Contains(err.Error(), "terminal not supported"),
		"ReleaseTerminal: expected 'terminal not supported', got %q", err.Error())

	_, err = client.WaitForTerminalExit(ctx, acp.WaitForTerminalExitRequest{})
	require.Error(t, err)
	assert.True(t, strings.Contains(err.Error(), "terminal not supported"),
		"WaitForTerminalExit: expected 'terminal not supported', got %q", err.Error())
}

// ── convertMcpServers ─────────────────────────────────────────────────────────

func TestConvertMcpServers_SSEBranch(t *testing.T) {
	servers := []spec.McpServer{
		{Type: "sse", URL: "http://example.com/sse"},
	}
	result := convertMcpServers(servers)
	require.Len(t, result, 1)
	require.NotNil(t, result[0].Sse, "expected Sse field to be populated for type=sse")
	assert.Equal(t, "http://example.com/sse", result[0].Sse.Url)
	assert.Nil(t, result[0].Http, "expected Http field to be nil for type=sse")
}

func TestConvertMcpServers_HTTPBranch(t *testing.T) {
	servers := []spec.McpServer{
		{Type: "http", URL: "http://example.com/mcp"},
	}
	result := convertMcpServers(servers)
	require.Len(t, result, 1)
	require.NotNil(t, result[0].Http, "expected Http field to be populated for type=http")
	assert.Equal(t, "http://example.com/mcp", result[0].Http.Url)
	assert.Nil(t, result[0].Sse, "expected Sse field to be nil for type=http")
}

func TestConvertMcpServers_Empty(t *testing.T) {
	result := convertMcpServers(nil)
	assert.Empty(t, result, "nil input should produce empty slice")
}

// ── acpClient.SessionUpdate ───────────────────────────────────────────────────

func TestAcpClient_SessionUpdate_DropsWhenFull(t *testing.T) {
	mgr := newTestManager(spec.ApproveAll)
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
	mgr := newTestManager(spec.ApproveAll)
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
