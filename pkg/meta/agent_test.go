// Package meta provides metadata storage for OAR session/workspace/room records.
package meta

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// testRoom creates a room for test use.
func createTestRoom(t *testing.T, store *Store, name string) {
	t.Helper()
	ctx := context.Background()
	err := store.CreateRoom(ctx, &Room{Name: name, CommunicationMode: CommunicationModeMesh})
	require.NoError(t, err, "CreateRoom(%s) should succeed", name)
}

// createTestWorkspace creates a workspace for test use and returns its ID.
func createTestWorkspace(t *testing.T, store *Store, id, name string) {
	t.Helper()
	ctx := context.Background()
	err := store.CreateWorkspace(ctx, &Workspace{
		ID:     id,
		Name:   name,
		Path:   "/tmp/" + name,
		Source: []byte(`{"type":"emptyDir"}`),
		Status: WorkspaceStatusActive,
	})
	require.NoError(t, err, "CreateWorkspace(%s) should succeed", name)
}

// makeAgent returns a minimal valid Agent for tests.
func makeAgent(id, room, name, wsID string) *Agent {
	return &Agent{
		ID:           id,
		Room:         room,
		Name:         name,
		RuntimeClass: "default",
		WorkspaceID:  wsID,
		State:        AgentStateCreating,
	}
}

// TestAgentCRUDRoundTrip verifies create, get, update state, and delete.
func TestAgentCRUDRoundTrip(t *testing.T) {
	t.Parallel()
	store := newTestStore(t)
	ctx := context.Background()

	createTestRoom(t, store, "room1")
	createTestWorkspace(t, store, "ws1", "workspace1")

	agent := makeAgent("agent-1", "room1", "alpha", "ws1")
	agent.Description = "test agent"
	agent.SystemPrompt = "you are a test"
	agent.Labels = map[string]string{"env": "test"}

	// Create
	err := store.CreateAgent(ctx, agent)
	require.NoError(t, err, "CreateAgent should succeed")

	// Get
	got, err := store.GetAgent(ctx, "agent-1")
	require.NoError(t, err, "GetAgent should succeed")
	require.NotNil(t, got, "GetAgent should return an agent")
	assert.Equal(t, "agent-1", got.ID)
	assert.Equal(t, "room1", got.Room)
	assert.Equal(t, "alpha", got.Name)
	assert.Equal(t, "default", got.RuntimeClass)
	assert.Equal(t, "ws1", got.WorkspaceID)
	assert.Equal(t, AgentStateCreating, got.State)
	assert.Equal(t, "test agent", got.Description)
	assert.Equal(t, "you are a test", got.SystemPrompt)
	assert.Equal(t, map[string]string{"env": "test"}, got.Labels)

	// Update state
	err = store.UpdateAgent(ctx, "agent-1", AgentStateRunning, "", nil)
	require.NoError(t, err, "UpdateAgent should succeed")

	updated, err := store.GetAgent(ctx, "agent-1")
	require.NoError(t, err)
	assert.Equal(t, AgentStateRunning, updated.State)

	// Update to error with message
	err = store.UpdateAgent(ctx, "agent-1", AgentStateError, "something went wrong", nil)
	require.NoError(t, err, "UpdateAgent to error should succeed")

	errAgent, err := store.GetAgent(ctx, "agent-1")
	require.NoError(t, err)
	assert.Equal(t, AgentStateError, errAgent.State)
	assert.Equal(t, "something went wrong", errAgent.ErrorMessage)

	// Delete
	err = store.DeleteAgent(ctx, "agent-1")
	require.NoError(t, err, "DeleteAgent should succeed")

	deleted, err := store.GetAgent(ctx, "agent-1")
	require.NoError(t, err)
	assert.Nil(t, deleted, "GetAgent after delete should return nil")
}

