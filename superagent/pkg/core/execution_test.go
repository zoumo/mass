package core

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/zoumo/agent-sdk-go/agent"
	"github.com/zoumo/mass/mass-go/pkg/resource"
	"github.com/zoumo/mass/mass-go/pkg/runtime"
)

func TestSessionReuseRequiresMatchingProfileAndLocation(t *testing.T) {
	previous := Binding{
		ID: "previous", Role: "issue", SessionID: "native", SessionUID: "uid", Name: "old", Workspace: "/project", Namespace: "topic",
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
		name     string
		saved    agent.PermissionPreset
		features *runtime.SessionFeatures
		modify   func(*Binding)
		reuse    bool
	}{
		{name: "legacy keeps omission", reuse: true},
		{name: "new instruction format", modify: func(b *Binding) { b.AgentPromptVersion = agentPromptVersion }},
		{name: "legacy manual review and disabled questions", saved: agent.PermissionPresetManualReview, features: &runtime.SessionFeatures{AgentQuestions: new(false)}, reuse: true},
		{name: "runtime", modify: func(b *Binding) { b.RuntimeRef = "other" }},
		{name: "model", modify: func(b *Binding) { b.Model = "other" }},
		{name: "effort", modify: func(b *Binding) { b.Effort = "" }},
		{name: "workspace", modify: func(b *Binding) { b.Workspace = "/other" }},
		{name: "namespace", modify: func(b *Binding) { b.Namespace = "other" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			spec := makeSpec(config, previous)
			spec.Config.PermissionPreset = tc.saved
			spec.Features = tc.features
			require.NoError(t, putJSON(t.Context(), tx, "UPDATE session_bindings SET spec=?2 WHERE id=?1", spec, previous.ID))
			next := Binding{ExecutionProfile: previous.ExecutionProfile, Workspace: previous.Workspace, Namespace: previous.Namespace, Name: "new"}
			if tc.modify != nil {
				tc.modify(&next)
			}
			saved, err := usePreviousSession(t.Context(), tx, &next, previous, spec.SystemPrompt)
			require.NoError(t, err)
			require.Equal(t, previous.ID, next.PreviousBindingID)
			if tc.reuse {
				require.Equal(t, &spec, saved)
				require.Equal(t, previous.SessionID, next.SessionID)
				require.Equal(t, previous.SessionUID, next.SessionUID)
				require.Equal(t, previous.Name, next.Name)
				require.Equal(t, previous.ID, next.CapabilityFileID)
			} else {
				require.Nil(t, saved)
				require.Empty(t, next.SessionID)
				require.Empty(t, next.SessionUID)
				require.Empty(t, next.CapabilityFileID)
				require.Equal(t, "new", next.Name)
			}
		})
	}
}

func TestRouterInitializationKeepsLegacySessionSpec(t *testing.T) {
	t.Run("version 0", func(t *testing.T) {
		testRouterInitializationKeepsSessionSpec(t, 0, "deduplicate by event_id")
	})
	t.Run("version 1", func(t *testing.T) {
		testRouterInitializationKeepsSessionSpec(t, 1, "On route_request")
	})
}

func testRouterInitializationKeepsSessionSpec(t *testing.T, version int, prompt string) {
	t.Helper()
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
	require.Equal(t, &runtime.SessionFeatures{AgentQuestions: new(false)}, spec.Features)
	require.Equal(t, 2, binding.AgentPromptVersion)
	require.Contains(t, spec.SystemPrompt, "deduplicate by event_id")
	binding.AgentPromptVersion = version
	spec.SystemPrompt = makeSpec(config, binding).SystemPrompt
	require.Contains(t, spec.SystemPrompt, prompt)
	require.NoError(t, putJSON(t.Context(), tx, "UPDATE session_bindings SET body=?2 WHERE id=?1", binding, binding.ID))
	require.NoError(t, enqueueInternalEvent(t.Context(), tx, internalEvent{
		ID: "legacy-id", Kind: routeRequestKind,
		Sender: eventIdentity{Kind: "core"}, Recipient: eventIdentity{Kind: "router"},
		Refs: []eventReference{{Kind: messageRefKind, ID: "mass-m1"}},
	}))
	_, err = tx.ExecContext(t.Context(), "UPDATE internal_events SET created_at=? WHERE event_id='legacy-id'", time.Now().Add(-time.Minute).Format(time.RFC3339Nano))
	require.NoError(t, err)
	spec.Config.PermissionPreset = agent.PermissionPresetManualReview
	spec.Features = nil
	require.NoError(t, putJSON(t.Context(), tx, "UPDATE session_bindings SET spec=?2 WHERE id=?1", spec, binding.ID))
	require.NoError(t, tx.Commit())
	require.NoError(t, store.Close())

	reopened, err := Open(config)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, reopened.Close()) })
	tx, err = reopened.db.BeginTx(t.Context(), nil)
	require.NoError(t, err)
	var saved resource.SessionSpec
	require.NoError(t, readJSON(t.Context(), tx, "SELECT spec FROM session_bindings WHERE id=?", &saved, binding.ID))
	require.Equal(t, spec, saved)
	require.NoError(t, tx.Rollback())
	batch, err := reopened.selectInternalBatch(t.Context(), binding)
	require.NoError(t, err)
	wire := `{"events":[{"event_id":"legacy-id","kind":"route_request","refs":[{"kind":"trigger_message","id":"mass-m1"}]}]}`
	if version == 0 {
		wire = `{"bindingId":"` + binding.ID + `","generation":1,"schemaVersion":4,"events":[{"event_id":"legacy-id","kind":"route_request","sender":{"kind":"core"},"recipient":{"kind":"router"},"refs":[{"kind":"trigger_message","id":"mass-m1"}]}]}`
	}
	require.JSONEq(t, wire, string(batch.wire))
}
