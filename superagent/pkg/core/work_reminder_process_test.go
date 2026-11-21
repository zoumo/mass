package core_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/zoumo/mass/mass-go/pkg/client"
	"github.com/zoumo/mass/mass-go/pkg/events"
	"github.com/zoumo/mass/mass-go/pkg/resource"
	"github.com/zoumo/mass/mass-go/pkg/store"

	"github.com/zoumo/mass/superagent/pkg/core"
)

const reportReminder = "report_reminder"

type bindingObservation struct {
	core.SessionBinding
	ReminderScheduled   bool            `json:"reminderScheduled"`
	ReminderEligibility string          `json:"reminderEligibility"`
	Reminder            workEventRecord `json:"reminder"`
}

func waitIssueObservation(t *testing.T, p *process, x *core.SessionBinding, state string) bindingObservation {
	t.Helper()
	var observed bindingObservation
	require.Eventually(t, func() bool {
		observed = readRecord[bindingObservation](t, p, "session-bindings/"+x.ID)
		return observed.WorkObservation != nil && observed.WorkObservation.State == state
	}, 5*time.Second, 20*time.Millisecond, "waiting for %s on %s", state, x.ID)
	return observed
}

func startReminderTopic(t *testing.T, f *fixture, p *process, peer *nativeSubmitPeer) (*roleMCPClient, core.SessionBinding) {
	t.Helper()
	router := startRoleMCP(t, f, waitBinding(t, p, "router", "", "ready"))
	b, _ := nativeWorkTopic(t, f, p, peer, router, "missing-report")
	return startRoleMCP(t, f, b), b
}

func startReminderWorker(t *testing.T, f *fixture, p *process, peer *nativeSubmitPeer, topic *roleMCPClient) (core.WorkReceipt, core.SessionBinding, nativeSubmitCall) {
	t.Helper()
	req := issueInput(f.dir, "work")
	req.Start = true
	work := mcpWork[core.WorkReceipt](t, topic, "create_issue", req)
	b := waitBinding(t, p, "issue", work.SessionBinding.ID, "ready")
	first := peer.next(t, "turn/start")
	require.Equal(t, b.SessionID, first.session)
	decodeInternalBatch(t, first, work.SessionBinding.StartEventID)
	return work, b, first
}

func watchNative(t *testing.T, massd *client.Client, b core.SessionBinding) *client.Subscription {
	t.Helper()
	sub, err := massd.Subscribe(t.Context(), resource.SessionRef{SessionID: b.SessionID})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, sub.Close()) })
	return sub
}

func nextNativeMethod(t *testing.T, sub *client.Subscription, method string) events.SessionEvent {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 4*time.Second)
	defer cancel()
	for {
		ev, err := sub.Next(ctx)
		require.NoError(t, err, "waiting for %s", method)
		m, _ := ev.Meta["codex"].(map[string]any)
		if m["method"] == method {
			return ev
		}
	}
}

func checkReminderCall(t *testing.T, peer *nativeSubmitPeer, b core.SessionBinding, observed bindingObservation) nativeSubmitCall {
	t.Helper()
	require.True(t, observed.ReminderScheduled)
	require.NotEmpty(t, observed.ReminderEventID)
	require.Equal(t, observed.ReminderEventID, observed.Reminder.ID)
	call := peer.next(t, "turn/start")
	require.Equal(t, b.SessionID, call.session)
	ev := decodeInternalBatch(t, call, observed.ReminderEventID)[0]
	require.Equal(t, reportReminder, ev.Kind)
	require.JSONEq(t, `{"events":[{"event_id":"`+observed.ReminderEventID+`","kind":"report_reminder"}]}`, call.text)
	require.Empty(t, ev.Body)
	require.Empty(t, ev.Refs)
	return call
}

func updateReminderWork(t *testing.T, topic *roleMCPClient, work core.WorkReceipt) core.WorkReceipt {
	t.Helper()
	return mcpWork[core.WorkReceipt](t, topic, "issue_update_requirements", core.UpdateRequirementsRequest{
		OperationID: "update", IssueID: work.Issue.ID, ExpectedRevision: work.Issue.Revision,
		Goal: "Also verify the amended sample.", NotifyIssueSession: true,
	})
}

