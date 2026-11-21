package core_test

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/zoumo/agent-sdk-go/agent"
	"github.com/zoumo/mass/mass-go/pkg/client"
	"github.com/zoumo/mass/mass-go/pkg/resource"
	"github.com/zoumo/mass/mass-go/pkg/runtime"

	"github.com/zoumo/mass/superagent/pkg/core"
)

func TestExecutionTemplateConfig(t *testing.T) {
	f := fixtureAt(t)
	f.execution(&routeBoundary{url: "ws://127.0.0.1:12345/rpc"})
	valid, err := os.ReadFile(f.config)
	require.NoError(t, err)
	config, err := core.LoadConfig(f.config)
	require.NoError(t, err)
	require.Equal(t, []core.IssueTemplate{
		{ExecutionProfile: issueModel, Description: "Implement changes"},
		{ExecutionProfile: reviewModel, Description: "Review results"},
	}, config.Sessions.IssueTemplates)

	// A stable HTTPS endpoint can point at Core from a different massd host.
	remote := "https://core.example.com"
	override := "externalURL: " + remote + "\n" + string(valid)
	override = strings.Replace(override, "listen: "+config.Listen, "listen: 127.0.0.1:0", 1)
	write(t, f.config, override)
	config, err = core.LoadConfig(f.config)
	require.NoError(t, err)
	require.Equal(t, remote, config.ExternalURL)
	require.Equal(t, "127.0.0.1:0", config.Listen)

	_, templates, found := strings.Cut(fixtureExecutionProfiles, "  issueTemplates:\n")
	require.True(t, found)
	for _, tc := range []struct {
		name, from, to string
	}{
		{"missing router", "    router:\n      runtimeRef: agent\n      model: router-model\n      effort: low\n", ""},
		{"missing topic model", "      model: topic-model\n", ""},
		{"bad runtime name", "    topic:\n      runtimeRef: agent", "    topic:\n      runtimeRef: Invalid/Runtime"},
		{"empty issue list", "  issueTemplates:\n" + templates, "  issueTemplates: []\n"},
		{"missing description", "      description: Review results\n", ""},
		{"blank description", "description: Review results", "description: '   '"},
		{"duplicate pair", "model: review-model", "model: issue-model"},
		{"unknown role field", "model: topic-model", "model: topic-model\n      other: value"},
		{"unknown template field", "description: Review results", "description: Review results\n      other: value"},
		{"numeric model", "model: router-model", "model: 123"},
		{"numeric effort", "effort: medium", "effort: 2"},
		{"numeric permission preset", "model: router-model", "model: router-model\n      permissionPreset: 2"},
		{"boolean permission preset", "model: topic-model", "model: topic-model\n      permissionPreset: true"},
		{"invalid permission preset", "model: issue-model", "model: issue-model\n      permissionPreset: fullaccess"},
		{"null permission preset", "model: review-model", "model: review-model\n      permissionPreset: null"},
		{"legacy field", "  issueTemplates:", "  runtimeRef: agent\n  issueTemplates:"},
		{"removed execution block", "sessions:", "execution:"},
		{"removed MCP command", "sessions:", "sessions:\n  mcpCommand: superagent"},
		{"remote cleartext", "sessions:", "externalURL: http://core.example.com\nsessions:"},
		{"embedded credentials", "sessions:", "externalURL: https://user:pass@core.example.com\nsessions:"},
		{"endpoint query", "sessions:", "externalURL: https://core.example.com?token=value\nsessions:"},
		{"endpoint path", "sessions:", "externalURL: https://core.example.com/v1/mcp\nsessions:"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			require.Contains(t, string(valid), tc.from)
			write(t, f.config, strings.Replace(string(valid), tc.from, tc.to, 1))
			_, err := core.LoadConfig(f.config)
			var configErr *core.Error
			require.ErrorAs(t, err, &configErr)
			require.Equal(t, "invalid_argument", configErr.Code)
			if strings.Contains(tc.to, "permissionPreset") {
				require.ErrorContains(t, err, "remove permissionPreset")
			}
		})
	}
	f.fails("invalid_argument")
}

