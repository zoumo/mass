package core

import (
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/zoumo/agent-sdk-go/agent"

	"github.com/zoumo/mass/massd/pkg/resource"
	"github.com/zoumo/mass/massd/pkg/runtime"
)

func TestEventCandidates(t *testing.T) {
	ctx := t.Context()
	store, err := OpenStore(Config{DataDir: t.TempDir()}, TelemetryOptions{})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Close()) })
	require.Empty(t, store.eventCandidates(ctx))

	for _, binding := range []struct {
		id, role, owner, state, due, body string
		generation                        int
	}{
		{"old-topic", "topic", "topic-1", "revoked", "", `{}`, 1},
		{"current-topic", "topic", "topic-1", "ready", "", `{}`, 2},
		{"stop-only", "issue", "issue-1", "revoked", "", `{"stop":{"status":"requested"}}`, 1},
		{"future", "issue", "issue-2", "pending", after(time.Hour), `{}`, 1},
		{"old-issue", "issue", "issue-3", "revoked", "", `{}`, 1},
		{"current-issue", "issue", "issue-3", "ready", "", `{}`, 2},
		{"ready-issue", "issue", "issue-4", "ready", "", `{"stop":{"status":"requested"}}`, 1},
		{"issue-with-input", "issue", "issue-5", "ready", "", `{}`, 1},
		{"router", "router", "", "ready", "", `{}`, 1},
		{"sent-topic", "topic", "topic-2", "ready", "", `{}`, 1},
		{"due", "topic", "topic-3", "pending", after(-time.Hour), `{}`, 1},
		{"first-attempt", "topic", "topic-4", "pending", "", `{}`, 1},
		{"finished-stop", "topic", "topic-5", "revoked", "", `{"stop":{"status":"observed"}}`, 1},
	} {
		_, err := store.db.ExecContext(ctx, `INSERT INTO session_bindings
 (id,role,owner_id,state,next_attempt_at,body,generation,created_at,token,spec) VALUES(?,?,?,?,?,?,?,?,?,'{}')`,
			binding.id, binding.role, binding.owner, binding.state, binding.due, binding.body, binding.generation, now(), binding.id)
		require.NoError(t, err)
	}
	for _, event := range []struct {
		id, status string
		recipient  eventIdentity
	}{
		{"topic-pending", "pending", eventIdentity{Kind: "topic", TopicID: "topic-1"}},
		{"topic-dispatching", "dispatching", eventIdentity{Kind: "topic", TopicID: "topic-1"}},
		{"retired-input", "dispatching", eventIdentity{Kind: "issue", SessionBindingID: "old-issue"}},
		{"issue-owner-id", "pending", eventIdentity{Kind: "issue", SessionBindingID: "issue-3"}},
		{"current-input", "pending", eventIdentity{Kind: "issue", SessionBindingID: "ready-issue"}},
		{"issue-retry", "dispatching", eventIdentity{Kind: "issue", SessionBindingID: "issue-with-input"}},
		{"router-input", "dispatching", eventIdentity{Kind: "router"}},
		{"router-queued", "pending", eventIdentity{Kind: "router"}},
		{"settled-input", "sent", eventIdentity{Kind: "topic", TopicID: "topic-2"}},
		{"superseded-input", "superseded", eventIdentity{Kind: "topic", TopicID: "topic-2"}},
		{"wrong-role", "pending", eventIdentity{Kind: "topic", TopicID: "current-issue"}},
		{"future-input", "pending", eventIdentity{Kind: "issue", SessionBindingID: "future"}},
	} {
		recipient, err := json.Marshal(event.recipient)
		require.NoError(t, err)
		_, err = store.db.ExecContext(ctx, `INSERT INTO internal_events
 (event_id,kind,status,sender,recipient,created_at) VALUES(?,'context_supplement',?,'{"kind":"core"}',?,?)`,
			event.id, event.status, string(recipient), now())
		require.NoError(t, err)
	}

	require.Equal(t, []eventCandidate{
		{id: "current-topic", role: "topic"},
		{id: "stop-only", role: "issue", stop: true},
		{id: "ready-issue", role: "issue", stop: true},
		{id: "issue-with-input", role: "issue"},
		{id: "router", role: "router"},
		{id: "due", role: "topic"},
		{id: "first-attempt", role: "topic"},
	}, store.eventCandidates(ctx))

	router := eventCandidate{id: "router", role: "router"}
	_, err = store.db.ExecContext(ctx, "UPDATE session_bindings SET state='pending',next_attempt_at=? WHERE id='router'", after(time.Hour))
	require.NoError(t, err)
	require.NotContains(t, store.eventCandidates(ctx), router)
	_, err = store.db.ExecContext(ctx, "UPDATE session_bindings SET state='ready',owner_id='other' WHERE id='router'")
	require.NoError(t, err)
	require.NotContains(t, store.eventCandidates(ctx), router)
	_, err = store.db.ExecContext(ctx, "UPDATE session_bindings SET owner_id='',next_attempt_at='' WHERE id='router'")
	require.NoError(t, err)

	_, err = store.db.ExecContext(ctx, "UPDATE internal_events SET status='sent' WHERE status IN ('pending','dispatching')")
	require.NoError(t, err)
	require.Equal(t, []eventCandidate{
		{id: "stop-only", role: "issue", stop: true},
		{id: "ready-issue", role: "issue", stop: true},
		{id: "due", role: "topic"},
		{id: "first-attempt", role: "topic"},
	}, store.eventCandidates(ctx))

	rows, err := store.db.QueryContext(ctx, "EXPLAIN QUERY PLAN "+eventCandidatesSQL, now())
	require.NoError(t, err)
	defer func() { require.NoError(t, rows.Close()) }()
	var plan []string
	for rows.Next() {
		var id, parent, aux int
		var detail string
		require.NoError(t, rows.Scan(&id, &parent, &aux, &detail))
		plan = append(plan, detail)
	}
	require.NoError(t, rows.Err())
	details := strings.Join(plan, "\n")
	require.NotContains(t, details, "\nSCAN b")
	require.NotContains(t, details, "\nSCAN internal_events")
	for _, access := range []string{
		"session_bindings_pending (next_attempt_at<?)",
		"internal_events_pending (<expr>=? AND <expr>=?)",
		"internal_events_pending (<expr>=?)",
		"session_bindings_ready_owner (role=? AND owner_id=?)",
		"SCAN session_bindings USING INDEX session_bindings_stop",
		"SCAN c\nSEARCH b USING INDEX",
	} {
		require.Contains(t, details, access)
	}
}