func TestReportReminderIssueIdle(t *testing.T) {
	t.Parallel()
	peer := newNativeSubmitPeer(t)
	endpoint, massd := startEventTestMassd(t, peer)
	f, _ := eventFixture(t, endpoint)
	p := f.start()
	topic, topicBinding := startReminderTopic(t, f, p, peer)
	work, b, first := startReminderWorker(t, f, p, peer, topic)
	sub := watchNative(t, massd, b)
	first.idle()
	nextNativeMethod(t, sub, "thread/status/changed")
	first.answer <- nativeSubmitAnswer{workID: "work-one"}
	waitWorkEvent(t, p, work.SessionBinding.StartEventID, "sent")
	initial := waitIssueObservation(t, p, work.SessionBinding, "observing")
	require.Equal(t, "work-one", initial.WorkObservation.WorkID)
	require.True(t, initial.WorkObservation.Continuous)
	nextNativeMethod(t, sub, "turn/started")
	require.Equal(t, "awaiting_correlated_end", readRecord[bindingObservation](t, p, "session-bindings/"+work.SessionBinding.ID).ReminderEligibility)
	peer.quiet(t, 300*time.Millisecond)

	update := updateReminderWork(t, topic, work)
	second := peer.next(t, "turn/start")
	require.Equal(t, b.SessionID, second.session)
	decodeInternalBatch(t, second, update.NotificationEventID)
	// The older turn can still produce a real, eligible-shaped terminal fact
	// while the new native submit is waiting for its response.
	first.end("work-one")
	ev := nextNativeMethod(t, sub, "turn/completed")
	require.Equal(t, "completed", ev.Payload().(events.StateUpdate).StopReason)
	require.Equal(t, "work-one", ev.Meta["codex"].(map[string]any)["turnId"])
	require.Empty(t, workEvents(t, p, reportReminder))
	second.answer <- nativeSubmitAnswer{workID: "work-two"}
	waitWorkEvent(t, p, update.NotificationEventID, "sent")
	nextNativeMethod(t, sub, "turn/started")
	proof := waitIssueObservation(t, p, work.SessionBinding, "observing").WorkObservation
	require.Equal(t, "work-two", proof.WorkID)
	require.NotEqual(t, initial.WorkObservation.AttemptID, proof.AttemptID)

	second.notify("thread/started", map[string]any{"thread": map[string]string{"id": "child", "parentThreadId": b.SessionID}})
	child := second
	child.session = "child"
	child.end("child-work")
	ev = nextNativeMethod(t, sub, "turn/completed")
	require.Equal(t, "child_thread_notification_unsupported", ev.Payload().(events.SessionError).Reason)
	require.Equal(t, "child", ev.Meta["codex"].(map[string]any)["sourceThreadId"])
	first.end("work-one")
	ev = nextNativeMethod(t, sub, "turn/completed")
	require.Equal(t, "stale_native_turn", ev.Payload().(events.SessionError).Reason)
	require.Equal(t, "work-one", ev.Meta["codex"].(map[string]any)["turnId"])
	peer.quiet(t, 300*time.Millisecond)
	require.Empty(t, workEvents(t, p, reportReminder))

	second.notify("item/completed", map[string]any{
		"threadId":      b.SessionID,
		"turnId":        "work-two",
		"completedAtMs": time.Now().UnixMilli(),
		"item":          map[string]string{"type": "futureNativeItem", "id": "future-1"},
	})
	ev = nextNativeMethod(t, sub, "item/completed")
	require.Equal(t, "unsupported_native_item", ev.Payload().(events.SessionError).Reason)
	require.Equal(t, b.SessionID, ev.SessionID)
	require.Equal(t, "work-two", ev.Meta["codex"].(map[string]any)["turnId"])

	second.idle()
	ev = nextNativeMethod(t, sub, "thread/status/changed")
	require.Equal(t, events.Idle, *ev.Payload().(events.StateUpdate).State)
	ended := waitIssueObservation(t, p, work.SessionBinding, "ended")
	require.Equal(t, ev.EventID, ended.WorkObservation.End.EventID)
	require.Equal(t, "idle", ended.WorkObservation.End.Reason)
	require.Equal(t, b.ID, ended.WorkObservation.SessionBindingID)
	require.Equal(t, b.Generation, ended.WorkObservation.Generation)
	require.Equal(t, b.SessionUID, ended.WorkObservation.SessionUID)
	require.Equal(t, b.SessionID, ended.WorkObservation.SessionID)
	require.True(t, ended.WorkObservation.Continuous)
	require.Empty(t, ended.WorkObservation.ObservationGap)
	require.Equal(t, "already_scheduled", ended.ReminderEligibility)
	reminder := checkReminderCall(t, peer, b, ended)
	reminder.answer <- nativeSubmitAnswer{workID: "reminder-turn", complete: true}
	waitWorkEvent(t, p, ended.ReminderEventID, "sent")
	ev = nextNativeMethod(t, sub, "turn/completed")
	require.Equal(t, "reminder-turn", ev.Meta["codex"].(map[string]any)["turnId"])
	peer.quiet(t, 300*time.Millisecond)
	require.Len(t, workEvents(t, p, reportReminder), 1)
	saved := readRecord[bindingObservation](t, p, "session-bindings/"+work.SessionBinding.ID)
	require.Equal(t, ended.WorkObservation, saved.WorkObservation)
	require.Equal(t, "sent", saved.Reminder.Status)
	var history store.HistoryPage
	require.NoError(t, massd.History(t.Context(), resource.SessionRef{SessionID: b.SessionID}, store.HistoryQuery{}, &history))
	for _, historical := range history.Events {
		require.NotEqual(t, ended.WorkObservation.End.EventID, historical.EventID)
	}
	require.Equal(t, update.Issue, readRecord[core.Issue](t, p, "issues/"+work.Issue.ID))
	require.Equal(t, "active", readRecord[core.Topic](t, p, "topics/"+topicBinding.OwnerID).Status)
	t.Log("Issue observation continued after the unsupported item. Current Session idle scheduled one reminder.")
}

