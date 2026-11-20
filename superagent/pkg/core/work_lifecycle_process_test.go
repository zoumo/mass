package core_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/zoumo/agent-sdk-go/agent"
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

func TestWorkerSessionRecoveryThroughMassd(t *testing.T) {
	peer := newNativeDeliveryPeer(t)
	endpoint, massd, data := startDeliveryMassdWithData(t, peer)
	f, _ := deliveryFixture(t, endpoint)
	p := f.start()
	routerBinding := waitBinding(t, p, "router", "", "ready")
	router := startRoleMCP(t, f, routerBinding)
	topicBinding, source := nativeWorkTopic(t, f, p, peer, router, "worker-recovery")
	topic := startRoleMCP(t, f, topicBinding)
	create := issueInput(f.dir, "recoverable-worker")
	create.Start = true
	create.Materials = []core.Material{{Label: "Recovery request", MessageID: source.MessageID}}
	work := mcpWork[core.WorkReceipt](t, topic, "issue_create", create)
	old := waitBinding(t, p, "worker", work.Execution.ID, "ready")
	worker := startRoleMCP(t, f, old)
	first := peer.next(t, "turn/start")
	decodeInternalBatch(t, first, work.Execution.StartEventID)
	first.accept()
	sent := waitWorkDelivery(t, p, work.Execution.StartEventID, "sent")
	history := mcpWork[core.Report](t, worker, "report_submit", core.ReportRequest{
		OperationID: "retain-progress", Kind: "progress", Summary: "Output from the original Worker", GoalRevision: work.Execution.Revision,
	})
	report := peer.next(t, "turn/start")
	decodeInternalBatch(t, report, history.EventID)
	report.answer <- nativeDeliveryAnswer{complete: true}
	waitWorkDelivery(t, p, history.EventID, "sent")
	issue := mcpWork[core.WorkReceipt](t, worker, "issue_get_current", struct{}{}).Issue
	ref := resource.SessionRef{SessionID: old.SessionID}
	var original resource.Session
	require.NoError(t, massd.GetSession(t.Context(), ref, &original))
	var closed resource.Session
	require.NoError(t, massd.CloseSession(t.Context(), ref, &closed))
	require.Equal(t, resource.SessionReasonOwnerReleased, closed.Status.Reason)
	gate := &nativeOpenGate{entered: make(chan struct{}), release: make(chan struct{})}
	peer.resumeGates.Store(old.SessionID, gate)
	continued := mcpWork[core.WorkReceipt](t, topic, "issue_update_requirements", core.UpdateRequirementsRequest{
		OperationID: "resume-worker", IssueID: issue.ID, ExpectedRevision: issue.Revision,
		Goal: "Resume the same Worker using the saved workspace.", NotifyWorker: true,
	})
	select {
	case <-gate.entered:
	case <-time.After(8 * time.Second):
		t.Fatal("Core did not resume the original Session")
	}
	var opening resource.Session
	require.NoError(t, massd.GetSession(t.Context(), ref, &opening))
	require.Equal(t, "opening", string(opening.Status.State))
	unrelated := submit(t, p, f, input("unrelated-during-resume"))
	routerInput := peer.next(t, "turn/start")
	require.Equal(t, routerBinding.SessionID, routerInput.session)
	decodeInternalBatch(t, routerInput, unrelated.EventID)
	routerInput.accept()
	waitRouteStatus(t, p, unrelated.EventID, "sent")
	require.Eventually(t, func() bool {
		binding := readRecord[core.Binding](t, p, "bindings/"+old.ID)
		return binding.State == "pending" && strings.HasPrefix(binding.LastError, "session_prepare_")
	}, 10*time.Second, 25*time.Millisecond, "Core should record its resume timeout before the late response")
	require.Len(t, page(t, p, "executions").Items, 1)
	close(gate.release)
	update := peer.next(t, "turn/start")
	require.Equal(t, original.Status.SessionID, update.session)
	decodeInternalBatch(t, update, continued.NotificationEventID)
	update.accept()
	waitWorkDelivery(t, p, continued.NotificationEventID, "sent")
	resumed := waitBinding(t, p, "worker", work.Execution.ID, "ready")
	require.Equal(t, old.SessionID, resumed.SessionID)
	require.Equal(t, old.SessionUID, resumed.SessionUID)
	var applied resource.Session
	require.NoError(t, massd.GetSession(t.Context(), ref, &applied))
	require.Equal(t, original.Spec, applied.Spec)
	require.Equal(t, sent, waitWorkDelivery(t, p, work.Execution.StartEventID, "sent"))

	require.NoError(t, massd.CloseSession(t.Context(), ref, &closed))
	vault := filepath.Join(data, "private", "codex", fmt.Sprintf("%x", sha256.Sum256([]byte("agent"))))
	require.NoError(t, os.Remove(filepath.Join(vault, fmt.Sprintf("%x.json", sha256.Sum256([]byte(old.SessionID))))))
	newSession := &nativeOpenGate{entered: make(chan struct{}), release: make(chan struct{})}
	peer.nextStartGate.Store(newSession)
	nextGoal := mcpWork[core.WorkReceipt](t, topic, "issue_update_requirements", core.UpdateRequirementsRequest{
		OperationID: "replace-unrecoverable", IssueID: issue.ID, ExpectedRevision: continued.Issue.Revision,
		Goal: "Carry on with the new goal and the original Report.", NotifyWorker: true,
	})
	select {
	case <-newSession.entered:
	case <-time.After(8 * time.Second):
		t.Fatal("Core did not start the replacement Session")
	}
	p.stop(t, true)
	close(newSession.release)
	p = f.start()
	started := peer.next(t, "turn/start")
	events := decodeInternalBatch(t, started)
	require.Len(t, events, 1)
	require.Equal(t, "worker_started", events[0].Kind)
	next := readRecord[core.Issue](t, p, "issues/"+issue.ID).CurrentExecutionID
	require.NotEqual(t, work.Execution.ID, next)
	successor := waitBinding(t, p, "worker", next, "ready")
	require.Equal(t, started.session, successor.SessionID)
	require.NotEqual(t, old.SessionID, successor.SessionID)
	require.NotEqual(t, old.SessionUID, successor.SessionUID)
	require.Equal(t, old.Workspace, successor.Workspace)
	assertSessionProfile(t, massd, peer, successor, workerModel)
	started.accept()
	waitWorkDelivery(t, p, events[0].ID, "sent")
	waitWorkDelivery(t, p, nextGoal.NotificationEventID, "superseded")
	current := startRoleMCP(t, f, successor)
	continuation := mcpWork[core.WorkReceipt](t, current, "issue_get_current", struct{}{})
	require.Equal(t, next, continuation.Execution.ID)
	require.Equal(t, issue.ID, continuation.Issue.ID)
	require.Equal(t, nextGoal.Issue.Requirements, continuation.Issue.Requirements)
	require.Equal(t, nextGoal.Issue.RequirementsRevision, continuation.Issue.RequirementsRevision)
	require.Equal(t, create.Materials, continuation.Issue.Materials)
	require.Equal(t, issue.Blockers, continuation.Issue.Blockers)
	require.Equal(t, []core.Report{history}, continuation.Issue.Reports)
	require.Equal(t, "historical", readRecord[core.Execution](t, p, "executions/"+work.Execution.ID).Authority)
	mcpWorkError(t, worker, "issue_get_current", "stale_binding", struct{}{})
	require.Len(t, page(t, p, "executions").Items, 2)
	require.EqualValues(t, 4, peer.threadIDs.Load())
	require.NoError(t, massd.GetSession(t.Context(), ref, &closed))
	require.Equal(t, resource.SessionReasonRecoveryMissing, closed.Status.Reason)
	require.Equal(t, sent, waitWorkDelivery(t, p, work.Execution.StartEventID, "sent"))
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
	require.Equal(t, "worker_started", decodeInternalBatch(t, nextInput, next.Execution.StartEventID)[0].Kind)
	current := roleWork[core.WorkReceipt](t, p, roleToken(t, f, nextBinding), "issue_get_current", struct{}{})
	require.Equal(t, next.Execution.ID, current.Execution.ID)
	require.Equal(t, amended.Issue.Requirements, current.Issue.Requirements)
	nextInput.accept()
	waitWorkDelivery(t, p, next.Execution.StartEventID, "sent")
	dispatches, _ := conversationPage(t, p, url.Values{
		"topicId": {topicBinding.OwnerID}, "filter": {"worker_dispatch"},
	})
	require.Len(t, dispatches, 2)
	for i, execution := range []*core.Execution{next.Execution, x} {
		require.Equal(t, "worker_dispatch/"+execution.StartEventID, dispatches[i].ID)
		require.Equal(t, "worker_started", dispatches[i].DispatchKind)
		require.Equal(t, issue.ID, dispatches[i].IssueID)
		require.Equal(t, execution.ID, dispatches[i].ExecutionID)
		require.Equal(t, execution.Generation, dispatches[i].Generation)
	}
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
	endpoint, c, _ := startNativeMassd(t, massd, resource.RuntimeSpec{
		Type:     "claude",
		Features: &runtime.SessionFeatures{AgentQuestions: new(true)},
		Config: runtime.Config{"executable": launcher, "envPolicy": "replace", "env": map[string]any{
			"PEER_KIND": "claude", "PEER_DIR": native, "WORK_DIR": workspace, "CLAUDE_CONFIG_DIR": config, "SAVED_TRANSCRIPT_DIR": project,
			"CLAUDE_WIRE_PEER": peer, "PEER_CLAUDE_FULL_ACCESS": "1",
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
	observed := waitWorkerObservation(t, p, work.Execution, "ended")
	require.Equal(t, "claude", observed.WorkObservation.Provider)
	require.Empty(t, observed.WorkObservation.WorkID)
	require.Equal(t, "idle", observed.WorkObservation.End.Reason)
	require.True(t, observed.WorkObservation.Continuous)
	require.Equal(t, "already_scheduled", observed.ReminderEligibility)
	require.True(t, observed.ReminderScheduled)
	waitWorkDelivery(t, p, observed.ReminderEventID, "sent")
	require.Len(t, workDeliveries(t, p, reportReminder), 1)
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
				Method            string
				NativePID         int
				MCPPID            int
				QuestionsDisabled bool
			}
			if json.Unmarshal([]byte(line), &row) == nil && row.NativePID == nativePID && row.Method == want {
				if want == "opened" {
					require.True(t, row.QuestionsDisabled, "Claude launch must disallow AskUserQuestion")
				}
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
	require.Contains(t, readTrace(), `"method":"full_access","params":{"resumed":false,"sessionId":"`+old.SessionID+`"}`)
	require.Contains(t, readTrace(), `"method":"full_access","params":{"resumed":true,"sessionId":"`+old.SessionID+`"}`)
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
	require.Equal(t, &runtime.InitialSessionConfig{Model: reviewModel.Model, PermissionPreset: agent.PermissionPresetFullAccess}, reviewSession.Spec.Config)
	require.Equal(t, &runtime.SessionFeatures{AgentQuestions: new(false)}, reviewSession.Spec.Features)
	require.Equal(t, &runtime.SessionFeatures{AgentQuestions: new(false)}, reviewSession.Status.EffectiveFeatures)
	historical := mcpWork[struct{ Preparation core.Binding }](t, topic, "work_query", core.QueryRequest{
		Collection: "executions", ID: next.Execution.ID,
	})
	require.Equal(t, fresh.SessionID, historical.Preparation.SessionID)
	require.Equal(t, workerModel, historical.Preparation.ExecutionProfile)
	t.Logf("Claude native PID %d and its MCP PID %d released before offline readback. Matching parameters resumed the same Session with a fresh token, and a new profile created a separate Session.", nativePID, nativeMCP)

	reviewStart := waitWorkDelivery(t, p, review.Execution.StartEventID, "sent")
	reviewRef := resource.SessionRef{SessionID: reviewBinding.SessionID}
	require.NoError(t, c.CloseSession(t.Context(), reviewRef, &released))
	require.Equal(t, resource.SessionReasonOwnerReleased, released.Status.Reason)
	reviewUpdate := mcpWork[core.WorkReceipt](t, topic, "issue_update_requirements", core.UpdateRequirementsRequest{
		OperationID: "claude-normal-resume", IssueID: review.Issue.ID, ExpectedRevision: review.Issue.Revision,
		Goal: "Continue reviewing after closing the Session.", NotifyWorker: true,
	})
	waitWorkDelivery(t, p, reviewUpdate.NotificationEventID, "sent")
	reattached := waitBinding(t, p, "worker", review.Execution.ID, "ready")
	require.Equal(t, reviewBinding.SessionID, reattached.SessionID)
	require.Equal(t, reviewBinding.SessionUID, reattached.SessionUID)
	require.Contains(t, readTrace(), `"method":"full_access","params":{"resumed":true,"sessionId":"`+reattached.SessionID+`"}`)
	require.Equal(t, reviewStart, waitWorkDelivery(t, p, review.Execution.StartEventID, "sent"))

	require.NoError(t, c.CloseSession(t.Context(), reviewRef, &released))
	require.NoError(t, os.Remove(filepath.Join(project, reviewBinding.SessionID+".jsonl")))
	lostReview := mcpWork[core.WorkReceipt](t, topic, "issue_update_requirements", core.UpdateRequirementsRequest{
		OperationID: "claude-replace-missing-transcript", IssueID: review.Issue.ID, ExpectedRevision: reviewUpdate.Issue.Revision,
		Goal: "Continue the review with the latest goal.", NotifyWorker: true,
	})
	var successor core.Issue
	require.Eventually(t, func() bool {
		successor = readRecord[core.Issue](t, p, "issues/"+review.Issue.ID)
		return successor.CurrentExecutionID != "" && successor.CurrentExecutionID != review.Execution.ID
	}, 8*time.Second, 25*time.Millisecond)
	replacement := waitBinding(t, p, "worker", successor.CurrentExecutionID, "ready")
	require.NotEqual(t, reattached.SessionID, replacement.SessionID)
	require.NotEqual(t, reattached.SessionUID, replacement.SessionUID)
	require.Equal(t, lostReview.Issue.Requirements, successor.Requirements)
	require.Equal(t, reviewUpdate.Issue.Reports, successor.Reports)
	require.NoError(t, c.GetSession(t.Context(), reviewRef, &released))
	require.Equal(t, resource.SessionReasonRecoveryMissing, released.Status.Reason)
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
