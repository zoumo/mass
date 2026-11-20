package core_test

import (
	"encoding/json"
	"net/http"
	"os"
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
	require.Equal(t, []core.WorkerTemplate{
		{ExecutionProfile: workerModel, Description: "Implement changes"},
		{ExecutionProfile: reviewModel, Description: "Review results"},
	}, config.Execution.WorkerTemplates)

	_, templates, found := strings.Cut(fixtureExecutionProfiles, "  workerTemplates:\n")
	require.True(t, found)
	for _, tc := range []struct {
		name, from, to string
	}{
		{"missing router", "    router:\n      runtimeRef: agent\n      model: router-model\n      effort: low\n", ""},
		{"missing topic model", "      model: topic-model\n", ""},
		{"bad runtime name", "    topic:\n      runtimeRef: agent", "    topic:\n      runtimeRef: Invalid/Runtime"},
		{"empty worker list", "  workerTemplates:\n" + templates, "  workerTemplates: []\n"},
		{"missing description", "      description: Review results\n", ""},
		{"blank description", "description: Review results", "description: '   '"},
		{"duplicate pair", "model: review-model", "model: worker-model"},
		{"unknown role field", "model: topic-model", "model: topic-model\n      other: value"},
		{"unknown template field", "description: Review results", "description: Review results\n      other: value"},
		{"numeric model", "model: router-model", "model: 123"},
		{"numeric effort", "effort: medium", "effort: 2"},
		{"numeric permission preset", "model: router-model", "model: router-model\n      permissionPreset: 2"},
		{"boolean permission preset", "model: topic-model", "model: topic-model\n      permissionPreset: true"},
		{"invalid permission preset", "model: worker-model", "model: worker-model\n      permissionPreset: fullaccess"},
		{"legacy field", "  workerTemplates:", "  runtimeRef: agent\n  workerTemplates:"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			require.Contains(t, string(valid), tc.from)
			write(t, f.config, strings.Replace(string(valid), tc.from, tc.to, 1))
			_, err := core.LoadConfig(f.config)
			var configErr *core.Error
			require.ErrorAs(t, err, &configErr)
			require.Equal(t, "invalid_argument", configErr.Code)
		})
	}
	f.fails("invalid_argument")
}

var (
	workerModel = core.ExecutionProfile{RuntimeRef: "agent", Model: "worker-model", Effort: "medium"}
	reviewModel = core.ExecutionProfile{RuntimeRef: "agent", Model: "review-model"}
)

func assertSessionProfile(t *testing.T, massd *client.Client, peer *nativeDeliveryPeer, binding core.Binding, want core.ExecutionProfile) {
	t.Helper()
	require.Equal(t, want, binding.ExecutionProfile)
	var session resource.Session
	require.NoError(t, massd.GetSession(t.Context(), resource.SessionRef{SessionID: binding.SessionID}, &session))
	require.Equal(t, want.RuntimeRef, session.Spec.RuntimeRef)
	require.Equal(t, binding.Workspace, session.Spec.Cwd)
	require.Equal(t, binding.Namespace, session.Metadata.Namespace)
	require.Equal(t, binding.SessionUID, session.Metadata.UID)
	preset := want.PermissionPreset
	if preset == "" {
		preset = agent.PermissionPresetFullAccess
	}
	config := runtime.InitialSessionConfig{Model: want.Model, Effort: want.Effort, PermissionPreset: preset}
	require.Equal(t, &config, session.Spec.Config)
	native, ok := peer.initialConfigs.Load(binding.SessionID)
	require.True(t, ok)
	require.Equal(t, config, native)
}

func assertTemplateAccess(t *testing.T, p *process, binding core.Binding, f *fixture, mcp *stdioClient, permitted bool) {
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
		require.Contains(t, tools, "execution_template_query")
		return
	}
	require.NotContains(t, tools, "execution_template_query")
	mcpWorkError(t, mcp, "execution_template_query", "forbidden", struct{}{})
	code, body := request(t, http.MethodPost, p.endpoint, "/v1/role/execution_template_query", roleToken(t, f, binding), struct{}{})
	errorCode(t, code, body, http.StatusForbidden, "forbidden")
}

func queryTemplates(t *testing.T, topic *stdioClient) []core.WorkerTemplate {
	t.Helper()
	return mcpWork[struct{ Items []core.WorkerTemplate }](t, topic, "execution_template_query", struct{}{}).Items
}