func TestReportReminderAfterCrashAndDuplicate(t *testing.T) {
	t.Parallel()
	peer := newNativeSubmitPeer(t)
	endpoint, massd := startEventTestMassd(t, peer)
	f, _ := eventFixture(t, endpoint)
	gate := t.TempDir()
	release := filepath.Join(gate, "release")
	write(t, release, "")
	p := f.start("SUPERAGENT_TEST_GATE_STAGE=issue_before_claim", "SUPERAGENT_TEST_GATE_DIR="+gate)
	topic, _ := startReminderTopic(t, f, p, peer)
	work, b, first := startReminderWorker(t, f, p, peer, topic)
	sub := watchNative(t, massd, b)
	require.NoError(t, os.Remove(release))
	first.answer <- nativeSubmitAnswer{workID: "early-finish", endBeforeReply: true}
	ev := nextNativeMethod(t, sub, "turn/completed")
	ended := waitIssueObservation(t, p, work.SessionBinding, "ended")
	require.Equal(t, ev.EventID, ended.WorkObservation.End.EventID)
	require.True(t, ended.ReminderScheduled)
	require.Empty(t, waitWorkEvent(t, p, ended.ReminderEventID, "pending").SentAt)
	peer.quiet(t, 300*time.Millisecond)
	p.stop(t, true)

	settleGate := t.TempDir()
	p = f.start("SUPERAGENT_TEST_GATE_STAGE=internal_event_before_settlement", "SUPERAGENT_TEST_GATE_DIR="+settleGate)
	require.Equal(t, ended.WorkObservation, readRecord[bindingObservation](t, p, "session-bindings/"+work.SessionBinding.ID).WorkObservation)
	firstReminder := checkReminderCall(t, peer, b, ended)
	firstReminder.answer <- nativeSubmitAnswer{workID: "first-reminder", complete: true}
	waitWorkGate(t, settleGate, "internal_event_before_settlement")
	require.Empty(t, waitWorkEvent(t, p, ended.ReminderEventID, "dispatching").SentAt)
	p.stop(t, true)

	p = f.start()
	waitWorkEvent(t, p, ended.ReminderEventID, "dispatching")
	retry := checkReminderCall(t, peer, b, ended)
	require.Equal(t, firstReminder.text, retry.text)
	retry.answer <- nativeSubmitAnswer{workID: "duplicate-reminder", complete: true}
	require.NotEmpty(t, waitWorkEvent(t, p, ended.ReminderEventID, "sent").SentAt)
	peer.quiet(t, 350*time.Millisecond)
	require.Len(t, workEvents(t, p, reportReminder), 1)
	t.Log("The CLI sent idle before its prompt response. SIGKILL after scheduling preserved the pending Event. SIGKILL after native reminder acceptance retained the same Event and wire batch on retry.")
}

