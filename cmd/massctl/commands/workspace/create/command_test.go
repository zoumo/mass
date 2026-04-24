package create

import (
	"bytes"
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zoumo/mass/cmd/massctl/commands/cliutil"
	runapi "github.com/zoumo/mass/pkg/agentrun/api"
	pkgariapi "github.com/zoumo/mass/pkg/ari/api"
)

// ── mock AgentRunOps (stub — not used in create tests) ───────────────────────

type mockAgentRunOps struct{}

func (m *mockAgentRunOps) Prompt(context.Context, pkgariapi.ObjectKey, []runapi.ContentBlock) (*pkgariapi.AgentRunPromptResult, error) {
	return &pkgariapi.AgentRunPromptResult{}, nil
}
func (m *mockAgentRunOps) Cancel(context.Context, pkgariapi.ObjectKey) error { return nil }
func (m *mockAgentRunOps) Stop(context.Context, pkgariapi.ObjectKey) error   { return nil }
func (m *mockAgentRunOps) Restart(context.Context, pkgariapi.ObjectKey) (*pkgariapi.AgentRun, error) {
	return &pkgariapi.AgentRun{}, nil
}

func (m *mockAgentRunOps) TaskCreate(context.Context, *pkgariapi.AgentRunTaskCreateParams) (*pkgariapi.AgentRunTaskCreateResult, error) {
	return &pkgariapi.AgentRunTaskCreateResult{}, nil
}

func (m *mockAgentRunOps) TaskGet(context.Context, *pkgariapi.AgentRunTaskGetParams) (*pkgariapi.AgentTask, error) {
	return &pkgariapi.AgentTask{}, nil
}

func (m *mockAgentRunOps) TaskList(context.Context, *pkgariapi.AgentRunTaskListParams) (*pkgariapi.AgentRunTaskListResult, error) {
	return &pkgariapi.AgentRunTaskListResult{}, nil
}

func (m *mockAgentRunOps) TaskRetry(context.Context, *pkgariapi.AgentRunTaskRetryParams) (*pkgariapi.AgentRunTaskRetryResult, error) {
	return &pkgariapi.AgentRunTaskRetryResult{}, nil
}

// ── mock WorkspaceOps (stub — not used in create tests) ──────────────────────

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
func (m *mockClient) DisconnectNotify() <-chan struct{}  { return make(chan struct{}) }

// clientFn returns a cliutil.ClientFn that always returns mc.
func clientFn(mc *mockClient) cliutil.ClientFn {
	return func() (pkgariapi.Client, error) {
		return mc, nil
	}
}

// ── Local subcommand tests ──────────────────────────────────────────────────

func TestLocalSuccess(t *testing.T) {
	var created bool
	mc := newMockClient()
	mc.createFn = func(_ context.Context, obj pkgariapi.Object) error {
		created = true
		ws, ok := obj.(*pkgariapi.Workspace)
		require.True(t, ok, "expected *Workspace")
		assert.Equal(t, "myws", ws.Metadata.Name)
		assert.NotEmpty(t, ws.Spec.Source, "source should be populated")
		return nil
	}

	cmd := NewCommand(clientFn(mc))
	cmd.SetArgs([]string{"local", "--name", "myws", "--path", "/tmp/code"})
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})

	err := cmd.Execute()
	require.NoError(t, err)
	assert.True(t, created, "Create should have been called")
}

func TestLocalMissingName(t *testing.T) {
	mc := newMockClient()
	cmd := NewCommand(clientFn(mc))
	cmd.SetArgs([]string{"local", "--path", "/tmp/code"})
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})

	err := cmd.Execute()
	assert.Error(t, err, "local without --name should fail")
}

func TestLocalMissingPath(t *testing.T) {
	mc := newMockClient()
	cmd := NewCommand(clientFn(mc))
	cmd.SetArgs([]string{"local", "--name", "myws"})
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})

	err := cmd.Execute()
	assert.Error(t, err, "local without --path should fail")
}

// ── Git subcommand tests ────────────────────────────────────────────────────

func TestGitSuccess(t *testing.T) {
	var created bool
	mc := newMockClient()
	mc.createFn = func(_ context.Context, obj pkgariapi.Object) error {
		created = true
		ws, ok := obj.(*pkgariapi.Workspace)
		require.True(t, ok, "expected *Workspace")
		assert.Equal(t, "gitws", ws.Metadata.Name)
		assert.NotEmpty(t, ws.Spec.Source, "source should be populated")
		return nil
	}

	cmd := NewCommand(clientFn(mc))
	cmd.SetArgs([]string{"git", "--name", "gitws", "--url", "https://github.com/example/repo.git"})
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})

	err := cmd.Execute()
	require.NoError(t, err)
	assert.True(t, created, "Create should have been called")
}

func TestGitMissingURL(t *testing.T) {
	mc := newMockClient()
	cmd := NewCommand(clientFn(mc))
	cmd.SetArgs([]string{"git", "--name", "gitws"})
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})

	err := cmd.Execute()
	assert.Error(t, err, "git without --url should fail")
}

// ── Empty subcommand tests ──────────────────────────────────────────────────

func TestEmptySuccess(t *testing.T) {
	var created bool
	mc := newMockClient()
	mc.createFn = func(_ context.Context, obj pkgariapi.Object) error {
		created = true
		ws, ok := obj.(*pkgariapi.Workspace)
		require.True(t, ok, "expected *Workspace")
		assert.Equal(t, "emptyws", ws.Metadata.Name)
		return nil
	}

	cmd := NewCommand(clientFn(mc))
	cmd.SetArgs([]string{"empty", "--name", "emptyws"})
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})

	err := cmd.Execute()
	require.NoError(t, err)
	assert.True(t, created, "Create should have been called")
}

func TestEmptyMissingName(t *testing.T) {
	mc := newMockClient()
	cmd := NewCommand(clientFn(mc))
	cmd.SetArgs([]string{"empty"})
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})

	err := cmd.Execute()
	assert.Error(t, err, "empty without --name should fail")
}
