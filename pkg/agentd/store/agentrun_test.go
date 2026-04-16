package store_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	pkgariapi "github.com/zoumo/mass/pkg/ari/api"
	apiruntime "github.com/zoumo/mass/pkg/runtime-spec/api"
)

// makeAgentRun returns a minimal valid AgentRun for test use.
func makeAgentRun(workspace, name string) *pkgariapi.AgentRun {
	return &pkgariapi.AgentRun{
		Metadata: pkgariapi.ObjectMeta{
			Workspace: workspace,
			Name:      name,
		},
		Spec: pkgariapi.AgentRunSpec{
			Agent: "default",
		},
		Status: pkgariapi.AgentRunStatus{
			State: apiruntime.StatusIdle,
		},
	}
}

// ── Create ────────────────────────────────────────────────────────────────────

func TestCreateAgentRun(t *testing.T) {
	s := tempStore(t)
	agent := makeAgentRun("ws", "agent1")
	require.NoError(t, s.CreateAgentRun(t.Context(), agent))

	got, err := s.GetAgentRun(t.Context(), "ws", "agent1")
	require.NoError(t, err)
	require.NotNil(t, got)
	require.Equal(t, "agent1", got.Metadata.Name)
	require.Equal(t, "ws", got.Metadata.Workspace)
}

func TestCreateAgentRun_DuplicateRejected(t *testing.T) {
	s := tempStore(t)
	require.NoError(t, s.CreateAgentRun(t.Context(), makeAgentRun("ws", "agent-dup")))

	err := s.CreateAgentRun(t.Context(), makeAgentRun("ws", "agent-dup"))
	require.Error(t, err, "duplicate (workspace, name) should be rejected")
}

func TestCreateAgentRun_MissingWorkspace(t *testing.T) {
	s := tempStore(t)
	err := s.CreateAgentRun(t.Context(), &pkgariapi.AgentRun{
		Metadata: pkgariapi.ObjectMeta{Name: "agent1"},
		Spec:     pkgariapi.AgentRunSpec{Agent: "default"},
	})
	require.Error(t, err)
}

func TestCreateAgentRun_MissingName(t *testing.T) {
	s := tempStore(t)
	err := s.CreateAgentRun(t.Context(), &pkgariapi.AgentRun{
		Metadata: pkgariapi.ObjectMeta{Workspace: "ws"},
		Spec:     pkgariapi.AgentRunSpec{Agent: "default"},
	})
	require.Error(t, err)
}

func TestCreateAgentRun_MissingRuntimeClass(t *testing.T) {
	s := tempStore(t)
	err := s.CreateAgentRun(t.Context(), &pkgariapi.AgentRun{
		Metadata: pkgariapi.ObjectMeta{Workspace: "ws", Name: "agent1"},
	})
	require.Error(t, err)
}

// ── Get ───────────────────────────────────────────────────────────────────────

func TestGetAgentRun_NotFound(t *testing.T) {
	s := tempStore(t)
	got, err := s.GetAgentRun(t.Context(), "ws", "ghost")
	require.NoError(t, err)
	require.Nil(t, got)
}

func TestGetAgentRun_NoWorkspaceBucket(t *testing.T) {
	s := tempStore(t)
	got, err := s.GetAgentRun(t.Context(), "nobody", "agent1")
	require.NoError(t, err)
	require.Nil(t, got)
}

func TestGetAgentRun_ByWorkspaceName(t *testing.T) {
	s := tempStore(t)
	require.NoError(t, s.CreateAgentRun(t.Context(), makeAgentRun("myws", "myagent")))

	got, err := s.GetAgentRun(t.Context(), "myws", "myagent")
	require.NoError(t, err)
	require.NotNil(t, got)
	require.Equal(t, "myagent", got.Metadata.Name)
	require.Equal(t, "myws", got.Metadata.Workspace)
}

// ── List ──────────────────────────────────────────────────────────────────────

func TestListAgentRuns_AllWorkspaces(t *testing.T) {
	s := tempStore(t)
	require.NoError(t, s.CreateAgentRun(t.Context(), makeAgentRun("ws1", "a1")))
	require.NoError(t, s.CreateAgentRun(t.Context(), makeAgentRun("ws1", "a2")))
	require.NoError(t, s.CreateAgentRun(t.Context(), makeAgentRun("ws2", "a3")))

	all, err := s.ListAgentRuns(t.Context(), nil)
	require.NoError(t, err)
	require.Len(t, all, 3)
}

func TestListAgentRuns_FilterByWorkspace(t *testing.T) {
	s := tempStore(t)
	require.NoError(t, s.CreateAgentRun(t.Context(), makeAgentRun("ws1", "a1")))
	require.NoError(t, s.CreateAgentRun(t.Context(), makeAgentRun("ws2", "a2")))

	ws1agents, err := s.ListAgentRuns(t.Context(), &pkgariapi.AgentRunFilter{Workspace: "ws1"})
	require.NoError(t, err)
	require.Len(t, ws1agents, 1)
	require.Equal(t, "a1", ws1agents[0].Metadata.Name)
}