func TestReportReminderObservationGaps(t *testing.T) {
	t.Parallel()
	peer := newNativeSubmitPeer(t)
	endpoint, massd := startEventTestMassd(t, peer)
	sockets := make(chan func(), 8)
	proxy := proxyRPC(t, endpoint, func(method string, disconnect func()) bool {
		if method == "session.subscribe" {
			sockets <- disconnect
		}
		return false
	}, func() {})
	f, _ := eventFixture(t, proxy)
	p := f.start()
	topic, _ := startReminderTopic(t, f, p, peer)
	work, b, first := startReminderWorker(t, f, p, peer, topic)
	sub := watchNative(t, massd, b)
	first.answer <- nativeSubmitAnswer{workID: "work-before-disconnect"}
	waitIssueObservation(t, p, work.SessionBinding, "observing")
	select {
	case cut := <-sockets:
		cut()
	case <-time.After(4 * time.Second):
		t.Fatal("Core subscription was not intercepted")
	}
	disconnected := waitIssueObservation(t, p, work.SessionBinding, "unknown")
	require.Equal(t, "live_subscription_interrupted", disconnected.WorkObservation.ObservationGap)
	require.Equal(t, "work-before-disconnect", disconnected.WorkObservation.WorkID)
	require.False(t, disconnected.WorkObservation.Continuous)
	first.end("work-before-disconnect")
	nextNativeMethod(t, sub, "turn/completed")
	peer.quiet(t, 300*time.Millisecond)
	require.Empty(t, workEvents(t, p, reportReminder))
	select {
	case <-sockets:
		t.Fatal("Core reattached the interrupted observation")
	default:
	}

	update := updateReminderWork(t, topic, work)
	second := peer.next(t, "turn/start")
	decodeInternalBatch(t, second, update.NotificationEventID)
	second.answer <- nativeSubmitAnswer{workID: "work-before-offline"}
	live := waitIssueObservation(t, p, work.SessionBinding, "observing")
	require.Equal(t, "work-before-offline", live.WorkObservation.WorkID)
	p.stop(t, true)
	second.end(live.WorkObservation.WorkID)
	nextNativeMethod(t, sub, "turn/completed")
	p = f.start()
	waitBinding(t, p, "issue", b.ID, "ready")
	restarted := waitIssueObservation(t, p, work.SessionBinding, "unknown")
	require.Equal(t, "core_observation_interrupted", restarted.WorkObservation.ObservationGap)
	require.Equal(t, live.WorkObservation.WorkID, restarted.WorkObservation.WorkID)
	require.False(t, restarted.ReminderScheduled)
	peer.quiet(t, 350*time.Millisecond)
	require.Empty(t, workEvents(t, p, reportReminder))
	t.Log("An interrupted public WebSocket and a Core-offline completion each retained unknown. A later explicit submit obtained a fresh subscription and its own work reference.")
}

