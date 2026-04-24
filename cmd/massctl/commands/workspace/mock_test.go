package workspace

import (
	"context"

	"github.com/zoumo/mass/cmd/massctl/commands/cliutil"
	runapi "github.com/zoumo/mass/pkg/agentrun/api"
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

type mockAgentRunOps struct {
	stopFn func(context.Context, pkgariapi.ObjectKey) error
}

func (m *mockAgentRunOps) Prompt(context.Context, pkgariapi.ObjectKey, []runapi.ContentBlock) (*pkgariapi.AgentRunPromptResult, error) {
	return &pkgariapi.AgentRunPromptResult{}, nil
}
func (m *mockAgentRunOps) Cancel(context.Context, pkgariapi.ObjectKey) error { return nil }
func (m *mockAgentRunOps) Stop(ctx context.Context, key pkgariapi.ObjectKey) error {
	if m.stopFn != nil {
		return m.stopFn(ctx, key)
	}
	return nil
}

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

// ── mock SystemOps (stub — not used in workspace tests) ────────────────────────

type mockSystemOps struct{}

func (m *mockSystemOps) Info(context.Context) (*pkgariapi.SystemInfoResult, error) {
	return &pkgariapi.SystemInfoResult{}, nil
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
	systemOps    *mockSystemOps
}

func newMockClient() *mockClient {
	return &mockClient{
		agentRunOps:  &mockAgentRunOps{},
		workspaceOps: &mockWorkspaceOps{},
		systemOps:    &mockSystemOps{},
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
func (m *mockClient) System() pkgariapi.SystemOps        { return m.systemOps }
func (m *mockClient) Close() error                       { return nil }
func (m *mockClient) DisconnectNotify() <-chan struct{}  { return make(chan struct{}) }

// clientFn returns a cliutil.ClientFn that always returns mc.
func clientFn(mc *mockClient) cliutil.ClientFn {
	return func() (pkgariapi.Client, error) {
		return mc, nil
	}
}
