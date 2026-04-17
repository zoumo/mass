package workspace

import (
	"bytes"
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zoumo/mass/cmd/massctl/commands/cliutil"
	pkgariapi "github.com/zoumo/mass/pkg/ari/api"
)

// ── mock WorkspaceOps ────────────────────────────────────────────────────────

type mockWorkspaceOps struct {
	sendFn func(ctx context.Context, req *pkgariapi.WorkspaceSendParams) (*pkgariapi.WorkspaceSendResult, error)
}

func (m *mockWorkspaceOps) Send(ctx context.Context, req *pkgariapi.WorkspaceSendParams) (*pkgariapi.WorkspaceSendResult, error) {
	if m.sendFn != nil {
		return m.sendFn(ctx, req)
	}
	return &pkgariapi.WorkspaceSendResult{Delivered: true}, nil
}

// ── mock AgentRunOps (stub — not used in workspace tests) ────────────────────

type mockAgentRunOps struct{}

func (m *mockAgentRunOps) Prompt(context.Context, pkgariapi.ObjectKey, []pkgariapi.ContentBlock) (*pkgariapi.AgentRunPromptResult, error) {
	return &pkgariapi.AgentRunPromptResult{}, nil
}
func (m *mockAgentRunOps) Cancel(context.Context, pkgariapi.ObjectKey) error { return nil }
func (m *mockAgentRunOps) Stop(context.Context, pkgariapi.ObjectKey) error   { return nil }
func (m *mockAgentRunOps) Restart(context.Context, pkgariapi.ObjectKey) (*pkgariapi.AgentRun, error) {
	return &pkgariapi.AgentRun{}, nil
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

// ── Tests ────────────────────────────────────────────────────────────────────

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
	cmd.SetArgs([]string{"get", "--name", "ws1"})
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})

	err := cmd.Execute()
	require.NoError(t, err)
	assert.Equal(t, "ws1", gotKey.Name)
}

func TestGetMissingName(t *testing.T) {
	mc := newMockClient()
	cmd := NewCommand(clientFn(mc))
	cmd.SetArgs([]string{"get"})
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})

	err := cmd.Execute()
	assert.Error(t, err, "get without --name should fail")
}

func TestDeleteSuccess(t *testing.T) {
	var deleted bool
	mc := newMockClient()
	mc.deleteFn = func(_ context.Context, key pkgariapi.ObjectKey, obj pkgariapi.Object) error {
		deleted = true
		return nil
	}

	cmd := NewCommand(clientFn(mc))
	cmd.SetArgs([]string{"delete", "--name", "ws1"})
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})

	err := cmd.Execute()
	require.NoError(t, err)
	assert.True(t, deleted, "Delete should have been called")
}

func TestSendSuccess(t *testing.T) {
	var sentReq *pkgariapi.WorkspaceSendParams
	mc := newMockClient()
	mc.workspaceOps.sendFn = func(_ context.Context, req *pkgariapi.WorkspaceSendParams) (*pkgariapi.WorkspaceSendResult, error) {
		sentReq = req
		return &pkgariapi.WorkspaceSendResult{Delivered: true}, nil
	}

	cmd := NewCommand(clientFn(mc))
	cmd.SetArgs([]string{"send", "--name", "ws1", "--from", "agentA", "--to", "agentB", "--text", "hello"})
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})

	err := cmd.Execute()
	require.NoError(t, err)
	require.NotNil(t, sentReq, "Send should have been called")
	assert.Equal(t, "ws1", sentReq.Workspace)
	assert.Equal(t, "agentA", sentReq.From)
	assert.Equal(t, "agentB", sentReq.To)
}

func TestSendMissingFlags(t *testing.T) {
	mc := newMockClient()

	tests := []struct {
		name string
		args []string
	}{
		{"missing all", []string{"send"}},
		{"missing --from", []string{"send", "--name", "ws1", "--to", "b", "--text", "hi"}},
		{"missing --to", []string{"send", "--name", "ws1", "--from", "a", "--text", "hi"}},
		{"missing --text", []string{"send", "--name", "ws1", "--from", "a", "--to", "b"}},
		{"missing --name", []string{"send", "--from", "a", "--to", "b", "--text", "hi"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cmd := NewCommand(clientFn(mc))
			cmd.SetArgs(tt.args)
			cmd.SetOut(&bytes.Buffer{})
			cmd.SetErr(&bytes.Buffer{})

			err := cmd.Execute()
			assert.Error(t, err, "send with missing required flags should fail")
		})
	}
}
