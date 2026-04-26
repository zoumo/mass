// Package agentd implements the agent daemon that manages agent runtime lifecycle.
// This file tests the AgentRunManager for agent lifecycle management.
package agentd

import (
	"context"
	"log/slog"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zoumo/mass/pkg/agentd/store"
	pkgariapi "github.com/zoumo/mass/pkg/ari/api"
	apiruntime "github.com/zoumo/mass/pkg/runtime-spec/api"
)

// newTestMetaStore creates a file-backed bbolt store in a temp directory.
// bbolt does not support in-memory mode; each test gets its own DB file.
func newTestMetaStore(t *testing.T) *store.Store {
	t.Helper()

	metaStore, err := store.NewStore(filepath.Join(t.TempDir(), "meta.db"), slog.Default())
	require.NoError(t, err, "NewStore should succeed")
	require.NotNil(t, metaStore, "Store should not be nil")

	t.Cleanup(func() {
		_ = metaStore.Close()
	})

	return metaStore
}

// newTestAgentManager creates an AgentRunManager with a temp bbolt store.
func newTestAgentManager(t *testing.T) *AgentRunManager {
	t.Helper()
	metaStore := newTestMetaStore(t)
	return NewAgentRunManager(metaStore, slog.Default())
}

// makeTestAgentRun builds a minimal valid Agent struct using the new model.
func makeTestAgentRun(workspace, name string) *pkgariapi.AgentRun {
	return &pkgariapi.AgentRun{
		Metadata: pkgariapi.ObjectMeta{
			Workspace: workspace,
			Name:      name,
			Labels:    map[string]string{"env": "test"},
		},
		Spec: pkgariapi.AgentRunSpec{
			Agent:        "default",
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
	assert.Equal(t, apiruntime.PhaseCreating, agent.Status.Phase, "default state should be creating")

	got, err := am.Get(ctx, "default", "alpha")
	require.NoError(t, err, "Get should succeed")
	require.NotNil(t, got, "Get should return the agent")

	assert.Equal(t, "default", got.Metadata.Workspace)
	assert.Equal(t, "alpha", got.Metadata.Name)
	assert.Equal(t, "default", got.Spec.Agent)
	assert.Equal(t, "you are a test", got.Spec.SystemPrompt)
	assert.Equal(t, apiruntime.PhaseCreating, got.Status.Phase)
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

	require.NoError(t, am.UpdateStatus(ctx, "ws1", "a1", pkgariapi.AgentRunStatus{Phase: apiruntime.PhaseStopped}))

	stoppedAgents, err := am.List(ctx, &pkgariapi.AgentRunFilter{Phase: apiruntime.PhaseStopped})
	require.NoError(t, err)
	require.Len(t, stoppedAgents, 1)
	assert.Equal(t, "a1", stoppedAgents[0].Metadata.Name)

	creatingAgents, err := am.List(ctx, &pkgariapi.AgentRunFilter{Phase: apiruntime.PhaseCreating})
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

	wsAAgents, err := am.List(ctx, &pkgariapi.AgentRunFilter{Workspace: "wsA"})
	require.NoError(t, err)
	require.Len(t, wsAAgents, 1)
	assert.Equal(t, "agent1", wsAAgents[0].Metadata.Name)

	wsBAgents, err := am.List(ctx, &pkgariapi.AgentRunFilter{Workspace: "wsB"})
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

	require.NoError(t, am.UpdateStatus(ctx, "ws1", "stateful", pkgariapi.AgentRunStatus{Phase: apiruntime.PhaseRunning}))

	got, err := am.Get(ctx, "ws1", "stateful")
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, apiruntime.PhaseRunning, got.Status.Phase)
}

// TestAgentDelete_RequiresStopped tests that a stopped agent can be deleted.
func TestAgentDelete_RequiresStopped(t *testing.T) {
	t.Parallel()

	am := newTestAgentManager(t)
	ctx := context.Background()

	agent := makeTestAgentRun("ws1", "deletable")
	require.NoError(t, am.Create(ctx, agent))

	// Transition to stopped.
	require.NoError(t, am.UpdateStatus(ctx, "ws1", "deletable", pkgariapi.AgentRunStatus{Phase: apiruntime.PhaseStopped}))

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
	require.NoError(t, am.UpdateStatus(ctx, "ws1", "errored", pkgariapi.AgentRunStatus{
		Phase:        apiruntime.PhaseError,
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
	assert.Equal(t, apiruntime.PhaseCreating, notStopped.Phase)
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

// TestAgentDelete_NotFound tests that Delete returns ErrAgentRunNotFound for a missing agent.
func TestAgentDelete_NotFound(t *testing.T) {
	t.Parallel()

	am := newTestAgentManager(t)
	ctx := context.Background()

	err := am.Delete(ctx, "ws1", "ghost")
	require.Error(t, err, "Delete of missing agent should fail")

	var notFound *ErrAgentRunNotFound
	require.ErrorAs(t, err, &notFound, "error should be ErrAgentRunNotFound")
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

	var alreadyExists *ErrAgentRunAlreadyExists
	require.ErrorAs(t, err, &alreadyExists)
	assert.Equal(t, "ws1", alreadyExists.Workspace)
	assert.Equal(t, "dup", alreadyExists.Name)
}

// TestAgentUpdateStatus_NotFound tests that UpdateStatus returns ErrAgentRunNotFound for a missing agent.
func TestAgentUpdateStatus_NotFound(t *testing.T) {
	t.Parallel()

	am := newTestAgentManager(t)
	ctx := context.Background()

	err := am.UpdateStatus(ctx, "ws1", "ghost", pkgariapi.AgentRunStatus{Phase: apiruntime.PhaseRunning})
	require.Error(t, err, "UpdateStatus of missing agent should fail")

	var notFound *ErrAgentRunNotFound
	require.ErrorAs(t, err, &notFound, "error should be ErrAgentRunNotFound")
	assert.Equal(t, "ws1", notFound.Workspace)
	assert.Equal(t, "ghost", notFound.Name)
}

// TestAgentTransitionState_NotFound tests that TransitionState returns ErrAgentRunNotFound for a missing agent.
func TestAgentTransitionState_NotFound(t *testing.T) {
	t.Parallel()

	am := newTestAgentManager(t)
	ctx := context.Background()

	ok, err := am.TransitionState(ctx, "ws1", "phantom", apiruntime.PhaseIdle, apiruntime.PhaseRunning)
	require.Error(t, err, "TransitionState of missing agent should fail")
	assert.False(t, ok)

	var notFound *ErrAgentRunNotFound
	require.ErrorAs(t, err, &notFound, "error should be ErrAgentRunNotFound")
	assert.Equal(t, "ws1", notFound.Workspace)
	assert.Equal(t, "phantom", notFound.Name)
}

// ────────────────────────────────────────────────────────────────────────────
// Error type formatting
// ────────────────────────────────────────────────────────────────────────────

func TestErrorTypes_Format(t *testing.T) {
	t.Parallel()

	t.Run("ErrAgentRunNotFound", func(t *testing.T) {
		err := &ErrAgentRunNotFound{Workspace: "ws", Name: "a1"}
		assert.Contains(t, err.Error(), "ws/a1")
		assert.Contains(t, err.Error(), "not found")
	})

	t.Run("ErrDeleteNotStopped", func(t *testing.T) {
		err := &ErrDeleteNotStopped{Workspace: "ws", Name: "a1", Phase: apiruntime.PhaseRunning}
		assert.Contains(t, err.Error(), "ws/a1")
		assert.Contains(t, err.Error(), "running")
	})

	t.Run("ErrAgentRunAlreadyExists", func(t *testing.T) {
		err := &ErrAgentRunAlreadyExists{Workspace: "ws", Name: "a1"}
		assert.Contains(t, err.Error(), "ws")
		assert.Contains(t, err.Error(), "a1")
		assert.Contains(t, err.Error(), "already exists")
	})
}

// ────────────────────────────────────────────────────────────────────────────
// TransitionState success/mismatch
// ────────────────────────────────────────────────────────────────────────────

func TestAgentTransitionState_Success(t *testing.T) {
	t.Parallel()

	am := newTestAgentManager(t)
	ctx := context.Background()

	agent := makeTestAgentRun("ws1", "trans")
	require.NoError(t, am.Create(ctx, agent))
	// State starts as "creating".

	// Transition creating → idle should succeed.
	ok, err := am.TransitionState(ctx, "ws1", "trans", apiruntime.PhaseCreating, apiruntime.PhaseIdle)
	require.NoError(t, err)
	assert.True(t, ok, "transition from matching state should succeed")

	got, err := am.Get(ctx, "ws1", "trans")
	require.NoError(t, err)
	assert.Equal(t, apiruntime.PhaseIdle, got.Status.Phase)
}

func TestAgentTransitionState_Mismatch(t *testing.T) {
	t.Parallel()

	am := newTestAgentManager(t)
	ctx := context.Background()

	agent := makeTestAgentRun("ws1", "mismatch")
	require.NoError(t, am.Create(ctx, agent))
	// State is "creating".

	// Transition idle → running should fail (current state is creating, not idle).
	ok, err := am.TransitionState(ctx, "ws1", "mismatch", apiruntime.PhaseIdle, apiruntime.PhaseRunning)
	require.NoError(t, err, "mismatch should not be an error")
	assert.False(t, ok, "transition from wrong state should return false")

	// State should remain creating.
	got, err := am.Get(ctx, "ws1", "mismatch")
	require.NoError(t, err)
	assert.Equal(t, apiruntime.PhaseCreating, got.Status.Phase)
}

// ────────────────────────────────────────────────────────────────────────────
// List combined filters and nil filter
// ────────────────────────────────────────────────────────────────────────────

func TestAgentList_NilFilter(t *testing.T) {
	t.Parallel()

	am := newTestAgentManager(t)
	ctx := context.Background()

	require.NoError(t, am.Create(ctx, makeTestAgentRun("ws1", "a1")))
	require.NoError(t, am.Create(ctx, makeTestAgentRun("ws2", "a2")))

	all, err := am.List(ctx, nil)
	require.NoError(t, err)
	assert.Len(t, all, 2, "nil filter should return all agents")
}

func TestAgentList_CombinedFilter(t *testing.T) {
	t.Parallel()

	am := newTestAgentManager(t)
	ctx := context.Background()

	require.NoError(t, am.Create(ctx, makeTestAgentRun("ws1", "a1")))
	require.NoError(t, am.Create(ctx, makeTestAgentRun("ws1", "a2")))
	require.NoError(t, am.Create(ctx, makeTestAgentRun("ws2", "a3")))

	// Set a1 to stopped.
	require.NoError(t, am.UpdateStatus(ctx, "ws1", "a1", pkgariapi.AgentRunStatus{Phase: apiruntime.PhaseStopped}))

	// Filter: workspace=ws1 AND state=stopped → only a1.
	result, err := am.List(ctx, &pkgariapi.AgentRunFilter{
		Workspace: "ws1",
		Phase:     apiruntime.PhaseStopped,
	})
	require.NoError(t, err)
	require.Len(t, result, 1)
	assert.Equal(t, "a1", result[0].Metadata.Name)
}