func TestSessionReuseRequiresMatchingProfileAndLocation(t *testing.T) {
	previous := SessionBinding{
		ID: "previous", Role: "issue", SessionID: "native", SessionUID: "uid", Name: "old", Workspace: "/project", Namespace: "topic",
		ExecutionProfile: ExecutionProfile{RuntimeRef: "agent", Model: "model", Effort: "medium"},
		Close:            &BindingClose{Reusable: true},
	}
	config := Config{DataDir: t.TempDir(), Listen: "127.0.0.1:24370", Sessions: &SessionsConfig{
		Router: RouterSessionConfig{ExecutionProfile: previous.ExecutionProfile, Workspace: "/project"}, Namespace: "routing",
	}}
	store, err := OpenStore(config, TelemetryOptions{})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Close()) })
	tx, err := store.db.BeginTx(t.Context(), nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = tx.Rollback() })
	_, err = insertBinding(t.Context(), tx, config, previous, SessionBinding{})
	require.NoError(t, err)
	remote := &RemoteComputerConfig{CUA: CUAConfig{Command: "/cua-mcp", ConfigFile: "/cua.yaml"}}
	changedRemote := &RemoteComputerConfig{CUA: CUAConfig{Command: "/cua-mcp", ConfigFile: "/another.yaml"}}
	for _, tc := range []struct {
		name                      string
		saved                     agent.PermissionPreset
		features                  *runtime.SessionFeatures
		savedRemote, wantedRemote *RemoteComputerConfig
		modify                    func(*SessionBinding)
		reuse                     bool
	}{
		{name: "matching location and model", reuse: true},
		{name: "preserves saved permissions and features", saved: agent.PermissionPresetManualReview, features: &runtime.SessionFeatures{AgentQuestions: new(false)}, reuse: true},
		{name: "runtime", modify: func(b *SessionBinding) { b.RuntimeRef = "other" }},
		{name: "model", modify: func(b *SessionBinding) { b.Model = "other" }},
		{name: "effort", modify: func(b *SessionBinding) { b.Effort = "" }},
		{name: "workspace", modify: func(b *SessionBinding) { b.Workspace = "/other" }},
		{name: "namespace", modify: func(b *SessionBinding) { b.Namespace = "other" }},
		{name: "matching remote computer", savedRemote: remote, wantedRemote: remote, reuse: true},
		{name: "changed remote computer", savedRemote: remote, wantedRemote: changedRemote},
	} {
		t.Run(tc.name, func(t *testing.T) {
			spec, err := makeSpec(config, previous)
			require.NoError(t, err)
			spec.Config.PermissionPreset = tc.saved
			spec.Features = tc.features
			tc.savedRemote.applySessionTools(previous.Role, &spec)
			require.NoError(t, putJSON(t.Context(), tx, "UPDATE session_bindings SET spec=?2 WHERE id=?1", spec, previous.ID))
			next := SessionBinding{Role: previous.Role, ExecutionProfile: previous.ExecutionProfile, Workspace: previous.Workspace, Namespace: previous.Namespace, Name: "new"}
			if tc.modify != nil {
				tc.modify(&next)
			}
			saved, err := usePreviousSession(t.Context(), tx, &next, previous, spec.SystemPrompt, tc.wantedRemote)
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
		Router:    RouterSessionConfig{ExecutionProfile: ExecutionProfile{RuntimeRef: "agent", Model: "model"}, Workspace: "/project"},
		Namespace: "routing",
	}}
	store, err := OpenStore(config, TelemetryOptions{})
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

	reopened, err := OpenStore(config, TelemetryOptions{})
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
	require.Len(t, batch.input.Content, 1)
	require.JSONEq(t, wire, batch.input.Content[0].Text.Text)
}

