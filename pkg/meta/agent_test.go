package meta_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/open-agent-d/open-agent-d/pkg/meta"
	"github.com/open-agent-d/open-agent-d/pkg/spec"
)

// makeAgent returns a minimal valid Agent for test use.
func makeAgent(workspace, name string) *meta.Agent {
	return &meta.Agent{
		Metadata: meta.ObjectMeta{
			Workspace: workspace,
			Name:      name,
		},
		Spec: meta.AgentSpec{
			RuntimeClass: "default",
		},
		Status: meta.AgentStatus{
			State: spec.StatusIdle,
		},
	}
}

// ── Create ────────────────────────────────────────────────────────────────────

func TestCreateAgent(t *testing.T) {
	s := tempStore(t)
	agent := makeAgent("ws", "agent1")
	require.NoError(t, s.CreateAgent(t.Context(), agent))

	got, err := s.GetAgent(t.Context(), "ws", "agent1")
	require.NoError(t, err)
	require.NotNil(t, got)
	require.Equal(t, "agent1", got.Metadata.Name)
	require.Equal(t, "ws", got.Metadata.Workspace)
}

func TestCreateAgent_DuplicateRejected(t *testing.T) {
	s := tempStore(t)
	require.NoError(t, s.CreateAgent(t.Context(), makeAgent("ws", "agent-dup")))

	err := s.CreateAgent(t.Context(), makeAgent("ws", "agent-dup"))
	require.Error(t, err, "duplicate (workspace, name) should be rejected")
}

func TestCreateAgent_MissingWorkspace(t *testing.T) {
	s := tempStore(t)
	err := s.CreateAgent(t.Context(), &meta.Agent{
		Metadata: meta.ObjectMeta{Name: "agent1"},
		Spec:     meta.AgentSpec{RuntimeClass: "default"},
	})
	require.Error(t, err)
}

func TestCreateAgent_MissingName(t *testing.T) {
	s := tempStore(t)
	err := s.CreateAgent(t.Context(), &meta.Agent{
		Metadata: meta.ObjectMeta{Workspace: "ws"},
		Spec:     meta.AgentSpec{RuntimeClass: "default"},
	})
	require.Error(t, err)
}

func TestCreateAgent_MissingRuntimeClass(t *testing.T) {
	s := tempStore(t)
	err := s.CreateAgent(t.Context(), &meta.Agent{
		Metadata: meta.ObjectMeta{Workspace: "ws", Name: "agent1"},
	})
	require.Error(t, err)
}

// ── Get ───────────────────────────────────────────────────────────────────────

func TestGetAgent_NotFound(t *testing.T) {
	s := tempStore(t)
	got, err := s.GetAgent(t.Context(), "ws", "ghost")
	require.NoError(t, err)
	require.Nil(t, got)
}

func TestGetAgent_NoWorkspaceBucket(t *testing.T) {
	s := tempStore(t)
	// workspace "nobody" has no agents sub-bucket yet.
	got, err := s.GetAgent(t.Context(), "nobody", "agent1")
	require.NoError(t, err)
	require.Nil(t, got)
}

func TestGetAgent_ByWorkspaceName(t *testing.T) {
	s := tempStore(t)
	require.NoError(t, s.CreateAgent(t.Context(), makeAgent("myws", "myagent")))

	got, err := s.GetAgent(t.Context(), "myws", "myagent")
	require.NoError(t, err)
	require.NotNil(t, got)
	require.Equal(t, "myagent", got.Metadata.Name)
	require.Equal(t, "myws", got.Metadata.Workspace)
}

// ── List ──────────────────────────────────────────────────────────────────────

func TestListAgents_AllWorkspaces(t *testing.T) {
	s := tempStore(t)
	require.NoError(t, s.CreateAgent(t.Context(), makeAgent("ws1", "a1")))
	require.NoError(t, s.CreateAgent(t.Context(), makeAgent("ws1", "a2")))
	require.NoError(t, s.CreateAgent(t.Context(), makeAgent("ws2", "a3")))

	all, err := s.ListAgents(t.Context(), nil)
	require.NoError(t, err)
	require.Len(t, all, 3)
}

