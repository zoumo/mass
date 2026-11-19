package core_test

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/zoumo/mass/mass-go/pkg/resource"
	"github.com/zoumo/mass/mass-go/pkg/runtime"

	"github.com/zoumo/mass/superagent/pkg/core"
)

func lifecycle(op string, revision int) core.LifecycleRequest {
	return core.LifecycleRequest{OperationID: op, ExpectedRevision: revision, Reason: op + " by explicit request"}
}

func issueLifecycle(op string, issue core.Issue) core.IssueLifecycleRequest {
	return core.IssueLifecycleRequest{LifecycleRequest: lifecycle(op, issue.Revision), IssueID: issue.ID}
}

func startRequest(op string, issue core.Issue) core.StartIssueRequest {
	return core.StartIssueRequest{OperationID: op, IssueID: issue.ID, ExpectedRevision: issue.Revision}
}

func inspectRoleBinding(t *testing.T, p *process, operatorFile, id string) core.BindingInspection {
	t.Helper()
	cmd := exec.CommandContext(t.Context(), build(t), "inspect-binding", id, "--endpoint", p.endpoint, "--operator-token-file", operatorFile)
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "%s", out)
	var inspection core.BindingInspection
	require.NoError(t, json.Unmarshal(out, &inspection))
	return inspection
}

func recoverRoleBinding(t *testing.T, p *process, operatorFile string, req core.RecoveryRequest) core.Recovery {
	t.Helper()
	args := []string{
		"recover-input", req.BindingID, "--endpoint", p.endpoint, "--operator-token-file", operatorFile,
		"--mode", req.Mode, "--operation-id", req.OperationID, "--reconciliation", req.Reconciliation, "--accept-possible-duplicate",
	}
	if req.StopRuntime {
		args = append(args, "--stop-runtime")
	}
	out, err := exec.CommandContext(t.Context(), build(t), args...).CombinedOutput()
	require.NoError(t, err, "%s", out)
	var recovery core.Recovery
	require.NoError(t, json.Unmarshal(out, &recovery))
	require.Equal(t, "completed", recovery.Status)
	return recovery
}

func recoveryFor(op, mode, bindingID string) core.RecoveryRequest {
	return core.RecoveryRequest{
		OperationID: op, BindingID: bindingID, Mode: mode,
		Reconciliation: "Inspected the original binding and native ownership", AcceptPossibleDuplicate: true,
	}
}

func waitObservedStop(t *testing.T, p *process, collection, id string) core.StopRequest {
	t.Helper()
	var result struct{ Stop *core.StopRequest }
	require.Eventually(t, func() bool {
		result = readRecord[struct{ Stop *core.StopRequest }](t, p, collection+"/"+id)
		return result.Stop != nil && result.Stop.Status == "observed"
	}, 8*time.Second, 25*time.Millisecond)
	return *result.Stop
}