func TestListAgentRuns_FilterByState(t *testing.T) {
	s := tempStore(t)

	agentRunning := makeAgentRun("ws", "runner")
	agentRunning.Status.State = apiruntime.StatusRunning
	require.NoError(t, s.CreateAgentRun(t.Context(), agentRunning))

	agentIdle := makeAgentRun("ws", "idler")
	agentIdle.Status.State = apiruntime.StatusIdle
	require.NoError(t, s.CreateAgentRun(t.Context(), agentIdle))

	running, err := s.ListAgentRuns(t.Context(), &pkgariapi.AgentRunFilter{State: apiruntime.StatusRunning})
	require.NoError(t, err)
	require.Len(t, running, 1)
	require.Equal(t, "runner", running[0].Metadata.Name)
}

func TestListAgentRuns_Empty(t *testing.T) {
	s := tempStore(t)
	all, err := s.ListAgentRuns(t.Context(), nil)
	require.NoError(t, err)
	require.Empty(t, all)
}

func TestListAgentRuns_FilterByWorkspace_NoMatch(t *testing.T) {
	s := tempStore(t)
	require.NoError(t, s.CreateAgentRun(t.Context(), makeAgentRun("ws1", "a1")))

	result, err := s.ListAgentRuns(t.Context(), &pkgariapi.AgentRunFilter{Workspace: "nobody"})
	require.NoError(t, err)
	require.Empty(t, result)
}

// ── UpdateAgentRunStatus ──────────────────────────────────────────────────────

func TestUpdateAgentRunStatus(t *testing.T) {
	s := tempStore(t)
	require.NoError(t, s.CreateAgentRun(t.Context(), makeAgentRun("ws", "a")))

	newStatus := pkgariapi.AgentRunStatus{
		State:          apiruntime.StatusRunning,
		RunSocketPath: "/tmp/run.sock",
		RunPID:        12345,
	}
	require.NoError(t, s.UpdateAgentRunStatus(t.Context(), "ws", "a", newStatus))

	got, err := s.GetAgentRun(t.Context(), "ws", "a")
	require.NoError(t, err)
	require.Equal(t, apiruntime.StatusRunning, got.Status.State)
	require.Equal(t, "/tmp/run.sock", got.Status.RunSocketPath)
	require.Equal(t, 12345, got.Status.RunPID)
}

func TestUpdateAgentRunStatus_NotFound(t *testing.T) {
	s := tempStore(t)
	err := s.UpdateAgentRunStatus(t.Context(), "ws", "ghost", pkgariapi.AgentRunStatus{State: apiruntime.StatusRunning})
	require.Error(t, err)
}

func TestTransitionAgentRunState(t *testing.T) {
	s := tempStore(t)
	agent := makeAgentRun("ws", "reserved")
	agent.Status.RunSocketPath = "/tmp/run.sock"
	require.NoError(t, s.CreateAgentRun(t.Context(), agent))

	ok, err := s.TransitionAgentRunState(t.Context(), "ws", "reserved", apiruntime.StatusIdle, apiruntime.StatusRunning)
	require.NoError(t, err)
	require.True(t, ok)

	got, err := s.GetAgentRun(t.Context(), "ws", "reserved")
	require.NoError(t, err)
	require.Equal(t, apiruntime.StatusRunning, got.Status.State)
	require.Equal(t, "/tmp/run.sock", got.Status.RunSocketPath)
}

func TestTransitionAgentRunState_WrongExpectedState(t *testing.T) {
	s := tempStore(t)
	require.NoError(t, s.CreateAgentRun(t.Context(), makeAgentRun("ws", "busy")))

	ok, err := s.TransitionAgentRunState(t.Context(), "ws", "busy", apiruntime.StatusStopped, apiruntime.StatusRunning)
	require.NoError(t, err)
	require.False(t, ok)

	got, err := s.GetAgentRun(t.Context(), "ws", "busy")
	require.NoError(t, err)
	require.Equal(t, apiruntime.StatusIdle, got.Status.State)
}

// ── Delete ────────────────────────────────────────────────────────────────────

func TestDeleteAgentRun(t *testing.T) {
	s := tempStore(t)
	require.NoError(t, s.CreateAgentRun(t.Context(), makeAgentRun("ws", "a")))
	require.NoError(t, s.DeleteAgentRun(t.Context(), "ws", "a"))

	got, err := s.GetAgentRun(t.Context(), "ws", "a")
	require.NoError(t, err)
	require.Nil(t, got)
}

func TestDeleteAgentRun_NotFound(t *testing.T) {
	s := tempStore(t)
	err := s.DeleteAgentRun(t.Context(), "ws", "ghost")
	require.Error(t, err)
}

func TestDeleteAgentRun_SameName_DifferentWorkspace(t *testing.T) {
	s := tempStore(t)
	require.NoError(t, s.CreateAgentRun(t.Context(), makeAgentRun("ws1", "common")))
	require.NoError(t, s.CreateAgentRun(t.Context(), makeAgentRun("ws2", "common")))

	require.NoError(t, s.DeleteAgentRun(t.Context(), "ws1", "common"))

	got, err := s.GetAgentRun(t.Context(), "ws2", "common")
	require.NoError(t, err)
	require.NotNil(t, got)
}
