package acp

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/coder/acp-go-sdk"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	apiruntime "github.com/zoumo/mass/pkg/runtime-spec/api"
)

// newTestManager builds a Manager with no agent process — just enough to
// exercise unexported helpers and acpClient methods.
func newTestManager(policy apiruntime.PermissionPolicy) *Manager {
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
	cfg := apiruntime.Config{
		MassVersion:    "0.1.0",
		Metadata:       apiruntime.Metadata{Name: "test-agent"},
		AgentRoot:      apiruntime.AgentRoot{Path: "workspace"},
		ClientProtocol: apiruntime.ClientProtocolACP,
		Session: apiruntime.Session{
			Permissions: policy,
		},
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
	rejectOpt := acp.PermissionOption{OptionId: "reject-1", Kind: acp.PermissionOptionKindRejectOnce, Name: "Reject"}

	tests := []struct {
		name      string
		options   []acp.PermissionOption
		wantOptID acp.PermissionOptionId
	}{
		{
			name:      "with reject option: selects it",
			options:   []acp.PermissionOption{rejectOpt},
			wantOptID: "reject-1",
		},
		{
			name:      "no options: returns canceled",
			options:   nil,
			wantOptID: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mgr := newTestManager(apiruntime.DenyAll)
			defer cleanupManager(mgr)
			client := &acpClient{mgr: mgr, logger: slog.Default()}

			resp, err := client.RequestPermission(context.Background(), acp.RequestPermissionRequest{
				Options: tt.options,
			})
			require.NoError(t, err)
			if tt.wantOptID != "" {
				require.NotNil(t, resp.Outcome.Selected)
				assert.Equal(t, tt.wantOptID, resp.Outcome.Selected.OptionId)
			} else {
				assert.NotNil(t, resp.Outcome.Cancelled) //nolint:misspell
			}
		})
	}
}

func TestAcpClient_RequestPermission_ApproveReads(t *testing.T) {
	readKind := acp.ToolKindRead
	searchKind := acp.ToolKindSearch
	editKind := acp.ToolKindEdit
	executeKind := acp.ToolKindExecute

	allowOpt := acp.PermissionOption{OptionId: "allow-1", Kind: acp.PermissionOptionKindAllowOnce, Name: "Allow"}
	rejectOpt := acp.PermissionOption{OptionId: "reject-1", Kind: acp.PermissionOptionKindRejectOnce, Name: "Reject"}
	both := []acp.PermissionOption{allowOpt, rejectOpt}

	titleStr := func(s string) *string { return &s }

	tests := []struct {
		name      string
		toolCall  acp.ToolCallUpdate
		options   []acp.PermissionOption
		wantAllow bool
	}{
		{
			name:      "read kind → allow",
			toolCall:  acp.ToolCallUpdate{Kind: &readKind},
			options:   both,
			wantAllow: true,
		},
		{
			name:      "search kind → allow",
			toolCall:  acp.ToolCallUpdate{Kind: &searchKind},
			options:   both,
			wantAllow: true,
		},
		{
			name:      "edit kind → reject",
			toolCall:  acp.ToolCallUpdate{Kind: &editKind},
			options:   both,
			wantAllow: false,
		},
		{
			name:      "execute kind → reject",
			toolCall:  acp.ToolCallUpdate{Kind: &executeKind},
			options:   both,
			wantAllow: false,
		},
		{
			name:      "nil kind, title 'Read config file' → allow",
			toolCall:  acp.ToolCallUpdate{Title: titleStr("Read config file")},
			options:   both,
			wantAllow: true,
		},
		{
			name:      "nil kind, title 'Write auth.go' → reject",
			toolCall:  acp.ToolCallUpdate{Title: titleStr("Write auth.go")},
			options:   both,
			wantAllow: false,
		},
		{
			name:      "nil kind, nil title → reject (unknown defaults to deny)",
			toolCall:  acp.ToolCallUpdate{},
			options:   both,
			wantAllow: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mgr := newTestManager(apiruntime.ApproveReads)
			defer cleanupManager(mgr)
			client := &acpClient{mgr: mgr, logger: slog.Default()}

			resp, err := client.RequestPermission(context.Background(), acp.RequestPermissionRequest{
				ToolCall: tt.toolCall,
				Options:  tt.options,
			})

			require.NoError(t, err)
			if tt.wantAllow {
				require.NotNil(t, resp.Outcome.Selected)
				assert.Equal(t, acp.PermissionOptionId("allow-1"), resp.Outcome.Selected.OptionId)
			} else {
				// either canceled or selected reject option
				if resp.Outcome.Selected != nil {
					assert.Equal(t, acp.PermissionOptionId("reject-1"), resp.Outcome.Selected.OptionId)
				} else {
					assert.NotNil(t, resp.Outcome.Cancelled) //nolint:misspell
				}
			}
		})
	}
}