func TestListAgents_FilterByWorkspace(t *testing.T) {
	s := tempStore(t)
	require.NoError(t, s.CreateAgent(t.Context(), makeAgent("ws1", "a1")))
	require.NoError(t, s.CreateAgent(t.Context(), makeAgent("ws2", "a2")))

	ws1agents, err := s.ListAgents(t.Context(), &meta.AgentFilter{Workspace: "ws1"})
	require.NoError(t, err)
	require.Len(t, ws1agents, 1)
	require.Equal(t, "a1", ws1agents[0].Metadata.Name)
}

func TestListAgents_FilterByState(t *testing.T) {
	s := tempStore(t)

	agentRunning := makeAgent("ws", "runner")
	agentRunning.Status.State = spec.StatusRunning
	require.NoError(t, s.CreateAgent(t.Context(), agentRunning))

	agentIdle := makeAgent("ws", "idler")
	agentIdle.Status.State = spec.StatusIdle
	require.NoError(t, s.CreateAgent(t.Context(), agentIdle))

	running, err := s.ListAgents(t.Context(), &meta.AgentFilter{State: spec.StatusRunning})
	require.NoError(t, err)
	require.Len(t, running, 1)
	require.Equal(t, "runner", running[0].Metadata.Name)
}

func TestListAgents_Empty(t *testing.T) {
	s := tempStore(t)
	all, err := s.ListAgents(t.Context(), nil)
	require.NoError(t, err)
	require.Empty(t, all)
}

func TestListAgents_FilterByWorkspace_NoMatch(t *testing.T) {
	s := tempStore(t)
	require.NoError(t, s.CreateAgent(t.Context(), makeAgent("ws1", "a1")))

	result, err := s.ListAgents(t.Context(), &meta.AgentFilter{Workspace: "nobody"})
	require.NoError(t, err)
	require.Empty(t, result)
}

// ── UpdateAgentStatus ────────────────────────────────────────────────────────

func TestUpdateAgentStatus(t *testing.T) {
	s := tempStore(t)
	require.NoError(t, s.CreateAgent(t.Context(), makeAgent("ws", "a")))

	newStatus := meta.AgentStatus{
		State:          spec.StatusRunning,
		ShimSocketPath: "/tmp/shim.sock",
		ShimPID:        12345,
	}
	require.NoError(t, s.UpdateAgentStatus(t.Context(), "ws", "a", newStatus))

	got, err := s.GetAgent(t.Context(), "ws", "a")
	require.NoError(t, err)
	require.Equal(t, spec.StatusRunning, got.Status.State)
	require.Equal(t, "/tmp/shim.sock", got.Status.ShimSocketPath)
	require.Equal(t, 12345, got.Status.ShimPID)
}

func TestUpdateAgentStatus_NotFound(t *testing.T) {
	s := tempStore(t)
	err := s.UpdateAgentStatus(t.Context(), "ws", "ghost", meta.AgentStatus{State: spec.StatusRunning})
	require.Error(t, err)
}

// ── Delete ────────────────────────────────────────────────────────────────────

func TestDeleteAgent(t *testing.T) {
	s := tempStore(t)
	require.NoError(t, s.CreateAgent(t.Context(), makeAgent("ws", "a")))
	require.NoError(t, s.DeleteAgent(t.Context(), "ws", "a"))

	got, err := s.GetAgent(t.Context(), "ws", "a")
	require.NoError(t, err)
	require.Nil(t, got)
}

func TestDeleteAgent_NotFound(t *testing.T) {
	s := tempStore(t)
	err := s.DeleteAgent(t.Context(), "ws", "ghost")
	require.Error(t, err)
}

func TestDeleteAgent_SameName_DifferentWorkspace(t *testing.T) {
	s := tempStore(t)
	// Same name in two different workspaces — should be independent.
	require.NoError(t, s.CreateAgent(t.Context(), makeAgent("ws1", "common")))
	require.NoError(t, s.CreateAgent(t.Context(), makeAgent("ws2", "common")))

	// Delete from ws1 only.
	require.NoError(t, s.DeleteAgent(t.Context(), "ws1", "common"))

	// ws2 copy should still exist.
	got, err := s.GetAgent(t.Context(), "ws2", "common")
	require.NoError(t, err)
	require.NotNil(t, got)
}