var (
	issueModel  = core.ExecutionProfile{RuntimeRef: "agent", Model: "issue-model", Effort: "medium"}
	reviewModel = core.ExecutionProfile{RuntimeRef: "agent", Model: "review-model"}
)

func assertSessionProfile(t *testing.T, massd *client.Client, peer *nativeSubmitPeer, binding core.SessionBinding, want core.ExecutionProfile) {
	t.Helper()
	require.Equal(t, want, binding.ExecutionProfile)
	var session resource.Session
	require.NoError(t, massd.GetSession(t.Context(), resource.SessionRef{SessionID: binding.SessionID}, &session))
	require.Equal(t, want.RuntimeRef, session.Spec.RuntimeRef)
	require.Equal(t, binding.Workspace, session.Spec.Cwd)
	require.Equal(t, binding.Namespace, session.Metadata.Namespace)
	require.Equal(t, binding.SessionUID, session.Metadata.UID)
	config := runtime.InitialSessionConfig{Model: want.Model, Effort: want.Effort, PermissionPreset: agent.PermissionPresetFullAccess}
	require.Equal(t, &config, session.Spec.Config)
	require.Equal(t, &runtime.SessionFeatures{AgentQuestions: new(false)}, session.Spec.Features)
	require.Equal(t, &runtime.SessionFeatures{AgentQuestions: new(false)}, session.Status.EffectiveFeatures)
	native, ok := peer.initialConfigs.Load(binding.SessionID)
	require.True(t, ok)
	require.Equal(t, config, native)
	questions, ok := peer.nativeQuestions.Load(binding.SessionID)
	require.True(t, ok)
	require.Equal(t, new(false), questions)
}

func assertTemplateAccess(t *testing.T, p *process, binding core.SessionBinding, f *fixture, mcp *roleMCPClient, permitted bool) {
	t.Helper()
	var result struct {
		Tools []struct {
			Name string
		}
	}
	require.NoError(t, json.Unmarshal(mcp.call("tools/list", struct{}{})["result"], &result))
	var tools []string
	for _, tool := range result.Tools {
		tools = append(tools, tool.Name)
	}
	if permitted {
		require.Contains(t, tools, "get_topic")
		return
	}
	require.NotContains(t, tools, "get_topic")
	mcpWorkError(t, mcp, "get_topic", "forbidden", struct{}{})
	code, body := requestMCPTool(t, p.endpoint, "get_topic", roleToken(t, f, binding), struct{}{})
	errorCode(t, code, body, http.StatusForbidden, "forbidden")
}

func queryTemplates(t *testing.T, topic *roleMCPClient) []core.IssueTemplate {
	t.Helper()
	return mcpWork[struct{ ExecutionTemplates []core.IssueTemplate }](t, topic, "get_topic", map[string][]string{"include": {"executionTemplates"}}).ExecutionTemplates
}

