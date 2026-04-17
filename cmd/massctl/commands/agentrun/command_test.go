package agentrun

import (
	"bytes"
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	pkgariapi "github.com/zoumo/mass/pkg/ari/api"
	"github.com/zoumo/mass/cmd/massctl/commands/cliutil"
)

// ── mock AgentRunOps ─────────────────────────────────────────────────────────

type mockAgentRunOps struct {
	promptFn  func(ctx context.Context, key pkgariapi.ObjectKey, prompt []pkgariapi.ContentBlock) (*pkgariapi.AgentRunPromptResult, error)
	cancelFn  func(ctx context.Context, key pkgariapi.ObjectKey) error
	stopFn    func(ctx context.Context, key pkgariapi.ObjectKey) error
	restartFn func(ctx context.Context, key pkgariapi.ObjectKey) (*pkgariapi.AgentRun, error)
}

func (m *mockAgentRunOps) Prompt(ctx context.Context, key pkgariapi.ObjectKey, prompt []pkgariapi.ContentBlock) (*pkgariapi.AgentRunPromptResult, error) {
	if m.promptFn != nil {
		return m.promptFn(ctx, key, prompt)
	}
	return &pkgariapi.AgentRunPromptResult{Accepted: true}, nil
}

func (m *mockAgentRunOps) Cancel(ctx context.Context, key pkgariapi.ObjectKey) error {
	if m.cancelFn != nil {
		return m.cancelFn(ctx, key)
	}
	return nil
}

func (m *mockAgentRunOps) Stop(ctx context.Context, key pkgariapi.ObjectKey) error {
	if m.stopFn != nil {
		return m.stopFn(ctx, key)
	}
	return nil
}

func (m *mockAgentRunOps) Restart(ctx context.Context, key pkgariapi.ObjectKey) (*pkgariapi.AgentRun, error) {
	if m.restartFn != nil {
		return m.restartFn(ctx, key)
	}
	return &pkgariapi.AgentRun{}, nil
}

// ── mock WorkspaceOps (stub — not used in agentrun tests) ────────────────────

type mockWorkspaceOps struct{}

func (m *mockWorkspaceOps) Send(context.Context, *pkgariapi.WorkspaceSendParams) (*pkgariapi.WorkspaceSendResult, error) {
	return &pkgariapi.WorkspaceSendResult{}, nil
}

// ── mock Client ──────────────────────────────────────────────────────────────

type mockClient struct {
	createFn func(ctx context.Context, obj pkgariapi.Object) error
	getFn    func(ctx context.Context, key pkgariapi.ObjectKey, obj pkgariapi.Object) error
	updateFn func(ctx context.Context, obj pkgariapi.Object) error
	listFn   func(ctx context.Context, list pkgariapi.ObjectList, opts ...pkgariapi.ListOption) error
	deleteFn func(ctx context.Context, key pkgariapi.ObjectKey, obj pkgariapi.Object) error

	agentRunOps  *mockAgentRunOps
	workspaceOps *mockWorkspaceOps
}

func newMockClient() *mockClient {
	return &mockClient{
		agentRunOps:  &mockAgentRunOps{},
		workspaceOps: &mockWorkspaceOps{},
	}
}

func (m *mockClient) Create(ctx context.Context, obj pkgariapi.Object) error {
	if m.createFn != nil {
		return m.createFn(ctx, obj)
	}
	return nil
}

func (m *mockClient) Get(ctx context.Context, key pkgariapi.ObjectKey, obj pkgariapi.Object) error {
	if m.getFn != nil {
		return m.getFn(ctx, key, obj)
	}
	return nil
}

func (m *mockClient) Update(ctx context.Context, obj pkgariapi.Object) error {
	if m.updateFn != nil {
		return m.updateFn(ctx, obj)
	}
	return nil
}

func (m *mockClient) List(ctx context.Context, list pkgariapi.ObjectList, opts ...pkgariapi.ListOption) error {
	if m.listFn != nil {
		return m.listFn(ctx, list, opts...)
	}
	return nil
}

func (m *mockClient) Delete(ctx context.Context, key pkgariapi.ObjectKey, obj pkgariapi.Object) error {
	if m.deleteFn != nil {
		return m.deleteFn(ctx, key, obj)
	}
	return nil
}

func (m *mockClient) AgentRuns() pkgariapi.AgentRunOps   { return m.agentRunOps }
func (m *mockClient) Workspaces() pkgariapi.WorkspaceOps { return m.workspaceOps }
func (m *mockClient) Close() error                       { return nil }
func (m *mockClient) DisconnectNotify() <-chan struct{}   { return make(chan struct{}) }