// TestAgentGetByRoomName verifies lookup by (room, name) unique pair.
func TestAgentGetByRoomName(t *testing.T) {
	t.Parallel()
	store := newTestStore(t)
	ctx := context.Background()

	createTestRoom(t, store, "roomA")
	createTestWorkspace(t, store, "wsA", "workspaceA")

	agent := makeAgent("agent-rn-1", "roomA", "beta", "wsA")
	require.NoError(t, store.CreateAgent(ctx, agent))

	// Look up by room+name
	got, err := store.GetAgentByRoomName(ctx, "roomA", "beta")
	require.NoError(t, err, "GetAgentByRoomName should succeed")
	require.NotNil(t, got)
	assert.Equal(t, "agent-rn-1", got.ID)
	assert.Equal(t, "roomA", got.Room)
	assert.Equal(t, "beta", got.Name)

	// Non-existent combination returns nil
	missing, err := store.GetAgentByRoomName(ctx, "roomA", "nonexistent")
	require.NoError(t, err)
	assert.Nil(t, missing)
}

// TestAgentUniqueRoomName verifies that two agents with the same room+name are rejected.
func TestAgentUniqueRoomName(t *testing.T) {
	t.Parallel()
	store := newTestStore(t)
	ctx := context.Background()

	createTestRoom(t, store, "roomU")
	createTestWorkspace(t, store, "wsU", "workspaceU")

	agent1 := makeAgent("agent-u-1", "roomU", "gamma", "wsU")
	require.NoError(t, store.CreateAgent(ctx, agent1))

	agent2 := makeAgent("agent-u-2", "roomU", "gamma", "wsU")
	err := store.CreateAgent(ctx, agent2)
	require.Error(t, err, "Duplicate room+name should fail")
	assert.Contains(t, err.Error(), "already exists")
}

// TestAgentFKConstraintRoom verifies that creating an agent with a non-existent room fails.
func TestAgentFKConstraintRoom(t *testing.T) {
	t.Parallel()
	store := newTestStore(t)
	ctx := context.Background()

	createTestWorkspace(t, store, "wsFKR", "workspaceFKR")

	agent := makeAgent("agent-fkr-1", "nonexistent-room", "delta", "wsFKR")
	err := store.CreateAgent(ctx, agent)
	require.Error(t, err, "Agent with non-existent room should fail")
	assert.Contains(t, err.Error(), "foreign key constraint")
}

// TestAgentFKConstraintWorkspace verifies that creating an agent with a non-existent workspace fails.
func TestAgentFKConstraintWorkspace(t *testing.T) {
	t.Parallel()
	store := newTestStore(t)
	ctx := context.Background()

	createTestRoom(t, store, "roomFKW")

	agent := makeAgent("agent-fkw-1", "roomFKW", "epsilon", "nonexistent-ws")
	err := store.CreateAgent(ctx, agent)
	require.Error(t, err, "Agent with non-existent workspace should fail")
	assert.Contains(t, err.Error(), "foreign key constraint")
}