func TestLegacyRouterSessionResumesWithSavedSpec(t *testing.T) {
	peer := newNativeSubmitPeer(t)
	endpoint, massd := startEventTestMassd(t, peer)
	f, _ := eventFixture(t, endpoint)
	config, err := core.LoadConfig(f.config)
	require.NoError(t, err)
	store, err := core.Open(config)
	require.NoError(t, err)
	require.NoError(t, store.Close())

	db, err := sql.Open("sqlite", filepath.Join(f.data, "core.sqlite"))
	require.NoError(t, err)
	defer db.Close()
	var body, saved []byte
	require.NoError(t, db.QueryRowContext(t.Context(), "SELECT body,spec FROM session_bindings WHERE role='router'").Scan(&body, &saved))
	var binding core.SessionBinding
	require.NoError(t, json.Unmarshal(body, &binding))
	var spec resource.SessionSpec
	require.NoError(t, json.Unmarshal(saved, &spec))
	spec.Config.PermissionPreset = agent.PermissionPresetManualReview
	spec.Features = nil
	saved, err = json.Marshal(spec)
	require.NoError(t, err)
	_, err = db.ExecContext(t.Context(), "UPDATE session_bindings SET spec=? WHERE id=?", saved, binding.ID)
	require.NoError(t, err)

	write(t, testCapabilityPath(f, binding), "AUTHORIZATION='Bearer synthetic-initial-token'\n")
	var original resource.Session
	require.NoError(t, massd.CreateSession(t.Context(), resource.Session{
		Metadata: resource.Metadata{Namespace: binding.Namespace, Name: binding.Name}, Spec: spec,
	}, &original))
	ref := resource.SessionRef{SessionID: original.Status.SessionID}
	require.NoError(t, massd.CloseSession(t.Context(), ref, nil))

	p := f.start()
	ready := waitBinding(t, p, "router", "", "ready")
	require.Equal(t, original.Status.SessionID, ready.SessionID)
	require.Equal(t, original.Metadata.UID, ready.SessionUID)
	var resumed resource.Session
	require.NoError(t, massd.GetSession(t.Context(), ref, &resumed))
	require.Equal(t, spec, resumed.Spec)
	require.Equal(t, &runtime.SessionFeatures{AgentQuestions: new(true)}, resumed.Status.EffectiveFeatures)
	native, ok := peer.resumeConfigs.Load(ready.SessionID)
	require.True(t, ok)
	require.Equal(t, *spec.Config, native)
}

