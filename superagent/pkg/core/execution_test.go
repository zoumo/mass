package core

import (
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/zoumo/agent-sdk-go/agent"
	"github.com/zoumo/mass/mass-go/pkg/resource"
	"github.com/zoumo/mass/mass-go/pkg/runtime"
)

func TestSessionReuseRequiresMatchingProfileAndLocation(t *testing.T) {
	previous := Binding{
		ID: "previous", Role: "worker", SessionID: "native", SessionUID: "uid", Name: "old", Workspace: "/project", Namespace: "topic",
		ExecutionProfile: ExecutionProfile{RuntimeRef: "agent", Model: "model", Effort: "medium"},
		Close:            &BindingClose{Reusable: true},
	}
	config := Config{DataDir: t.TempDir(), Execution: &ExecutionConfig{
		Roles: ExecutionRoles{Router: previous.ExecutionProfile}, RouterWorkspace: "/project", Namespace: "routing", MCPCommand: "/core",
	}}
	store, err := Open(config)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Close()) })
	tx, err := store.db.BeginTx(t.Context(), nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = tx.Rollback() })
	_, err = insertBinding(t.Context(), tx, config, previous, Binding{})
	require.NoError(t, err)
	for _, tc := range []struct {
		name             string
		saved, requested agent.PermissionPreset
		modify           func(*Binding)
		reuse            bool
	}{
		{name: "legacy keeps omission", reuse: true},
		{name: "implicit keeps full access", saved: agent.PermissionPresetFullAccess, reuse: true},
		{name: "explicit same as saved", saved: agent.PermissionPresetManualReview, requested: agent.PermissionPresetManualReview, reuse: true},
		{name: "explicit SDK default matches omission", requested: agent.PermissionPresetAutoReview, reuse: true},
		{name: "explicit different from saved", saved: agent.PermissionPresetFullAccess, requested: agent.PermissionPresetManualReview},
		{name: "explicit different from SDK default", requested: agent.PermissionPresetFullAccess},
		{name: "runtime", modify: func(b *Binding) { b.RuntimeRef = "other" }},
		{name: "model", modify: func(b *Binding) { b.Model = "other" }},
		{name: "effort", modify: func(b *Binding) { b.Effort = "" }},
		{name: "workspace", modify: func(b *Binding) { b.Workspace = "/other" }},
		{name: "namespace", modify: func(b *Binding) { b.Namespace = "other" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			spec := makeSpec(config, previous)
			spec.Config.PermissionPreset = tc.saved
			require.NoError(t, putJSON(t.Context(), tx, "UPDATE session_bindings SET spec=?2 WHERE id=?1", spec, previous.ID))
			next := Binding{ExecutionProfile: previous.ExecutionProfile, Workspace: previous.Workspace, Namespace: previous.Namespace, Name: "new"}
			next.PermissionPreset = tc.requested
			if tc.modify != nil {
				tc.modify(&next)
			}
			saved, err := usePreviousSession(t.Context(), tx, &next, previous)
			require.NoError(t, err)
			require.Equal(t, previous.ID, next.PreviousBindingID)
			if tc.reuse {
				require.Equal(t, tc.saved, saved)
				require.Equal(t, previous.SessionID, next.SessionID)
				require.Equal(t, previous.SessionUID, next.SessionUID)
				require.Equal(t, previous.Name, next.Name)
				require.Equal(t, previous.ID, next.CapabilityFileID)
			} else {
				require.Empty(t, next.SessionID)
				require.Empty(t, next.SessionUID)
				require.Empty(t, next.CapabilityFileID)
				require.Equal(t, "new", next.Name)
			}
		})
	}
}

func TestRouterInitializationKeepsLegacySessionSpec(t *testing.T) {
	config := Config{DataDir: t.TempDir(), Execution: &ExecutionConfig{
		Roles:           ExecutionRoles{Router: ExecutionProfile{RuntimeRef: "agent", Model: "model"}},
		RouterWorkspace: "/project", Namespace: "routing", MCPCommand: "/core",
	}}
	store, err := Open(config)
	require.NoError(t, err)
	tx, err := store.db.BeginTx(t.Context(), nil)
	require.NoError(t, err)
	var binding Binding
	require.NoError(t, readJSON(t.Context(), tx, "SELECT body FROM session_bindings WHERE role='router'", &binding))
	var spec resource.SessionSpec
	require.NoError(t, readJSON(t.Context(), tx, "SELECT spec FROM session_bindings WHERE id=?", &spec, binding.ID))
	require.Equal(t, agent.PermissionPresetFullAccess, spec.Config.PermissionPreset)
	spec.Config.PermissionPreset = ""
	require.NoError(t, putJSON(t.Context(), tx, "UPDATE session_bindings SET spec=?2 WHERE id=?1", spec, binding.ID))
	require.NoError(t, tx.Commit())
	require.NoError(t, store.Close())

	reopened, err := Open(config)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, reopened.Close()) })
	tx, err = reopened.db.BeginTx(t.Context(), nil)
	require.NoError(t, err)
	defer tx.Rollback()
	var saved resource.SessionSpec
	require.NoError(t, readJSON(t.Context(), tx, "SELECT spec FROM session_bindings WHERE id=?", &saved, binding.ID))
	require.Equal(t, &runtime.InitialSessionConfig{Model: "model"}, saved.Config)
}
