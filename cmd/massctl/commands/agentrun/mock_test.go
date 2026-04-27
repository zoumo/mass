package agentrun

import (
	"context"

	"github.com/zoumo/mass/cmd/massctl/commands/cliutil"
	runapi "github.com/zoumo/mass/pkg/agentrun/api"
	pkgariapi "github.com/zoumo/mass/pkg/ari/api"
	ariclient "github.com/zoumo/mass/pkg/ari/client"
)

// ── mock AgentRunOps ─────────────────────────────────────────────────────────

type mockAgentRunOps struct {
	promptFn     func(ctx context.Context, key pkgariapi.ObjectKey, prompt []runapi.ContentBlock) (*pkgariapi.AgentRunPromptResult, error)
	cancelFn     func(ctx context.Context, key pkgariapi.ObjectKey) error
	stopFn       func(ctx context.Context, key pkgariapi.ObjectKey) error
	restartFn    func(ctx context.Context, key pkgariapi.ObjectKey) (*pkgariapi.AgentRun, error)
	taskCreateFn func(ctx context.Context, params *pkgariapi.AgentRunTaskDoParams) (*pkgariapi.AgentTask, error)
	taskGetFn    func(ctx context.Context, params *pkgariapi.AgentRunTaskGetParams) (*pkgariapi.AgentTask, error)
	taskListFn   func(ctx context.Context, params *pkgariapi.AgentRunTaskListParams) (*pkgariapi.AgentRunTaskListResult, error)
	taskRetryFn  func(ctx context.Context, params *pkgariapi.AgentRunTaskRetryParams) (*pkgariapi.AgentTask, error)
}

func (m *mockAgentRunOps) Prompt(ctx context.Context, key pkgariapi.ObjectKey, prompt []runapi.ContentBlock) (*pkgariapi.AgentRunPromptResult, error) {
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

func (m *mockAgentRunOps) TaskDo(ctx context.Context, params *pkgariapi.AgentRunTaskDoParams) (*pkgariapi.AgentTask, error) {
	if m.taskCreateFn != nil {
		return m.taskCreateFn(ctx, params)
	}
	return &pkgariapi.AgentTask{}, nil
}

func (m *mockAgentRunOps) TaskGet(ctx context.Context, params *pkgariapi.AgentRunTaskGetParams) (*pkgariapi.AgentTask, error) {
	if m.taskGetFn != nil {
		return m.taskGetFn(ctx, params)
	}
	return &pkgariapi.AgentTask{}, nil
}

func (m *mockAgentRunOps) TaskList(ctx context.Context, params *pkgariapi.AgentRunTaskListParams) (*pkgariapi.AgentRunTaskListResult, error) {
	if m.taskListFn != nil {
		return m.taskListFn(ctx, params)
	}
	return &pkgariapi.AgentRunTaskListResult{}, nil
}

func (m *mockAgentRunOps) TaskRetry(ctx context.Context, params *pkgariapi.AgentRunTaskRetryParams) (*pkgariapi.AgentTask, error) {
	if m.taskRetryFn != nil {
		return m.taskRetryFn(ctx, params)
	}
	return &pkgariapi.AgentTask{}, nil
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

func (m *mockClient) AgentRuns() ariclient.AgentRunOps   { return m.agentRunOps }
func (m *mockClient) Workspaces() ariclient.WorkspaceOps { return m.workspaceOps }
func (m *mockClient) System() ariclient.SystemOps        { return m.systemOps }
func (m *mockClient) Close() error                       { return nil }
func (m *mockClient) DisconnectNotify() <-chan struct{}  { return make(chan struct{}) }

// ── mock SystemOps ──────────────────────────────────────────────────────────────

type mockSystemOps struct {
	infoFn func(ctx context.Context) (*pkgariapi.SystemInfoResult, error)
}

func (m *mockSystemOps) Info(ctx context.Context) (*pkgariapi.SystemInfoResult, error) {
	if m.infoFn != nil {
		return m.infoFn(ctx)
	}
	return &pkgariapi.SystemInfoResult{}, nil
}

// clientFn returns a cliutil.ClientFn that always returns mc.
func clientFn(mc *mockClient) cliutil.ClientFn {
	return func() (ariclient.Client, error) {
		return mc, nil
	}
}