func TestExecutionTemplateSelectionAndRestart(t *testing.T) {
	peer := newNativeSubmitPeer(t)
	endpoint, massd := startEventTestMassd(t, peer)
	var baseRuntime resource.Runtime
	require.NoError(t, massd.GetRuntime(t.Context(), "agent", &baseRuntime))
	for _, name := range []string{"router-agent", "topic-agent"} {
		require.NoError(t, massd.CreateRuntime(t.Context(), resource.Runtime{
			Metadata: resource.Metadata{Name: name}, Spec: baseRuntime.Spec,
		}, nil))
	}

	f, _ := eventFixture(t, endpoint)
	raw, err := os.ReadFile(f.config)
	require.NoError(t, err)
	config := strings.Replace(string(raw), "runtimeRef: agent\n      model: router-model", "runtimeRef: router-agent\n      model: router-model", 1)
	config = strings.Replace(config, "runtimeRef: agent\n      model: topic-model", "runtimeRef: topic-agent\n      model: topic-model", 1)
	const missingTemplate = "    - runtimeRef: missing-agent\n      model: missing-model\n      description: Missing Runtime fixture\n"
	write(t, f.config, config+missingTemplate)
	p := f.start()
	routerBinding := waitBinding(t, p, "router", "", "ready")
	router := startRoleMCP(t, f, routerBinding)
	routerProfile := core.ExecutionProfile{RuntimeRef: "router-agent", Model: "router-model", Effort: "low"}
	assertSessionProfile(t, massd, peer, routerBinding, routerProfile)
	routerRef := resource.SessionRef{SessionID: routerBinding.SessionID}
	require.NoError(t, massd.CloseSession(t.Context(), routerRef, nil))
	require.NoError(t, massd.ResumeSession(t.Context(), routerRef, nil))
	resumedConfig, ok := peer.resumeConfigs.Load(routerBinding.SessionID)
	require.True(t, ok)
	require.Equal(t, agent.PermissionPresetFullAccess, resumedConfig.(runtime.InitialSessionConfig).PermissionPreset)
	assertTemplateAccess(t, p, routerBinding, f, router, false)

	topicBinding, _ := nativeWorkTopic(t, f, p, peer, router, "execution-templates")
	topic := startRoleMCP(t, f, topicBinding)
	topicProfile := core.ExecutionProfile{RuntimeRef: "topic-agent", Model: "topic-model"}
	assertSessionProfile(t, massd, peer, topicBinding, topicProfile)
	var topicSession resource.Session
	require.NoError(t, massd.GetSession(t.Context(), resource.SessionRef{SessionID: topicBinding.SessionID}, &topicSession))
	require.Contains(t, topicSession.Spec.SystemPrompt, "runtimeRef")
	require.Contains(t, topicSession.Spec.SystemPrompt, "issue-model")
	require.Contains(t, topicSession.Spec.SystemPrompt, "Review results")
	assertTemplateAccess(t, p, topicBinding, f, topic, true)
	missing := core.ExecutionProfile{RuntimeRef: "missing-agent", Model: "missing-model"}
	require.Equal(t, []core.IssueTemplate{
		{ExecutionProfile: issueModel, Description: "Implement changes"},
		{ExecutionProfile: reviewModel, Description: "Review results"},
		{ExecutionProfile: missing, Description: "Missing Runtime fixture"},
	}, queryTemplates(t, topic))
	mcpWorkError(t, topic, "get_topic", "invalid_argument", map[string]string{"runtimeRef": "agent"})

	started := issueInput(f.dir, "start-with-create")
	started.Start = true
	invalid := started
	invalid.RuntimeRef, invalid.Model = "missing-agent", "issue-model"
	mcpWorkError(t, topic, "create_issue", "invalid_argument", invalid)
	invalid.RuntimeRef, invalid.Model = "agent", ""
	mcpWorkError(t, topic, "create_issue", "invalid_argument", invalid)
	require.Empty(t, page(t, p, "issues").Items)
	require.Empty(t, issueBindings(t, p))
	require.Len(t, bindings(t, p), 2)
	require.Empty(t, workEvents(t, p, "issue_session_started"))

	work := mcpWork[core.WorkReceipt](t, topic, "create_issue", started)
	initialRetry := mcpWork[core.WorkReceipt](t, topic, "create_issue", started)
	require.Equal(t, work.Issue, initialRetry.Issue)
	require.Equal(t, work.SessionBinding.ID, initialRetry.SessionBinding.ID)
	require.Equal(t, issueModel, work.Issue.ExecutionProfile)
	first := verifyTemplateIssue(t, p, massd, peer, work, issueModel)
	issue := startRoleMCP(t, f, first)
	assertTemplateAccess(t, p, first, f, issue, false)
	require.Equal(t, issueModel, mcpWork[core.WorkReceipt](t, issue, "get_issue", struct{}{}).Issue.ExecutionProfile)
	changedRetry := started
	changedRetry.Model = "review-model"
	mcpWorkError(t, topic, "create_issue", "conflict", changedRetry)

	keep := mcpWork[core.WorkReceipt](t, topic, "create_issue", issueInput(f.dir, "use-removed-template")).Issue
	switchIssue := mcpWork[core.WorkReceipt](t, topic, "create_issue", issueInput(f.dir, "switch-template")).Issue
	switchIssue = blockers(t, topic, switchIssue, "pending-approval", human("Wait for evidence"))
	baseStart := startRequest("replace-profile", switchIssue)
	partial := baseStart
	partial.Model = new("review-model")
	mcpWorkError(t, topic, "start_issue", "invalid_argument", partial)
	mcpWorkError(t, topic, "start_issue", "invalid_argument", map[string]any{
		"operationId": "null-selection", "issueId": switchIssue.ID, "expectedRevision": switchIssue.Revision, "model": nil,
	})
	override := baseStart
	override.RuntimeRef, override.Model = new("agent"), new("review-model")
	stale := override
	stale.ExpectedRevision--
	mcpWorkError(t, topic, "start_issue", "conflict", stale)
	mcpWorkError(t, topic, "start_issue", "failed_precondition", override)
	current := readRecord[core.Issue](t, p, "issues/"+switchIssue.ID)
	require.Equal(t, switchIssue, current)
	require.Len(t, issueBindings(t, p), 1)
	require.Len(t, bindings(t, p), 3)
	require.Len(t, workEvents(t, p, "issue_session_started"), 1)

	p.stop(t, false)
	updated := strings.Replace(config, "    - runtimeRef: agent\n      model: issue-model\n      effort: medium\n      description: Implement changes\n", "", 1)
	updated = strings.Replace(updated, "description: Review results", "description: Review results with new guidance", 1)
	write(t, f.config, strings.Replace(updated, "effort: low", "effort: high", 1)+missingTemplate)
	f.fails("failed_precondition")
	write(t, f.config, updated+missingTemplate)
	p = f.start()
	require.Equal(t, routerBinding.SessionUID, waitBinding(t, p, "router", "", "ready").SessionUID)
	require.Equal(t, topicBinding.SessionUID, waitBinding(t, p, "topic", topicBinding.OwnerID, "ready").SessionUID)
	recovered := waitBinding(t, p, "issue", work.SessionBinding.ID, "ready")
	require.Equal(t, first.ID, recovered.ID)
	require.Equal(t, first.SessionID, recovered.SessionID)
	require.Equal(t, first.SessionUID, recovered.SessionUID)
	assertSessionProfile(t, massd, peer, recovered, issueModel)
	retry := mcpWork[core.WorkReceipt](t, topic, "create_issue", started)
	require.Equal(t, work.Issue, retry.Issue)
	require.Equal(t, work.SessionBinding.ID, retry.SessionBinding.ID)
	require.Equal(t, work.SessionBinding.ID, retry.SessionBinding.ID)
	require.Equal(t, keep, readRecord[core.Issue](t, p, "issues/"+keep.ID))
	require.Equal(t, []core.IssueTemplate{
		{ExecutionProfile: reviewModel, Description: "Review results with new guidance"},
		{ExecutionProfile: missing, Description: "Missing Runtime fixture"},
	}, queryTemplates(t, topic))

	removed := startRequest("explicit-removed", keep)
	removed.RuntimeRef, removed.Model = new("agent"), new("issue-model")
	mcpWorkError(t, topic, "start_issue", "invalid_argument", removed)
	require.Equal(t, keep, readRecord[core.Issue](t, p, "issues/"+keep.ID))
	verifyTemplateIssue(t, p, massd, peer, mcpWork[core.WorkReceipt](t, topic, "start_issue", startRequest("keep-profile", keep)), issueModel)

	override.Resolve = []core.BlockerResolution{{BlockerID: switchIssue.Blockers[0].ID, Reason: "Verified evidence"}}
	replacement := mcpWork[core.WorkReceipt](t, topic, "start_issue", override)
	require.Empty(t, replacement.Issue.Blockers)
	require.Equal(t, reviewModel, replacement.Issue.ExecutionProfile)
	verifyTemplateIssue(t, p, massd, peer, replacement, reviewModel)

	badRuntime := issueInput(f.dir, "missing-runtime")
	badRuntime.RuntimeRef, badRuntime.Model, badRuntime.Start = missing.RuntimeRef, missing.Model, true
	unavailable := mcpWork[core.WorkReceipt](t, topic, "create_issue", badRuntime)
	require.Equal(t, missing, unavailable.Issue.ExecutionProfile)
	var pending core.SessionBinding
	require.Eventually(t, func() bool {
		pending = readRecord[core.SessionBinding](t, p, "session-bindings/"+unavailable.SessionBinding.ID)
		return pending.LastError != ""
	}, 5*time.Second, 50*time.Millisecond)
	require.Equal(t, missing, pending.ExecutionProfile)
	require.Equal(t, "pending", pending.State)
	require.Contains(t, pending.LastError, "session_prepare_not_found")
	require.Empty(t, pending.SessionID)
	t.Logf("Missing Runtime preserved the selected profile and exposed %s", pending.LastError)
}

func verifyTemplateIssue(t *testing.T, p *process, massd *client.Client, peer *nativeSubmitPeer, work core.WorkReceipt, profile core.ExecutionProfile) core.SessionBinding {
	t.Helper()
	require.NotNil(t, work.SessionBinding)
	binding := waitBinding(t, p, "issue", work.SessionBinding.ID, "ready")
	assertSessionProfile(t, massd, peer, binding, profile)
	call := peer.next(t, "turn/start")
	require.Equal(t, binding.SessionID, call.session)
	require.Equal(t, "issue_session_started", decodeInternalBatch(t, call, work.SessionBinding.StartEventID)[0].Kind)
	call.accept()
	waitWorkEvent(t, p, work.SessionBinding.StartEventID, "sent")
	return binding
}