// clientFn returns a cliutil.ClientFn that always returns mc.
func clientFn(mc *mockClient) cliutil.ClientFn {
	return func() (pkgariapi.Client, error) {
		return mc, nil
	}
}

// ── Tests ────────────────────────────────────────────────────────────────────

func TestCreateMissingFlags(t *testing.T) {
	mc := newMockClient()
	cmd := NewCommand(clientFn(mc))
	cmd.SetArgs([]string{"create"})
	// Silence usage/error output from cobra.
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})

	err := cmd.Execute()
	assert.Error(t, err, "create without required flags should fail")
}

func TestCreateSuccess(t *testing.T) {
	var created bool
	mc := newMockClient()
	mc.createFn = func(_ context.Context, obj pkgariapi.Object) error {
		created = true
		return nil
	}

	cmd := NewCommand(clientFn(mc))
	cmd.SetArgs([]string{"create", "--workspace", "ws1", "--name", "run1", "--agent", "agent1"})
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})

	err := cmd.Execute()
	require.NoError(t, err)
	assert.True(t, created, "Create should have been called")
}

func TestListSuccess(t *testing.T) {
	var listed bool
	mc := newMockClient()
	mc.listFn = func(_ context.Context, list pkgariapi.ObjectList, opts ...pkgariapi.ListOption) error {
		listed = true
		return nil
	}

	cmd := NewCommand(clientFn(mc))
	cmd.SetArgs([]string{"list"})
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})

	err := cmd.Execute()
	require.NoError(t, err)
	assert.True(t, listed, "List should have been called")
}

func TestGetSuccess(t *testing.T) {
	var gotKey pkgariapi.ObjectKey
	mc := newMockClient()
	mc.getFn = func(_ context.Context, key pkgariapi.ObjectKey, obj pkgariapi.Object) error {
		gotKey = key
		return nil
	}

	cmd := NewCommand(clientFn(mc))
	cmd.SetArgs([]string{"get", "--workspace", "ws1", "--name", "run1"})
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})

	err := cmd.Execute()
	require.NoError(t, err)
	assert.Equal(t, "ws1", gotKey.Workspace)
	assert.Equal(t, "run1", gotKey.Name)
}

func TestGetMissingFlags(t *testing.T) {
	mc := newMockClient()
	cmd := NewCommand(clientFn(mc))
	cmd.SetArgs([]string{"get"})
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})

	err := cmd.Execute()
	assert.Error(t, err, "get without required flags should fail")
}

func TestStopSuccess(t *testing.T) {
	var stopped bool
	mc := newMockClient()
	mc.agentRunOps.stopFn = func(_ context.Context, key pkgariapi.ObjectKey) error {
		stopped = true
		return nil
	}

	cmd := NewCommand(clientFn(mc))
	cmd.SetArgs([]string{"stop", "--workspace", "ws1", "--name", "run1"})
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})

	err := cmd.Execute()
	require.NoError(t, err)
	assert.True(t, stopped, "Stop should have been called")
}

func TestDeleteSuccess(t *testing.T) {
	var deleted bool
	mc := newMockClient()
	mc.deleteFn = func(_ context.Context, key pkgariapi.ObjectKey, obj pkgariapi.Object) error {
		deleted = true
		return nil
	}

	cmd := NewCommand(clientFn(mc))
	cmd.SetArgs([]string{"delete", "--workspace", "ws1", "--name", "run1"})
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})

	err := cmd.Execute()
	require.NoError(t, err)
	assert.True(t, deleted, "Delete should have been called")
}

func TestCancelSuccess(t *testing.T) {
	var cancelled bool
	mc := newMockClient()
	mc.agentRunOps.cancelFn = func(_ context.Context, key pkgariapi.ObjectKey) error {
		cancelled = true
		return nil
	}

	cmd := NewCommand(clientFn(mc))
	cmd.SetArgs([]string{"cancel", "--workspace", "ws1", "--name", "run1"})
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})

	err := cmd.Execute()
	require.NoError(t, err)
	assert.True(t, cancelled, "Cancel should have been called")
}

func TestRestartSuccess(t *testing.T) {
	var restarted bool
	mc := newMockClient()
	mc.agentRunOps.restartFn = func(_ context.Context, key pkgariapi.ObjectKey) (*pkgariapi.AgentRun, error) {
		restarted = true
		return &pkgariapi.AgentRun{}, nil
	}

	cmd := NewCommand(clientFn(mc))
	cmd.SetArgs([]string{"restart", "--workspace", "ws1", "--name", "run1"})
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})

	err := cmd.Execute()
	require.NoError(t, err)
	assert.True(t, restarted, "Restart should have been called")
}
