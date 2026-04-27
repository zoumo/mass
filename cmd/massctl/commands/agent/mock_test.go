package agent

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	runapi "github.com/zoumo/mass/pkg/agentrun/api"
	pkgariapi "github.com/zoumo/mass/pkg/ari/api"
	ariclient "github.com/zoumo/mass/pkg/ari/client"
)

// mockClient implements ariclient.Client for testing.
type mockClient struct {
	createFn func(ctx context.Context, obj pkgariapi.Object) error
	getFn    func(ctx context.Context, key pkgariapi.ObjectKey, obj pkgariapi.Object) error
	updateFn func(ctx context.Context, obj pkgariapi.Object) error
	listFn   func(ctx context.Context, list pkgariapi.ObjectList, opts ...pkgariapi.ListOption) error
	deleteFn func(ctx context.Context, key pkgariapi.ObjectKey, obj pkgariapi.Object) error
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

func (m *mockClient) AgentRuns() ariclient.AgentRunOps   { return &mockAgentRunOps{} }
func (m *mockClient) Workspaces() ariclient.WorkspaceOps { return &mockWorkspaceOps{} }
func (m *mockClient) System() ariclient.SystemOps        { return &mockSystemOps{} }
func (m *mockClient) Close() error                       { return nil }
func (m *mockClient) DisconnectNotify() <-chan struct{}  { return make(chan struct{}) }

// mock AgentRunOps (stub — not used in agent tests)
type mockAgentRunOps struct{}

func (m *mockAgentRunOps) Prompt(context.Context, pkgariapi.ObjectKey, []runapi.ContentBlock) (*pkgariapi.AgentRunPromptResult, error) {
	return nil, nil
}
func (m *mockAgentRunOps) Cancel(context.Context, pkgariapi.ObjectKey) error { return nil }
func (m *mockAgentRunOps) Stop(context.Context, pkgariapi.ObjectKey) error   { return nil }
func (m *mockAgentRunOps) Restart(context.Context, pkgariapi.ObjectKey) (*pkgariapi.AgentRun, error) {
	return nil, nil
}

func (m *mockAgentRunOps) TaskDo(context.Context, *pkgariapi.AgentRunTaskDoParams) (*pkgariapi.AgentTask, error) {
	return nil, nil
}

func (m *mockAgentRunOps) TaskGet(context.Context, *pkgariapi.AgentRunTaskGetParams) (*pkgariapi.AgentTask, error) {
	return nil, nil
}

func (m *mockAgentRunOps) TaskList(context.Context, *pkgariapi.AgentRunTaskListParams) (*pkgariapi.AgentRunTaskListResult, error) {
	return nil, nil
}

func (m *mockAgentRunOps) TaskRetry(context.Context, *pkgariapi.AgentRunTaskRetryParams) (*pkgariapi.AgentTask, error) {
	return nil, nil
}

// mock WorkspaceOps (stub — not used in agent tests)
type mockWorkspaceOps struct{}

func (m *mockWorkspaceOps) Send(context.Context, *pkgariapi.WorkspaceSendParams) (*pkgariapi.WorkspaceSendResult, error) {
	return nil, nil
}

// mock SystemOps (stub — not used in agent tests)
type mockSystemOps struct{}

func (m *mockSystemOps) Info(context.Context) (*pkgariapi.SystemInfoResult, error) {
	return nil, nil
}

// newMockClientFn returns a ClientFn that always returns the given mock.
func newMockClientFn(mock *mockClient) func() (ariclient.Client, error) {
	return func() (ariclient.Client, error) { return mock, nil }
}

// writeYAML creates a temp file with the given content and returns its path.
func writeYAML(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "agent.yaml")
	err := os.WriteFile(path, []byte(content), 0o644)
	require.NoError(t, err)
	return path
}