func TestClaudeRouterReplacementRespectsInputAndOwnership(t *testing.T) {
	for _, tc := range []struct {
		name, pendingState, wantError string
		changeBinding                 func(*SessionBinding)
		changeSession                 func(*resource.Session)
	}{
		{name: "one successor under concurrent retries", pendingState: "pending"},
		{name: "unconfirmed input stays with original authority", pendingState: "dispatching", wantError: "router_input_outcome_unknown"},
		{
			name: "unconfirmed previous owner", pendingState: "pending", wantError: "stale_binding",
			changeBinding: func(b *SessionBinding) { b.PreviousOwnerUnknown = true },
		},
		{
			name: "unconfirmed current owner", pendingState: "pending", wantError: "stale_binding",
			changeSession: func(s *resource.Session) { s.Status.State = "unknown" },
		},
		{
			name: "different native identity", pendingState: "pending", wantError: "stale_binding",
			changeSession: func(s *resource.Session) { s.Metadata.UID = "different-uid" },
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := t.Context()
			config := Config{DataDir: t.TempDir(), Listen: "127.0.0.1:24370", Sessions: &SessionsConfig{
				Router:    RouterSessionConfig{ExecutionProfile: ExecutionProfile{RuntimeRef: "agent", Model: "model"}, Workspace: "/project"},
				Namespace: "routing",
			}}
			store, err := OpenStore(config, TelemetryOptions{})
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