func TestLifecycleNativeCancelReopenAndReplace(t *testing.T) {
	peer := newNativeDeliveryPeer(t)
	endpoint, massd := startDeliveryMassd(t, peer)
	f, operatorFile := deliveryFixture(t, endpoint)
	p := f.start("SUPERAGENT_TEST_EVENTS=" + testProducerToken)
	router := startRoleMCP(t, f, waitBinding(t, p, "router", "", "ready"))
	topicBinding, source := nativeWorkTopic(t, f, p, peer, router, "lifecycle-topic")
	topic := startRoleMCP(t, f, topicBinding)
	issue := mcpWork[core.WorkReceipt](t, topic, "issue_create", issueInput(f.dir, "lifecycle-issue")).Issue
	dependent := mcpWork[core.WorkReceipt](t, topic, "issue_create", issueInput(f.dir, "lifecycle-dependency")).Issue
	dependent = blockers(t, topic, dependent, "set-dependency", dependency(issue))
	completed := mcpWork[core.WorkReceipt](t, topic, "issue_create", issueInput(f.dir, "existing-result")).Issue
	completed = mcpWork[core.WorkReceipt](t, topic, "issue_complete", core.CompleteIssueRequest{
		CompleteRequest: acceptance("accept-existing", completed.Revision, "retained.txt"), IssueID: completed.ID,
	}).Issue

	start := startRequest("first-start", issue)
	work := mcpWork[core.WorkReceipt](t, topic, "issue_start", start)
	x := work.Execution
	workerBinding := waitBinding(t, p, "worker", x.ID, "ready")
	worker := startRoleMCP(t, f, workerBinding)
	workerToken := roleToken(t, f, workerBinding)
	inFlight := peer.next(t, "turn/start")
	defer func() {
		select {
		case inFlight.answer <- nativeDeliveryAnswer{}:
		default:
		}
	}()
	require.Equal(t, workerBinding.SessionID, inFlight.session)
	decodeInternalBatch(t, inFlight, x.StartEventID)
	cancel := issueLifecycle("cancel-in-flight", work.Issue)
	canceled := mcpWork[core.WorkReceipt](t, topic, "issue_cancel", cancel)
	mcpWorkError(t, topic, "issue_cancel", "conflict", cancel)
	require.Equal(t, "canceled", canceled.Issue.Status)
	require.Empty(t, canceled.Issue.CurrentExecutionID)
	require.Equal(t, "requested", canceled.Execution.Stop.Status)
	reopen := issueLifecycle("open-issue", canceled.Issue)
	reopened := mcpWork[core.WorkReceipt](t, topic, "issue_reopen", reopen)
	mcpWorkError(t, topic, "issue_reopen", "conflict", reopen)
	require.Equal(t, "pending", reopened.Issue.Status)
	require.Nil(t, reopened.Execution)
	mcpWorkError(t, topic, "issue_start", "conflict", start)
	nextStart := startRequest("second-start", reopened.Issue)
	mcpWorkError(t, topic, "issue_start", "failed_precondition", nextStart)
	mcpWorkError(t, worker, "issue_get_current", "stale_binding", struct{}{})
	require.Len(t, page(t, p, "executions").Items, 1)
	require.Equal(t, dependent, readRecord[core.Issue](t, p, "issues/"+dependent.ID))
	dependencyEvents := dependencyDeliveries(t, f)
	require.Len(t, dependencyEvents, 2)
	for _, e := range dependencyEvents {
		require.Equal(t, "context_supplement", e.Kind)
		require.Contains(t, string(e.Body), dependent.ID)
	}
	require.Contains(t, string(dependencyEvents[0].Body), `"status":"canceled"`)
	require.Contains(t, string(dependencyEvents[1].Body), `"status":"pending"`)
	var notified []string
	for len(notified) < len(dependencyEvents) {
		dependencyNotice := peer.next(t, "thread/inject_items")
		dependencyNotice.accept()
		for _, e := range decodeInternalBatch(t, dependencyNotice) {
			notified = append(notified, e.ID)
		}
	}
	require.Equal(t, []string{dependencyEvents[0].ID, dependencyEvents[1].ID}, notified)
	inFlight.accept()
	require.Empty(t, waitWorkDelivery(t, p, x.StartEventID, "superseded").SentAt)
	stop := waitObservedStop(t, p, "executions", x.ID)
	require.Equal(t, "controlAcknowledged", stop.Receipt.Result)
	require.Equal(t, "idle", stop.Observation.State)
	require.Equal(t, "unknown", stop.WorkState)
	mcpWorkError(t, topic, "issue_start", "failed_precondition", nextStart)
	examination := inspectRoleBinding(t, p, operatorFile, workerBinding.ID)
	require.Equal(t, workerBinding.SessionID, examination.Binding.SessionID)
	require.Equal(t, "revoked", examination.Binding.State)
	require.Equal(t, "unknown", examination.Stop.WorkState)
	require.Nil(t, examination.Binding.Close)

	opToken, err := os.ReadFile(operatorFile)
	require.NoError(t, err)
	plain := recoveryFor("unsafe-detach", "settle", workerBinding.ID)
	status, data := request(t, http.MethodPost, p.endpoint, "/v1/operator/input-recovery", string(opToken), plain)
	errorCode(t, status, data, http.StatusConflict, "failed_precondition")
	require.Equal(t, "blocked", inspectRoleBinding(t, p, operatorFile, workerBinding.ID).Binding.Recovery.Status)
	old := recoveryFor("close-first-runtime", "replace", workerBinding.ID)
	old.StopRuntime = true
	release := recoverRoleBinding(t, p, operatorFile, old)
	require.Equal(t, "runtime_stopped", release.Close.Method)
	require.False(t, release.Close.Reusable)
	require.Equal(t, release, recoverRoleBinding(t, p, operatorFile, old))
	select {
	case <-peer.released:
	case <-time.After(4 * time.Second):
		t.Fatal("old native app-server connection never released")
	}
	var previous resource.Session
	require.NoError(t, massd.GetSession(t.Context(), resource.SessionRef{SessionID: workerBinding.SessionID}, &previous))
	require.Equal(t, "offline", string(previous.Status.State))

	amended := mcpWork[core.WorkReceipt](t, topic, "issue_update_requirements", core.UpdateRequirementsRequest{
		OperationID: "amend-while-reopened", IssueID: issue.ID, ExpectedRevision: reopened.Issue.Revision,
		Goal: "Verify the replacement build as well.",
	})
	nextStart.ExpectedRevision = amended.Issue.Revision
	next := mcpWork[core.WorkReceipt](t, topic, "issue_start", nextStart)
	nextBinding := waitBinding(t, p, "worker", next.Execution.ID, "ready")
	require.Equal(t, x.Generation+1, next.Execution.Generation)
	require.NotEqual(t, workerBinding.SessionID, nextBinding.SessionID)
	nextInput := peer.next(t, "turn/start")
	require.Equal(t, nextBinding.SessionID, nextInput.session)
	decodeInternalBatch(t, nextInput, next.Execution.StartEventID)
	require.Contains(t, nextInput.text, x.ID)
	nextInput.accept()
	waitWorkDelivery(t, p, next.Execution.StartEventID, "sent")
	mcpWorkError(t, worker, "issue_get_current", "stale_binding", struct{}{})
	for _, tool := range []string{"register_pr", "event_ack", "event_get", "issue_material_get"} {
		status, data = request(t, http.MethodPost, p.endpoint, "/v1/role/"+tool, workerToken, map[string]any{"messageId": source.MessageID})
		errorCode(t, status, data, http.StatusConflict, "stale_binding")
	}
	reportReq := core.ReportRequest{OperationID: "late-ready", Kind: "ready", Summary: "Late evidence on the first execution", GoalRevision: x.Revision}
	impossible := reportReq
	impossible.GoalRevision = next.Execution.Revision
	mcpWorkError(t, worker, "report_submit", "invalid_argument", impossible)
	report := mcpWork[core.Report](t, worker, "report_submit", reportReq)
	require.Equal(t, report, mcpWork[core.Report](t, worker, "report_submit", reportReq))
	require.True(t, report.Historical)
	require.Equal(t, x.ID, report.ExecutionID)
	notice := peer.next(t, "thread/inject_items")
	require.Equal(t, topicBinding.SessionID, notice.session)
	require.Equal(t, "context_supplement", decodeInternalBatch(t, notice, report.EventID)[0].Kind)
	notice.accept()
	waitWorkDelivery(t, p, report.EventID, "sent")
	require.Equal(t, next.Execution.ID, readRecord[core.Issue](t, p, "issues/"+issue.ID).CurrentExecutionID)
	waitWorkerObservation(t, p, next.Execution, "observing")
	nextInput.end("turn-" + nextInput.session)
	ended := waitWorkerObservation(t, p, next.Execution, "ended")
	reminder := checkReminderCall(t, peer, nextBinding, ended)
	reminder.answer <- nativeDeliveryAnswer{workID: "replacement-execution-reminder"}
	waitWorkDelivery(t, p, ended.ReminderEventID, "sent")
	require.Empty(t, readRecord[reminderExecution](t, p, "executions/"+x.ID).ReminderEventID)
	t.Log("Historical Report did not satisfy the replacement Execution's missing Report check.")

	// Cancel the Topic while it, and a Worker, have active native work.
	incoming := input("before-topic-cancel")
	incoming.Message.ThreadRef.ID, incoming.Message.Channel = "lifecycle-topic", nil
	activity := submit(t, p, f, incoming)
	activityInput := peer.next(t, "turn/start")
	decodeInternalBatch(t, activityInput, activity.EventID)
	activityInput.accept()
	waitRouteStatus(t, p, activity.EventID, "sent")
	sleepingTimer := mcpWork[core.Timer](t, topic, "timer_set", timerInput("before-topic-cancel", timerFuture))
	topicCancel := lifecycle("cancel-topic", 1)
	oldTopic := mcpWork[core.Topic](t, topic, "topic_cancel", topicCancel)
	mcpWorkError(t, topic, "topic_cancel", "stale_binding", topicCancel)
	require.Equal(t, "canceled", oldTopic.Status)
	require.Equal(t, "canceled", readRecord[core.Issue](t, p, "issues/"+issue.ID).Status)
	require.Equal(t, "canceled", readRecord[core.Issue](t, p, "issues/"+dependent.ID).Status)
	require.Equal(t, completed, readRecord[core.Issue](t, p, "issues/"+completed.ID))
	mcpWorkError(t, topic, "topic_reopen", "stale_binding", lifecycle("use-old-topic", oldTopic.Revision))
	require.Equal(t, "unknown", waitObservedStop(t, p, "topics", oldTopic.ID).WorkState)
	require.Equal(t, "unknown", waitObservedStop(t, p, "executions", next.Execution.ID).WorkState)
	follow := input("after-topic-cancel")
	follow.Message.ThreadRef.ID, follow.Message.Channel = "lifecycle-topic", nil
	queued := submit(t, p, f, follow)
	require.Equal(t, "pending", readRecord[observedRouteDelivery](t, p, "routing-deliveries/"+queued.EventID).Status)
	require.Equal(t, "pending", readRecord[observedRouteDelivery](t, p, "routing-deliveries/"+queued.EventID).Status)
	peer.quiet(t, 250*time.Millisecond)
	topicRecovery := recoveryFor("restore-topic", "settle", topicBinding.ID)
	topicRecovery.StopRuntime = true
	restored := recoverRoleBinding(t, p, operatorFile, topicRecovery)
	require.Len(t, restored.RelatedReleases, 1)
	require.Equal(t, nextBinding.ID, restored.RelatedReleases[0].BindingID)
	oldWorker := readRecord[core.Binding](t, p, "bindings/"+nextBinding.ID)
	require.Equal(t, "runtime_stopped", oldWorker.Close.Method)
	newTopic := waitBinding(t, p, "topic", oldTopic.ID, "ready")
	require.Equal(t, restored.ReplacementBinding, newTopic.ID)
	require.Equal(t, topicBinding.Generation+1, newTopic.Generation)
	require.NotEqual(t, topicBinding.SessionID, newTopic.SessionID)
	var foundFollowup, foundRecovery bool
	for range 4 {
		call := peer.next(t, "")
		require.Equal(t, newTopic.SessionID, call.session)
		for _, delivery := range decodeInternalBatch(t, call) {
			if delivery.ID == queued.EventID {
				require.Equal(t, "routed_message", delivery.Kind)
				foundFollowup = true
			}
		}
		foundRecovery = foundRecovery || strings.Contains(call.text, "binding_recovered")
		call.answer <- nativeDeliveryAnswer{complete: true}
		if foundFollowup && foundRecovery {
			break
		}
	}
	require.True(t, foundFollowup)
	require.True(t, foundRecovery)
	waitRouteStatus(t, p, queued.EventID, "sent")
	require.Equal(t, "canceled", readRecord[core.Topic](t, p, "topics/"+oldTopic.ID).Status)
	newOwner := startRoleMCP(t, f, newTopic)
	reopenedTopic := mcpWork[core.Topic](t, newOwner, "topic_reopen", lifecycle("reopen-topic", oldTopic.Revision))
	require.Equal(t, "active", reopenedTopic.Status)
	require.Equal(t, oldTopic.LifecycleGeneration+1, reopenedTopic.LifecycleGeneration)
	timerAfterReopen := mcpWork[timerRecord](t, newOwner, "work_query", core.QueryRequest{Collection: "timers", ID: sleepingTimer.ID})
	require.Equal(t, "invalidated", timerAfterReopen.Status)
	require.Empty(t, timerAfterReopen.TriggerMessageID)
	require.Equal(t, "canceled", readRecord[core.Issue](t, p, "issues/"+issue.ID).Status)
	require.Len(t, page(t, p, "executions").Items, 2)
	mcpWorkError(t, topic, "issue_create", "stale_binding", issueInput(f.dir, "old-delayed-command"))
	oldIssue := readRecord[core.Issue](t, p, "issues/"+issue.ID)
	openedIssue := mcpWork[core.WorkReceipt](t, newOwner, "issue_reopen", issueLifecycle("reopen-after-topic", oldIssue))
	reopenNotice := peer.next(t, "thread/inject_items")
	reopenNotice.accept()
	require.Equal(t, newTopic.SessionID, reopenNotice.session)
	third := mcpWork[core.WorkReceipt](t, newOwner, "issue_start", startRequest("start-after-topic", openedIssue.Issue))
	require.Equal(t, next.Execution.Generation+1, third.Execution.Generation)
	thirdBinding := waitBinding(t, p, "worker", third.Execution.ID, "ready")
	thirdInput := peer.next(t, "turn/start")
	thirdInput.accept()
	require.Equal(t, thirdBinding.SessionID, thirdInput.session)
	decodeInternalBatch(t, thirdInput, third.Execution.StartEventID)
	waitWorkDelivery(t, p, third.Execution.StartEventID, "sent")
	for _, path := range []string{"/v1/observer/topics/" + oldTopic.ID, "/v1/observer/bindings/" + newTopic.ID} {
		status, _ = request(t, http.MethodPost, p.endpoint, path, string(opToken), nil)
		require.Equal(t, http.StatusMethodNotAllowed, status)
	}
	t.Log("An in-flight send stayed superseded; no second authority before verified release. Topic recovery accounted for its retired Worker and allowed a later explicitly started generation.")
}

