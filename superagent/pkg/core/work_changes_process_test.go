package core_test

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/zoumo/mass/superagent/pkg/core"
)

type issueDetail struct {
	core.Issue
	LatestRequirementsNotification *workDeliveryRecord `json:"latestRequirementsNotification"`
	WorkerSession                  struct {
		ExecutionID string `json:"executionId"`
		State       string `json:"state"`
		ObservedAt  string `json:"observedAt"`
	} `json:"workerSession"`
}

func blockers(t *testing.T, topic *stdioClient, issue core.Issue, op string, add ...core.BlockerInput) core.Issue {
	t.Helper()
	return mcpWork[core.WorkReceipt](t, topic, "issue_update_blockers", core.UpdateBlockersRequest{
		OperationID: op, IssueID: issue.ID, ExpectedRevision: issue.Revision, Add: add,
	}).Issue
}

func human(reason string) core.BlockerInput {
	return core.BlockerInput{Kind: "human", Reason: reason}
}

func dependency(issue core.Issue) core.BlockerInput {
	return core.BlockerInput{Kind: "dependency", Reason: "Waiting for prerequisite results", DependencyIssueID: issue.ID}
}

func TestIssueBlockersAndRequirementsThroughNativeDelivery(t *testing.T) {
	t.Parallel()
	peer := newNativeDeliveryPeer(t)
	endpoint, _ := startDeliveryMassd(t, peer)
	f, _ := deliveryFixture(t, endpoint)
	p := f.start()
	router := startRoleMCP(t, f, waitBinding(t, p, "router", "", "ready"))
	topicBinding, source := nativeWorkTopic(t, f, p, peer, router, "changes-main")
	foreignBinding, _ := nativeWorkTopic(t, f, p, peer, router, "changes-foreign")
	topic, foreign := startRoleMCP(t, f, topicBinding), startRoleMCP(t, f, foreignBinding)
	token := roleToken(t, f, topicBinding)
	create := func(c *stdioClient, op string) core.Issue {
		return mcpWork[core.WorkReceipt](t, c, "issue_create", issueInput(f.dir, op)).Issue
	}
	a, b, c := create(topic, "a"), create(topic, "b"), create(topic, "c")
	other := create(foreign, "foreign")
	start := mcpWork[core.WorkReceipt](t, topic, "issue_start", startRequest("start-a", a))
	require.Equal(t, a.RequirementsRevision, start.Execution.Revision)
	workerBinding := waitBinding(t, p, "worker", start.Execution.ID, "ready")
	worker := startRoleMCP(t, f, workerBinding)
	native := peer.next(t, "turn/start")
	require.Equal(t, workerBinding.SessionID, native.session)
	startEvent := decodeInternalBatch(t, native, start.Execution.StartEventID)[0]
	require.Equal(t, "worker_started", startEvent.Kind)
	require.Empty(t, startEvent.Refs)
	native.accept()
	waitWorkDelivery(t, p, start.Execution.StartEventID, "sent")
	a = start.Issue
	a = blockers(t, topic, a, "block-a", human("Verify logs"), dependency(b), human("Get approval"))
	c = blockers(t, topic, c, "block-c", human("Confirm handoff"), dependency(a))
	require.Equal(t, 1, a.RequirementsRevision)
	require.Equal(t, "blocked", a.Status)
	require.Len(t, a.Blockers, 3)

	for _, invalid := range []struct {
		code   string
		target core.Issue
	}{
		{"not_found", other},
		{"not_found", core.Issue{ID: "issue_missing"}},
		{"invalid_argument", b},
		{"conflict", a}, // B → A → B
		{"conflict", c}, // B → C → A → B
	} {
		r := core.UpdateBlockersRequest{
			OperationID: "bad-" + invalid.target.ID, IssueID: b.ID, ExpectedRevision: b.Revision,
			Add: []core.BlockerInput{human("Roll this back"), dependency(invalid.target)},
		}
		mcpWorkError(t, topic, "issue_update_blockers", invalid.code, r)
		require.Equal(t, b, readRecord[core.Issue](t, p, "issues/"+b.ID))
	}
	mcpWorkError(t, foreign, "work_query", "not_found", core.QueryRequest{Collection: "issues", ID: a.ID})
	premature := core.UpdateRequirementsRequest{
		OperationID: "premature", IssueID: b.ID, ExpectedRevision: b.Revision, Goal: "Pending update", NotifyWorker: true,
	}
	mcpWorkError(t, topic, "issue_update_requirements", "failed_precondition", premature)
	require.Equal(t, b, readRecord[core.Issue](t, p, "issues/"+b.ID))
	require.Empty(t, workDeliveries(t, p, "worker_update"))

	require.Eventually(t, func() bool {
		detail := readRecord[issueDetail](t, p, "issues/"+a.ID)
		return detail.Status == "blocked" && detail.WorkerSession.ExecutionID == start.Execution.ID &&
			detail.WorkerSession.State == "running" && detail.WorkerSession.ObservedAt != ""
	}, 7*time.Second, 20*time.Millisecond)

	release := core.UpdateBlockersRequest{
		OperationID: "one-blocker", IssueID: a.ID, ExpectedRevision: a.Revision, Resolve: []core.BlockerResolution{{BlockerID: a.Blockers[0].ID, Reason: "The logs were reviewed", MessageID: source.MessageID}},
	}
	prior := a
	a = mcpWork[core.WorkReceipt](t, topic, "issue_update_blockers", release).Issue
	require.Equal(t, prior.Blockers[1:], a.Blockers)
	require.Len(t, a.ResolvedBlockers, 1)
	require.Equal(t, release.Resolve[0], a.ResolvedBlockers[0].Resolution.BlockerResolution)
	require.NotEmpty(t, a.ResolvedBlockers[0].Resolution.At)
	stale := release
	stale.ExpectedRevision = a.Revision
	stale.OperationID = "already-resolved"
	mcpWorkError(t, topic, "issue_update_blockers", "not_found", stale)
	require.Equal(t, "blocked", a.Status)
	require.Equal(t, start.Execution.ID, a.CurrentExecutionID)
	require.Equal(t, prior.RequirementsRevision, a.RequirementsRevision)
	mcpWorkError(t, topic, "issue_start", "conflict", core.StartIssueRequest{
		OperationID: "duplicate-start-with-resolution", IssueID: a.ID, ExpectedRevision: a.Revision,
		Resolve: []core.BlockerResolution{{BlockerID: a.Blockers[0].ID, Reason: "The logs were reviewed", MessageID: source.MessageID}},
	})
	require.Equal(t, a, readRecord[core.Issue](t, p, "issues/"+a.ID))
	structured := markdownReply(strings.Repeat("日志", 5300))
	context := core.WorkerUpdateRequest{
		OperationID: "background", IssueID: a.ID, ExpectedRevision: a.Revision, Mode: "context_supplement",
		Content: &structured, Materials: []core.Material{{MessageID: source.MessageID}},
	}
	supplement := mcpWork[core.WorkReceipt](t, topic, "issue_worker_update", context)
	require.Equal(t, supplement, mcpWork[core.WorkReceipt](t, topic, "issue_worker_update", context))
	require.Equal(t, a, supplement.Issue)
	call := peer.next(t, "thread/inject_items")
	require.Equal(t, workerBinding.SessionID, call.session)
	trimmed := decodeInternalBatch(t, call, supplement.NotificationEventID)[0]
	require.True(t, trimmed.Truncated)
	require.Equal(t, "context_supplement", trimmed.Kind)
	lookup := map[string]string{"delivery_id": supplement.NotificationEventID}
	full := mcpWork[internalEvent](t, worker, "delivery_get", lookup)
	require.Equal(t, context.Content, full.Content)
	require.Nil(t, trimmed.Content)
	require.Empty(t, full.Refs)
	require.JSONEq(t, `{"materials":[{"messageId":"`+source.MessageID+`"}]}`, string(full.Body))
	mcpWorkError(t, foreign, "delivery_get", "not_found", lookup)
	call.accept()
	waitWorkDelivery(t, p, supplement.NotificationEventID, "sent")
	savedsupplement, err := json.Marshal(structured)
	require.NoError(t, err)
	require.JSONEq(t, string(savedsupplement), string(waitInternalDelivery(t, f, supplement.NotificationEventID, "sent").Content))
	choices := core.StructuredContent{Type: "options", Data: json.RawMessage(`{"prompt":"**Choose an archive.**","options":[{"id":"recent","label":"Recent"},{"id":"older","label":"Older"}]}`)}
	choiceRequest := core.WorkerUpdateRequest{OperationID: "choices", IssueID: a.ID, ExpectedRevision: a.Revision, Mode: "context_supplement", Content: &choices}
	invalidChoices := choiceRequest
	invalidChoices.Content = &core.StructuredContent{Type: "options", Data: json.RawMessage(`{"prompt":"Choose an archive."}`)}
	mcpWorkError(t, topic, "issue_worker_update", "invalid_argument", invalidChoices)
	choiceReceipt := mcpWork[core.WorkReceipt](t, topic, "issue_worker_update", choiceRequest)
	call = peer.next(t, "thread/inject_items")
	choiceEvent := decodeInternalBatch(t, call, choiceReceipt.NotificationEventID)[0]
	require.Equal(t, &choices, choiceEvent.Content)
	choiceLookup := map[string]string{"delivery_id": choiceReceipt.NotificationEventID}
	require.Equal(t, &choices, mcpWork[internalEvent](t, worker, "delivery_get", choiceLookup).Content)
	call.accept()
	waitWorkDelivery(t, p, choiceReceipt.NotificationEventID, "sent")
	savedchoiceReceipt, err := json.Marshal(choices)
	require.NoError(t, err)
	require.JSONEq(t, string(savedchoiceReceipt), string(waitInternalDelivery(t, f, choiceReceipt.NotificationEventID, "sent").Content))
	materialsOnly := context
	materialsOnly.OperationID, materialsOnly.Content = "materials-only", nil
	materialsReceipt := mcpWork[core.WorkReceipt](t, topic, "issue_worker_update", materialsOnly)
	call = peer.next(t, "thread/inject_items")
	materialsEvent := decodeInternalBatch(t, call, materialsReceipt.NotificationEventID)[0]
	require.Nil(t, materialsEvent.Content)
	require.JSONEq(t, `{"materials":[{"messageId":"`+source.MessageID+`"}]}`, string(materialsEvent.Body))
	require.Len(t, materialsEvent.Messages, 1)
	require.JSONEq(t, `{"id":"`+source.MessageID+`","text":"Original input changes-main","actor":"sender-1"}`, string(materialsEvent.Messages[0]))
	call.accept()
	require.Empty(t, waitInternalDelivery(t, f, materialsReceipt.NotificationEventID, "sent").Content)
	context.Mode, context.OperationID = "continue", "too-early"
	mcpWorkError(t, topic, "issue_worker_update", "failed_precondition", context)
	peer.quiet(t, 300*time.Millisecond)

	silent := core.UpdateRequirementsRequest{OperationID: "silent", IssueID: a.ID, ExpectedRevision: a.Revision, Goal: "Read this on the next query."}
	missingRevision := silent
	missingRevision.ExpectedRevision = 0
	mcpWorkError(t, topic, "issue_update_requirements", "invalid_argument", missingRevision)
	mcpWorkError(t, foreign, "issue_update_requirements", "not_found", silent)
	mcpWorkError(t, worker, "issue_update_requirements", "forbidden", silent)
	a = mcpWork[core.WorkReceipt](t, topic, "issue_update_requirements", silent).Issue
	require.Equal(t, 2, a.RequirementsRevision)
	unchanged := silent
	unchanged.OperationID, unchanged.ExpectedRevision = "same-goal", a.Revision
	require.Equal(t, a, mcpWork[core.WorkReceipt](t, topic, "issue_update_requirements", unchanged).Issue)
	require.Equal(t, a, mcpWork[core.WorkReceipt](t, worker, "issue_get_current", struct{}{}).Issue)
	require.Empty(t, workDeliveries(t, p, "worker_update"))
	peer.quiet(t, 300*time.Millisecond)

	complete := core.CompleteIssueRequest{IssueID: b.ID, CompleteRequest: acceptance("complete-b", b.Revision, "verified.txt")}
	mcpWork[core.WorkReceipt](t, topic, "issue_complete", complete)
	mcpWorkError(t, topic, "issue_complete", "conflict", complete)
	facts := dependencyDeliveries(t, f)
	require.Len(t, facts, 1)
	factInput := peer.next(t, "thread/inject_items")
	require.Equal(t, topicBinding.SessionID, factInput.session)
	fact := decodeInternalBatch(t, factInput, facts[0].ID)[0]
	require.Equal(t, "context_supplement", fact.Kind)
	require.Empty(t, fact.Refs)
	require.JSONEq(t, `{"type":"issue.dependency_changed","dependencyIssueId":"`+b.ID+`","dependentIssueIds":["`+a.ID+`"],"previousStatus":"pending","status":"completed"}`, string(fact.Body))
	query := map[string]string{"delivery_id": fact.ID}
	fullFact := mcpWork[internalEvent](t, topic, "delivery_get", query)
	require.Equal(t, fact, fullFact)
	mcpWorkError(t, foreign, "delivery_get", "not_found", query)
	factInput.accept()
	require.NotEmpty(t, waitInternalDelivery(t, f, fact.ID, "sent").SentAt)
	require.Equal(t, a, readRecord[core.Issue](t, p, "issues/"+a.ID))
	require.Equal(t, c, readRecord[core.Issue](t, p, "issues/"+c.ID))
	require.Len(t, page(t, p, "executions").Items, 1)
	peer.quiet(t, 300*time.Millisecond)
	t.Log("dependency completion injected a context fact into Topic; no dependent state change, native stop or follow-on start")

	partial := core.UpdateBlockersRequest{
		OperationID: "remaining-human", IssueID: a.ID, ExpectedRevision: a.Revision,
		Resolve:      []core.BlockerResolution{{BlockerID: a.Blockers[1].ID, Reason: "Approval confirmed"}},
		NotifyWorker: true,
	}
	mcpWorkError(t, topic, "issue_update_blockers", "failed_precondition", partial)
	require.Equal(t, a, readRecord[core.Issue](t, p, "issues/"+a.ID))
	partial.NotifyWorker = false
	a = mcpWork[core.WorkReceipt](t, topic, "issue_update_blockers", partial).Issue
	require.Equal(t, "blocked", a.Status)
	require.Len(t, a.Blockers, 1)
	require.Equal(t, "dependency", a.Blockers[0].Kind)
	blockedStart := startRequest("blocked-start-c", c)
	blockedStart.Resolve = []core.BlockerResolution{{BlockerID: c.Blockers[0].ID, Reason: "Handoff verified"}}
	mcpWorkError(t, topic, "issue_start", "failed_precondition", blockedStart)
	require.Equal(t, c, readRecord[core.Issue](t, p, "issues/"+c.ID))
	resolve := core.UpdateBlockersRequest{
		OperationID: "resolve-dependency", IssueID: a.ID, ExpectedRevision: a.Revision,
		Resolve: []core.BlockerResolution{{BlockerID: a.Blockers[0].ID, Reason: "Verified dependency completion"}},
	}
	mcpWorkError(t, topic, "issue_update_blockers", "invalid_argument", resolve)
	resolve.UnblockedStatus, resolve.NotifyWorker = "active", true
	resolved := mcpWork[core.WorkReceipt](t, topic, "issue_update_blockers", resolve)
	mcpWorkError(t, topic, "issue_update_blockers", "conflict", resolve)
	require.Equal(t, "active", resolved.Issue.Status)
	require.Empty(t, resolved.Issue.Blockers)
	require.Len(t, resolved.Issue.ResolvedBlockers, 3)
	require.Equal(t, a.RequirementsRevision, resolved.Issue.RequirementsRevision)
	resumed := peer.next(t, "turn/start")
	require.Equal(t, workerBinding.SessionID, resumed.session)
	require.Equal(t, "worker_update", decodeInternalBatch(t, resumed, resolved.NotificationEventID)[0].Kind)
	resumed.accept()
	waitWorkDelivery(t, p, resolved.NotificationEventID, "sent")
	a = resolved.Issue
	continueCard := markdownReply("**Continue reviewing the archive.**")
	continued := mcpWork[core.WorkReceipt](t, topic, "issue_worker_update", core.WorkerUpdateRequest{
		OperationID: "continue-with-content", IssueID: a.ID, ExpectedRevision: a.Revision, Mode: "continue", Content: &continueCard,
	})
	continuedInput := peer.next(t, "turn/start")
	require.Equal(t, workerBinding.SessionID, continuedInput.session)
	continuedEvent := decodeInternalBatch(t, continuedInput, continued.NotificationEventID)[0]
	require.Equal(t, &continueCard, continuedEvent.Content)
	var deliveredIssue core.Issue
	require.NoError(t, json.Unmarshal(continuedEvent.Issue, &deliveredIssue))
	require.Equal(t, a.Requirements, deliveredIssue.Requirements)
	require.Equal(t, a.RequirementsRevision, deliveredIssue.RequirementsRevision)
	require.Equal(t, "active", deliveredIssue.Status)
	require.Empty(t, deliveredIssue.Blockers)
	continuedInput.accept()
	waitWorkDelivery(t, p, continued.NotificationEventID, "sent")

	type updateResult struct {
		req    core.UpdateRequirementsRequest
		status int
		body   []byte
	}
	results := make(chan updateResult, 2)
	race := make(chan struct{})
	for _, suffix := range []string{"α", "β"} {
		go func() {
			r := core.UpdateRequirementsRequest{
				OperationID: "notify-" + suffix, IssueID: a.ID, ExpectedRevision: a.Revision,
				Goal: strings.Repeat(suffix, 5300), NotifyWorker: true,
			}
			<-race
			status, body := request(t, http.MethodPost, p.endpoint, "/v1/role/issue_update_requirements", token, r)
			results <- updateResult{r, status, body}
		}()
	}
	close(race)
	var winner updateResult
	var accepted, conflicts int
	for range 2 {
		select {
		case result := <-results:
			if result.status == http.StatusOK {
				winner = result
				accepted++
			} else {
				errorCode(t, result.status, result.body, http.StatusConflict, "conflict")
				var conflict struct{ Error core.Error }
				require.NoError(t, json.Unmarshal(result.body, &conflict))
				require.Equal(t, a.Revision+1, conflict.Error.CurrentRevision)
				conflicts++
			}
		case <-time.After(10 * time.Second):
			t.Fatal("requirements update did not settle")
		}
	}
	require.Equal(t, 1, accepted)
	require.Equal(t, 1, conflicts)
	var updateResponse struct{ Result core.WorkReceipt }
	require.NoError(t, json.Unmarshal(winner.body, &updateResponse))
	updated := updateResponse.Result
	mcpWorkError(t, topic, "issue_update_requirements", "conflict", winner.req)
	require.Equal(t, winner.req.Goal, updated.Issue.Requirements)
	require.Equal(t, a.RequirementsRevision+1, updated.Issue.RequirementsRevision)
	require.NotEmpty(t, updated.NotificationEventID)
	notice := peer.next(t, "turn/start")
	require.Equal(t, workerBinding.SessionID, notice.session)
	wire := decodeInternalBatch(t, notice, updated.NotificationEventID)[0]
	require.Equal(t, "worker_update", wire.Kind)
	require.Empty(t, wire.Body)
	require.Empty(t, wire.Refs)
	require.Empty(t, wire.Issue)
	notice.accept()
	waitWorkDelivery(t, p, updated.NotificationEventID, "sent")
	require.Len(t, workDeliveries(t, p, "worker_update"), 3)
	view := mcpWork[issueDetail](t, topic, "work_query", core.QueryRequest{Collection: "issues", ID: a.ID})
	require.Equal(t, updated.Issue, view.Issue)
	require.NotNil(t, view.LatestRequirementsNotification)
	require.Equal(t, "sent", view.LatestRequirementsNotification.Status)
	require.Equal(t, updated.Issue.RequirementsRevision, view.LatestRequirementsNotification.Revision)
	require.Equal(t, updated.Issue, mcpWork[core.WorkReceipt](t, worker, "issue_get_current", struct{}{}).Issue)
	t.Log("Concurrent updates at the same Issue revision committed once, and the Worker read the full goal through issue_get_current.")

	a = updated.Issue
	reportReq := core.ReportRequest{OperationID: "report", Kind: "progress", Summary: "Applied updated requirements", GoalRevision: a.Revision}
	mcpWorkError(t, worker, "report_submit", "invalid_argument", reportReq)
	reportReq.GoalRevision = a.RequirementsRevision
	report := mcpWork[core.Report](t, worker, "report_submit", reportReq)
	require.Equal(t, a.RequirementsRevision, report.Revision)
	reportInput := peer.next(t, "turn/start")
	reportEvent := decodeInternalBatch(t, reportInput, report.EventID)[0]
	require.Empty(t, reportEvent.Report)
	require.Empty(t, reportEvent.Issue)
	require.JSONEq(t, `{"kind":"report","id":"`+report.ID+`"}`, string(reportEvent.Refs[0]))
	reportInput.answer <- nativeDeliveryAnswer{complete: true}
	waitWorkDelivery(t, p, report.EventID, "sent")

	rejected := mcpWork[core.WorkReceipt](t, topic, "issue_update_requirements", core.UpdateRequirementsRequest{
		OperationID: "native-rejection", IssueID: a.ID, ExpectedRevision: a.Revision,
		Goal: "Keep this requirement when delivery fails.", NotifyWorker: true,
	})
	rejectedInput := peer.next(t, "turn/start")
	require.Equal(t, workerBinding.SessionID, rejectedInput.session)
	decodeInternalBatch(t, rejectedInput, rejected.NotificationEventID)
	rejectedInput.answer <- nativeDeliveryAnswer{code: -32602, message: "failed to submit turn input: PlanMode"}
	require.Empty(t, waitWorkDelivery(t, p, rejected.NotificationEventID, "pending").SentAt)
	observation := readRecord[issueDetail](t, p, "issues/"+a.ID)
	require.Equal(t, []core.Report{report}, observation.Reports)
	require.Equal(t, rejected.Issue, observation.Issue)
	require.NotNil(t, observation.LatestRequirementsNotification)
	require.Equal(t, rejected.NotificationEventID, observation.LatestRequirementsNotification.ID)
	require.Equal(t, "pending", observation.LatestRequirementsNotification.Status)
	retry := peer.next(t, "turn/start")
	require.Equal(t, rejectedInput.text, retry.text)
	retry.accept()
	waitWorkDelivery(t, p, rejected.NotificationEventID, "sent")
	peer.quiet(t, 300*time.Millisecond)
	waiting := blockers(t, topic, rejected.Issue, "wait-for-confirmation", human("Wait for final confirmation"))
	pending := mcpWork[core.WorkReceipt](t, topic, "issue_update_blockers", core.UpdateBlockersRequest{
		OperationID: "resolve-and-leave-pending", IssueID: waiting.ID, ExpectedRevision: waiting.Revision, UnblockedStatus: "pending",
		Resolve: []core.BlockerResolution{{BlockerID: waiting.Blockers[0].ID, Reason: "Confirmation received"}},
	})
	require.Equal(t, "pending", pending.Issue.Status)
	require.Empty(t, pending.Issue.CurrentExecutionID)
	require.Equal(t, "historical", pending.Execution.Authority)
	require.Empty(t, pending.NotificationEventID)
	_ = waitObservedStop(t, p, "executions", workerBinding.OwnerID)
	require.Len(t, page(t, p, "executions").Items, 1)
	t.Log("an explicit native rejection left the committed requirements readable and kept the update pending")
}

