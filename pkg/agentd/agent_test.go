// Package agentd implements the agent daemon that manages agent runtime lifecycle.
// This file tests the AgentManager for agent lifecycle management.
package agentd

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/open-agent-d/open-agent-d/pkg/meta"
	"github.com/open-agent-d/open-agent-d/pkg/spec"
)

// newTestMetaStore creates a file-backed bbolt store in a temp directory.
// bbolt does not support in-memory mode; each test gets its own DB file.
func newTestMetaStore(t *testing.T) *meta.Store {
	t.Helper()

	store, err := meta.NewStore(filepath.Join(t.TempDir(), "meta.db"))
	require.NoError(t, err, "NewStore should succeed")
	require.NotNil(t, store, "Store should not be nil")

	t.Cleanup(func() {
		_ = store.Close()
	})

	return store
}

// newTestAgentManager creates an AgentManager with a temp bbolt store.
func newTestAgentManager(t *testing.T) *AgentManager {
	t.Helper()
	store := newTestMetaStore(t)
	return NewAgentManager(store)
}

// makeTestAgentRun builds a minimal valid Agent struct using the new model.
func makeTestAgentRun(workspace, name string) *meta.AgentRun {
	return &meta.AgentRun{
		Metadata: meta.ObjectMeta{
			Workspace: workspace,
			Name:      name,
			Labels:    map[string]string{"env": "test"},
		},
		Spec: meta.AgentRunSpec{
			RuntimeClass: "default",
			Description:  "test agent",
			SystemPrompt: "you are a test",
		},
	}
}

// TestAgentCreate_RoundTrip tests Create → Get round-trip and verifies all fields.
func TestAgentCreate_RoundTrip(t *testing.T) {
	t.Parallel()

	am := newTestAgentManager(t)
	ctx := context.Background()

	agent := makeTestAgentRun("default", "alpha")
	require.NoError(t, am.Create(ctx, agent), "Create should succeed")

	// Verify default state was applied.
	assert.Equal(t, spec.StatusCreating, agent.Status.State, "default state should be creating")

	got, err := am.Get(ctx, "default", "alpha")
	require.NoError(t, err, "Get should succeed")
	require.NotNil(t, got, "Get should return the agent")

	assert.Equal(t, "default", got.Metadata.Workspace)
	assert.Equal(t, "alpha", got.Metadata.Name)
	assert.Equal(t, "default", got.Spec.RuntimeClass)
	assert.Equal(t, "test agent", got.Spec.Description)
	assert.Equal(t, "you are a test", got.Spec.SystemPrompt)
	assert.Equal(t, spec.StatusCreating, got.Status.State)
	assert.Equal(t, map[string]string{"env": "test"}, got.Metadata.Labels)
}

// TestAgentGetByWorkspaceName tests Create → GetByWorkspaceName lookup.
func TestAgentGetByWorkspaceName(t *testing.T) {
	t.Parallel()

	am := newTestAgentManager(t)
	ctx := context.Background()

	agent := makeTestAgentRun("ws1", "beta")
	require.NoError(t, am.Create(ctx, agent))

	got, err := am.GetByWorkspaceName(ctx, "ws1", "beta")
	require.NoError(t, err, "GetByWorkspaceName should succeed")
	require.NotNil(t, got, "GetByWorkspaceName should return the agent")
	assert.Equal(t, "ws1", got.Metadata.Workspace)
	assert.Equal(t, "beta", got.Metadata.Name)
}

// TestAgentList_StateFilter tests List with a state filter.
func TestAgentList_StateFilter(t *testing.T) {
	t.Parallel()

	am := newTestAgentManager(t)
	ctx := context.Background()

	a1 := makeTestAgentRun("ws1", "a1")
	a2 := makeTestAgentRun("ws1", "a2")
	require.NoError(t, am.Create(ctx, a1))
	require.NoError(t, am.Create(ctx, a2))

	require.NoError(t, am.UpdateStatus(ctx, "ws1", "a1", meta.AgentRunStatus{State: spec.StatusStopped}))

	stoppedAgents, err := am.List(ctx, &meta.AgentRunFilter{State: spec.StatusStopped})
	require.NoError(t, err)
	require.Len(t, stoppedAgents, 1)
	assert.Equal(t, "a1", stoppedAgents[0].Metadata.Name)

	creatingAgents, err := am.List(ctx, &meta.AgentRunFilter{State: spec.StatusCreating})
	require.NoError(t, err)
	require.Len(t, creatingAgents, 1)
	assert.Equal(t, "a2", creatingAgents[0].Metadata.Name)
}

// TestAgentList_WorkspaceFilter tests List with a workspace filter.
func TestAgentList_WorkspaceFilter(t *testing.T) {
	t.Parallel()

	am := newTestAgentManager(t)
	ctx := context.Background()

	a1 := makeTestAgentRun("wsA", "agent1")
	a2 := makeTestAgentRun("wsB", "agent2")
	require.NoError(t, am.Create(ctx, a1))
	require.NoError(t, am.Create(ctx, a2))

	wsAAgents, err := am.List(ctx, &meta.AgentRunFilter{Workspace: "wsA"})
	require.NoError(t, err)
	require.Len(t, wsAAgents, 1)
	assert.Equal(t, "agent1", wsAAgents[0].Metadata.Name)

	wsBAgents, err := am.List(ctx, &meta.AgentRunFilter{Workspace: "wsB"})
	require.NoError(t, err)
	require.Len(t, wsBAgents, 1)
	assert.Equal(t, "agent2", wsBAgents[0].Metadata.Name)
}

