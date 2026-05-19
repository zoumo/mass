package client

import (
	"context"

	runapi "github.com/zoumo/mass/pkg/agentrun/api"
	pkgariapi "github.com/zoumo/mass/pkg/ari/api"
)

// Client is the unified ARI client interface (controller-runtime style).
// CRUD operations are type-switched via Object/ObjectList.
// Domain-specific operations are accessed via sub-interfaces.
type Client interface {
	// Create persists a new domain object. The obj is updated in-place
	// with server-assigned fields (timestamps, initial status).
	Create(ctx context.Context, obj pkgariapi.Object) error

	// Get retrieves a domain object by key. The obj is updated in-place.
	Get(ctx context.Context, key pkgariapi.ObjectKey, obj pkgariapi.Object) error

	// Update modifies an existing domain object. The obj is updated in-place
	// with server-assigned fields.
	Update(ctx context.Context, obj pkgariapi.Object) error

	// List retrieves domain objects matching the given options.
	// The list is updated in-place.
	List(ctx context.Context, list pkgariapi.ObjectList, opts ...pkgariapi.ListOption) error

	// Delete removes a domain object by key. The obj parameter serves
	// as a type marker to determine which resource type to delete.
	Delete(ctx context.Context, key pkgariapi.ObjectKey, obj pkgariapi.Object) error

	// AgentRuns returns the sub-interface for non-CRUD agent run operations.
	AgentRuns() AgentRunOps

	// Workspaces returns the sub-interface for non-CRUD workspace operations.
	Workspaces() WorkspaceOps

	// System returns the sub-interface for system-level operations.
	System() SystemOps

	// Close closes the underlying connection.
	Close() error

	// DisconnectNotify returns a channel that is closed when the connection drops.
	DisconnectNotify() <-chan struct{}
}

// AgentRunOps provides non-CRUD operations on agent runs.
type AgentRunOps interface {
	// Prompt sends a multimodal prompt ([]runapi.ContentBlock) to an agent run's
	// initial session. For multi-session, use PromptSession.
	Prompt(ctx context.Context, key pkgariapi.ObjectKey, prompt []runapi.ContentBlock) (*pkgariapi.AgentRunPromptResult, error)

	// PromptSession addresses a specific session id (opened via NewSession).
	PromptSession(ctx context.Context, key pkgariapi.ObjectKey, sessionID string, prompt []runapi.ContentBlock) (*pkgariapi.AgentRunPromptResult, error)

	// Cancel cancels the current turn of an agent run.
	Cancel(ctx context.Context, key pkgariapi.ObjectKey) error

	// Stop gracefully stops an agent run.
	Stop(ctx context.Context, key pkgariapi.ObjectKey) error

	// Restart stops and restarts an agent run.
	Restart(ctx context.Context, key pkgariapi.ObjectKey) (*pkgariapi.AgentRun, error)

	// TaskDo creates a task file and prompts the agent.
	TaskDo(ctx context.Context, params *pkgariapi.AgentRunTaskDoParams) (*pkgariapi.AgentTask, error)

	// TaskGet retrieves a task by ID.
	TaskGet(ctx context.Context, params *pkgariapi.AgentRunTaskGetParams) (*pkgariapi.AgentTask, error)

	// TaskList lists all tasks for an agent run.
	TaskList(ctx context.Context, params *pkgariapi.AgentRunTaskListParams) (*pkgariapi.AgentRunTaskListResult, error)

	// TaskRetry retries an existing task by bumping its attempt count and re-prompting the agent.
	TaskRetry(ctx context.Context, params *pkgariapi.AgentRunTaskRetryParams) (*pkgariapi.AgentTask, error)

	// NewSession opens an additional ACP session on the running agent process
	// (no fork+exec). Returns the agent-issued sessionId.
	NewSession(ctx context.Context, params *pkgariapi.AgentRunNewSessionParams) (*pkgariapi.AgentRunNewSessionResult, error)

	// EndSession releases runtime tracking of a session id.
	EndSession(ctx context.Context, key pkgariapi.ObjectKey, sessionID string) error

	// ListSessions enumerates active session ids on the agent.
	ListSessions(ctx context.Context, key pkgariapi.ObjectKey) (*pkgariapi.AgentRunListSessionsResult, error)
}

// WorkspaceOps provides non-CRUD operations on workspaces.
type WorkspaceOps interface {
	// Send routes a message between agent runs within a workspace.
	Send(ctx context.Context, req *pkgariapi.WorkspaceSendParams) (*pkgariapi.WorkspaceSendResult, error)
}

// SystemOps provides system-level operations.
type SystemOps interface {
	// Info returns daemon version and runtime information.
	Info(ctx context.Context) (*pkgariapi.SystemInfoResult, error)
}
