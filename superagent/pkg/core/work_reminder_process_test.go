package core_test

import (
	"bytes"
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

type reminderExecution struct {
	core.Execution
	ReminderScheduled   bool               `json:"reminderScheduled"`
	ReminderEligibility string             `json:"reminderEligibility"`
	Reminder            workDeliveryRecord `json:"reminder"`
}

func waitWorkerObservation(t *testing.T, p *process, x *core.Execution, state string) reminderExecution {
	t.Helper()
	var observed reminderExecution
	require.Eventually(t, func() bool {
		observed = readRecord[reminderExecution](t, p, "executions/"+x.ID)
		return observed.WorkObservation != nil && observed.WorkObservation.State == state
	}, 5*time.Second, 20*time.Millisecond, "waiting for %s on %s", state, x.ID)
	return observed
}

func startReminderTopic(t *testing.T, f *fixture, p *process, peer *nativeDeliveryPeer) (*stdioClient, core.Binding) {
	t.Helper()
	router := startRoleMCP(t, f, waitBinding(t, p, "router", "", "ready"))
	b, _ := nativeWorkTopic(t, f, p, peer, router, "missing-report")
	return startRoleMCP(t, f, b), b
}

func startReminderWorker(t *testing.T, f *fixture, p *process, peer *nativeDeliveryPeer, topic *stdioClient) (core.WorkReceipt, core.Binding, nativeDeliveryCall) {
	t.Helper()
	req := issueInput(f.dir, "work")
	req.Start = true
	work := mcpWork[core.WorkReceipt](t, topic, "issue_create", req)
	b := waitBinding(t, p, "worker", work.Execution.ID, "ready")
	first := peer.next(t, "turn/start")
	require.Equal(t, b.SessionID, first.session)
	decodeInternalBatch(t, first, work.Execution.StartEventID)
	return work, b, first
}

func watchNative(t *testing.T, massd *client.Client, b core.Binding) *client.Subscription {
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

func checkReminderCall(t *testing.T, peer *nativeDeliveryPeer, b core.Binding, observed reminderExecution) nativeDeliveryCall {
	t.Helper()
	require.True(t, observed.ReminderScheduled)
	require.NotEmpty(t, observed.ReminderEventID)
	require.Equal(t, observed.ReminderEventID, observed.Reminder.ID)
	call := peer.next(t, "turn/start")
	require.Equal(t, b.SessionID, call.session)
	ev := decodeInternalBatch(t, call, observed.ReminderEventID)[0]
	require.Equal(t, reportReminder, ev.Kind)
	require.Equal(t, b.OwnerID, ev.Recipient.ExecutionID)
	require.Contains(t, string(ev.Content.Body), `"executionId":"`+b.OwnerID+`"`)
	require.Contains(t, string(ev.Content.Body), "Do not run the Issue again")
	return call
}

func updateReminderWork(t *testing.T, topic *stdioClient, work core.WorkReceipt) core.WorkReceipt {
	t.Helper()
	return mcpWork[core.WorkReceipt](t, topic, "issue_update_requirements", core.UpdateRequirementsRequest{
		OperationID: "update", IssueID: work.Issue.ID, ExpectedRevision: work.Issue.Revision,
		Requirements: "Also verify the amended sample.", NotifyWorker: true,
	})
}

func TestReportReminderNativeCorrelation(t *testing.T) {
	t.Parallel()
	peer := newNativeDeliveryPeer(t)
	endpoint, massd := startDeliveryMassd(t, peer)
	f, _ := deliveryFixture(t, endpoint)
	p := f.start()
	topic, topicBinding := startReminderTopic(t, f, p, peer)
	work, b, first := startReminderWorker(t, f, p, peer, topic)
	sub := watchNative(t, massd, b)
	first.answer <- nativeDeliveryAnswer{workID: "work-one"}
	waitWorkDelivery(t, p, work.Execution.StartEventID, "sent")
	initial := waitWorkerObservation(t, p, work.Execution, "observing")
	require.Equal(t, "work-one", initial.WorkObservation.WorkID)
	require.True(t, initial.WorkObservation.Continuous)
	nextNativeMethod(t, sub, "turn/started")
	first.idle()
	ev := nextNativeMethod(t, sub, "thread/status/changed")
	require.Equal(t, events.Idle, *ev.Payload().(events.StateUpdate).State)
	require.Equal(t, "awaiting_correlated_end", readRecord[reminderExecution](t, p, "executions/"+work.Execution.ID).ReminderEligibility)
	peer.quiet(t, 300*time.Millisecond)

	update := updateReminderWork(t, topic, work)
	second := peer.next(t, "turn/start")
	require.Equal(t, b.SessionID, second.session)
	decodeInternalBatch(t, second, update.NotificationEventID)
	// The older turn can still produce a real, eligible-shaped terminal fact
	// while the new native submit is waiting for its response.
	first.end("work-one")
	ev = nextNativeMethod(t, sub, "turn/completed")
	require.Equal(t, "completed", ev.Payload().(events.StateUpdate).StopReason)
	require.Equal(t, "work-one", ev.Meta["codex"].(map[string]any)["turnId"])
	require.Empty(t, workDeliveries(t, p, reportReminder))
	second.answer <- nativeDeliveryAnswer{workID: "work-two"}
	waitWorkDelivery(t, p, update.NotificationEventID, "sent")
	nextNativeMethod(t, sub, "turn/started")
	proof := waitWorkerObservation(t, p, work.Execution, "observing").WorkObservation
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
	require.Empty(t, workDeliveries(t, p, reportReminder))

	second.end("work-two")
	ev = nextNativeMethod(t, sub, "turn/completed")
	ended := waitWorkerObservation(t, p, work.Execution, "ended")
	require.Equal(t, ev.EventID, ended.WorkObservation.End.EventID)
	require.Equal(t, "completed", ended.WorkObservation.End.Reason)
	require.Equal(t, b.ID, ended.WorkObservation.BindingID)
	require.Equal(t, b.Generation, ended.WorkObservation.Generation)
	require.Equal(t, b.SessionUID, ended.WorkObservation.SessionUID)
	require.Equal(t, b.SessionID, ended.WorkObservation.SessionID)
	require.True(t, ended.WorkObservation.Continuous)
	require.Equal(t, "already_scheduled", ended.ReminderEligibility)
	reminder := checkReminderCall(t, peer, b, ended)
	reminder.answer <- nativeDeliveryAnswer{workID: "reminder-turn", complete: true}
	waitWorkDelivery(t, p, ended.ReminderEventID, "sent")
	ev = nextNativeMethod(t, sub, "turn/completed")
	require.Equal(t, "reminder-turn", ev.Meta["codex"].(map[string]any)["turnId"])
	peer.quiet(t, 300*time.Millisecond)
	require.Len(t, workDeliveries(t, p, reportReminder), 1)
	saved := readRecord[reminderExecution](t, p, "executions/"+work.Execution.ID)
	require.Equal(t, ended.WorkObservation, saved.WorkObservation)
	require.Equal(t, "sent", saved.Reminder.Status)
	var history store.HistoryPage
	require.NoError(t, massd.History(t.Context(), resource.SessionRef{SessionID: b.SessionID}, store.HistoryQuery{}, &history))
	for _, historical := range history.Events {
		require.NotEqual(t, ended.WorkObservation.End.EventID, historical.EventID)
	}
	require.Equal(t, update.Issue, readRecord[core.Issue](t, p, "issues/"+work.Issue.ID))
	require.Equal(t, "active", readRecord[core.Topic](t, p, "topics/"+topicBinding.OwnerID).Status)
	t.Log("Idle, child completion and a stale earlier turn did not qualify; matching root work scheduled one reminder and the reminder's own completion did not schedule another.")
}

func TestReportReminderAfterCrashAndDuplicate(t *testing.T) {
	t.Parallel()
	peer := newNativeDeliveryPeer(t)
	endpoint, massd := startDeliveryMassd(t, peer)
	f, _ := deliveryFixture(t, endpoint)
	gate := t.TempDir()
	release := filepath.Join(gate, "release")
	write(t, release, "")
	p := f.start("SUPERAGENT_TEST_GATE_STAGE=worker_before_claim", "SUPERAGENT_TEST_GATE_DIR="+gate)
	topic, _ := startReminderTopic(t, f, p, peer)
	work, b, first := startReminderWorker(t, f, p, peer, topic)
	sub := watchNative(t, massd, b)
	require.NoError(t, os.Remove(release))
	first.answer <- nativeDeliveryAnswer{workID: "early-finish", endBeforeReply: true}
	ev := nextNativeMethod(t, sub, "turn/completed")
	ended := waitWorkerObservation(t, p, work.Execution, "ended")
	require.Equal(t, ev.EventID, ended.WorkObservation.End.EventID)
	require.True(t, ended.ReminderScheduled)
	require.Empty(t, waitWorkDelivery(t, p, ended.ReminderEventID, "pending").SentAt)
	peer.quiet(t, 300*time.Millisecond)
	p.stop(t, true)

	settleGate := t.TempDir()
	p = f.start("SUPERAGENT_TEST_GATE_STAGE=internal_delivery_before_settlement", "SUPERAGENT_TEST_GATE_DIR="+settleGate)
	require.Equal(t, ended.WorkObservation, readRecord[reminderExecution](t, p, "executions/"+work.Execution.ID).WorkObservation)
	firstReminder := checkReminderCall(t, peer, b, ended)
	firstReminder.answer <- nativeDeliveryAnswer{workID: "first-reminder", complete: true}
	waitWorkGate(t, settleGate, "internal_delivery_before_settlement")
	require.Empty(t, waitWorkDelivery(t, p, ended.ReminderEventID, "dispatching").SentAt)
	p.stop(t, true)

	p = f.start()
	waitWorkDelivery(t, p, ended.ReminderEventID, "dispatching")
	retry := checkReminderCall(t, peer, b, ended)
	require.Equal(t, firstReminder.text, retry.text)
	retry.answer <- nativeDeliveryAnswer{workID: "duplicate-reminder", complete: true}
	require.NotEmpty(t, waitWorkDelivery(t, p, ended.ReminderEventID, "sent").SentAt)
	peer.quiet(t, 350*time.Millisecond)
	require.Len(t, workDeliveries(t, p, reportReminder), 1)
	t.Log("The CLI sent terminal notification before its prompt response. SIGKILL after scheduling preserved the pending Event; SIGKILL after native reminder acceptance retained the same Event and wire batch on retry.")
}

func TestReportReminderObservationGaps(t *testing.T) {
	t.Parallel()
	peer := newNativeDeliveryPeer(t)
	endpoint, massd := startDeliveryMassd(t, peer)
	sockets := make(chan func(), 8)
	proxy := proxyRPC(t, endpoint, func(method string, disconnect func()) bool {
		if method == "session.subscribe" {
			sockets <- disconnect
		}
		return false
	}, func() {})
	f, _ := deliveryFixture(t, proxy)
	p := f.start()
	topic, _ := startReminderTopic(t, f, p, peer)
	work, b, first := startReminderWorker(t, f, p, peer, topic)
	sub := watchNative(t, massd, b)
	first.answer <- nativeDeliveryAnswer{workID: "work-before-disconnect"}
	waitWorkerObservation(t, p, work.Execution, "observing")
	select {
	case cut := <-sockets:
		cut()
	case <-time.After(4 * time.Second):
		t.Fatal("Core subscription was not intercepted")
	}
	disconnected := waitWorkerObservation(t, p, work.Execution, "unknown")
	require.Equal(t, "live_subscription_interrupted", disconnected.WorkObservation.ObservationGap)
	require.Equal(t, "work-before-disconnect", disconnected.WorkObservation.WorkID)
	require.False(t, disconnected.WorkObservation.Continuous)
	first.end("work-before-disconnect")
	nextNativeMethod(t, sub, "turn/completed")
	peer.quiet(t, 300*time.Millisecond)
	require.Empty(t, workDeliveries(t, p, reportReminder))
	select {
	case <-sockets:
		t.Fatal("Core reattached the interrupted observation")
	default:
	}

	update := updateReminderWork(t, topic, work)
	second := peer.next(t, "turn/start")
	decodeInternalBatch(t, second, update.NotificationEventID)
	second.answer <- nativeDeliveryAnswer{workID: "work-before-offline"}
	live := waitWorkerObservation(t, p, work.Execution, "observing")
	require.Equal(t, "work-before-offline", live.WorkObservation.WorkID)
	p.stop(t, true)
	second.end(live.WorkObservation.WorkID)
	nextNativeMethod(t, sub, "turn/completed")
	p = f.start()
	waitBinding(t, p, "worker", b.OwnerID, "ready")
	restarted := waitWorkerObservation(t, p, work.Execution, "unknown")
	require.Equal(t, "core_observation_interrupted", restarted.WorkObservation.ObservationGap)
	require.Equal(t, live.WorkObservation.WorkID, restarted.WorkObservation.WorkID)
	require.False(t, restarted.ReminderScheduled)
	peer.quiet(t, 350*time.Millisecond)
	require.Empty(t, workDeliveries(t, p, reportReminder))
	t.Log("An interrupted public WebSocket and a Core-offline completion each retained unknown. A later explicit submit obtained a fresh subscription and its own work reference.")
}

func TestReportReminderReportAndCancellationFences(t *testing.T) {
	t.Parallel()
	for _, scenario := range []string{"report-first", "reminder-first", "cancellation"} {
		t.Run(scenario, func(t *testing.T) {
			peer := newNativeDeliveryPeer(t)
			endpoint, massd := startDeliveryMassd(t, peer)
			f, _ := deliveryFixture(t, endpoint)
			gate := t.TempDir()
			stage := "worker_before_rpc"
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
			first.answer <- nativeDeliveryAnswer{workID: "work-with-report-race"}
			waitWorkerObservation(t, p, work.Execution, "observing")
			req := core.ReportRequest{OperationID: "report", Kind: "ready", Summary: "Work was completed before a reminder was needed.", GoalRevision: work.Execution.Revision}
			token := roleToken(t, f, b)
			if scenario == "report-first" {
				submitConcurrentReport(t, p, token, req, gate, release, first, sub)
				deliverReminderTestReport(t, p, peer, topicBinding)
				ended := waitWorkerObservation(t, p, work.Execution, "ended")
				require.False(t, ended.ReminderScheduled)
				require.Equal(t, "report_received", ended.ReminderEligibility)
				require.Empty(t, workDeliveries(t, p, reportReminder))
				return
			}
			require.NoError(t, os.Remove(release))
			require.NoError(t, os.Remove(filepath.Join(gate, stage)))
			first.end("work-with-report-race")
			ended := waitWorkerObservation(t, p, work.Execution, "ended")
			require.True(t, ended.ReminderScheduled)
			waitWorkGate(t, gate, stage)
			waitWorkDelivery(t, p, ended.ReminderEventID, "pending")
			if scenario == "reminder-first" {
				report := roleWork[core.Report](t, p, token, "report_submit", req)
				require.Equal(t, b.OwnerID, report.ExecutionID)
				require.False(t, report.Historical)
				deliverReminderTestReport(t, p, peer, topicBinding)
			} else {
				canceled := mcpWork[core.WorkReceipt](t, topic, "issue_cancel", issueLifecycle("cancel-reminder", work.Issue))
				require.Equal(t, "canceled", canceled.Issue.Status)
				require.Equal(t, "historical", canceled.Execution.Authority)
			}
			write(t, release, "")
			require.Empty(t, waitWorkDelivery(t, p, ended.ReminderEventID, "superseded").SentAt)
			peer.quiet(t, 300*time.Millisecond)
			require.Len(t, workDeliveries(t, p, reportReminder), 1)
			require.Equal(t, ended.ReminderEventID, readRecord[reminderExecution](t, p, "executions/"+b.OwnerID).ReminderEventID)
		})
	}
}

func submitConcurrentReport(t *testing.T, p *process, token string, req core.ReportRequest, gate, release string, first nativeDeliveryCall, sub *client.Subscription) {
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
	body, err := json.Marshal(report)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.endpoint+"/v1/role/report_submit", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return fmt.Errorf("Report returned HTTP %d", res.StatusCode)
	}
	return nil
}

func deliverReminderTestReport(t *testing.T, p *process, peer *nativeDeliveryPeer, topic core.Binding) {
	t.Helper()
	report := peer.next(t, "turn/start")
	require.Equal(t, topic.SessionID, report.session)
	ev := decodeInternalBatch(t, report)[0]
	require.Equal(t, "report_submitted", ev.Kind)
	report.answer <- nativeDeliveryAnswer{workID: "review-worker-report", complete: true}
	waitWorkDelivery(t, p, ev.ID, "sent")
}