func TestIssueRequirementsCommitCrash(t *testing.T) {
	for _, stage := range []string{"requirements_before_notification", "requirements_after_commit"} {
		t.Run(stage, func(t *testing.T) {
			boundary := newRouteBoundary(t)
			f := fixtureAt(t)
			f.execution(boundary)
			gate := t.TempDir()
			p := f.start("SUPERAGENT_TEST_GATE_STAGE="+stage, "SUPERAGENT_TEST_GATE_DIR="+gate)
			topic, token := workBoundaryTopic(t, f, p)
			create := issueInput(f.dir, "create")
			create.Start = true
			work := roleWork[core.WorkReceipt](t, p, token, "issue_create", create)
			binding := waitBinding(t, p, "worker", work.Execution.ID, "ready")
			waitWorkDelivery(t, p, work.Execution.StartEventID, "sent")
			r := core.UpdateRequirementsRequest{
				OperationID: "crash-update", IssueID: work.Issue.ID,
				ExpectedRevision: work.Issue.Revision, Goal: "Verify the crash-safe requirements", NotifyWorker: true,
			}
			payload, err := json.Marshal(r)
			require.NoError(t, err)
			req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, p.endpoint+"/v1/role/issue_update_requirements", bytes.NewReader(payload))
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
			beforeReplay := readRecord[issueDetail](t, p, "issues/"+r.IssueID)
			events := workDeliveries(t, p, "worker_update")
			if stage == "requirements_before_notification" {
				require.Equal(t, work.Issue, beforeReplay.Issue)
				require.Nil(t, beforeReplay.LatestRequirementsNotification)
				require.Empty(t, events)
			} else {
				require.Equal(t, r.Goal, beforeReplay.Requirements)
				require.Equal(t, work.Issue.RequirementsRevision+1, beforeReplay.RequirementsRevision)
				require.Len(t, events, 1)
				require.Equal(t, beforeReplay.LatestRequirementsEventID, events[0].ID)
			}
			mcp := startRoleMCP(t, f, topic)
			var saved core.WorkReceipt
			if stage == "requirements_after_commit" {
				mcpWorkError(t, mcp, "issue_update_requirements", "conflict", r)
				saved = core.WorkReceipt{Issue: beforeReplay.Issue, NotificationEventID: beforeReplay.LatestRequirementsEventID}
			} else {
				saved = mcpWork[core.WorkReceipt](t, mcp, "issue_update_requirements", r)
			}
			mcpWorkError(t, mcp, "issue_update_requirements", "conflict", r)
			require.Equal(t, r.Goal, saved.Issue.Requirements)
			require.Equal(t, work.Issue.RequirementsRevision+1, saved.Issue.RequirementsRevision)
			require.Len(t, workDeliveries(t, p, "worker_update"), 1)
			require.Equal(t, binding.OwnerID, waitWorkDelivery(t, p, saved.NotificationEventID, "sent").Recipient.ExecutionID)
			t.Logf("%s restart saw %d committed notifications and matching requirements; replay delivered one event", stage, len(events))
		})
	}
}

