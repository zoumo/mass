package core_test

import (
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
	"github.com/zoumo/mass/mass-go/pkg/client"
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

func inspectRoleBinding(t *testing.T, p *process, operatorFile, id string) core.SessionBindingInspection {
	t.Helper()
	cmd := exec.CommandContext(t.Context(), build(t), "inspect-session-binding", id, "--endpoint", p.endpoint, "--operator-token-file", operatorFile)
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "%s", out)
	var inspection core.SessionBindingInspection
	require.NoError(t, json.Unmarshal(out, &inspection))
	return inspection
}

func recoverRoleBinding(t *testing.T, p *process, operatorFile string, req core.RecoveryRequest) core.Recovery {
	t.Helper()
	args := []string{
		"recover-input", req.SessionBindingID, "--endpoint", p.endpoint, "--operator-token-file", operatorFile,
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
		OperationID: op, SessionBindingID: bindingID, Mode: mode,
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

func TestIssueSessionRecoveryThroughMassd(t *testing.T) {
	peer := newNativeSubmitPeer(t)
	endpoint, massd, data := startEventTestMassdWithData(t, peer)
	f, _ := eventFixture(t, endpoint)
	p := f.start()
	routerBinding := waitBinding(t, p, "router", "", "ready")
	router := startRoleMCP(t, f, routerBinding)
	topicBinding, source := nativeWorkTopic(t, f, p, peer, router, "issue-recovery")
	topic := startRoleMCP(t, f, topicBinding)
	create := issueInput(f.dir, "recoverable-issue")
	create.Start = true
	create.Materials = []core.Material{{Label: "Recovery request", MessageID: source.MessageID}}
	work := mcpWork[core.WorkReceipt](t, topic, "create_issue", create)
	old := waitBinding(t, p, "issue", work.SessionBinding.ID, "ready")
	issueMCP := startRoleMCP(t, f, old)
	first := peer.next(t, "turn/start")
	decodeInternalBatch(t, first, work.SessionBinding.StartEventID)
	first.accept()
	sent := waitWorkEvent(t, p, work.SessionBinding.StartEventID, "sent")
	history := mcpWork[core.Report](t, issueMCP, "submit_report", core.ReportRequest{
		OperationID: "retain-progress", Kind: "progress", Summary: "Output from the original Issue Session", GoalRevision: work.SessionBinding.StartGoalRevision,
	})
	report := peer.next(t, "turn/start")
	decodeInternalBatch(t, report, history.EventID)
	report.answer <- nativeSubmitAnswer{complete: true}
	waitWorkEvent(t, p, history.EventID, "sent")
	issue := mcpWork[core.WorkReceipt](t, issueMCP, "get_issue", struct{}{}).Issue
	ref := resource.SessionRef{SessionID: old.SessionID}
	var original resource.Session
	require.NoError(t, massd.GetSession(t.Context(), ref, &original))
	var closed resource.Session
	require.NoError(t, massd.CloseSession(t.Context(), ref, &closed))
	require.Equal(t, resource.SessionReasonOwnerReleased, closed.Status.Reason)
	gate := &nativeOpenGate{entered: make(chan struct{}), release: make(chan struct{})}
	peer.resumeGates.Store(old.SessionID, gate)
	continued := mcpWork[core.WorkReceipt](t, topic, "issue_update_requirements", core.UpdateRequirementsRequest{
		OperationID: "resume-issue", IssueID: issue.ID, ExpectedRevision: issue.Revision,
		Goal: "Resume the same Issue Session using the saved workspace.", NotifyIssueSession: true,
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
		binding := readRecord[core.SessionBinding](t, p, "session-bindings/"+old.ID)
		return binding.State == "pending" && strings.HasPrefix(binding.LastError, "session_prepare_")
	}, 10*time.Second, 25*time.Millisecond, "Core should record its resume timeout before the late response")
	require.Len(t, issueBindings(t, p), 1)
	close(gate.release)
	update := peer.next(t, "turn/start")
	require.Equal(t, original.Status.SessionID, update.session)
	decodeInternalBatch(t, update, continued.NotificationEventID)
	update.accept()
	waitWorkEvent(t, p, continued.NotificationEventID, "sent")
	resumed := waitBinding(t, p, "issue", work.SessionBinding.ID, "ready")
	require.Equal(t, old.SessionID, resumed.SessionID)
	require.Equal(t, old.SessionUID, resumed.SessionUID)
	var applied resource.Session
	require.NoError(t, massd.GetSession(t.Context(), ref, &applied))
	require.Equal(t, original.Spec, applied.Spec)
	require.Equal(t, sent, waitWorkEvent(t, p, work.SessionBinding.StartEventID, "sent"))

	require.NoError(t, massd.CloseSession(t.Context(), ref, &closed))
	vault := filepath.Join(data, "private", "codex", fmt.Sprintf("%x", sha256.Sum256([]byte("agent"))))
	require.NoError(t, os.Remove(filepath.Join(vault, fmt.Sprintf("%x.json", sha256.Sum256([]byte(old.SessionID))))))
	newSession := &nativeOpenGate{entered: make(chan struct{}), release: make(chan struct{})}
	peer.nextStartGate.Store(newSession)
	nextGoal := mcpWork[core.WorkReceipt](t, topic, "issue_update_requirements", core.UpdateRequirementsRequest{
		OperationID: "replace-unrecoverable", IssueID: issue.ID, ExpectedRevision: continued.Issue.Revision,
		Goal: "Carry on with the new goal and the original Report.", NotifyIssueSession: true,
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
	require.Equal(t, "issue_session_started", events[0].Kind)
	next := readRecord[core.Issue](t, p, "issues/"+issue.ID).CurrentSessionBindingID
	require.NotEqual(t, work.SessionBinding.ID, next)
	successor := waitBinding(t, p, "issue", next, "ready")
	require.Equal(t, started.session, successor.SessionID)
	require.NotEqual(t, old.SessionID, successor.SessionID)
	require.NotEqual(t, old.SessionUID, successor.SessionUID)
	require.Equal(t, old.Workspace, successor.Workspace)
	assertSessionProfile(t, massd, peer, successor, issueModel)
	started.accept()
	waitWorkEvent(t, p, events[0].ID, "sent")
	waitWorkEvent(t, p, nextGoal.NotificationEventID, "superseded")
	current := startRoleMCP(t, f, successor)
	continuation := mcpWork[core.WorkReceipt](t, current, "get_issue", struct{}{})
	require.Equal(t, next, continuation.Issue.CurrentSessionBindingID)
	require.Equal(t, issue.ID, continuation.Issue.ID)
	require.Equal(t, nextGoal.Issue.Requirements, continuation.Issue.Requirements)
	require.Equal(t, nextGoal.Issue.RequirementsRevision, continuation.Issue.RequirementsRevision)
	require.Equal(t, create.Materials, continuation.Issue.Materials)
	require.Equal(t, issue.Blockers, continuation.Issue.Blockers)
	require.Equal(t, history, readRecord[core.Report](t, p, "reports/"+history.ID))
	require.Equal(t, "revoked", readRecord[core.SessionBinding](t, p, "session-bindings/"+work.SessionBinding.ID).State)
	mcpWorkError(t, issueMCP, "get_issue", "stale_binding", struct{}{})
	require.Len(t, issueBindings(t, p), 2)
	require.EqualValues(t, 4, peer.threadIDs.Load())
	require.NoError(t, massd.GetSession(t.Context(), ref, &closed))
	require.Equal(t, resource.SessionReasonRecoveryMissing, closed.Status.Reason)
	require.Equal(t, sent, waitWorkEvent(t, p, work.SessionBinding.StartEventID, "sent"))
}

func TestLifecycleNativeCancelReopenAndReplace(t *testing.T) {
	peer := newNativeSubmitPeer(t)
	endpoint, massd := startEventTestMassd(t, peer)
	f, operatorFile := eventFixture(t, endpoint)
	p := f.start("SUPERAGENT_TEST_EVENTS=" + testProducerToken)
	router := startRoleMCP(t, f, waitBinding(t, p, "router", "", "ready"))
	topicBinding, source := nativeWorkTopic(t, f, p, peer, router, "lifecycle-topic")
	topic := startRoleMCP(t, f, topicBinding)
	issue := mcpWork[core.WorkReceipt](t, topic, "create_issue", issueInput(f.dir, "lifecycle-issue")).Issue
	dependent := mcpWork[core.WorkReceipt](t, topic, "create_issue", issueInput(f.dir, "lifecycle-dependency")).Issue
	dependent = blockers(t, topic, dependent, "set-dependency", dependency(issue))
	completed := mcpWork[core.WorkReceipt](t, topic, "create_issue", issueInput(f.dir, "existing-result")).Issue
	completed = mcpWork[core.WorkReceipt](t, topic, "issue_complete", core.CompleteIssueRequest{
		CompleteRequest: acceptance("accept-existing", completed.Revision, "retained.txt"), IssueID: completed.ID,
	}).Issue

	start := startRequest("first-start", issue)
	work := mcpWork[core.WorkReceipt](t, topic, "start_issue", start)
	x := work.SessionBinding
	issueBinding := waitBinding(t, p, "issue", x.ID, "ready")
	issueMCP := startRoleMCP(t, f, issueBinding)
	issueToken := roleToken(t, f, issueBinding)
	inFlight := peer.next(t, "turn/start")
	defer func() {
		select {
		case inFlight.answer <- nativeSubmitAnswer{}:
		default:
		}
	}()
	require.Equal(t, issueBinding.SessionID, inFlight.session)
	decodeInternalBatch(t, inFlight, x.StartEventID)
	cancel := issueLifecycle("cancel-in-flight", work.Issue)
	canceled := mcpWork[core.WorkReceipt](t, topic, "issue_cancel", cancel)
	mcpWorkError(t, topic, "issue_cancel", "conflict", cancel)
	require.Equal(t, "canceled", canceled.Issue.Status)
	require.Empty(t, canceled.Issue.CurrentSessionBindingID)
	require.Equal(t, "requested", canceled.SessionBinding.Stop.Status)
	reopen := issueLifecycle("open-issue", canceled.Issue)
	reopened := mcpWork[core.WorkReceipt](t, topic, "issue_reopen", reopen)
	mcpWorkError(t, topic, "issue_reopen", "conflict", reopen)
	require.Equal(t, "pending", reopened.Issue.Status)
	require.Nil(t, reopened.SessionBinding)
	mcpWorkError(t, topic, "start_issue", "conflict", start)
	nextStart := startRequest("second-start", reopened.Issue)
	mcpWorkError(t, topic, "start_issue", "failed_precondition", nextStart)
	mcpWorkError(t, issueMCP, "get_issue", "stale_binding", struct{}{})
	require.Len(t, issueBindings(t, p), 1)
	require.Equal(t, dependent, readRecord[core.Issue](t, p, "issues/"+dependent.ID))
	dependencyEvents := dependencyEvents(t, f)
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
	require.Empty(t, waitWorkEvent(t, p, x.StartEventID, "superseded").SentAt)
	stop := waitObservedStop(t, p, "session-bindings", x.ID)
	require.Equal(t, "controlAcknowledged", stop.Receipt.Result)
	require.Equal(t, "idle", stop.Observation.State)
	require.Equal(t, "unknown", stop.WorkState)
	mcpWorkError(t, topic, "start_issue", "failed_precondition", nextStart)
	examination := inspectRoleBinding(t, p, operatorFile, issueBinding.ID)
	require.Equal(t, issueBinding.SessionID, examination.SessionBinding.SessionID)
	require.Equal(t, "revoked", examination.SessionBinding.State)
	require.Equal(t, "unknown", examination.SessionBinding.Stop.WorkState)
	require.Nil(t, examination.SessionBinding.Close)

	opToken, err := os.ReadFile(operatorFile)
	require.NoError(t, err)
	plain := recoveryFor("unsafe-detach", "settle", issueBinding.ID)
	status, data := request(t, http.MethodPost, p.endpoint, "/v1/operator/input-recovery", string(opToken), plain)
	errorCode(t, status, data, http.StatusConflict, "failed_precondition")
	require.Equal(t, "blocked", inspectRoleBinding(t, p, operatorFile, issueBinding.ID).SessionBinding.Recovery.Status)
	old := recoveryFor("close-first-runtime", "replace", issueBinding.ID)
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
	require.NoError(t, massd.GetSession(t.Context(), resource.SessionRef{SessionID: issueBinding.SessionID}, &previous))
	require.Equal(t, "offline", string(previous.Status.State))

	amended := mcpWork[core.WorkReceipt](t, topic, "issue_update_requirements", core.UpdateRequirementsRequest{
		OperationID: "amend-while-reopened", IssueID: issue.ID, ExpectedRevision: reopened.Issue.Revision,
		Goal: "Verify the replacement build as well.",
	})
	nextStart.ExpectedRevision = amended.Issue.Revision
	next := mcpWork[core.WorkReceipt](t, topic, "start_issue", nextStart)
	nextBinding := waitBinding(t, p, "issue", next.SessionBinding.ID, "ready")
	require.Equal(t, x.Generation+1, next.SessionBinding.Generation)
	require.NotEqual(t, issueBinding.SessionID, nextBinding.SessionID)
	nextInput := peer.next(t, "turn/start")
	require.Equal(t, nextBinding.SessionID, nextInput.session)
	require.Equal(t, "issue_session_started", decodeInternalBatch(t, nextInput, next.SessionBinding.StartEventID)[0].Kind)
	current := roleWork[core.WorkReceipt](t, p, roleToken(t, f, nextBinding), "get_issue", struct{}{})
	require.Equal(t, next.SessionBinding.ID, current.Issue.CurrentSessionBindingID)
	require.Equal(t, amended.Issue.Requirements, current.Issue.Requirements)
	nextInput.accept()
	waitWorkEvent(t, p, next.SessionBinding.StartEventID, "sent")
	dispatches, _ := conversationPage(t, p, url.Values{
		"topicId": {topicBinding.OwnerID}, "filter": {"issue_session_dispatch"},
	})
	require.Len(t, dispatches, 2)
	for i, binding := range []*core.SessionBinding{next.SessionBinding, x} {
		require.Equal(t, "issue_session_dispatch/"+binding.StartEventID, dispatches[i].ID)
		require.Equal(t, "issue_session_started", dispatches[i].DispatchKind)
		require.Equal(t, issue.ID, dispatches[i].IssueID)
		require.Equal(t, binding.ID, dispatches[i].SessionBindingID)
	}
	mcpWorkError(t, issueMCP, "get_issue", "stale_binding", struct{}{})
	for tool, args := range map[string]any{
		"update_issue":       map[string]any{"operationId": "stale", "title": "Stale", "expectedRevision": issue.Revision},
		"get_internal_event": map[string]string{"event_id": x.StartEventID},
		"get_message":        map[string]string{"messageId": source.MessageID},
	} {
		status, data = requestMCPTool(t, p.endpoint, tool, issueToken, args)
		errorCode(t, status, data, http.StatusConflict, "stale_binding")
	}
	reportReq := core.ReportRequest{OperationID: "late-ready", Kind: "ready", Summary: "Late evidence on the previous binding", GoalRevision: x.StartGoalRevision}
	impossible := reportReq
	impossible.GoalRevision = next.SessionBinding.StartGoalRevision
	mcpWorkError(t, issueMCP, "submit_report", "invalid_argument", impossible)
	report := mcpWork[core.Report](t, issueMCP, "submit_report", reportReq)
	require.Equal(t, report, mcpWork[core.Report](t, issueMCP, "submit_report", reportReq))
	require.True(t, report.Historical)
	require.Equal(t, x.ID, report.SessionBindingID)
	notice := peer.next(t, "thread/inject_items")
	require.Equal(t, topicBinding.SessionID, notice.session)
	require.Equal(t, "context_supplement", decodeInternalBatch(t, notice, report.EventID)[0].Kind)
	notice.accept()
	waitWorkEvent(t, p, report.EventID, "sent")
	require.Equal(t, next.SessionBinding.ID, readRecord[core.Issue](t, p, "issues/"+issue.ID).CurrentSessionBindingID)
	waitIssueObservation(t, p, next.SessionBinding, "observing")
	nextInput.end("turn-" + nextInput.session)
	ended := waitIssueObservation(t, p, next.SessionBinding, "ended")
	reminder := checkReminderCall(t, peer, nextBinding, ended)
	reminder.answer <- nativeSubmitAnswer{workID: "replacement-binding-reminder"}
	waitWorkEvent(t, p, ended.ReminderEventID, "sent")
	require.Empty(t, readRecord[bindingObservation](t, p, "session-bindings/"+x.ID).ReminderEventID)
	t.Log("Historical Report did not satisfy the replacement SessionBinding's missing Report check.")

	// Cancel the Topic while it, and a Issue Session, have active native work.
	incoming := input("before-topic-cancel")
	incoming.Message.ThreadRef.ID, incoming.Message.Channel = "lifecycle-topic", nil
	activity := submit(t, p, f, incoming)
	activityInput := peer.next(t, "turn/start")
	decodeInternalBatch(t, activityInput, activity.EventID)
	activityInput.accept()
	waitRouteStatus(t, p, activity.EventID, "sent")
	sleepingTimer := mcpWork[core.Timer](t, topic, "schedule_followup", timerInput("before-topic-cancel", timerFuture))
	topicCancel := lifecycle("cancel-topic", 1)
	oldTopic := mcpWork[core.Topic](t, topic, "topic_cancel", topicCancel)
	mcpWorkError(t, topic, "topic_cancel", "stale_binding", topicCancel)
	require.Equal(t, "canceled", oldTopic.Status)
	require.Equal(t, "canceled", readRecord[core.Issue](t, p, "issues/"+issue.ID).Status)
	require.Equal(t, "canceled", readRecord[core.Issue](t, p, "issues/"+dependent.ID).Status)
	require.Equal(t, completed, readRecord[core.Issue](t, p, "issues/"+completed.ID))
	mcpWorkError(t, topic, "topic_reopen", "stale_binding", lifecycle("use-old-topic", oldTopic.Revision))
	require.Equal(t, "unknown", waitObservedStop(t, p, "topics", oldTopic.ID).WorkState)
	require.Equal(t, "unknown", waitObservedStop(t, p, "session-bindings", next.SessionBinding.ID).WorkState)
	follow := input("after-topic-cancel")
	follow.Message.ThreadRef.ID, follow.Message.Channel = "lifecycle-topic", nil
	queued := submit(t, p, f, follow)
	require.Equal(t, "pending", readRecord[observedRoutingEvent](t, p, "routing-events/"+queued.EventID).Status)
	require.Equal(t, "pending", readRecord[observedRoutingEvent](t, p, "routing-events/"+queued.EventID).Status)
	peer.quiet(t, 250*time.Millisecond)
	topicRecovery := recoveryFor("restore-topic", "settle", topicBinding.ID)
	topicRecovery.StopRuntime = true
	restored := recoverRoleBinding(t, p, operatorFile, topicRecovery)
	require.Len(t, restored.RelatedReleases, 1)
	require.Equal(t, nextBinding.ID, restored.RelatedReleases[0].SessionBindingID)
	retiredBinding := readRecord[core.SessionBinding](t, p, "session-bindings/"+nextBinding.ID)
	require.Equal(t, "runtime_stopped", retiredBinding.Close.Method)
	newTopic := waitBinding(t, p, "topic", oldTopic.ID, "ready")
	require.Equal(t, restored.ReplacementSessionBindingID, newTopic.ID)
	require.Equal(t, topicBinding.Generation+1, newTopic.Generation)
	require.NotEqual(t, topicBinding.SessionID, newTopic.SessionID)
	var foundFollowup, foundRecovery bool
	for range 4 {
		call := peer.next(t, "")
		require.Equal(t, newTopic.SessionID, call.session)
		for _, internalEvent := range decodeInternalBatch(t, call) {
			if internalEvent.ID == queued.EventID {
				require.Equal(t, "routed_message", internalEvent.Kind)
				foundFollowup = true
			}
		}
		foundRecovery = foundRecovery || strings.Contains(call.text, "binding_recovered")
		call.answer <- nativeSubmitAnswer{complete: true}
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
	timers := mcpWork[struct{ Followups []timerRecord }](t, newOwner, "get_topic", map[string][]string{"include": {"followups"}}).Followups
	require.Len(t, timers, 1)
	timerAfterReopen := timers[0]
	require.Equal(t, sleepingTimer.ID, timerAfterReopen.ID)
	require.Equal(t, "invalidated", timerAfterReopen.Status)
	require.Empty(t, timerAfterReopen.TriggerMessageID)
	require.Equal(t, "canceled", readRecord[core.Issue](t, p, "issues/"+issue.ID).Status)
	require.Len(t, issueBindings(t, p), 2)
	mcpWorkError(t, topic, "create_issue", "stale_binding", issueInput(f.dir, "old-delayed-command"))
	oldIssue := readRecord[core.Issue](t, p, "issues/"+issue.ID)
	openedIssue := mcpWork[core.WorkReceipt](t, newOwner, "issue_reopen", issueLifecycle("reopen-after-topic", oldIssue))
	reopenNotice := peer.next(t, "thread/inject_items")
	reopenNotice.accept()
	require.Equal(t, newTopic.SessionID, reopenNotice.session)
	third := mcpWork[core.WorkReceipt](t, newOwner, "start_issue", startRequest("start-after-topic", openedIssue.Issue))
	require.Equal(t, next.SessionBinding.Generation+1, third.SessionBinding.Generation)
	thirdBinding := waitBinding(t, p, "issue", third.SessionBinding.ID, "ready")
	thirdInput := peer.next(t, "turn/start")
	thirdInput.accept()
	require.Equal(t, thirdBinding.SessionID, thirdInput.session)
	decodeInternalBatch(t, thirdInput, third.SessionBinding.StartEventID)
	waitWorkEvent(t, p, third.SessionBinding.StartEventID, "sent")
	for _, path := range []string{"/v1/observer/topics/" + oldTopic.ID, "/v1/observer/session-bindings/" + newTopic.ID} {
		status, _ = request(t, http.MethodPost, p.endpoint, path, string(opToken), nil)
		require.Equal(t, http.StatusMethodNotAllowed, status)
	}
	t.Log("An in-flight send stayed superseded; no second authority before verified release. Topic recovery accounted for its retired Issue Session and allowed a later explicitly started generation.")
}

func startClaudeBindingPeer(t *testing.T) (endpoint string, c *client.Client, native, config, workspace, project string) {
	t.Helper()
	massd, _ := dispatchBinaries(t)
	temp := t.TempDir()
	native, config = filepath.Join(temp, "native"), filepath.Join(temp, "claude")
	require.NoError(t, os.Mkdir(native, 0o700))
	require.NoError(t, os.Mkdir(config, 0o700))
	peer := filepath.Join(temp, "claude-wire-peer")
	cmd := exec.CommandContext(t.Context(), "go", "build", "-o", peer, "./tests/integration/testdata/ticket14-recovery-peer")
	cmd.Dir = "../../../mass-go"
	cmd.Env = append(os.Environ(), "GOPROXY=off", "GOSUMDB=off")
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "%s", out)
	launcher := filepath.Join(temp, "claude-with-mcp")
	cmd = exec.CommandContext(t.Context(), "go", "build", "-o", launcher, "./pkg/core/testdata/claude-mcp-peer")
	cmd.Dir = "../.."
	cmd.Env = append(os.Environ(), "GOPROXY=off", "GOSUMDB=off")
	out, err = cmd.CombinedOutput()
	require.NoError(t, err, "%s", out)
	workspace, err = filepath.EvalSymlinks(temp)
	require.NoError(t, err)
	project = filepath.Join(config, "projects", regexp.MustCompile(`[^a-zA-Z0-9]`).ReplaceAllString(workspace, "-"))
	endpoint, c, _ = startNativeMassd(t, massd, resource.RuntimeSpec{
		Type:     "claude",
		Features: &runtime.SessionFeatures{AgentQuestions: new(true)},
		Config: runtime.Config{"executable": launcher, "envPolicy": "replace", "env": map[string]any{
			"PEER_KIND": "claude", "PEER_DIR": native, "WORK_DIR": workspace, "CLAUDE_CONFIG_DIR": config, "SAVED_TRANSCRIPT_DIR": project,
			"CLAUDE_WIRE_PEER": peer, "PEER_CLAUDE_FULL_ACCESS": "1",
		}},
	})
	return endpoint, c, native, config, workspace, project
}

func TestClaudeRouterReplacesMissingProjectAfterOwnerRelease(t *testing.T) {
	endpoint, c, native, _, workspace, project := startClaudeBindingPeer(t)
	f, _ := eventFixture(t, endpoint)
	raw, err := os.ReadFile(f.config)
	require.NoError(t, err)
	fixtureWorkspace, err := filepath.EvalSymlinks(f.dir)
	require.NoError(t, err)
	write(t, f.config, strings.Replace(string(raw), "routerWorkspace: "+fixtureWorkspace, "routerWorkspace: "+workspace, 1))

	p := f.start()
	original := waitBinding(t, p, "router", "", "ready")
	require.NoFileExists(t, filepath.Join(project, original.SessionID+".jsonl"))
	ref := resource.SessionRef{SessionID: original.SessionID}
	var stopped resource.Runtime
	require.NoError(t, c.StopRuntime(t.Context(), original.RuntimeRef, &stopped))
	require.Equal(t, "stopped", stopped.Status.State)
	var oldSession resource.Session
	require.NoError(t, c.GetSession(t.Context(), ref, &oldSession))
	require.Equal(t, "offline", string(oldSession.Status.State))
	require.Equal(t, resource.SessionReasonOwnerReleased, oldSession.Status.Reason)
	require.NoDirExists(t, filepath.Dir(project))

	message := submit(t, p, f, input("claude-router-recovery"))
	require.Eventually(t, func() bool {
		return readRecord[core.SessionBinding](t, p, "session-bindings/"+original.ID).State == "revoked"
	}, 10*time.Second, 25*time.Millisecond)
	replacement := waitBinding(t, p, "router", "", "ready")
	require.Equal(t, original.Generation+1, replacement.Generation)
	require.Equal(t, original.ID, replacement.PreviousSessionBindingID)
	require.NotEqual(t, original.SessionID, replacement.SessionID)
	require.NotEqual(t, original.SessionUID, replacement.SessionUID)
	require.NoError(t, c.GetSession(t.Context(), ref, &oldSession))
	require.Equal(t, "offline", string(oldSession.Status.State))
	require.Equal(t, resource.SessionReasonRecoveryMissing, oldSession.Status.Reason)
	retired := readRecord[core.SessionBinding](t, p, "session-bindings/"+original.ID)
	require.Equal(t, "revoked", retired.State)
	require.NotNil(t, retired.Close)
	require.Equal(t, resource.SessionReasonRecoveryMissing, retired.Close.Method)
	require.False(t, retired.Close.Reusable)

	waitRouteStatus(t, p, message.EventID, "sent")
	var delivered int
	require.Eventually(t, func() bool {
		raw, _ := os.ReadFile(filepath.Join(native, "wire.jsonl"))
		delivered = 0
		for line := range strings.SplitSeq(string(raw), "\n") {
			var row struct {
				Method string
				Params struct {
					SessionID string
					Text      string
				}
			}
			if json.Unmarshal([]byte(line), &row) == nil && row.Method == "input" && strings.Contains(row.Params.Text, message.EventID) {
				delivered++
				if row.Params.SessionID != replacement.SessionID {
					return false
				}
			}
		}
		_, err := os.Stat(filepath.Join(project, replacement.SessionID+".jsonl"))
		return delivered == 1 && err == nil
	}, 5*time.Second, 25*time.Millisecond)
	require.Len(t, bindings(t, p), 2)
}

func TestLifecycleClaudeReusesClosedSessionWithFreshMCP(t *testing.T) {
	endpoint, c, native, _, workspace, project := startClaudeBindingPeer(t)
	require.NoError(t, os.MkdirAll(project, 0o700))
	f, operatorFile := eventFixture(t, endpoint)
	p := f.start()
	router := startRoleMCP(t, f, waitBinding(t, p, "router", "", "ready"))
	source := submit(t, p, f, input("claude-reuse"))
	waitRouteStatus(t, p, source.EventID, "sent")
	decision := mcpWork[core.RouteDecision](t, router, "route_messages", newRoute("claude-reuse", []string{source.MessageID}, workspace))
	topic := startRoleMCP(t, f, waitBinding(t, p, "topic", decision.Targets[0].TopicID, "ready"))
	waitDelivered(t, p, source.MessageID, "topic", decision.Targets[0].TopicID)
	create := issueInput(workspace, "claude-issue")
	create.Start = true
	work := mcpWork[core.WorkReceipt](t, topic, "create_issue", create)
	old := waitBinding(t, p, "issue", work.SessionBinding.ID, "ready")
	oldProcess := startRoleMCP(t, f, old)
	oldCredential := roleToken(t, f, old)
	waitWorkEvent(t, p, work.SessionBinding.StartEventID, "sent")
	observed := waitIssueObservation(t, p, work.SessionBinding, "ended")
	require.Equal(t, "claude", observed.WorkObservation.Provider)
	require.Empty(t, observed.WorkObservation.WorkID)
	require.Equal(t, "idle", observed.WorkObservation.End.Reason)
	require.True(t, observed.WorkObservation.Continuous)
	require.Equal(t, "already_scheduled", observed.ReminderEligibility)
	require.True(t, observed.ReminderScheduled)
	waitWorkEvent(t, p, observed.ReminderEventID, "sent")
	require.Len(t, workEvents(t, p, reportReminder), 1)
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
	findMCP := func(want string) bool {
		raw, err := os.ReadFile(filepath.Join(native, "mcp.jsonl"))
		if err != nil {
			return false
		}
		for line := range strings.SplitSeq(string(raw), "\n") {
			var row struct {
				Method            string
				NativePID         int
				QuestionsDisabled bool
			}
			if json.Unmarshal([]byte(line), &row) == nil && row.NativePID == nativePID && row.Method == want {
				if want == "opened" {
					require.True(t, row.QuestionsDisabled, "Claude launch must disallow AskUserQuestion")
				}
				return true
			}
		}
		return false
	}
	require.True(t, findMCP("opened"))
	canceled := mcpWork[core.WorkReceipt](t, topic, "issue_cancel", issueLifecycle("claude-cancel", work.Issue))
	_ = waitObservedStop(t, p, "session-bindings", work.SessionBinding.ID)
	req := recoveryFor("close-claude-session", "settle", old.ID)
	closed := recoverRoleBinding(t, p, operatorFile, req)
	require.Equal(t, "native_session_closed", closed.Close.Method)
	require.True(t, closed.Close.Reusable)
	require.ErrorIs(t, syscall.Kill(nativePID, 0), syscall.ESRCH)
	require.True(t, findMCP("released"))
	var released resource.Session
	require.NoError(t, c.GetSession(t.Context(), resource.SessionRef{SessionID: old.SessionID}, &released))
	require.Equal(t, "offline", string(released.Status.State))
	reopened := mcpWork[core.WorkReceipt](t, topic, "issue_reopen", issueLifecycle("claude-reopen", canceled.Issue))
	next := mcpWork[core.WorkReceipt](t, topic, "start_issue", startRequest("claude-start-new-generation", reopened.Issue))
	require.Equal(t, work.Issue.ID, work.SessionBinding.OwnerID)
	require.Equal(t, 1, work.SessionBinding.Generation)
	require.Equal(t, work.Issue.ID, next.SessionBinding.OwnerID)
	require.Equal(t, 2, next.SessionBinding.Generation)
	replayed := mcpWork[core.WorkReceipt](t, topic, "create_issue", create)
	require.Equal(t, work.Issue.ID, replayed.Issue.ID)
	require.Equal(t, next.SessionBinding.ID, replayed.Issue.CurrentSessionBindingID)
	require.Equal(t, work.SessionBinding.ID, replayed.SessionBinding.ID)
	fresh := waitBinding(t, p, "issue", next.SessionBinding.ID, "ready")
	require.Equal(t, old.SessionID, fresh.SessionID)
	require.Equal(t, old.SessionUID, fresh.SessionUID)
	require.Equal(t, old.ID, fresh.CapabilityFileID)
	require.Equal(t, old.Generation+1, fresh.Generation)
	require.Contains(t, readTrace(), `"method":"full_access","params":{"resumed":false,"sessionId":"`+old.SessionID+`"}`)
	require.Contains(t, readTrace(), `"method":"full_access","params":{"resumed":true,"sessionId":"`+old.SessionID+`"}`)
	require.NotEqual(t, oldCredential, roleToken(t, f, fresh))
	require.Eventually(t, func() bool {
		raw, err := os.ReadFile(filepath.Join(native, "mcp.jsonl"))
		return err == nil && strings.Contains(string(raw), `"id":"`+fresh.ID+`"`)
	}, 5*time.Second, 25*time.Millisecond, "resumed native client must authenticate as the new binding")
	current := startRoleMCP(t, f, fresh)
	require.Equal(t, next.SessionBinding.ID, mcpWork[core.WorkReceipt](t, current, "get_issue", struct{}{}).Issue.CurrentSessionBindingID)
	mcpWorkError(t, oldProcess, "get_issue", "stale_binding", struct{}{})
	reportRequest := core.ReportRequest{
		OperationID: "claude-report", Kind: "progress", Summary: "Work completed", GoalRevision: work.SessionBinding.StartGoalRevision,
	}
	historicalReport := mcpWork[core.Report](t, oldProcess, "submit_report", reportRequest)
	require.Equal(t, "mass-t1-i1-r1", historicalReport.ID)
	require.Equal(t, work.SessionBinding.ID, historicalReport.SessionBindingID)
	require.True(t, historicalReport.Historical)
	reportRequest.GoalRevision = next.SessionBinding.StartGoalRevision
	currentReport := mcpWork[core.Report](t, current, "submit_report", reportRequest)
	require.Equal(t, "mass-t1-i1-r2", currentReport.ID)
	require.Equal(t, next.SessionBinding.ID, currentReport.SessionBindingID)
	require.False(t, currentReport.Historical)
	require.Equal(t, currentReport, mcpWork[core.Report](t, current, "submit_report", reportRequest))
	reportRequest.GoalRevision = work.SessionBinding.StartGoalRevision
	require.Equal(t, historicalReport, mcpWork[core.Report](t, oldProcess, "submit_report", reportRequest))
	waitWorkEvent(t, p, next.SessionBinding.StartEventID, "sent")
	recanceled := mcpWork[core.WorkReceipt](t, topic, "issue_cancel", issueLifecycle("cancel-resumed", next.Issue))
	waitObservedStop(t, p, "session-bindings", next.SessionBinding.ID)
	settled := recoverRoleBinding(t, p, operatorFile, recoveryFor("settle-resumed", "settle", fresh.ID))
	require.True(t, settled.Close.Reusable)
	reopened = mcpWork[core.WorkReceipt](t, topic, "issue_reopen", issueLifecycle("open-for-review", recanceled.Issue))
	reselect := startRequest("start-review", reopened.Issue)
	reselect.RuntimeRef, reselect.Model = new("agent"), new("review-model")
	review := mcpWork[core.WorkReceipt](t, topic, "start_issue", reselect)
	reviewBinding := waitBinding(t, p, "issue", review.SessionBinding.ID, "ready")
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
	historical := readRecord[core.SessionBinding](t, p, "session-bindings/"+next.SessionBinding.ID)
	require.Equal(t, fresh.SessionID, historical.SessionID)
	require.Equal(t, issueModel, historical.ExecutionProfile)
	t.Logf("Claude native PID %d exited before offline readback. Matching parameters resumed the same Session with a fresh HTTP MCP token, and a new profile created a separate Session.", nativePID)

	reviewStart := waitWorkEvent(t, p, review.SessionBinding.StartEventID, "sent")
	reviewRef := resource.SessionRef{SessionID: reviewBinding.SessionID}
	require.NoError(t, c.CloseSession(t.Context(), reviewRef, &released))
	require.Equal(t, resource.SessionReasonOwnerReleased, released.Status.Reason)
	reviewUpdate := mcpWork[core.WorkReceipt](t, topic, "issue_update_requirements", core.UpdateRequirementsRequest{
		OperationID: "claude-normal-resume", IssueID: review.Issue.ID, ExpectedRevision: review.Issue.Revision,
		Goal: "Continue reviewing after closing the Session.", NotifyIssueSession: true,
	})
	waitWorkEvent(t, p, reviewUpdate.NotificationEventID, "sent")
	reattached := waitBinding(t, p, "issue", review.SessionBinding.ID, "ready")
	require.Equal(t, reviewBinding.SessionID, reattached.SessionID)
	require.Equal(t, reviewBinding.SessionUID, reattached.SessionUID)
	require.Contains(t, readTrace(), `"method":"full_access","params":{"resumed":true,"sessionId":"`+reattached.SessionID+`"}`)
	require.Equal(t, reviewStart, waitWorkEvent(t, p, review.SessionBinding.StartEventID, "sent"))

	require.NoError(t, c.CloseSession(t.Context(), reviewRef, &released))
	require.NoError(t, os.Remove(filepath.Join(project, reviewBinding.SessionID+".jsonl")))
	lostReview := mcpWork[core.WorkReceipt](t, topic, "issue_update_requirements", core.UpdateRequirementsRequest{
		OperationID: "claude-replace-missing-transcript", IssueID: review.Issue.ID, ExpectedRevision: reviewUpdate.Issue.Revision,
		Goal: "Continue the review with the latest goal.", NotifyIssueSession: true,
	})
	var successor core.Issue
	require.Eventually(t, func() bool {
		successor = readRecord[core.Issue](t, p, "issues/"+review.Issue.ID)
		return successor.CurrentSessionBindingID != "" && successor.CurrentSessionBindingID != review.SessionBinding.ID
	}, 8*time.Second, 25*time.Millisecond)
	replacement := waitBinding(t, p, "issue", successor.CurrentSessionBindingID, "ready")
	require.NotEqual(t, reattached.SessionID, replacement.SessionID)
	require.NotEqual(t, reattached.SessionUID, replacement.SessionUID)
	require.Equal(t, lostReview.Issue.Requirements, successor.Requirements)
	require.Equal(t, reviewUpdate.Issue.Reports, successor.Reports)
	require.NoError(t, c.GetSession(t.Context(), reviewRef, &released))
	require.Equal(t, resource.SessionReasonRecoveryMissing, released.Status.Reason)
}

func TestTopicCancelRollsBackIssueStopsAndDependencyEvent(t *testing.T) {
	f := fixtureAt(t)
	f.execution(newRouteBoundary(t))
	p := f.start()
	binding, token := workBoundaryTopic(t, f, p)
	topic := startRoleMCP(t, f, binding)
	original := mcpWork[core.Topic](t, topic, "get_topic", struct{}{})
	create := issueInput(f.dir, "cancel-active")
	create.Start = true
	started := mcpWork[core.WorkReceipt](t, topic, "create_issue", create)
	waitWorkEvent(t, p, started.SessionBinding.StartEventID, "sent")
	dependent := mcpWork[core.WorkReceipt](t, topic, "create_issue", issueInput(f.dir, "cancel-dependent")).Issue
	dependent = blockers(t, topic, dependent, "block-on-active", dependency(started.Issue))
	pending := mcpWork[core.WorkReceipt](t, topic, "create_issue", issueInput(f.dir, "cancel-pending")).Issue
	mcpWorkError(t, topic, "topic_complete", "failed_precondition", acceptance("unfinished", original.Revision, "notes.txt"))

	p.stop(t, false)
	const stage = "dependency_before_commit"
	gate := t.TempDir()
	p = f.start("SUPERAGENT_TEST_GATE_STAGE="+stage, "SUPERAGENT_TEST_GATE_DIR="+gate)
	cancel := lifecycle("cancel-topic-at-once", original.Revision)
	req, err := newMCPToolRequest(t.Context(), p.endpoint, token, "topic_cancel", cancel)
	require.NoError(t, err)
	done := make(chan mcpHTTPResult, 1)
	go func() { done <- doMCPRequest(req) }()
	waitWorkGate(t, gate, stage)
	p.stop(t, true)
	require.Error(t, (<-done).err)

	p = f.start()
	topic = startRoleMCP(t, f, binding)
	require.Equal(t, original, mcpWork[core.Topic](t, topic, "get_topic", struct{}{}))
	for _, before := range []core.Issue{started.Issue, dependent, pending} {
		require.Equal(t, before, mcpWork[core.Issue](t, topic, "get_issue", map[string]string{"issueId": before.ID}))
	}
	for _, id := range []string{binding.ID, started.SessionBinding.ID} {
		current := readRecord[core.SessionBinding](t, p, "session-bindings/"+id)
		require.NotEqual(t, "revoked", current.State)
		require.Nil(t, current.Stop)
	}
	require.Empty(t, dependencyEvents(t, f))

	canceled := mcpWork[core.Topic](t, topic, "topic_cancel", cancel)
	require.Equal(t, "canceled", canceled.Status)
	require.NotNil(t, canceled.Cancellation)
	for _, before := range []core.Issue{started.Issue, dependent, pending} {
		after := readRecord[core.Issue](t, p, "issues/"+before.ID)
		require.Equal(t, "canceled", after.Status)
		require.Equal(t, before.Revision+1, after.Revision)
		require.Equal(t, canceled.Cancellation, after.Cancellation)
		require.Empty(t, after.CurrentSessionBindingID)
	}
	for _, id := range []string{binding.ID, started.SessionBinding.ID} {
		retired := readRecord[core.SessionBinding](t, p, "session-bindings/"+id)
		require.Equal(t, "revoked", retired.State)
		require.NotNil(t, retired.Stop)
		require.Equal(t, canceled.Cancellation.At, retired.Stop.RequestedAt)
	}
	events := dependencyEvents(t, f)
	require.Len(t, events, 1)
	require.Equal(t, "pending", events[0].Status)
	require.JSONEq(t, fmt.Sprintf(`{"type":"issue.dependency_changed","topicId":%q,"dependencyIssueId":%q,
 "previousStatus":"active","status":"canceled","dependentIssueIds":[%q]}`, binding.OwnerID, started.Issue.ID, dependent.ID), string(events[0].Body))
	mcpWorkError(t, topic, "topic_cancel", "stale_binding", cancel)
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
			target := mcpWork[core.WorkReceipt](t, topic, "create_issue", issueInput(f.dir, "target")).Issue
			dependent := mcpWork[core.WorkReceipt](t, topic, "create_issue", issueInput(f.dir, "dependent")).Issue
			dependent = blockers(t, topic, dependent, "add-dependency", dependency(target))
			p.stop(t, false)
			gate := t.TempDir()
			p = f.start("SUPERAGENT_TEST_GATE_STAGE="+stage, "SUPERAGENT_TEST_GATE_DIR="+gate)
			cancel := issueLifecycle("crash-cancel", target)
			req, err := newMCPToolRequest(t.Context(), p.endpoint, token, "issue_cancel", cancel)
			require.NoError(t, err)
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
			events := dependencyEvents(t, f)
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
			require.Len(t, dependencyEvents(t, f), 2)
			require.Equal(t, dependent, readRecord[core.Issue](t, p, "issues/"+dependent.ID))
			require.Empty(t, issueBindings(t, p))
			t.Logf("%s: restart saw %d committed dependency facts; cancel/reopen replay left two facts and no downstream changes", stage, len(events))
		})
	}
}