func TestExecutionTemplateSelectionAndRestart(t *testing.T) {
	peer := newNativeDeliveryPeer(t)
	endpoint, massd := startDeliveryMassd(t, peer)
	var baseRuntime resource.Runtime
	require.NoError(t, massd.GetRuntime(t.Context(), "agent", &baseRuntime))
	for _, name := range []string{"router-agent", "topic-agent"} {
		require.NoError(t, massd.CreateRuntime(t.Context(), resource.Runtime{
			Metadata: resource.Metadata{Name: name}, Spec: baseRuntime.Spec,
		}, nil))
	}

	f, _ := deliveryFixture(t, endpoint)
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
	assertTemplateAccess(t, p, topicBinding, f, topic, true)
	missing := core.ExecutionProfile{RuntimeRef: "missing-agent", Model: "missing-model"}
	require.Equal(t, []core.WorkerTemplate{
		{ExecutionProfile: workerModel, Description: "Implement changes"},
		{ExecutionProfile: reviewModel, Description: "Review results"},
		{ExecutionProfile: missing, Description: "Missing Runtime fixture"},
	}, queryTemplates(t, topic))
	mcpWorkError(t, topic, "execution_template_query", "invalid_argument", map[string]string{"runtimeRef": "agent"})

	started := issueInput(f.dir, "start-with-create")
	started.Start = true
	invalid := started
	invalid.RuntimeRef, invalid.Model = "missing-agent", "worker-model"
	mcpWorkError(t, topic, "issue_create", "invalid_argument", invalid)
	invalid.RuntimeRef, invalid.Model = "agent", ""
	mcpWorkError(t, topic, "issue_create", "invalid_argument", invalid)
	require.Empty(t, page(t, p, "issues").Items)
	require.Empty(t, page(t, p, "executions").Items)
	require.Len(t, bindings(t, p), 2)
	require.Empty(t, workDeliveries(t, p, "worker_started"))

	work := mcpWork[core.WorkReceipt](t, topic, "issue_create", started)
	require.Equal(t, work, mcpWork[core.WorkReceipt](t, topic, "issue_create", started))
	require.Equal(t, workerModel, work.Issue.ExecutionProfile)
	first := verifyTemplateWorker(t, p, massd, peer, work, workerModel)
	worker := startRoleMCP(t, f, first)
	assertTemplateAccess(t, p, first, f, worker, false)
	require.Equal(t, workerModel, mcpWork[core.WorkReceipt](t, worker, "issue_get_current", struct{}{}).Issue.ExecutionProfile)
	changedRetry := started
	changedRetry.Model = "review-model"
	mcpWorkError(t, topic, "issue_create", "conflict", changedRetry)

	keep := mcpWork[core.WorkReceipt](t, topic, "issue_create", issueInput(f.dir, "use-removed-template")).Issue
	switchIssue := mcpWork[core.WorkReceipt](t, topic, "issue_create", issueInput(f.dir, "switch-template")).Issue
	switchIssue = blockers(t, topic, switchIssue, "pending-approval", human("Wait for evidence"))
	baseStart := startRequest("replace-profile", switchIssue)
	partial := baseStart
	partial.Model = new("review-model")
	mcpWorkError(t, topic, "issue_start", "invalid_argument", partial)
	mcpWorkError(t, topic, "issue_start", "invalid_argument", map[string]any{
		"operationId": "null-selection", "issueId": switchIssue.ID, "expectedRevision": switchIssue.Revision, "model": nil,
	})
	override := baseStart
	override.RuntimeRef, override.Model = new("agent"), new("review-model")
	stale := override
	stale.ExpectedRevision--
	mcpWorkError(t, topic, "issue_start", "conflict", stale)
	mcpWorkError(t, topic, "issue_start", "failed_precondition", override)
	current := readRecord[core.Issue](t, p, "issues/"+switchIssue.ID)
	require.Equal(t, switchIssue, current)
	require.Len(t, page(t, p, "executions").Items, 1)
	require.Len(t, bindings(t, p), 3)
	require.Len(t, workDeliveries(t, p, "worker_started"), 1)

	p.stop(t, false)
	updated := strings.Replace(config, "    - runtimeRef: agent\n      model: worker-model\n      effort: medium\n      description: Implement changes\n", "", 1)
	updated = strings.Replace(updated, "description: Review results", "description: Review results with new guidance", 1)
	updated = strings.Replace(updated, "model: review-model", "model: review-model\n      permissionPreset: manualReview", 1)
	explicitReview := reviewModel
	explicitReview.PermissionPreset = agent.PermissionPresetManualReview
	write(t, f.config, strings.Replace(updated, "effort: low", "effort: high", 1)+missingTemplate)
	f.fails("failed_precondition")
	write(t, f.config, updated+missingTemplate)
	p = f.start()
	require.Equal(t, routerBinding.SessionUID, waitBinding(t, p, "router", "", "ready").SessionUID)
	require.Equal(t, topicBinding.SessionUID, waitBinding(t, p, "topic", topicBinding.OwnerID, "ready").SessionUID)
	recovered := waitBinding(t, p, "worker", work.Execution.ID, "ready")
	require.Equal(t, first.ID, recovered.ID)
	require.Equal(t, first.SessionID, recovered.SessionID)
	require.Equal(t, first.SessionUID, recovered.SessionUID)
	assertSessionProfile(t, massd, peer, recovered, workerModel)
	retry := mcpWork[core.WorkReceipt](t, topic, "issue_create", started)
	require.Equal(t, work.Issue, retry.Issue)
	require.Equal(t, work.Execution.ID, retry.Execution.ID)
	require.Equal(t, work.Execution.BindingID, retry.Execution.BindingID)
	require.Equal(t, keep, readRecord[core.Issue](t, p, "issues/"+keep.ID))
	require.Equal(t, []core.WorkerTemplate{
		{ExecutionProfile: explicitReview, Description: "Review results with new guidance"},
		{ExecutionProfile: missing, Description: "Missing Runtime fixture"},
	}, queryTemplates(t, topic))

	removed := startRequest("explicit-removed", keep)
	removed.RuntimeRef, removed.Model = new("agent"), new("worker-model")
	mcpWorkError(t, topic, "issue_start", "invalid_argument", removed)
	require.Equal(t, keep, readRecord[core.Issue](t, p, "issues/"+keep.ID))
	verifyTemplateWorker(t, p, massd, peer, mcpWork[core.WorkReceipt](t, topic, "issue_start", startRequest("keep-profile", keep)), workerModel)

	override.Resolve = []core.BlockerResolution{{BlockerID: switchIssue.Blockers[0].ID, Reason: "Verified evidence"}}
	replacement := mcpWork[core.WorkReceipt](t, topic, "issue_start", override)
	require.Empty(t, replacement.Issue.Blockers)
	require.Equal(t, explicitReview, replacement.Issue.ExecutionProfile)
	verifyTemplateWorker(t, p, massd, peer, replacement, explicitReview)

	badRuntime := issueInput(f.dir, "missing-runtime")
	badRuntime.RuntimeRef, badRuntime.Model, badRuntime.Start = missing.RuntimeRef, missing.Model, true
	unavailable := mcpWork[core.WorkReceipt](t, topic, "issue_create", badRuntime)
	require.Equal(t, missing, unavailable.Issue.ExecutionProfile)
	var pending core.Binding
	require.Eventually(t, func() bool {
		pending = readRecord[core.Binding](t, p, "bindings/"+unavailable.Execution.BindingID)
		return pending.LastError != ""
	}, 5*time.Second, 50*time.Millisecond)
	require.Equal(t, missing, pending.ExecutionProfile)
	require.Equal(t, "pending", pending.State)
	require.Contains(t, pending.LastError, "session_prepare_not_found")
	require.Empty(t, pending.SessionID)
	t.Logf("Missing Runtime preserved the selected profile and exposed %s", pending.LastError)
}

func verifyTemplateWorker(t *testing.T, p *process, massd *client.Client, peer *nativeDeliveryPeer, work core.WorkReceipt, profile core.ExecutionProfile) core.Binding {
	t.Helper()
	require.NotNil(t, work.Execution)
	binding := waitBinding(t, p, "worker", work.Execution.ID, "ready")
	assertSessionProfile(t, massd, peer, binding, profile)
	call := peer.next(t, "turn/start")
	require.Equal(t, binding.SessionID, call.session)
	decodeInternalBatch(t, call, work.Execution.StartEventID)
	require.Contains(t, call.text, work.Issue.ID)
	call.accept()
	waitWorkDelivery(t, p, work.Execution.StartEventID, "sent")
	return binding
}
