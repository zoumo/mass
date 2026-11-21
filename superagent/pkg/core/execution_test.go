package core

import (
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/zoumo/agent-sdk-go/agent"
	"github.com/zoumo/mass/mass-go/pkg/resource"
	"github.com/zoumo/mass/mass-go/pkg/runtime"
)

func TestSessionReuseRequiresMatchingProfileAndLocation(t *testing.T) {
	previous := SessionBinding{
		ID: "previous", Role: "issue", SessionID: "native", SessionUID: "uid", Name: "old", Workspace: "/project", Namespace: "topic",
		ExecutionProfile: ExecutionProfile{RuntimeRef: "agent", Model: "model", Effort: "medium"},
		Close:            &BindingClose{Reusable: true},
	}
	config := Config{DataDir: t.TempDir(), Listen: "127.0.0.1:24370", Sessions: &SessionsConfig{
		Roles: ExecutionRoles{Router: previous.ExecutionProfile}, RouterWorkspace: "/project", Namespace: "routing",
	}}
	store, err := Open(config)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Close()) })
	tx, err := store.db.BeginTx(t.Context(), nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = tx.Rollback() })
	_, err = insertBinding(t.Context(), tx, config, previous, SessionBinding{})
	require.NoError(t, err)
	for _, tc := range []struct {
		name     string
		saved    agent.PermissionPreset
		features *runtime.SessionFeatures
		modify   func(*SessionBinding)
		reuse    bool
	}{
		{name: "matching location and model", reuse: true},
		{name: "preserves saved permissions and features", saved: agent.PermissionPresetManualReview, features: &runtime.SessionFeatures{AgentQuestions: new(false)}, reuse: true},
		{name: "runtime", modify: func(b *SessionBinding) { b.RuntimeRef = "other" }},
		{name: "model", modify: func(b *SessionBinding) { b.Model = "other" }},
		{name: "effort", modify: func(b *SessionBinding) { b.Effort = "" }},
		{name: "workspace", modify: func(b *SessionBinding) { b.Workspace = "/other" }},
		{name: "namespace", modify: func(b *SessionBinding) { b.Namespace = "other" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			spec, err := makeSpec(config, previous)
			require.NoError(t, err)
			spec.Config.PermissionPreset = tc.saved
			spec.Features = tc.features
			require.NoError(t, putJSON(t.Context(), tx, "UPDATE session_bindings SET spec=?2 WHERE id=?1", spec, previous.ID))
			next := SessionBinding{ExecutionProfile: previous.ExecutionProfile, Workspace: previous.Workspace, Namespace: previous.Namespace, Name: "new"}
			if tc.modify != nil {
				tc.modify(&next)
			}
			saved, err := usePreviousSession(t.Context(), tx, &next, previous, spec.SystemPrompt)
			require.NoError(t, err)
			require.Equal(t, previous.ID, next.PreviousSessionBindingID)
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

func TestRoleMCPEndpointDefaultsToLoopback(t *testing.T) {
	config := Config{Listen: "0.0.0.0:24370"}
	endpoint, err := config.roleMCPEndpoint()
	require.NoError(t, err)
	require.Equal(t, "http://127.0.0.1:24370/v1/mcp", endpoint)
}

func TestRouterInitializationKeepsPersistedSessionSpec(t *testing.T) {
	config := Config{DataDir: t.TempDir(), Listen: "127.0.0.1:24370", ExternalURL: "https://core.example.com", Sessions: &SessionsConfig{
		Roles:           ExecutionRoles{Router: ExecutionProfile{RuntimeRef: "agent", Model: "model"}},
		RouterWorkspace: "/project", Namespace: "routing",
	}}
	store, err := Open(config)
	require.NoError(t, err)
	tx, err := store.db.BeginTx(t.Context(), nil)
	require.NoError(t, err)
	var binding SessionBinding
	require.NoError(t, readJSON(t.Context(), tx, "SELECT body FROM session_bindings WHERE role='router'", &binding))
	var spec resource.SessionSpec
	require.NoError(t, readJSON(t.Context(), tx, "SELECT spec FROM session_bindings WHERE id=?", &spec, binding.ID))
	require.Len(t, *spec.MCPServers, 1)
	require.Equal(t, runtime.MCPTransportHTTP, (*spec.MCPServers)[0].Type)
	require.Equal(t, "https://core.example.com/v1/mcp", (*spec.MCPServers)[0].URL)
	require.Equal(t, agent.PermissionPresetFullAccess, spec.Config.PermissionPreset)
	require.Equal(t, &runtime.SessionFeatures{AgentQuestions: new(false)}, spec.Features)
	require.Contains(t, spec.SystemPrompt, "deduplicate by event_id")
	spec.SystemPrompt = "Previously persisted Router instructions."
	require.NoError(t, enqueueInternalEvent(t.Context(), tx, internalEvent{
		ID: "saved-event", Kind: routeRequestKind,
		Sender: eventIdentity{Kind: "core"}, Recipient: eventIdentity{Kind: "router"},
		Refs: []eventReference{{Kind: messageRefKind, ID: "mass-m1"}},
	}))
	_, err = tx.ExecContext(t.Context(), "UPDATE internal_events SET created_at=? WHERE event_id='saved-event'", time.Now().Add(-time.Minute).Format(time.RFC3339Nano))
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
	wire := `{"events":[{"event_id":"saved-event","kind":"route_request","refs":[{"kind":"trigger_message","id":"mass-m1"}]}]}`
	require.JSONEq(t, wire, string(batch.wire))
}

func TestClaudeRouterReplacementRespectsInputAndOwnership(t *testing.T) {
	for _, tc := range []struct {
		name, pendingState, wantError string
		changeBinding                 func(*SessionBinding)
		changeSession                 func(*resource.Session)
	}{
		{name: "one successor under concurrent retries", pendingState: "pending"},
		{name: "unconfirmed input stays with original authority", pendingState: "dispatching", wantError: "router_input_outcome_unknown"},
		{name: "unconfirmed previous owner", pendingState: "pending", wantError: "stale_binding",
			changeBinding: func(b *SessionBinding) { b.PreviousOwnerUnknown = true }},
		{name: "unconfirmed current owner", pendingState: "pending", wantError: "stale_binding",
			changeSession: func(s *resource.Session) { s.Status.State = "unknown" }},
		{name: "different native identity", pendingState: "pending", wantError: "stale_binding",
			changeSession: func(s *resource.Session) { s.Metadata.UID = "different-uid" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := t.Context()
			config := Config{DataDir: t.TempDir(), Listen: "127.0.0.1:24370", Sessions: &SessionsConfig{
				Roles:           ExecutionRoles{Router: ExecutionProfile{RuntimeRef: "agent", Model: "model"}},
				RouterWorkspace: "/project", Namespace: "routing",
			}}
			store, err := Open(config)
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, store.Close()) })
			tx, err := store.db.BeginTx(ctx, nil)
			require.NoError(t, err)
			defer tx.Rollback()
			var binding SessionBinding
			require.NoError(t, readJSON(ctx, tx, "SELECT body FROM session_bindings WHERE role='router'", &binding))
			binding.SessionID, binding.SessionUID, binding.RuntimeType = "native", "uid", "claude"
			if tc.changeBinding != nil {
				tc.changeBinding(&binding)
			}
			require.NoError(t, putJSON(ctx, tx, "UPDATE session_bindings SET body=?2 WHERE id=?1", binding, binding.ID))
			for _, id := range []string{"uncertain-or-pending", "already-sent"} {
				require.NoError(t, enqueueInternalEvent(ctx, tx, internalEvent{
					ID: id, Kind: routeRequestKind,
					Sender: eventIdentity{Kind: "core"}, Recipient: eventIdentity{Kind: "router"},
					Refs: []eventReference{{Kind: messageRefKind, ID: "mass-m1"}},
				}))
			}
			_, err = tx.ExecContext(ctx, "UPDATE internal_events SET status=? WHERE event_id='uncertain-or-pending'", tc.pendingState)
			require.NoError(t, err)
			_, err = tx.ExecContext(ctx, "UPDATE internal_events SET status='sent' WHERE event_id='already-sent'")
			require.NoError(t, err)
			require.NoError(t, tx.Commit())

			session := resource.Session{
				Metadata: resource.Metadata{UID: binding.SessionUID, ResourceVersion: "released"},
				Status: resource.SessionStatus{
					SessionID: binding.SessionID, State: "offline", Reason: resource.SessionReasonRecoveryMissing,
				},
			}
			if tc.changeSession != nil {
				tc.changeSession(&session)
			}
			if tc.wantError != "" {
				require.Equal(t, tc.wantError, errorCode(store.replaceUnrecoverableBinding(ctx, binding, session)))
			} else {
				results := make(chan string, 2)
				var workers sync.WaitGroup
				for range 2 {
					workers.Go(func() { results <- errorCode(store.replaceUnrecoverableBinding(ctx, binding, session)) })
				}
				workers.Wait()
				require.ElementsMatch(t, []string{"", "not_found"}, []string{<-results, <-results})
			}

			var count int
			require.NoError(t, store.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM session_bindings WHERE role='router'").Scan(&count))
			if tc.wantError != "" {
				require.Equal(t, 1, count)
			} else {
				require.Equal(t, 2, count)
				var next SessionBinding
				read, err := store.db.BeginTx(ctx, nil)
				require.NoError(t, err)
				defer read.Rollback()
				require.NoError(t, readJSON(ctx, read, "SELECT body FROM session_bindings WHERE role='router' AND state='pending'", &next))
				require.NoError(t, read.Commit())
				require.Equal(t, binding.Generation+1, next.Generation)
				require.Equal(t, binding.ID, next.PreviousSessionBindingID)
				require.Empty(t, next.SessionID)
				require.Empty(t, next.CapabilityFileID)
			}
			var status string
			require.NoError(t, store.db.QueryRowContext(ctx, "SELECT status FROM internal_events WHERE event_id='uncertain-or-pending'").Scan(&status))
			require.Equal(t, tc.pendingState, status)
			require.NoError(t, store.db.QueryRowContext(ctx, "SELECT status FROM internal_events WHERE event_id='already-sent'").Scan(&status))
			require.Equal(t, "sent", status)
		})
	}
}