// TestListAgentsFiltering verifies filtering by state and by room.
func TestListAgentsFiltering(t *testing.T) {
	t.Parallel()
	store := newTestStore(t)
	ctx := context.Background()

	createTestRoom(t, store, "roomL1")
	createTestRoom(t, store, "roomL2")
	createTestWorkspace(t, store, "wsL", "workspaceL")

	// Create agents in different rooms and states
	agents := []*Agent{
		{ID: "agent-l-1", Room: "roomL1", Name: "zeta", RuntimeClass: "default", WorkspaceID: "wsL", State: AgentStateCreating},
		{ID: "agent-l-2", Room: "roomL1", Name: "eta", RuntimeClass: "default", WorkspaceID: "wsL", State: AgentStateRunning},
		{ID: "agent-l-3", Room: "roomL2", Name: "theta", RuntimeClass: "default", WorkspaceID: "wsL", State: AgentStateRunning},
		{ID: "agent-l-4", Room: "roomL2", Name: "iota", RuntimeClass: "default", WorkspaceID: "wsL", State: AgentStateStopped},
	}
	for _, a := range agents {
		require.NoError(t, store.CreateAgent(ctx, a))
	}

	// List all
	all, err := store.ListAgents(ctx, nil)
	require.NoError(t, err)
	assert.Len(t, all, 4)

	// Filter by state=running
	running, err := store.ListAgents(ctx, &AgentFilter{State: AgentStateRunning})
	require.NoError(t, err)
	assert.Len(t, running, 2)
	for _, a := range running {
		assert.Equal(t, AgentStateRunning, a.State)
	}

	// Filter by room=roomL1
	room1Agents, err := store.ListAgents(ctx, &AgentFilter{Room: "roomL1"})
	require.NoError(t, err)
	assert.Len(t, room1Agents, 2)
	for _, a := range room1Agents {
		assert.Equal(t, "roomL1", a.Room)
	}

	// Filter by room=roomL2 and state=running
	combo, err := store.ListAgents(ctx, &AgentFilter{Room: "roomL2", State: AgentStateRunning})
	require.NoError(t, err)
	assert.Len(t, combo, 1)
	assert.Equal(t, "agent-l-3", combo[0].ID)
}

// TestAgentUpdateNonExistent verifies updating a non-existent agent returns an error.
func TestAgentUpdateNonExistent(t *testing.T) {
	t.Parallel()
	store := newTestStore(t)
	ctx := context.Background()

	err := store.UpdateAgent(ctx, "nonexistent-id", AgentStateRunning, "", nil)
	require.Error(t, err, "UpdateAgent on non-existent ID should fail")
	assert.Contains(t, err.Error(), "does not exist")
}

// TestAgentDeleteNonExistent verifies deleting a non-existent agent returns an error.
func TestAgentDeleteNonExistent(t *testing.T) {
	t.Parallel()
	store := newTestStore(t)
	ctx := context.Background()

	err := store.DeleteAgent(ctx, "nonexistent-id")
	require.Error(t, err, "DeleteAgent on non-existent ID should fail")
	assert.Contains(t, err.Error(), "does not exist")
}

// TestSchemav3AgentsTableExists verifies that the agents table, indexes, and
// trigger are created after NewStore (schema v3).
func TestSchemav3AgentsTableExists(t *testing.T) {
	t.Parallel()
	store := newTestStore(t)
	ctx := context.Background()

	// Verify agents table exists.
	var tableExists bool
	err := store.db.QueryRowContext(ctx,
		"SELECT EXISTS(SELECT 1 FROM sqlite_master WHERE type='table' AND name='agents')",
	).Scan(&tableExists)
	require.NoError(t, err)
	assert.True(t, tableExists, "agents table should exist")

	// Verify indexes exist.
	expectedIndexes := []string{
		"idx_agents_room",
		"idx_agents_state",
		"idx_agents_room_name",
	}
	for _, idx := range expectedIndexes {
		var exists bool
		err := store.db.QueryRowContext(ctx,
			"SELECT EXISTS(SELECT 1 FROM sqlite_master WHERE type='index' AND name=?)",
			idx,
		).Scan(&exists)
		require.NoError(t, err)
		assert.True(t, exists, "index %s should exist", idx)
	}

	// Verify trigger exists.
	var triggerExists bool
	err = store.db.QueryRowContext(ctx,
		"SELECT EXISTS(SELECT 1 FROM sqlite_master WHERE type='trigger' AND name='trg_agents_updated')",
	).Scan(&triggerExists)
	require.NoError(t, err)
	assert.True(t, triggerExists, "trg_agents_updated trigger should exist")

	// Verify schema version is 4 (v3 agents table + v4 sessions.agent_id FK).
	var version int
	err = store.db.QueryRowContext(ctx,
		"SELECT MAX(version) FROM schema_version",
	).Scan(&version)
	require.NoError(t, err)
	assert.Equal(t, 4, version, "schema version should be 4")
}
