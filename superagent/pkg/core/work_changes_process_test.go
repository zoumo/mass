package core_test

import (
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
	LatestRequirementsNotification *workEventRecord `json:"latestRequirementsNotification"`
	IssueSession                   struct {
		SessionBindingID string `json:"sessionBindingId"`
		State            string `json:"state"`
		ObservedAt       string `json:"observedAt"`
	} `json:"issueSession"`
}

func blockers(t *testing.T, topic *roleMCPClient, issue core.Issue, op string, add ...core.BlockerInput) core.Issue {
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

func TestIssueBlockersAndRequirementsThroughNativeEvent(t *testing.T) {
	t.Parallel()
	peer := newNativeSubmitPeer(t)
	endpoint, _ := startEventTestMassd(t, peer)
	f, _ := eventFixture(t, endpoint)
	p := f.start()
	router := startRoleMCP(t, f, waitBinding(t, p, "router", "", "ready"))
	topicBinding, source := nativeWorkTopic(t, f, p, peer, router, "changes-main")
	foreignBinding, _ := nativeWorkTopic(t, f, p, peer, router, "changes-foreign")
	topic, foreign := startRoleMCP(t, f, topicBinding), startRoleMCP(t, f, foreignBinding)
	token := roleToken(t, f, topicBinding)
	create := func(c *roleMCPClient, op string) core.Issue {
		return mcpWork[core.WorkReceipt](t, c, "create_issue", issueInput(f.dir, op)).Issue
	}
	a, b, c := create(topic, "a"), create(topic, "b"), create(topic, "c")
	other := create(foreign, "foreign")
	start := mcpWork[core.WorkReceipt](t, topic, "start_issue", startRequest("start-a", a))
	require.Equal(t, a.RequirementsRevision, start.SessionBinding.StartGoalRevision)
	issueBinding := waitBinding(t, p, "issue", start.SessionBinding.ID, "ready")
	issue := startRoleMCP(t, f, issueBinding)
	native := peer.next(t, "turn/start")
	require.Equal(t, issueBinding.SessionID, native.session)
	startEvent := decodeInternalBatch(t, native, start.SessionBinding.StartEventID)[0]
	require.Equal(t, "issue_session_started", startEvent.Kind)
	require.Empty(t, startEvent.Refs)
	native.accept()
	waitWorkEvent(t, p, start.SessionBinding.StartEventID, "sent")
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
	mcpWorkError(t, foreign, "get_issue", "not_found", map[string]string{"issueId": a.ID})
	premature := core.UpdateRequirementsRequest{
		OperationID: "premature", IssueID: b.ID, ExpectedRevision: b.Revision, Goal: "Pending update", NotifyIssueSession: true,
	}
	mcpWorkError(t, topic, "issue_update_requirements", "failed_precondition", premature)
	require.Equal(t, b, readRecord[core.Issue](t, p, "issues/"+b.ID))
	require.Empty(t, workEvents(t, p, "issue_session_update"))

	require.Eventually(t, func() bool {
		detail := readRecord[issueDetail](t, p, "issues/"+a.ID)
		return detail.Status == "blocked" && detail.IssueSession.SessionBindingID == start.SessionBinding.ID &&
			detail.IssueSession.State == "running" && detail.IssueSession.ObservedAt != ""
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
	require.Equal(t, start.SessionBinding.ID, a.CurrentSessionBindingID)
	require.Equal(t, prior.RequirementsRevision, a.RequirementsRevision)
	mcpWorkError(t, topic, "start_issue", "conflict", core.StartIssueRequest{
		OperationID: "duplicate-start-with-resolution", IssueID: a.ID, ExpectedRevision: a.Revision,
		Resolve: []core.BlockerResolution{{BlockerID: a.Blockers[0].ID, Reason: "The logs were reviewed", MessageID: source.MessageID}},
	})
	require.Equal(t, a, readRecord[core.Issue](t, p, "issues/"+a.ID))
	structured := markdownReply(strings.Repeat("日志", 5300))
	context := core.IssueSessionInputRequest{
		OperationID: "background", IssueID: a.ID, ExpectedRevision: a.Revision, Mode: "context_supplement",
		Content: &structured, Materials: []core.Material{{MessageID: source.MessageID}},
	}
	supplement := mcpWork[core.WorkReceipt](t, topic, "issue_session_input", context)
	mcpWorkError(t, topic, "issue_session_input", "conflict", context)
	require.Equal(t, a.Revision+1, supplement.Issue.Revision)
	require.Equal(t, a.Materials, supplement.Issue.Materials)
	call := peer.next(t, "thread/inject_items")
	require.Equal(t, issueBinding.SessionID, call.session)
	trimmed := decodeInternalBatch(t, call, supplement.NotificationEventID)[0]
	require.True(t, trimmed.Truncated)
	require.Equal(t, "context_supplement", trimmed.Kind)
	lookup := map[string]string{"event_id": supplement.NotificationEventID}
	full := mcpWork[internalEvent](t, issue, "get_internal_event", lookup)
	require.Equal(t, context.Content, full.Content)
	require.Nil(t, trimmed.Content)
	require.Empty(t, full.Refs)
	require.JSONEq(t, `{"materials":[{"messageId":"`+source.MessageID+`"}]}`, string(full.Body))
	mcpWorkError(t, foreign, "get_internal_event", "not_found", lookup)
	call.accept()
	waitWorkEvent(t, p, supplement.NotificationEventID, "sent")
	savedsupplement, err := json.Marshal(structured)
	require.NoError(t, err)
	require.JSONEq(t, string(savedsupplement), string(waitInternalEvent(t, f, supplement.NotificationEventID, "sent").Content))
	choices := core.StructuredContent{Type: "options", Data: json.RawMessage(`{"prompt":"**Choose an archive.**","options":[{"id":"recent","label":"Recent"},{"id":"older","label":"Older"}]}`)}
	a = supplement.Issue
	choiceRequest := core.IssueSessionInputRequest{OperationID: "choices", IssueID: a.ID, ExpectedRevision: a.Revision, Mode: "context_supplement", Content: &choices}
	invalidChoices := choiceRequest
	invalidChoices.Content = &core.StructuredContent{Type: "options", Data: json.RawMessage(`{"prompt":"Choose an archive."}`)}
	mcpWorkError(t, topic, "issue_session_input", "invalid_argument", invalidChoices)
	choiceReceipt := mcpWork[core.WorkReceipt](t, topic, "issue_session_input", choiceRequest)
	call = peer.next(t, "thread/inject_items")
	choiceEvent := decodeInternalBatch(t, call, choiceReceipt.NotificationEventID)[0]
	require.Equal(t, &choices, choiceEvent.Content)
	choiceLookup := map[string]string{"event_id": choiceReceipt.NotificationEventID}
	require.Equal(t, &choices, mcpWork[internalEvent](t, issue, "get_internal_event", choiceLookup).Content)
	call.accept()
	waitWorkEvent(t, p, choiceReceipt.NotificationEventID, "sent")
	savedchoiceReceipt, err := json.Marshal(choices)
	require.NoError(t, err)
	require.JSONEq(t, string(savedchoiceReceipt), string(waitInternalEvent(t, f, choiceReceipt.NotificationEventID, "sent").Content))
	materialsOnly := context
	materialsOnly.OperationID, materialsOnly.Content, materialsOnly.ExpectedRevision = "materials-only", nil, choiceReceipt.Issue.Revision
	materialsReceipt := mcpWork[core.WorkReceipt](t, topic, "issue_session_input", materialsOnly)
	call = peer.next(t, "thread/inject_items")
	materialsEvent := decodeInternalBatch(t, call, materialsReceipt.NotificationEventID)[0]
	require.Nil(t, materialsEvent.Content)
	require.JSONEq(t, `{"materials":[{"messageId":"`+source.MessageID+`"}]}`, string(materialsEvent.Body))
	require.Len(t, materialsEvent.Messages, 1)
	require.JSONEq(t, `{"id":"`+source.MessageID+`","text":"Original input changes-main","actor":"sender-1"}`, string(materialsEvent.Messages[0]))
	call.accept()
	require.Empty(t, waitInternalEvent(t, f, materialsReceipt.NotificationEventID, "sent").Content)
	a = materialsReceipt.Issue
	context.Mode, context.OperationID, context.ExpectedRevision = "continue", "too-early", a.Revision
	mcpWorkError(t, topic, "issue_session_input", "failed_precondition", context)
	peer.quiet(t, 300*time.Millisecond)

	silent := core.UpdateRequirementsRequest{OperationID: "silent", IssueID: a.ID, ExpectedRevision: a.Revision, Goal: "Read this on the next query."}
	missingRevision := silent
	missingRevision.ExpectedRevision = 0
	mcpWorkError(t, topic, "issue_update_requirements", "invalid_argument", missingRevision)
	mcpWorkError(t, foreign, "issue_update_requirements", "not_found", silent)
	mcpWorkError(t, issue, "update_issue", "invalid_argument", map[string]any{
		"operationId": silent.OperationID, "expectedRevision": silent.ExpectedRevision, "goal": silent.Goal,
	})
	a = mcpWork[core.WorkReceipt](t, topic, "issue_update_requirements", silent).Issue
	require.Equal(t, 2, a.RequirementsRevision)
	unchanged := silent
	unchanged.OperationID, unchanged.ExpectedRevision = "same-goal", a.Revision
	require.Equal(t, a, mcpWork[core.WorkReceipt](t, topic, "issue_update_requirements", unchanged).Issue)
	require.Equal(t, a, mcpWork[core.WorkReceipt](t, issue, "get_issue", struct{}{}).Issue)
	require.Empty(t, workEvents(t, p, "issue_session_update"))
	peer.quiet(t, 300*time.Millisecond)

	complete := core.CompleteIssueRequest{IssueID: b.ID, CompleteRequest: acceptance("complete-b", b.Revision, "verified.txt")}
	completed := mcpWork[core.WorkReceipt](t, topic, "issue_complete", complete)
	require.Equal(t, "completed", completed.Issue.Status)
	mcpWorkError(t, topic, "issue_complete", "conflict", complete)
	facts := dependencyEvents(t, f)
	require.Len(t, facts, 1)
	factInput := peer.next(t, "thread/inject_items")
	require.Equal(t, topicBinding.SessionID, factInput.session)
	fact := decodeInternalBatch(t, factInput, facts[0].ID)[0]
	require.Equal(t, "context_supplement", fact.Kind)
	require.Empty(t, fact.Refs)
	require.JSONEq(t, `{"type":"issue.dependency_changed","dependencyIssueId":"`+b.ID+`","dependentIssueIds":["`+a.ID+`"],"previousStatus":"pending","status":"completed"}`, string(fact.Body))
	query := map[string]string{"event_id": fact.ID}
	fullFact := mcpWork[internalEvent](t, topic, "get_internal_event", query)
	require.Equal(t, fact, fullFact)
	mcpWorkError(t, foreign, "get_internal_event", "not_found", query)
	factInput.accept()
	require.NotEmpty(t, waitInternalEvent(t, f, fact.ID, "sent").SentAt)
	require.Equal(t, a, readRecord[core.Issue](t, p, "issues/"+a.ID))
	require.Equal(t, c, readRecord[core.Issue](t, p, "issues/"+c.ID))
	require.Len(t, issueBindings(t, p), 1)
	peer.quiet(t, 300*time.Millisecond)
	t.Log("dependency completion injected a context fact into Topic; no dependent state change, native stop or follow-on start")

	partial := core.UpdateBlockersRequest{
		OperationID: "remaining-human", IssueID: a.ID, ExpectedRevision: a.Revision,
		Resolve:            []core.BlockerResolution{{BlockerID: a.Blockers[1].ID, Reason: "Approval confirmed"}},
		NotifyIssueSession: true,
	}
	mcpWorkError(t, topic, "issue_update_blockers", "failed_precondition", partial)
	require.Equal(t, a, readRecord[core.Issue](t, p, "issues/"+a.ID))
	partial.NotifyIssueSession = false
	a = mcpWork[core.WorkReceipt](t, topic, "issue_update_blockers", partial).Issue
	require.Equal(t, "blocked", a.Status)
	require.Len(t, a.Blockers, 1)
	require.Equal(t, "dependency", a.Blockers[0].Kind)
	blockedStart := startRequest("blocked-start-c", c)
	blockedStart.Resolve = []core.BlockerResolution{{BlockerID: c.Blockers[0].ID, Reason: "Handoff verified"}}
	mcpWorkError(t, topic, "start_issue", "failed_precondition", blockedStart)
	require.Equal(t, c, readRecord[core.Issue](t, p, "issues/"+c.ID))
	resolve := core.UpdateBlockersRequest{
		OperationID: "resolve-dependency", IssueID: a.ID, ExpectedRevision: a.Revision,
		Resolve: []core.BlockerResolution{{BlockerID: a.Blockers[0].ID, Reason: "Verified dependency completion"}},
	}
	mcpWorkError(t, topic, "issue_update_blockers", "invalid_argument", resolve)
	resolve.UnblockedStatus, resolve.NotifyIssueSession = "active", true
	resolved := mcpWork[core.WorkReceipt](t, topic, "issue_update_blockers", resolve)
	mcpWorkError(t, topic, "issue_update_blockers", "conflict", resolve)
	require.Equal(t, "active", resolved.Issue.Status)
	require.Empty(t, resolved.Issue.Blockers)
	require.Len(t, resolved.Issue.ResolvedBlockers, 3)
	require.Equal(t, a.RequirementsRevision, resolved.Issue.RequirementsRevision)
	resumed := peer.next(t, "turn/start")
	require.Equal(t, issueBinding.SessionID, resumed.session)
	require.Equal(t, "issue_session_update", decodeInternalBatch(t, resumed, resolved.NotificationEventID)[0].Kind)
	resumed.accept()
	waitWorkEvent(t, p, resolved.NotificationEventID, "sent")
	a = resolved.Issue
	continueCard := markdownReply("**Continue reviewing the archive.**")
	continued := mcpWork[core.WorkReceipt](t, topic, "issue_session_input", core.IssueSessionInputRequest{
		OperationID: "continue-with-content", IssueID: a.ID, ExpectedRevision: a.Revision, Mode: "continue", Content: &continueCard,
	})
	continuedInput := peer.next(t, "turn/start")
	require.Equal(t, issueBinding.SessionID, continuedInput.session)
	continuedEvent := decodeInternalBatch(t, continuedInput, continued.NotificationEventID)[0]
	require.Equal(t, &continueCard, continuedEvent.Content)
	var deliveredIssue core.Issue
	require.NoError(t, json.Unmarshal(continuedEvent.Issue, &deliveredIssue))
	require.Equal(t, a.Requirements, deliveredIssue.Requirements)
	require.Equal(t, a.RequirementsRevision, deliveredIssue.RequirementsRevision)
	require.Equal(t, "active", deliveredIssue.Status)
	require.Empty(t, deliveredIssue.Blockers)
	continuedInput.accept()
	waitWorkEvent(t, p, continued.NotificationEventID, "sent")
	a = continued.Issue

	type updateResult struct {
		req core.UpdateRequirementsRequest
		mcpHTTPResult
	}
	results := make(chan updateResult, 2)
	race := make(chan struct{})
	for _, suffix := range []string{"α", "β"} {
		r := core.UpdateRequirementsRequest{
			OperationID: "notify-" + suffix, IssueID: a.ID, ExpectedRevision: a.Revision,
			Goal: strings.Repeat(suffix, 5300), NotifyIssueSession: true,
		}
		req, err := newMCPToolRequest(t.Context(), p.endpoint, token, "issue_update_requirements", r)
		require.NoError(t, err)
		go func() {
			<-race
			results <- updateResult{r, doMCPRequest(req)}
		}()
	}
	close(race)
	var winner updateResult
	var accepted, conflicts int
	for range 2 {
		select {
		case result := <-results:
			require.NoError(t, result.err)
			result.status, result.body = normalizeMCPToolResult(t, result.status, result.body)
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
	require.Equal(t, issueBinding.SessionID, notice.session)
	wire := decodeInternalBatch(t, notice, updated.NotificationEventID)[0]
	require.Equal(t, "issue_session_update", wire.Kind)
	require.Empty(t, wire.Body)
	require.Empty(t, wire.Refs)
	require.Empty(t, wire.Issue)
	notice.accept()
	waitWorkEvent(t, p, updated.NotificationEventID, "sent")
	require.Len(t, workEvents(t, p, "issue_session_update"), 3)
	view := mcpWork[issueDetail](t, topic, "get_issue", map[string]string{"issueId": a.ID})
	require.Equal(t, updated.Issue, view.Issue)
	require.NotNil(t, view.LatestRequirementsNotification)
	require.Equal(t, "sent", view.LatestRequirementsNotification.Status)
	require.Equal(t, updated.Issue.RequirementsRevision, view.LatestRequirementsNotification.Revision)
	require.Equal(t, updated.Issue, mcpWork[core.WorkReceipt](t, issue, "get_issue", struct{}{}).Issue)
	t.Log("Concurrent updates at the same Issue revision committed once, and the Issue Session read the full goal through get_issue.")

	a = updated.Issue
	require.Equal(t, start.SessionBinding.ID, a.CurrentSessionBindingID)
	staleReport := mcpWork[core.Report](t, issue, "submit_report", core.ReportRequest{
		OperationID: "report-before-goal-update", Kind: "ready", Summary: "Verified the previous goal", GoalRevision: start.SessionBinding.StartGoalRevision,
	})
	staleNotice := peer.next(t, "thread/inject_items")
	staleEvent := decodeInternalBatch(t, staleNotice, staleReport.EventID)[0]
	require.Equal(t, "context_supplement", staleEvent.Kind)
	require.Empty(t, staleEvent.Report)
	staleNotice.accept()
	waitWorkEvent(t, p, staleReport.EventID, "sent")
	var readback struct {
		SessionBindingID string `json:"sessionBindingId"`
		Current          *bool  `json:"current"`
		GoalRevision     int    `json:"goalRevision"`
	}
	require.NoError(t, json.Unmarshal(mcpWork[json.RawMessage](t, topic, "get_report", map[string]string{"reportId": staleReport.ID}), &readback))
	require.NotNil(t, readback.Current)
	require.False(t, *readback.Current)
	require.Equal(t, staleReport.Revision, readback.GoalRevision)
	require.Empty(t, readback.SessionBindingID)
	reportReq := core.ReportRequest{OperationID: "report", Kind: "progress", Summary: "Applied updated requirements", GoalRevision: a.Revision}
	mcpWorkError(t, issue, "submit_report", "invalid_argument", reportReq)
	reportReq.GoalRevision = a.RequirementsRevision
	report := mcpWork[core.Report](t, issue, "submit_report", reportReq)
	require.Equal(t, a.RequirementsRevision, report.Revision)
	reportInput := peer.next(t, "turn/start")
	reportEvent := decodeInternalBatch(t, reportInput, report.EventID)[0]
	require.Empty(t, reportEvent.Report)
	require.Empty(t, reportEvent.Issue)
	require.JSONEq(t, `{"kind":"report","id":"`+report.ID+`"}`, string(reportEvent.Refs[0]))
	reportInput.answer <- nativeSubmitAnswer{complete: true}
	waitWorkEvent(t, p, report.EventID, "sent")

	rejected := mcpWork[core.WorkReceipt](t, topic, "issue_update_requirements", core.UpdateRequirementsRequest{
		OperationID: "native-rejection", IssueID: a.ID, ExpectedRevision: a.Revision,
		Goal: "Keep this requirement when delivery fails.", NotifyIssueSession: true,
	})
	rejectedInput := peer.next(t, "turn/start")
	require.Equal(t, issueBinding.SessionID, rejectedInput.session)
	decodeInternalBatch(t, rejectedInput, rejected.NotificationEventID)
	rejectedInput.answer <- nativeSubmitAnswer{code: -32602, message: "failed to submit turn input: PlanMode"}
	require.Empty(t, waitWorkEvent(t, p, rejected.NotificationEventID, "pending").SentAt)
	observation := readRecord[issueDetail](t, p, "issues/"+a.ID)
	require.Equal(t, []core.Report{staleReport, report}, observation.Reports)
	require.Equal(t, rejected.Issue, observation.Issue)
	require.NotNil(t, observation.LatestRequirementsNotification)
	require.Equal(t, rejected.NotificationEventID, observation.LatestRequirementsNotification.ID)
	require.Equal(t, "pending", observation.LatestRequirementsNotification.Status)
	retry := peer.next(t, "turn/start")
	require.Equal(t, rejectedInput.text, retry.text)
	retry.accept()
	waitWorkEvent(t, p, rejected.NotificationEventID, "sent")
	peer.quiet(t, 300*time.Millisecond)
	waiting := blockers(t, topic, rejected.Issue, "wait-for-confirmation", human("Wait for final confirmation"))
	pending := mcpWork[core.WorkReceipt](t, topic, "issue_update_blockers", core.UpdateBlockersRequest{
		OperationID: "resolve-and-leave-pending", IssueID: waiting.ID, ExpectedRevision: waiting.Revision, UnblockedStatus: "pending",
		Resolve: []core.BlockerResolution{{BlockerID: waiting.Blockers[0].ID, Reason: "Confirmation received"}},
	})
	require.Equal(t, "pending", pending.Issue.Status)
	require.Empty(t, pending.Issue.CurrentSessionBindingID)
	require.Equal(t, "revoked", pending.SessionBinding.State)
	require.Empty(t, pending.NotificationEventID)
	_ = waitObservedStop(t, p, "session-bindings", issueBinding.ID)
	require.Len(t, issueBindings(t, p), 1)
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
			work := roleWork[core.WorkReceipt](t, p, token, "create_issue", create)
			binding := waitBinding(t, p, "issue", work.SessionBinding.ID, "ready")
			waitWorkEvent(t, p, work.SessionBinding.StartEventID, "sent")
			r := core.UpdateRequirementsRequest{
				OperationID: "crash-update", IssueID: work.Issue.ID,
				ExpectedRevision: work.Issue.Revision, Goal: "Verify the crash-safe requirements", NotifyIssueSession: true,
			}
			req, err := newMCPToolRequest(t.Context(), p.endpoint, token, "issue_update_requirements", r)
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
			beforeReplay := readRecord[issueDetail](t, p, "issues/"+r.IssueID)
			events := workEvents(t, p, "issue_session_update")
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
			saved := core.WorkReceipt{Issue: beforeReplay.Issue, NotificationEventID: beforeReplay.LatestRequirementsEventID}
			if stage == "requirements_before_notification" {
				saved = mcpWork[core.WorkReceipt](t, mcp, "issue_update_requirements", r)
			}
			mcpWorkError(t, mcp, "issue_update_requirements", "conflict", r)
			require.Equal(t, r.Goal, saved.Issue.Requirements)
			require.Equal(t, work.Issue.RequirementsRevision+1, saved.Issue.RequirementsRevision)
			require.Len(t, workEvents(t, p, "issue_session_update"), 1)
			require.Equal(t, binding.ID, waitWorkEvent(t, p, saved.NotificationEventID, "sent").Recipient.SessionBindingID)
			t.Logf("%s restart saw %d committed notifications and matching requirements; the committed update delivered one event", stage, len(events))
		})
	}
}

func TestIssueUpdateFencesGoalAndBlockersBeforeRPC(t *testing.T) {
	t.Parallel()
	peer := newNativeSubmitPeer(t)
	endpoint, _ := startEventTestMassd(t, peer)
	f, operatorFile := eventFixture(t, endpoint)
	gate := t.TempDir()
	stage := "issue_before_rpc"
	release := filepath.Join(gate, "release")
	write(t, release, "")
	p := f.start("SUPERAGENT_TEST_GATE_STAGE="+stage, "SUPERAGENT_TEST_GATE_DIR="+gate)
	t.Cleanup(func() { write(t, release, "") })
	topic, _ := startReminderTopic(t, f, p, peer)
	work, b, start := startReminderWorker(t, f, p, peer, topic)
	start.accept()
	waitWorkEvent(t, p, work.SessionBinding.StartEventID, "sent")
	issue := work.Issue
	for _, change := range []string{"goal", "blocker"} {
		require.NoError(t, os.Remove(release))
		require.NoError(t, os.Remove(filepath.Join(gate, stage)))
		req := core.IssueSessionInputRequest{OperationID: "continue-before-" + change, IssueID: issue.ID, ExpectedRevision: issue.Revision, Mode: "continue"}
		queued := mcpWork[core.WorkReceipt](t, topic, "issue_session_input", req)
		issue = queued.Issue
		waitWorkGate(t, gate, stage)
		require.Empty(t, waitWorkEvent(t, p, queued.NotificationEventID, "pending").SentAt)
		if change == "goal" {
			issue = mcpWork[core.WorkReceipt](t, topic, "issue_update_requirements", core.UpdateRequirementsRequest{
				OperationID: "new-goal", IssueID: issue.ID, ExpectedRevision: issue.Revision, Goal: "An amended goal.",
			}).Issue
		} else {
			issue = blockers(t, topic, issue, "need-more-input", human("Wait for confirmation"))
		}
		write(t, release, "")
		require.Empty(t, waitWorkEvent(t, p, queued.NotificationEventID, "superseded").SentAt)
		peer.quiet(t, 250*time.Millisecond)
		require.Equal(t, b.ID, readRecord[core.Issue](t, p, "issues/"+issue.ID).CurrentSessionBindingID)
	}
	recovery := recoveryFor("replace-blocked-issue", "replace", b.ID)
	recovery.StopRuntime = true
	released := recoverRoleBinding(t, p, operatorFile, recovery)
	require.Equal(t, "runtime_stopped", released.Close.Method)
	require.Empty(t, released.ReplacementSessionBindingID)
	require.Empty(t, readRecord[core.Issue](t, p, "issues/"+issue.ID).CurrentSessionBindingID)
	require.Equal(t, "blocked", readRecord[core.Issue](t, p, "issues/"+issue.ID).Status)
	require.Equal(t, "revoked", readRecord[core.SessionBinding](t, p, "session-bindings/"+b.ID).State)
	require.Len(t, issueBindings(t, p), 1)
}