func TestAcpClient_RequestPermission_ApproveAll(t *testing.T) {
	tests := []struct {
		name      string
		options   []acp.PermissionOption
		wantOptID acp.PermissionOptionId
	}{
		{
			name:      "no options: returns selected with empty optionId",
			options:   nil,
			wantOptID: "",
		},
		{
			name: "prefer allow_once over first option when first is reject",
			options: []acp.PermissionOption{
				{OptionId: "reject-1", Kind: acp.PermissionOptionKindRejectOnce, Name: "Reject"},
				{OptionId: "allow-1", Kind: acp.PermissionOptionKindAllowOnce, Name: "Allow"},
			},
			wantOptID: "allow-1",
		},
		{
			name: "prefer allow_once over allow_always",
			options: []acp.PermissionOption{
				{OptionId: "allow-always", Kind: acp.PermissionOptionKindAllowAlways, Name: "Allow always"},
				{OptionId: "allow-once", Kind: acp.PermissionOptionKindAllowOnce, Name: "Allow once"},
			},
			wantOptID: "allow-once",
		},
		{
			name: "no allow option: fall back to options[0]",
			options: []acp.PermissionOption{
				{OptionId: "reject-1", Kind: acp.PermissionOptionKindRejectOnce, Name: "Reject"},
			},
			wantOptID: "reject-1",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mgr := newTestManager(apiruntime.ApproveAll)
			defer cleanupManager(mgr)
			client := &acpClient{mgr: mgr, logger: slog.Default()}

			resp, err := client.RequestPermission(context.Background(), acp.RequestPermissionRequest{
				Options: tt.options,
			})
			require.NoError(t, err)
			require.NotNil(t, resp.Outcome.Selected)
			assert.Equal(t, tt.wantOptID, resp.Outcome.Selected.OptionId)
		})
	}
}

// ── Not-supported stubs ───────────────────────────────────────────────────────

func TestAcpClient_NotSupported(t *testing.T) {
	mgr := newTestManager(apiruntime.ApproveAll)
	defer cleanupManager(mgr)
	client := &acpClient{mgr: mgr, logger: slog.Default()}
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
	servers := []apiruntime.McpServer{
		{Type: "sse", URL: "http://example.com/sse"},
	}
	result := convertMcpServers(servers)
	require.Len(t, result, 1)
	require.NotNil(t, result[0].Sse, "expected Sse field to be populated for type=sse")
	assert.Equal(t, "http://example.com/sse", result[0].Sse.Url)
	assert.Nil(t, result[0].Http, "expected Http field to be nil for type=sse")
}

func TestConvertMcpServers_HTTPBranch(t *testing.T) {
	servers := []apiruntime.McpServer{
		{Type: "http", URL: "http://example.com/mcp"},
	}
	result := convertMcpServers(servers)
	require.Len(t, result, 1)
	require.NotNil(t, result[0].Http, "expected Http field to be populated for type=http")
	assert.Equal(t, "http://example.com/mcp", result[0].Http.Url)
	assert.Nil(t, result[0].Sse, "expected Sse field to be nil for type=http")
}

func TestConvertMcpServers_StdioBranch(t *testing.T) {
	servers := []apiruntime.McpServer{
		{
			Type:    "stdio",
			Name:    "room-tools",
			Command: "/usr/bin/room-mcp-server",
			Args:    []string{"--verbose"},
			Env:     []apiruntime.EnvVar{{Name: "FOO", Value: "bar"}, {Name: "BAZ", Value: "qux"}},
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
	mgr := newTestManager(apiruntime.ApproveAll)
	defer cleanupManager(mgr)
	client := &acpClient{mgr: mgr, logger: slog.Default()}

	// Fill the channel to capacity (64).
	for i := 0; i < cap(mgr.events); i++ {
		mgr.events <- acp.SessionNotification{}
	}

	// A SessionUpdate on a full channel must not block and must return nil.
	err := client.SessionUpdate(context.Background(), acp.SessionNotification{})
	assert.NoError(t, err, "SessionUpdate should not error even when channel is full")
}

func TestAcpClient_SessionUpdate_Delivers(t *testing.T) {
	mgr := newTestManager(apiruntime.ApproveAll)
	defer cleanupManager(mgr)
	client := &acpClient{mgr: mgr, logger: slog.Default()}

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