func TestWorkerUpdateFencesGoalAndBlockersBeforeRPC(t *testing.T) {
	t.Parallel()
	peer := newNativeDeliveryPeer(t)
	endpoint, _ := startDeliveryMassd(t, peer)
	f, operatorFile := deliveryFixture(t, endpoint)
	gate := t.TempDir()
	stage := "worker_before_rpc"
	release := filepath.Join(gate, "release")
	write(t, release, "")
	p := f.start("SUPERAGENT_TEST_GATE_STAGE="+stage, "SUPERAGENT_TEST_GATE_DIR="+gate)
	t.Cleanup(func() { write(t, release, "") })
	topic, _ := startReminderTopic(t, f, p, peer)
	work, b, start := startReminderWorker(t, f, p, peer, topic)
	start.accept()
	waitWorkDelivery(t, p, work.Execution.StartEventID, "sent")
	issue := work.Issue
	for _, change := range []string{"goal", "blocker"} {
		require.NoError(t, os.Remove(release))
		require.NoError(t, os.Remove(filepath.Join(gate, stage)))
		req := core.WorkerUpdateRequest{OperationID: "continue-before-" + change, IssueID: issue.ID, ExpectedRevision: issue.Revision, Mode: "continue"}
		queued := mcpWork[core.WorkReceipt](t, topic, "issue_worker_update", req)
		waitWorkGate(t, gate, stage)
		require.Empty(t, waitWorkDelivery(t, p, queued.NotificationEventID, "pending").SentAt)
		if change == "goal" {
			issue = mcpWork[core.WorkReceipt](t, topic, "issue_update_requirements", core.UpdateRequirementsRequest{
				OperationID: "new-goal", IssueID: issue.ID, ExpectedRevision: issue.Revision, Goal: "An amended goal.",
			}).Issue
		} else {
			issue = blockers(t, topic, issue, "need-more-input", human("Wait for confirmation"))
		}
		write(t, release, "")
		require.Empty(t, waitWorkDelivery(t, p, queued.NotificationEventID, "superseded").SentAt)
		peer.quiet(t, 250*time.Millisecond)
		require.Equal(t, b.OwnerID, readRecord[core.Issue](t, p, "issues/"+issue.ID).CurrentExecutionID)
	}
	recovery := recoveryFor("replace-blocked-worker", "replace", b.ID)
	recovery.StopRuntime = true
	released := recoverRoleBinding(t, p, operatorFile, recovery)
	require.Equal(t, "runtime_stopped", released.Close.Method)
	require.Empty(t, released.ReplacementBinding)
	require.Empty(t, readRecord[core.Issue](t, p, "issues/"+issue.ID).CurrentExecutionID)
	require.Equal(t, "blocked", readRecord[core.Issue](t, p, "issues/"+issue.ID).Status)
	require.Equal(t, "historical", readRecord[core.Execution](t, p, "executions/"+b.OwnerID).Authority)
	require.Len(t, page(t, p, "executions").Items, 1)
}
