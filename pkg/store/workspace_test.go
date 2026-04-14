package store_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	pkgariapi "github.com/zoumo/oar/pkg/ari/api"
)

// makeWorkspace returns a minimal valid Workspace for test use.
func makeWorkspace(name string) *pkgariapi.Workspace {
	return &pkgariapi.Workspace{
		Metadata: pkgariapi.ObjectMeta{Name: name},
		Spec:     pkgariapi.WorkspaceSpec{},
		Status:   pkgariapi.WorkspaceStatus{Phase: pkgariapi.WorkspacePhasePending},
	}
}

// ── Create ────────────────────────────────────────────────────────────────────

func TestCreateWorkspace(t *testing.T) {
	s := tempStore(t)
	ws := makeWorkspace("ws1")
	require.NoError(t, s.CreateWorkspace(t.Context(), ws))

	got, err := s.GetWorkspace(t.Context(), "ws1")
	require.NoError(t, err)
	require.NotNil(t, got)
	require.Equal(t, "ws1", got.Metadata.Name)
}

func TestCreateWorkspace_Duplicate(t *testing.T) {
	s := tempStore(t)
	ws := makeWorkspace("ws-dup")
	require.NoError(t, s.CreateWorkspace(t.Context(), ws))

	err := s.CreateWorkspace(t.Context(), makeWorkspace("ws-dup"))
	require.Error(t, err, "duplicate workspace should be rejected")
}

func TestCreateWorkspace_MissingName(t *testing.T) {
	s := tempStore(t)
	err := s.CreateWorkspace(t.Context(), &pkgariapi.Workspace{})
	require.Error(t, err)
}

// ── Get ───────────────────────────────────────────────────────────────────────

func TestGetWorkspace_NotFound(t *testing.T) {
	s := tempStore(t)
	got, err := s.GetWorkspace(t.Context(), "missing")
	require.NoError(t, err)
	require.Nil(t, got)
}

func TestGetWorkspace_EmptyName(t *testing.T) {
	s := tempStore(t)
	_, err := s.GetWorkspace(t.Context(), "")
	require.Error(t, err)
}

// ── List ──────────────────────────────────────────────────────────────────────

func TestListWorkspaces(t *testing.T) {
	s := tempStore(t)
	require.NoError(t, s.CreateWorkspace(t.Context(), makeWorkspace("a")))
	require.NoError(t, s.CreateWorkspace(t.Context(), makeWorkspace("b")))

	all, err := s.ListWorkspaces(t.Context(), nil)
	require.NoError(t, err)
	require.Len(t, all, 2)
}

func TestListWorkspaces_FilterByPhase(t *testing.T) {
	s := tempStore(t)

	wsPending := makeWorkspace("pending-ws")
	require.NoError(t, s.CreateWorkspace(t.Context(), wsPending))

	wsReady := makeWorkspace("ready-ws")
	require.NoError(t, s.CreateWorkspace(t.Context(), wsReady))
	require.NoError(t, s.UpdateWorkspaceStatus(t.Context(), "ready-ws",
		pkgariapi.WorkspaceStatus{Phase: pkgariapi.WorkspacePhaseReady, Path: "/tmp/ready"}))

	ready, err := s.ListWorkspaces(t.Context(), &pkgariapi.WorkspaceFilter{Phase: pkgariapi.WorkspacePhaseReady})
	require.NoError(t, err)
	require.Len(t, ready, 1)
	require.Equal(t, "ready-ws", ready[0].Metadata.Name)
}

func TestListWorkspaces_Empty(t *testing.T) {
	s := tempStore(t)
	all, err := s.ListWorkspaces(t.Context(), nil)
	require.NoError(t, err)
	require.Empty(t, all)
}

// ── UpdateStatus ─────────────────────────────────────────────────────────────

func TestUpdateWorkspaceStatus(t *testing.T) {
	s := tempStore(t)
	require.NoError(t, s.CreateWorkspace(t.Context(), makeWorkspace("ws-update")))

	newStatus := pkgariapi.WorkspaceStatus{
		Phase: pkgariapi.WorkspacePhaseReady,
		Path:  "/data/ready-ws",
	}
	require.NoError(t, s.UpdateWorkspaceStatus(t.Context(), "ws-update", newStatus))

	got, err := s.GetWorkspace(t.Context(), "ws-update")
	require.NoError(t, err)
	require.Equal(t, pkgariapi.WorkspacePhaseReady, got.Status.Phase)
	require.Equal(t, "/data/ready-ws", got.Status.Path)
}

func TestUpdateWorkspaceStatus_NotFound(t *testing.T) {
	s := tempStore(t)
	err := s.UpdateWorkspaceStatus(t.Context(), "ghost", pkgariapi.WorkspaceStatus{Phase: pkgariapi.WorkspacePhaseReady})
	require.Error(t, err)
}

// ── Delete ────────────────────────────────────────────────────────────────────

func TestDeleteWorkspace(t *testing.T) {
	s := tempStore(t)
	require.NoError(t, s.CreateWorkspace(t.Context(), makeWorkspace("ws-del")))
	require.NoError(t, s.DeleteWorkspace(t.Context(), "ws-del"))

	got, err := s.GetWorkspace(t.Context(), "ws-del")
	require.NoError(t, err)
	require.Nil(t, got)
}

func TestDeleteWorkspace_NotFound(t *testing.T) {
	s := tempStore(t)
	err := s.DeleteWorkspace(t.Context(), "ghost")
	require.Error(t, err)
}

func TestDeleteWorkspace_WithAgents(t *testing.T) {
	s := tempStore(t)

	require.NoError(t, s.CreateWorkspace(t.Context(), makeWorkspace("ws-with-agents")))

	agent := &pkgariapi.AgentRun{
		Metadata: pkgariapi.ObjectMeta{Workspace: "ws-with-agents", Name: "agent1"},
		Spec:     pkgariapi.AgentRunSpec{Agent: "default"},
	}
	require.NoError(t, s.CreateAgentRun(t.Context(), agent))

	err := s.DeleteWorkspace(t.Context(), "ws-with-agents")
	require.Error(t, err, "should refuse deletion when agents exist")
}
