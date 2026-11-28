package core_test

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/zoumo/agent-sdk-go/agent"

	"github.com/zoumo/mass/massd/pkg/client"
	"github.com/zoumo/mass/massd/pkg/resource"
	"github.com/zoumo/mass/massd/pkg/runtime"
	"github.com/zoumo/mass/superagent/pkg/core"
)

func TestExecutionTemplateConfig(t *testing.T) {
	f := fixtureAt(t)
	f.execution(&routeBoundary{url: "wss://mass.example.test/massd/"})
	valid, err := os.ReadFile(f.config)
	require.NoError(t, err)
	config, err := core.LoadConfig(f.config)
	require.NoError(t, err)
	require.Equal(t, []core.IssueTemplate{
		{Name: "default", ExecutionProfile: issueModel, Description: "Implement changes"},
		{Name: "review", ExecutionProfile: reviewModel, Description: "Review results"},
	}, config.Sessions.Issue.Templates)

	require.Equal(t, f.dir, config.Sessions.Router.Workspace)
	require.Equal(t, "superagent", config.Sessions.Namespace)
	loadCompose := func(raw string) (core.Config, error) {
		section := strings.Replace(raw, "massd:\n  endpoint: wss://mass.example.test/massd/\n", "", 1)
		return core.LoadComposeConfig(f.config, strings.NewReader(section))
	}
	composed, err := loadCompose(string(valid))
	require.NoError(t, err)
	require.Equal(t, config.Sessions, composed.Sessions)

	// A stable HTTPS endpoint can point at Core from a different massd host.
	remote := "https://core.example.com"
	override := "externalURL: " + remote + "\n" + string(valid)
	override = strings.Replace(override, "listen: "+config.Listen, "listen: 127.0.0.1:0", 1)
	write(t, f.config, override)
	config, err = core.LoadConfig(f.config)
	require.NoError(t, err)
	require.Equal(t, remote, config.ExternalURL)
	require.Equal(t, "127.0.0.1:0", config.Listen)

	_, templates, found := strings.Cut(fixtureSessions, "    templates:\n")
	require.True(t, found)
	router := fmt.Sprintf("  router:\n    workspace: %q\n    runtimeRef: agent\n    model: router-model\n    effort: low\n", f.dir)
	for _, tc := range []struct {
		name, from, to, field string
	}{
		{"null workspace", fmt.Sprintf("    workspace: %q", f.dir), "    workspace: null", "sessions.router"},
		{"invalid router mapping", router, "  router: false\n", "sessions.router"},
		{"bad runtime name", "  topic:\n    runtimeRef: agent", "  topic:\n    runtimeRef: Invalid/Runtime", "sessions.topic.runtimeRef"},
		{"explicit empty runtime", "  topic:\n    runtimeRef: agent", "  topic:\n    runtimeRef: ''", "sessions.topic.runtimeRef"},
		{"null issue list", "    templates:\n" + templates, "    templates: null\n", "sessions.issue"},
		{"empty issue list", "    templates:\n" + templates, "    templates: []\n", "sessions.issue.templates"},
		{"missing template name", "      - name: review\n        runtimeRef: agent", "      - runtimeRef: agent", "sessions.issue.templates[1].name"},
		{"blank template name", "name: review", "name: '   '", "sessions.issue.templates[1].name"},
		{"duplicate template name", "name: review", "name: default", "sessions.issue.templates[1].name"},
		{"numeric template name", "name: review", "name: 123", "sessions.issue.templates[1].name"},
		{"missing template runtime", "name: review\n        runtimeRef: agent", "name: review", "sessions.issue.templates[1].runtimeRef"},
		{"unknown role field", "model: topic-model", "model: topic-model\n    other: null", "sessions.topic"},
		{"unknown template field", "description: Review results", "description: Review results\n        other: value", "sessions.issue.templates[1]"},
		{"numeric model", "model: router-model", "model: 123", "sessions.router.model"},
		{"numeric effort", "effort: medium", "effort: 2", "sessions.issue.templates[0].effort"},
		{"numeric permission preset", "model: router-model", "model: router-model\n    permissionPreset: 2", "remove permissionPreset from sessions.router"},
		{"boolean permission preset", "model: topic-model", "model: topic-model\n    permissionPreset: true", "remove permissionPreset from sessions.topic"},
		{"invalid permission preset", "model: issue-model", "model: issue-model\n        permissionPreset: fullaccess", "remove permissionPreset from sessions.issue.templates[0]"},
		{"null permission preset", "model: review-model", "model: review-model\n        permissionPreset: null", "remove permissionPreset from sessions.issue.templates[1]"},
		{"legacy workspace field", "sessions:", "sessions:\n  routerWorkspace: .", "sessions"},
		{"legacy roles field", "sessions:", "sessions:\n  roles: {}", "sessions"},
		{"legacy templates field", "sessions:", "sessions:\n  issueTemplates: []", "sessions"},
		{"removed execution block", "sessions:", "execution:", "configuration"},
		{"removed MCP command", "sessions:", "sessions:\n  mcpCommand: superagent", "sessions"},
		{"remote cleartext", "sessions:", "externalURL: http://core.example.com\nsessions:", "externalURL"},
		{"embedded credentials", "sessions:", "externalURL: https://user:pass@core.example.com\nsessions:", "externalURL"},
		{"endpoint query", "sessions:", "externalURL: https://core.example.com?token=value\nsessions:", "externalURL"},
		{"endpoint path", "sessions:", "externalURL: https://core.example.com/apis/core/v1/mcp\nsessions:", "externalURL"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			require.Contains(t, string(valid), tc.from)
			raw := strings.Replace(string(valid), tc.from, tc.to, 1)
			write(t, f.config, raw)
			_, standaloneErr := core.LoadConfig(f.config)
			_, composeErr := loadCompose(raw)
			for _, err := range []error{standaloneErr, composeErr} {
				var configErr *core.Error
				require.ErrorAs(t, err, &configErr)
				require.Equal(t, "invalid_argument", configErr.Code)
				require.ErrorContains(t, err, tc.field)
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
	require.Equal(t, &runtime.SessionFeatures{AgentQuestions: new(false), RestrictLocalTools: new(false)}, session.Status.EffectiveFeatures)
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

func queryTemplates(t *testing.T, topic *roleMCPClient) []map[string]string {
	t.Helper()
	return mcpWork[struct{ ExecutionTemplates []map[string]string }](t, topic, "get_topic", map[string][]string{"include": {"executionTemplates"}}).ExecutionTemplates
}

func assertTemplateInputs(t *testing.T, topic *roleMCPClient) {
	t.Helper()
	var result struct {
		Tools []struct {
			Name        string
			InputSchema struct {
				Required   []string
				Properties map[string]json.RawMessage
			}
		}
	}
	require.NoError(t, json.Unmarshal(topic.call("tools/list", struct{}{})["result"], &result))
	checked := 0
	for _, tool := range result.Tools {
		if tool.Name != "create_issue" && tool.Name != "start_issue" {
			continue
		}
		checked++
		require.Contains(t, tool.InputSchema.Properties, "template")
		for _, key := range []string{"runtimeRef", "model", "effort"} {
			require.NotContains(t, tool.InputSchema.Properties, key)
		}
		if tool.Name == "create_issue" {
			require.Contains(t, tool.InputSchema.Required, "template")
		} else {
			require.NotContains(t, tool.InputSchema.Required, "template")
		}
	}
	require.Equal(t, 2, checked)
}

func TestLegacyRouterSessionResumesWithSavedSpec(t *testing.T) {
	peer := newNativeSubmitPeer(t)
	endpoint, massd := startEventTestMassd(t, peer)
	f, _ := eventFixture(t, endpoint)
	config, err := core.LoadConfig(f.config)
	require.NoError(t, err)
	store, err := core.OpenStore(config, core.TelemetryOptions{})
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

	credential := testCapabilityPath(f, binding)
	require.NoError(t, os.MkdirAll(filepath.Dir(credential), 0o700))
	write(t, credential, "AUTHORIZATION='Bearer synthetic-initial-token'\n")
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
	require.Equal(t, &runtime.SessionFeatures{AgentQuestions: new(true), RestrictLocalTools: new(false)}, resumed.Status.EffectiveFeatures)
	native, ok := peer.resumeConfigs.Load(ready.SessionID)
	require.True(t, ok)
	require.Equal(t, *spec.Config, native)
}

func TestSingleIssueTemplateRequiresName(t *testing.T) {
	peer := newNativeSubmitPeer(t)
	endpoint, _ := startEventTestMassd(t, peer)
	f, _ := eventFixture(t, endpoint)
	raw, err := os.ReadFile(f.config)
	require.NoError(t, err)
	config, _, found := strings.Cut(string(raw), "      - name: review\n")
	require.True(t, found)
	write(t, f.config, config)
	p := f.start()
	router := startRoleMCP(t, f, waitBinding(t, p, "router", "", "ready"))
	topicBinding, _ := nativeWorkTopic(t, f, p, peer, router, "one-template")
	topic := startRoleMCP(t, f, topicBinding)
	require.Equal(t, []map[string]string{{"name": "default", "description": "Implement changes"}}, queryTemplates(t, topic))

	request := issueInput(f.dir, "single-template")
	mcpWorkError(t, topic, "create_issue", "invalid_argument", map[string]any{
		"operationId": request.OperationID, "title": request.Title, "goal": request.Goal, "start": true,
	})
	require.Empty(t, page(t, p, "issues").Items)
	require.Empty(t, issueBindings(t, p))

	work := mcpWork[core.WorkReceipt](t, topic, "create_issue", request)
	require.Equal(t, "default", work.Issue.Template)
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
	cwd, err := filepath.EvalSymlinks(f.dir)
	require.NoError(t, err)
	routerWorkspace := filepath.Join(cwd, "router-workspace")
	require.NoError(t, os.Mkdir(routerWorkspace, 0o700))
	config := strings.Replace(string(raw), fmt.Sprintf("workspace: %q", cwd), fmt.Sprintf("workspace: %q", routerWorkspace), 1)
	config = strings.Replace(config, "runtimeRef: agent\n    model: router-model", "runtimeRef: router-agent\n    model: router-model", 1)
	config = strings.Replace(config, "runtimeRef: agent\n    model: topic-model", "runtimeRef: topic-agent\n    model: topic-model\n    effort: medium", 1)
	const additionalTemplates = `      - name: unavailable
        runtimeRef: missing-agent
        model: missing-model
        description: Missing Runtime fixture
      - name: careful
        runtimeRef: agent
        model: issue-model
        effort: high
        description: Work in depth
      - name: native
        runtimeRef: agent
`
	write(t, f.config, config+additionalTemplates)
	p := f.start()
	routerBinding := waitBinding(t, p, "router", "", "ready")
	router := startRoleMCP(t, f, routerBinding)
	routerProfile := core.ExecutionProfile{RuntimeRef: "router-agent", Model: "router-model", Effort: "low"}
	require.Equal(t, routerWorkspace, routerBinding.Workspace)
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
	topicProfile := core.ExecutionProfile{RuntimeRef: "topic-agent", Model: "topic-model", Effort: "medium"}
	require.Equal(t, f.dir, topicBinding.Workspace)
	assertSessionProfile(t, massd, peer, topicBinding, topicProfile)
	var topicSession resource.Session
	require.NoError(t, massd.GetSession(t.Context(), resource.SessionRef{SessionID: topicBinding.SessionID}, &topicSession))
	_, templateJSON, found := strings.Cut(topicSession.Spec.SystemPrompt, "\nConfigured Issue Agent templates\n")
	require.True(t, found)
	var promptTemplates []map[string]string
	require.NoError(t, json.Unmarshal([]byte(templateJSON), &promptTemplates))
	wantTemplates := []map[string]string{
		{"name": "default", "description": "Implement changes"},
		{"name": "review", "description": "Review results"},
		{"name": "unavailable", "description": "Missing Runtime fixture"},
		{"name": "careful", "description": "Work in depth"},
		{"name": "native", "description": ""},
	}
	require.Equal(t, wantTemplates, promptTemplates)
	require.Equal(t, wantTemplates, queryTemplates(t, topic))
	assertTemplateAccess(t, p, topicBinding, f, topic, true)
	assertTemplateInputs(t, topic)
	missing := core.ExecutionProfile{RuntimeRef: "missing-agent", Model: "missing-model"}
	mcpWorkError(t, topic, "get_topic", "invalid_argument", map[string]string{"runtimeRef": "agent"})

	started := issueInput(f.dir, "start-with-create")
	started.Start = true
	invalid := map[string]any{
		"operationId": started.OperationID, "title": started.Title, "goal": started.Goal, "start": true,
	}
	mcpWorkError(t, topic, "create_issue", "invalid_argument", invalid)
	for _, value := range []any{nil, "", "  ", "unknown", 123} {
		invalid["template"] = value
		mcpWorkError(t, topic, "create_issue", "invalid_argument", invalid)
	}
	invalid["template"] = "default"
	for _, key := range []string{"runtimeRef", "model", "effort"} {
		invalid[key] = "obsolete"
		mcpWorkError(t, topic, "create_issue", "invalid_argument", invalid)
		delete(invalid, key)
	}
	require.Empty(t, page(t, p, "issues").Items)
	require.Empty(t, issueBindings(t, p))
	require.Len(t, bindings(t, p), 2)
	require.Empty(t, workEvents(t, p, "issue_session_started"))

	work := mcpWork[core.WorkReceipt](t, topic, "create_issue", started)
	initialRetry := mcpWork[core.WorkReceipt](t, topic, "create_issue", started)
	require.Equal(t, work.Issue, initialRetry.Issue)
	require.Equal(t, work.SessionBinding.ID, initialRetry.SessionBinding.ID)
	require.Equal(t, "default", work.Issue.Template)
	require.Equal(t, issueModel, work.Issue.ExecutionProfile)
	first := verifyTemplateIssue(t, p, massd, peer, work, issueModel)
	issue := startRoleMCP(t, f, first)
	assertTemplateAccess(t, p, first, f, issue, false)
	require.Equal(t, issueModel, mcpWork[core.WorkReceipt](t, issue, "get_issue", struct{}{}).Issue.ExecutionProfile)
	changedRetry := started
	changedRetry.Template = "review"
	mcpWorkError(t, topic, "create_issue", "conflict", changedRetry)

	keep := mcpWork[core.WorkReceipt](t, topic, "create_issue", issueInput(f.dir, "use-removed-template")).Issue
	switchIssue := mcpWork[core.WorkReceipt](t, topic, "create_issue", issueInput(f.dir, "switch-template")).Issue
	switchIssue = blockers(t, topic, switchIssue, "pending-approval", human("Wait for evidence"))
	baseStart := startRequest("replace-profile", switchIssue)
	for _, invalid := range []map[string]any{{"template": nil}, {"model": "review-model"}, {"runtimeRef": "agent"}, {"effort": "high"}} {
		invalid["operationId"] = "invalid-selection"
		invalid["issueId"] = switchIssue.ID
		invalid["expectedRevision"] = switchIssue.Revision
		mcpWorkError(t, topic, "start_issue", "invalid_argument", invalid)
	}
	override := baseStart
	override.Template = new("review")
	stale := override
	stale.ExpectedRevision--
	mcpWorkError(t, topic, "start_issue", "conflict", stale)
	mcpWorkError(t, topic, "start_issue", "failed_precondition", override)
	current := readRecord[core.Issue](t, p, "issues/"+switchIssue.ID)
	require.Equal(t, switchIssue, current)
	require.Len(t, issueBindings(t, p), 1)
	require.Len(t, bindings(t, p), 3)
	require.Len(t, workEvents(t, p, "issue_session_started"), 1)

	originalBindings := []core.SessionBinding{routerBinding, topicBinding, first}
	originalSpecs := make([]resource.SessionSpec, 0, len(originalBindings))
	for _, binding := range originalBindings {
		var session resource.Session
		require.NoError(t, massd.GetSession(t.Context(), resource.SessionRef{SessionID: binding.SessionID}, &session))
		originalSpecs = append(originalSpecs, session.Spec)
	}

	p.stop(t, false)
	equivalent := strings.Replace(config, "sessions:\n", "sessions:\n  namespace: superagent\n", 1)
	write(t, f.config, equivalent+additionalTemplates)
	p = f.start()
	for i, binding := range originalBindings {
		identity := binding.OwnerID
		if binding.Role == "issue" {
			identity = binding.ID
		}
		restored := waitBinding(t, p, binding.Role, identity, "ready")
		require.Equal(t, binding.ID, restored.ID)
		require.Equal(t, binding.Name, restored.Name)
		require.Equal(t, binding.Namespace, restored.Namespace)
		require.Equal(t, binding.SessionID, restored.SessionID)
		require.Equal(t, binding.SessionUID, restored.SessionUID)
		var session resource.Session
		require.NoError(t, massd.GetSession(t.Context(), resource.SessionRef{SessionID: restored.SessionID}, &session))
		require.Equal(t, originalSpecs[i], session.Spec)
	}

	p.stop(t, false)
	updated := strings.Replace(equivalent, "      - name: default\n        runtimeRef: agent\n        model: issue-model\n        effort: medium\n        description: Implement changes\n", "", 1)
	updated = strings.Replace(updated, "description: Review results", "description: Review results with new guidance", 1)
	write(t, f.config, strings.Replace(updated, "effort: low", "effort: high", 1)+additionalTemplates)
	f.fails("failed_precondition")
	write(t, f.config, updated+additionalTemplates)
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
	require.Equal(t, keep, readRecord[core.Issue](t, p, "issues/"+keep.ID))
	wantTemplates[1]["description"] = "Review results with new guidance"
	require.Equal(t, wantTemplates[1:], queryTemplates(t, topic))

	removed := startRequest("explicit-removed", keep)
	removed.Template = new("default")
	mcpWorkError(t, topic, "start_issue", "invalid_argument", removed)
	require.Equal(t, keep, readRecord[core.Issue](t, p, "issues/"+keep.ID))
	verifyTemplateIssue(t, p, massd, peer, mcpWork[core.WorkReceipt](t, topic, "start_issue", startRequest("keep-profile", keep)), issueModel)

	override.Resolve = []core.BlockerResolution{{BlockerID: switchIssue.Blockers[0].ID, Reason: "Verified evidence"}}
	replacement := mcpWork[core.WorkReceipt](t, topic, "start_issue", override)
	require.Empty(t, replacement.Issue.Blockers)
	require.Equal(t, "review", replacement.Issue.Template)
	require.Equal(t, reviewModel, replacement.Issue.ExecutionProfile)
	verifyTemplateIssue(t, p, massd, peer, replacement, reviewModel)

	badRuntime := issueInput(f.dir, "missing-runtime")
	badRuntime.Template, badRuntime.Start = "unavailable", true
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
	for _, selection := range []struct {
		name string
		want core.ExecutionProfile
	}{
		{"careful", core.ExecutionProfile{RuntimeRef: issueModel.RuntimeRef, Model: issueModel.Model, Effort: "high"}},
		{"native", core.ExecutionProfile{RuntimeRef: "agent"}},
	} {
		req := issueInput(f.dir, "select-"+selection.name)
		req.Template, req.Start = selection.name, true
		selected := mcpWork[core.WorkReceipt](t, topic, "create_issue", req)
		require.Equal(t, selection.name, selected.Issue.Template)
		binding := verifyTemplateIssue(t, p, massd, peer, selected, selection.want)
		if selection.name == "native" {
			var session resource.Session
			require.NoError(t, massd.GetSession(t.Context(), resource.SessionRef{SessionID: binding.SessionID}, &session))
			rawConfig, err := json.Marshal(session.Spec.Config)
			require.NoError(t, err)
			require.JSONEq(t, `{"permissionPreset":"fullAccess"}`, string(rawConfig))
		}
	}
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