func TestLifecycleClaudeReusesClosedSessionWithFreshMCP(t *testing.T) {
	massd, _ := deliveryBinaries(t)
	temp := t.TempDir()
	native, config := filepath.Join(temp, "native"), filepath.Join(temp, "claude")
	require.NoError(t, os.Mkdir(native, 0o700))
	peer := filepath.Join(temp, "claude-wire-peer")
	cmd := exec.CommandContext(t.Context(), "go", "build", "-o", peer, "./tests/integration/testdata/ticket14-recovery-peer")
	cmd.Dir = "../../../mass-go"
	cmd.Env = append(os.Environ(), "GOWORK=off", "GOPROXY=off", "GOSUMDB=off")
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "%s", out)
	launcher := filepath.Join(temp, "claude-with-mcp")
	cmd = exec.CommandContext(t.Context(), "go", "build", "-o", launcher, "./pkg/core/testdata/claude-mcp-peer")
	cmd.Dir = "../.."
	cmd.Env = append(os.Environ(), "GOWORK=off", "GOPROXY=off", "GOSUMDB=off")
	out, err = cmd.CombinedOutput()
	require.NoError(t, err, "%s", out)
	workspace, err := filepath.EvalSymlinks(temp)
	require.NoError(t, err)
	project := filepath.Join(config, "projects", regexp.MustCompile(`[^a-zA-Z0-9]`).ReplaceAllString(workspace, "-"))
	require.NoError(t, os.MkdirAll(project, 0o700))
	endpoint, c := startNativeMassd(t, massd, resource.RuntimeSpec{
		Type: "claude",
		Config: runtime.Config{"executable": launcher, "envPolicy": "replace", "env": map[string]any{
			"PEER_KIND": "claude", "PEER_DIR": native, "WORK_DIR": workspace, "CLAUDE_CONFIG_DIR": config, "SAVED_TRANSCRIPT_DIR": project,
			"CLAUDE_WIRE_PEER": peer,
		}},
	})
	f, operatorFile := deliveryFixture(t, endpoint)
	p := f.start()
	router := startRoleMCP(t, f, waitBinding(t, p, "router", "", "ready"))
	source := submit(t, p, f, input("claude-reuse"))
	waitRouteStatus(t, p, source.EventID, "sent")
	decision := mcpWork[core.RouteDecision](t, router, "route_decide", newRoute("claude-reuse", []string{source.MessageID}, workspace))
	topic := startRoleMCP(t, f, waitBinding(t, p, "topic", decision.Targets[0].TopicID, "ready"))
	waitDelivered(t, p, source.MessageID, "topic", decision.Targets[0].TopicID)
	create := issueInput(workspace, "claude-worker")
	create.Start = true
	work := mcpWork[core.WorkReceipt](t, topic, "issue_create", create)
	old := waitBinding(t, p, "worker", work.Execution.ID, "ready")
	oldProcess := startRoleMCP(t, f, old)
	oldCredential := roleToken(t, f, old)
	waitWorkDelivery(t, p, work.Execution.StartEventID, "sent")
	delivered := waitWorkerObservation(t, p, work.Execution, "delivered")
	require.Equal(t, "claude", delivered.WorkObservation.Provider)
	require.Empty(t, delivered.WorkObservation.WorkID)
	require.Nil(t, delivered.WorkObservation.End)
	require.False(t, delivered.WorkObservation.Continuous)
	require.Equal(t, "end_unconfirmed", delivered.ReminderEligibility)
	require.False(t, delivered.ReminderScheduled)
	readTrace := func() string {
		raw, _ := os.ReadFile(filepath.Join(native, "wire.jsonl"))
		return string(raw)
	}
	var nativePID int
	require.Eventually(t, func() bool {
		for line := range strings.SplitSeq(readTrace(), "\n") {
			var row struct {
				Method string
				PID    int
				Params struct{ SessionID string }
			}
			if json.Unmarshal([]byte(line), &row) == nil && row.Method == "input" && row.Params.SessionID == old.SessionID {
				nativePID = row.PID
				return nativePID > 0
			}
		}
		return false
	}, 5*time.Second, 25*time.Millisecond)
	require.NoError(t, syscall.Kill(nativePID, 0))
	var nativeMCP int
	findMCP := func(want string) bool {
		raw, err := os.ReadFile(filepath.Join(native, "mcp.jsonl"))
		if err != nil {
			return false
		}
		for line := range strings.SplitSeq(string(raw), "\n") {
			var row struct {
				Method    string
				NativePID int
				MCPPID    int
			}
			if json.Unmarshal([]byte(line), &row) == nil && row.NativePID == nativePID && row.Method == want {
				nativeMCP = row.MCPPID
				return true
			}
		}
		return false
	}
	require.True(t, findMCP("opened"))
	require.NoError(t, syscall.Kill(nativeMCP, 0))
	canceled := mcpWork[core.WorkReceipt](t, topic, "issue_cancel", issueLifecycle("claude-cancel", work.Issue))
	_ = waitObservedStop(t, p, "executions", work.Execution.ID)
	req := recoveryFor("close-claude-session", "settle", old.ID)
	closed := recoverRoleBinding(t, p, operatorFile, req)
	require.Equal(t, "native_session_closed", closed.Close.Method)
	require.True(t, closed.Close.Reusable)
	require.ErrorIs(t, syscall.Kill(nativePID, 0), syscall.ESRCH)
	require.True(t, findMCP("released"))
	require.ErrorIs(t, syscall.Kill(nativeMCP, 0), syscall.ESRCH)
	var released resource.Session
	require.NoError(t, c.GetSession(t.Context(), resource.SessionRef{SessionID: old.SessionID}, &released))
	require.Equal(t, "offline", string(released.Status.State))
	reopened := mcpWork[core.WorkReceipt](t, topic, "issue_reopen", issueLifecycle("claude-reopen", canceled.Issue))
	next := mcpWork[core.WorkReceipt](t, topic, "issue_start", startRequest("claude-start-new-generation", reopened.Issue))
	require.Equal(t, "mass-t1-i1-e1", work.Execution.ID)
	require.Equal(t, "mass-t1-i1-e2", next.Execution.ID)
	replayed := mcpWork[core.WorkReceipt](t, topic, "issue_create", create)
	require.Equal(t, work.Issue.ID, replayed.Issue.ID)
	require.Equal(t, next.Execution.ID, replayed.Issue.CurrentExecutionID)
	require.Equal(t, work.Execution.ID, replayed.Execution.ID)
	fresh := waitBinding(t, p, "worker", next.Execution.ID, "ready")
	require.Equal(t, old.SessionID, fresh.SessionID)
	require.Equal(t, old.SessionUID, fresh.SessionUID)
	require.Equal(t, old.ID, fresh.CapabilityFileID)
	require.Equal(t, old.Generation+1, fresh.Generation)
	require.NotEqual(t, oldCredential, roleToken(t, f, fresh))
	current := startRoleMCP(t, f, fresh)
	require.Equal(t, next.Execution.ID, mcpWork[core.WorkReceipt](t, current, "issue_get_current", struct{}{}).Execution.ID)
	mcpWorkError(t, oldProcess, "issue_get_current", "stale_binding", struct{}{})
	reportRequest := core.ReportRequest{
		OperationID: "claude-report", Kind: "progress", Summary: "Execution facts", GoalRevision: work.Execution.Revision,
	}
	historicalReport := mcpWork[core.Report](t, oldProcess, "report_submit", reportRequest)
	require.Equal(t, "mass-t1-i1-r1", historicalReport.ID)
	require.Equal(t, work.Execution.ID, historicalReport.ExecutionID)
	require.True(t, historicalReport.Historical)
	reportRequest.GoalRevision = next.Execution.Revision
	currentReport := mcpWork[core.Report](t, current, "report_submit", reportRequest)
	require.Equal(t, "mass-t1-i1-r2", currentReport.ID)
	require.Equal(t, next.Execution.ID, currentReport.ExecutionID)
	require.False(t, currentReport.Historical)
	require.Equal(t, currentReport, mcpWork[core.Report](t, current, "report_submit", reportRequest))
	reportRequest.GoalRevision = work.Execution.Revision
	require.Equal(t, historicalReport, mcpWork[core.Report](t, oldProcess, "report_submit", reportRequest))
	waitWorkDelivery(t, p, next.Execution.StartEventID, "sent")
	recanceled := mcpWork[core.WorkReceipt](t, topic, "issue_cancel", issueLifecycle("cancel-resumed", next.Issue))
	waitObservedStop(t, p, "executions", next.Execution.ID)
	settled := recoverRoleBinding(t, p, operatorFile, recoveryFor("settle-resumed", "settle", fresh.ID))
	require.True(t, settled.Close.Reusable)
	reopened = mcpWork[core.WorkReceipt](t, topic, "issue_reopen", issueLifecycle("open-for-review", recanceled.Issue))
	reselect := startRequest("start-review", reopened.Issue)
	reselect.RuntimeRef, reselect.Model = new("agent"), new("review-model")
	review := mcpWork[core.WorkReceipt](t, topic, "issue_start", reselect)
	reviewBinding := waitBinding(t, p, "worker", review.Execution.ID, "ready")
	require.NotEqual(t, fresh.SessionID, reviewBinding.SessionID)
	require.NotEqual(t, fresh.SessionUID, reviewBinding.SessionUID)
	require.Empty(t, reviewBinding.CapabilityFileID)
	require.Equal(t, reviewModel, reviewBinding.ExecutionProfile)
	require.Equal(t, reviewModel, review.Issue.ExecutionProfile)
	var reviewSession resource.Session
	require.NoError(t, c.GetSession(t.Context(), resource.SessionRef{SessionID: reviewBinding.SessionID}, &reviewSession))
	require.Equal(t, &runtime.InitialSessionConfig{Model: reviewModel.Model}, reviewSession.Spec.Config)
	historical := mcpWork[struct{ Preparation core.Binding }](t, topic, "work_query", core.QueryRequest{
		Collection: "executions", ID: next.Execution.ID,
	})
	require.Equal(t, fresh.SessionID, historical.Preparation.SessionID)
	require.Equal(t, workerModel, historical.Preparation.ExecutionProfile)
	t.Logf("Claude native PID %d and its MCP PID %d released before offline readback. Matching parameters resumed the same Session with a fresh token, and a new profile created a separate Session.", nativePID, nativeMCP)
}

