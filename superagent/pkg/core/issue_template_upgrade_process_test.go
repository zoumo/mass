package core_test

import (
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/zoumo/agent-sdk-go/agent"

	"github.com/zoumo/mass/massd/pkg/client"
	"github.com/zoumo/mass/massd/pkg/events"
	"github.com/zoumo/mass/massd/pkg/resource"
	"github.com/zoumo/mass/massd/pkg/runtime"
	"github.com/zoumo/mass/massd/pkg/store"
	"github.com/zoumo/mass/superagent/pkg/core"
)

func TestNamedIssueTemplatesUpgradeTopicSession(t *testing.T) {
	boundary := newRouteBoundaryWithRuntime(t, "claude")
	f, operator := eventFixture(t, boundary.url)
	p := f.start()
	router := waitBinding(t, p, "router", "", "ready")
	message := submit(t, p, f, input("before-template-upgrade"))
	waitDelivered(t, p, message.MessageID, "router", "")
	decision := decide(t, p, roleToken(t, f, router), newRoute("template-upgrade", []string{message.MessageID}, f.dir))
	topicID := decision.Targets[0].TopicID
	oldTopic := waitBinding(t, p, "topic", topicID, "ready")
	topicEventID := waitDelivered(t, p, message.MessageID, "topic", topicID)
	sent := readRecord[workEventRecord](t, p, "internal-events/"+topicEventID)
	topic := startRoleMCP(t, f, oldTopic)
	oldIssue := mcpWork[core.WorkReceipt](t, topic, "create_issue", issueInput(f.dir, "before-template-upgrade")).Issue
	require.Equal(t, issueModel, oldIssue.ExecutionProfile)

	oldRef := resource.SessionRef{SessionID: oldTopic.SessionID}
	const oldHistory = "Topic result recorded before the template upgrade"
	state := events.Idle
	require.NoError(t, emitObserverEvent(t.Context(), boundary.driver, oldTopic, "", events.StateUpdate{State: &state}))
	require.NoError(t, emitObserverMessage(t.Context(), boundary.driver, oldTopic, oldHistory, true))
	massd, err := client.New(boundary.url, "", client.Options{WriteTimeout: time.Second, SubscriptionQueueCapacity: 8})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, massd.Close()) })
	var history json.RawMessage
	require.Eventually(t, func() bool {
		return massd.History(t.Context(), oldRef, store.HistoryQuery{Limit: 10}, &history) == nil &&
			strings.Contains(string(history), oldHistory)
	}, 5*time.Second, 25*time.Millisecond)
	p.stop(t, false)

	// Recreate the previous serialized prompt and Issue shape in the isolated stores.
	// A blocked Topic lets an admitted follow-up wait for verified replacement.
	oldNative, err := boundary.meta.GetSession(oldRef)
	require.NoError(t, err)
	oldNative.Spec.SystemPrompt = "Choose runtimeRef and model together.\n" +
		`[{"runtimeRef":"agent","model":"issue-model","description":"Implement changes"}]`
	_, err = boundary.meta.UpdateSession(oldNative)
	require.NoError(t, err)
	saved, err := json.Marshal(oldNative.Spec)
	require.NoError(t, err)
	db, err := sql.Open("sqlite", filepath.Join(f.data, "core.sqlite"))
	require.NoError(t, err)
	_, err = db.ExecContext(t.Context(), `UPDATE session_bindings SET spec=?,state='blocked',
		body=json_set(body,'$.state','blocked') WHERE id=?`, saved, oldTopic.ID)
	require.NoError(t, err)
	_, err = db.ExecContext(t.Context(), "UPDATE issues SET body=json_remove(body,'$.template') WHERE id=?", oldIssue.ID)
	require.NoError(t, err)
	require.NoError(t, db.Close())

	config, err := core.LoadConfig(f.config)
	require.NoError(t, err)
	oldStore, err := core.OpenStore(config, core.TelemetryOptions{})
	require.NoError(t, err)
	follow := input("pending-template-upgrade")
	queued, err := oldStore.SubmitTriggerMessage(t.Context(), fixtureTriggerSource, follow)
	require.NoError(t, err)
	require.NoError(t, oldStore.Close())

	// Change only the Issue template. An old Issue has no new name to resolve.
	configYAML, err := os.ReadFile(f.config)
	require.NoError(t, err)
	write(t, f.config, strings.Replace(string(configYAML), "model: issue-model", "model: new-default-model", 1))
	p = f.start()
	keptRouter := waitBinding(t, p, "router", "", "ready")
	require.Equal(t, router.ID, keptRouter.ID)
	require.Equal(t, router.SessionUID, keptRouter.SessionUID)
	require.Equal(t, sent, readRecord[workEventRecord](t, p, "internal-events/"+topicEventID))
	require.Empty(t, waitWorkEvent(t, p, queued.EventID, "pending").SentAt)

	inspection := inspectRoleBinding(t, p, operator, oldTopic.ID)
	require.Equal(t, oldTopic.SessionUID, inspection.SessionBinding.SessionUID)
	recovery := recoverRoleBinding(t, p, operator, recoveryFor("replace-legacy-template-topic", "replace", oldTopic.ID))
	require.Equal(t, "native_session_closed", recovery.Close.Method)
	newTopic := waitBinding(t, p, "topic", topicID, "ready")
	require.Equal(t, recovery.ReplacementSessionBindingID, newTopic.ID)
	require.NotEqual(t, oldTopic.SessionID, newTopic.SessionID)
	require.NotEmpty(t, waitWorkEvent(t, p, queued.EventID, "sent").SentAt)
	require.Equal(t, sent, readRecord[workEventRecord](t, p, "internal-events/"+topicEventID))

	var prior, replacement resource.Session
	require.NoError(t, massd.GetSession(t.Context(), oldRef, &prior))
	require.Equal(t, "offline", string(prior.Status.State))
	require.Equal(t, oldTopic.SessionUID, prior.Metadata.UID)
	require.Equal(t, oldNative.Spec, prior.Spec)
	require.NoError(t, massd.History(t.Context(), oldRef, store.HistoryQuery{Limit: 10}, &history))
	require.Contains(t, string(history), oldHistory)
	require.NoError(t, massd.GetSession(t.Context(), resource.SessionRef{SessionID: newTopic.SessionID}, &replacement))
	_, catalog, found := strings.Cut(replacement.Spec.SystemPrompt, "\nConfigured Issue Agent templates\n")
	require.True(t, found)
	require.Contains(t, replacement.Spec.SystemPrompt, "pass it as template")
	newOwner := startRoleMCP(t, f, newTopic)
	templates := queryTemplates(t, newOwner)
	catalogJSON, err := json.Marshal(templates)
	require.NoError(t, err)
	require.JSONEq(t, string(catalogJSON), catalog)
	require.Equal(t, []map[string]string{
		{"name": "default", "description": "Implement changes"},
		{"name": "review", "description": "Review results"},
	}, templates)
	assertTemplateInputs(t, newOwner)

	pending := readRecord[core.Issue](t, p, "issues/"+oldIssue.ID)
	require.Empty(t, pending.Template)
	require.Equal(t, issueModel, pending.ExecutionProfile)
	started := mcpWork[core.WorkReceipt](t, newOwner, "start_issue", startRequest("start-legacy-issue", pending))
	require.Empty(t, started.Issue.Template)
	issueBinding := waitBinding(t, p, "issue", started.SessionBinding.ID, "ready")
	require.Equal(t, issueModel, issueBinding.ExecutionProfile)
	var issueSession resource.Session
	require.NoError(t, massd.GetSession(t.Context(), resource.SessionRef{SessionID: issueBinding.SessionID}, &issueSession))
	require.Equal(t, &runtime.InitialSessionConfig{Model: issueModel.Model, Effort: issueModel.Effort, PermissionPreset: agent.PermissionPresetFullAccess}, issueSession.Spec.Config)

	create := issueInput(f.dir, "after-template-upgrade")
	create.Template = ""
	mcpWorkError(t, newOwner, "create_issue", "invalid_argument", create)
	create.Template = "default"
	created := mcpWork[core.WorkReceipt](t, newOwner, "create_issue", create)
	require.Equal(t, "default", created.Issue.Template)
	require.Equal(t, "new-default-model", created.Issue.ExecutionProfile.Model)
}