func TestReportReminderReportAndCancellationFences(t *testing.T) {
	t.Parallel()
	for _, scenario := range []string{"report-first", "reminder-first", "cancellation"} {
		t.Run(scenario, func(t *testing.T) {
			peer := newNativeSubmitPeer(t)
			endpoint, massd := startEventTestMassd(t, peer)
			f, _ := eventFixture(t, endpoint)
			gate := t.TempDir()
			stage := "issue_before_rpc"
			release := filepath.Join(gate, "release")
			if scenario == "report-first" {
				stage = "report_before_notification"
			} else {
				write(t, release, "")
			}
			p := f.start("SUPERAGENT_TEST_GATE_STAGE="+stage, "SUPERAGENT_TEST_GATE_DIR="+gate)
			t.Cleanup(func() { _ = os.WriteFile(release, nil, 0o600) })
			topic, topicBinding := startReminderTopic(t, f, p, peer)
			work, b, first := startReminderWorker(t, f, p, peer, topic)
			sub := watchNative(t, massd, b)
			first.answer <- nativeSubmitAnswer{workID: "work-with-report-race"}
			waitIssueObservation(t, p, work.SessionBinding, "observing")
			req := core.ReportRequest{OperationID: "report", Kind: "ready", Summary: "Work was completed before a reminder was needed.", GoalRevision: work.SessionBinding.StartGoalRevision}
			token := roleToken(t, f, b)
			if scenario == "report-first" {
				submitConcurrentReport(t, p, token, req, gate, release, first, sub)
				deliverReminderTestReport(t, p, peer, topicBinding)
				ended := waitIssueObservation(t, p, work.SessionBinding, "ended")
				require.False(t, ended.ReminderScheduled)
				require.Equal(t, "report_received", ended.ReminderEligibility)
				require.Empty(t, workEvents(t, p, reportReminder))
				return
			}
			require.NoError(t, os.Remove(release))
			require.NoError(t, os.Remove(filepath.Join(gate, stage)))
			first.end("work-with-report-race")
			ended := waitIssueObservation(t, p, work.SessionBinding, "ended")
			require.True(t, ended.ReminderScheduled)
			waitWorkGate(t, gate, stage)
			waitWorkEvent(t, p, ended.ReminderEventID, "pending")
			if scenario == "reminder-first" {
				report := roleWork[core.Report](t, p, token, "submit_report", req)
				require.Equal(t, b.ID, report.SessionBindingID)
				require.False(t, report.Historical)
				deliverReminderTestReport(t, p, peer, topicBinding)
			} else {
				canceled := mcpWork[core.WorkReceipt](t, topic, "issue_cancel", issueLifecycle("cancel-reminder", work.Issue))
				require.Equal(t, "canceled", canceled.Issue.Status)
				require.Equal(t, "revoked", canceled.SessionBinding.State)
			}
			write(t, release, "")
			require.Empty(t, waitWorkEvent(t, p, ended.ReminderEventID, "superseded").SentAt)
			peer.quiet(t, 300*time.Millisecond)
			require.Len(t, workEvents(t, p, reportReminder), 1)
			require.Equal(t, ended.ReminderEventID, readRecord[bindingObservation](t, p, "session-bindings/"+b.ID).ReminderEventID)
		})
	}
}

func submitConcurrentReport(t *testing.T, p *process, token string, req core.ReportRequest, gate, release string, first nativeSubmitCall, sub *client.Subscription) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 12*time.Second)
	defer cancel()
	response := make(chan error, 1)
	go func() {
		response <- postReport(ctx, p, token, req)
	}()
	waitWorkGate(t, gate, "report_before_notification")
	first.end("work-with-report-race")
	nextNativeMethod(t, sub, "turn/completed")
	write(t, release, "")
	require.NoError(t, <-response)
}

func postReport(ctx context.Context, p *process, token string, report core.ReportRequest) error {
	req, err := newMCPToolRequest(ctx, p.endpoint, token, "submit_report", report)
	if err != nil {
		return err
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return fmt.Errorf("Report returned HTTP %d", res.StatusCode)
	}
	var response mcpTestResponse
	if err := json.NewDecoder(res.Body).Decode(&response); err != nil {
		return err
	}
	if response.Error != nil {
		return fmt.Errorf("Report failed: %s", response.Error.Message)
	}
	var result mcpTestToolResult
	if err := json.Unmarshal(response.Result, &result); err != nil {
		return err
	}
	if result.IsError {
		return fmt.Errorf("Report failed: %s", result.StructuredContent)
	}
	return nil
}

func deliverReminderTestReport(t *testing.T, p *process, peer *nativeSubmitPeer, topic core.SessionBinding) {
	t.Helper()
	report := peer.next(t, "turn/start")
	require.Equal(t, topic.SessionID, report.session)
	ev := decodeInternalBatch(t, report)[0]
	require.Equal(t, "report_submitted", ev.Kind)
	report.answer <- nativeSubmitAnswer{workID: "review-issue-report", complete: true}
	waitWorkEvent(t, p, ev.ID, "sent")
}