func TestLifecycleDependencyCrashConsistency(t *testing.T) {
	for _, stage := range []string{"dependency_before_commit", "lifecycle_after_commit"} {
		t.Run(stage, func(t *testing.T) {
			boundary := newRouteBoundary(t)
			f := fixtureAt(t)
			f.execution(boundary)
			p := f.start()
			binding, token := workBoundaryTopic(t, f, p)
			topic := startRoleMCP(t, f, binding)
			target := mcpWork[core.WorkReceipt](t, topic, "issue_create", issueInput(f.dir, "target")).Issue
			dependent := mcpWork[core.WorkReceipt](t, topic, "issue_create", issueInput(f.dir, "dependent")).Issue
			dependent = blockers(t, topic, dependent, "add-dependency", dependency(target))
			p.stop(t, false)
			gate := t.TempDir()
			p = f.start("SUPERAGENT_TEST_GATE_STAGE="+stage, "SUPERAGENT_TEST_GATE_DIR="+gate)
			cancel := issueLifecycle("crash-cancel", target)
			raw, err := json.Marshal(cancel)
			require.NoError(t, err)
			req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, p.endpoint+"/v1/role/issue_cancel", bytes.NewReader(raw))
			require.NoError(t, err)
			req.Header.Set("Authorization", "Bearer "+token)
			done := make(chan error, 1)
			go func() {
				response, err := http.DefaultClient.Do(req)
				if err == nil {
					_, err = io.Copy(io.Discard, response.Body)
					_ = response.Body.Close()
				}
				done <- err
			}()
			waitWorkGate(t, gate, stage)
			p.stop(t, true)
			require.Error(t, <-done)
			p = f.start()
			state := readRecord[core.Issue](t, p, "issues/"+target.ID)
			events := dependencyDeliveries(t, f)
			if stage == "dependency_before_commit" {
				require.Empty(t, events)
				require.Equal(t, target, state)
			} else {
				require.Len(t, events, 1)
				require.Equal(t, "canceled", state.Status)
			}
			result := state
			if stage == "dependency_before_commit" {
				result = mcpWork[core.WorkReceipt](t, topic, "issue_cancel", cancel).Issue
			}
			mcpWorkError(t, topic, "issue_cancel", "conflict", cancel)
			require.Equal(t, "canceled", result.Status)
			open := issueLifecycle("crash-cancel-reopen", result)
			reopened := mcpWork[core.WorkReceipt](t, topic, "issue_reopen", open)
			mcpWorkError(t, topic, "issue_reopen", "conflict", open)
			require.Equal(t, "pending", reopened.Issue.Status)
			require.Len(t, dependencyDeliveries(t, f), 2)
			require.Equal(t, dependent, readRecord[core.Issue](t, p, "issues/"+dependent.ID))
			require.Empty(t, page(t, p, "executions").Items)
			t.Logf("%s: restart saw %d committed dependency facts; cancel/reopen replay left two facts and no downstream changes", stage, len(events))
		})
	}
}