// TestAgentUpdateStatus tests Create → UpdateStatus → Get verifying the state change.
func TestAgentUpdateStatus(t *testing.T) {
	t.Parallel()

	am := newTestAgentManager(t)
	ctx := context.Background()

	agent := makeTestAgentRun("ws1", "stateful")
	require.NoError(t, am.Create(ctx, agent))

	require.NoError(t, am.UpdateStatus(ctx, "ws1", "stateful", meta.AgentRunStatus{State: spec.StatusRunning}))

	got, err := am.Get(ctx, "ws1", "stateful")
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, spec.StatusRunning, got.Status.State)
}

// TestAgentDelete_RequiresStopped tests that a stopped agent can be deleted.
func TestAgentDelete_RequiresStopped(t *testing.T) {
	t.Parallel()

	am := newTestAgentManager(t)
	ctx := context.Background()

	agent := makeTestAgentRun("ws1", "deletable")
	require.NoError(t, am.Create(ctx, agent))

	// Transition to stopped.
	require.NoError(t, am.UpdateStatus(ctx, "ws1", "deletable", meta.AgentRunStatus{State: spec.StatusStopped}))

	// Delete should succeed.
	require.NoError(t, am.Delete(ctx, "ws1", "deletable"))

	// Agent should no longer exist.
	got, err := am.Get(ctx, "ws1", "deletable")
	require.NoError(t, err)
	assert.Nil(t, got)
}

// TestAgentDelete_AllowsError tests that an errored agent can be deleted.
func TestAgentDelete_AllowsError(t *testing.T) {
	t.Parallel()

	am := newTestAgentManager(t)
	ctx := context.Background()

	agent := makeTestAgentRun("ws1", "errored")
	require.NoError(t, am.Create(ctx, agent))
	require.NoError(t, am.UpdateStatus(ctx, "ws1", "errored", meta.AgentRunStatus{
		State:        spec.StatusError,
		ErrorMessage: "boom",
	}))

	require.NoError(t, am.Delete(ctx, "ws1", "errored"))

	got, err := am.Get(ctx, "ws1", "errored")
	require.NoError(t, err)
	assert.Nil(t, got)
}

// TestAgentDelete_Protected tests that an active agent cannot be deleted.
func TestAgentDelete_Protected(t *testing.T) {
	t.Parallel()

	am := newTestAgentManager(t)
	ctx := context.Background()

	agent := makeTestAgentRun("ws1", "protected")
	require.NoError(t, am.Create(ctx, agent))
	// State is "creating" — not stopped.

	err := am.Delete(ctx, "ws1", "protected")
	require.Error(t, err, "Delete should fail for active agent")

	var notStopped *ErrDeleteNotStopped
	require.ErrorAs(t, err, &notStopped, "error should be ErrDeleteNotStopped")
	assert.Equal(t, "ws1", notStopped.Workspace)
	assert.Equal(t, "protected", notStopped.Name)
	assert.Equal(t, spec.StatusCreating, notStopped.State)
}

// TestAgentGet_NotFound tests that Get returns nil,nil for a missing agent.
func TestAgentGet_NotFound(t *testing.T) {
	t.Parallel()

	am := newTestAgentManager(t)
	ctx := context.Background()

	got, err := am.Get(ctx, "ws1", "nonexistent")
	require.NoError(t, err, "Get on missing agent should return nil error")
	assert.Nil(t, got, "Get on missing agent should return nil")
}

// TestAgentDelete_NotFound tests that Delete returns ErrAgentNotFound for a missing agent.
func TestAgentDelete_NotFound(t *testing.T) {
	t.Parallel()

	am := newTestAgentManager(t)
	ctx := context.Background()

	err := am.Delete(ctx, "ws1", "ghost")
	require.Error(t, err, "Delete of missing agent should fail")

	var notFound *ErrAgentNotFound
	require.ErrorAs(t, err, &notFound, "error should be ErrAgentNotFound")
}

// TestAgentCreate_AlreadyExists tests that creating a duplicate agent fails.
func TestAgentCreate_AlreadyExists(t *testing.T) {
	t.Parallel()

	am := newTestAgentManager(t)
	ctx := context.Background()

	agent := makeTestAgentRun("ws1", "dup")
	require.NoError(t, am.Create(ctx, agent))

	agent2 := makeTestAgentRun("ws1", "dup")
	err := am.Create(ctx, agent2)
	require.Error(t, err, "Create of duplicate should fail")

	var alreadyExists *ErrAgentAlreadyExists
	require.ErrorAs(t, err, &alreadyExists)
	assert.Equal(t, "ws1", alreadyExists.Workspace)
	assert.Equal(t, "dup", alreadyExists.Name)
}
