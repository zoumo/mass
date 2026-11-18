package core_test

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/zoumo/mass/superagent/pkg/core"
)

type issueDetail struct {
	core.Issue
	LatestRequirementsNotification *core.Event `json:"latestRequirementsNotification"`
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
	peer := newNativeDeliveryPeer(t)
	endpoint, _ := startDeliveryMassd(t, peer)
	f, _ := deliveryFixture(t, endpoint)
	p := f.start()
	router := startRoleMCP(t, f, waitBinding(t, p, "router", "", "ready"))
	topicBinding, _ := nativeWorkTopic(t, f, p, peer, router, "changes-main")
	foreignBinding, _ := nativeWorkTopic(t, f, p, peer, router, "changes-foreign")
	topic, foreign := startRoleMCP(t, f, topicBinding), startRoleMCP(t, f, foreignBinding)
	token := roleToken(t, f, topicBinding)
	create := func(c *stdioClient, op string) core.Issue {
		return mcpWork[core.WorkReceipt](t, c, "issue_create", issueInput(f.dir, op)).Issue
	}
	a, b, c := create(topic, "a"), create(topic, "b"), create(topic, "c")
	other := create(foreign, "foreign")
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
		OperationID: "premature", IssueID: b.ID, ExpectedRevision: b.Revision, Requirements: "Pending update", NotifyWorker: true,
	}
	mcpWorkError(t, topic, "issue_update_requirements", "failed_precondition", premature)
	require.Equal(t, b, readRecord[core.Issue](t, p, "issues/"+b.ID))
	require.Empty(t, issueEvents(t, p, "issue.requirements_updated"))

	start := mcpWork[core.WorkReceipt](t, topic, "issue_start", core.StartIssueRequest{
		OperationID: "start-a", IssueID: a.ID, ExpectedRevision: a.Revision,
	})
	require.Equal(t, "blocked", start.Issue.Status)
	require.Equal(t, a.Blockers, start.Issue.Blockers)
	require.Equal(t, a.RequirementsRevision, start.Execution.Revision)
	a = start.Issue
	workerBinding := waitBinding(t, p, "worker", start.Execution.ID, "ready")
	worker := startRoleMCP(t, f, workerBinding)
	native := peer.next(t, "turn/start")
	require.Equal(t, workerBinding.SessionID, native.session)
	require.Equal(t, a.RequirementsRevision, decodeBatch(t, native, start.Execution.StartEventID).Events[0].Revision)
	native.accept()
	waitDelivery(t, p, start.Execution.StartEventID, "delivered")
	require.Eventually(t, func() bool {
		detail := readRecord[issueDetail](t, p, "issues/"+a.ID)
		return detail.Status == "blocked" && detail.WorkerSession.ExecutionID == start.Execution.ID &&
			detail.WorkerSession.State == "running" && detail.WorkerSession.ObservedAt != ""
	}, 7*time.Second, 20*time.Millisecond)

	release := core.UpdateBlockersRequest{
		OperationID: "one-blocker", IssueID: a.ID, ExpectedRevision: a.Revision, ResolveBlockerIDs: []string{a.Blockers[0].ID},
	}
	prior := a
	a = mcpWork[core.WorkReceipt](t, topic, "issue_update_blockers", release).Issue
	require.Equal(t, prior.Blockers[1:], a.Blockers)
	require.Equal(t, "blocked", a.Status)
	require.Equal(t, start.Execution.ID, a.CurrentExecutionID)
	require.Equal(t, prior.RequirementsRevision, a.RequirementsRevision)
	mcpWorkError(t, topic, "issue_start", "conflict", core.StartIssueRequest{
		OperationID: "duplicate-start-with-resolution", IssueID: a.ID, ExpectedRevision: a.Revision,
		ResolveBlockerIDs: []string{a.Blockers[0].ID},
	})
	require.Equal(t, a, readRecord[core.Issue](t, p, "issues/"+a.ID))
	peer.quiet(t, 300*time.Millisecond)

	silent := core.UpdateRequirementsRequest{OperationID: "silent", IssueID: a.ID, ExpectedRevision: a.Revision, Requirements: "Read this on the next query."}
	missingRevision := silent
	missingRevision.ExpectedRevision = 0
	mcpWorkError(t, topic, "issue_update_requirements", "invalid_argument", missingRevision)
	mcpWorkError(t, foreign, "issue_update_requirements", "not_found", silent)
	mcpWorkError(t, worker, "issue_update_requirements", "forbidden", silent)
	a = mcpWork[core.WorkReceipt](t, topic, "issue_update_requirements", silent).Issue
	require.Equal(t, 2, a.RequirementsRevision)
	require.Equal(t, a, mcpWork[core.WorkReceipt](t, worker, "issue_get_current", struct{}{}).Issue)
	require.Empty(t, issueEvents(t, p, "issue.requirements_updated"))
	peer.quiet(t, 300*time.Millisecond)

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
				Requirements: strings.Repeat(suffix, 5300), NotifyWorker: true,
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
	updated := mcpWork[core.WorkReceipt](t, topic, "issue_update_requirements", winner.req)
	require.Equal(t, winner.req.Requirements, updated.Issue.Requirements)
	require.Equal(t, a.RequirementsRevision+1, updated.Issue.RequirementsRevision)
	require.NotEmpty(t, updated.NotificationEventID)
	notice := peer.next(t, "turn/start")
	require.Equal(t, workerBinding.SessionID, notice.session)
	wire := decodeBatch(t, notice, updated.NotificationEventID).Events[0]
	require.Equal(t, "act", wire.Intent)
	require.Equal(t, "issue.requirements_updated", wire.Type)
	require.Equal(t, updated.Issue.RequirementsRevision, wire.Revision)
	require.Equal(t, start.Execution.Generation, wire.TargetGeneration)
	require.True(t, wire.Truncated)
	require.Equal(t, core.ContentRef{Collection: "events", ID: updated.NotificationEventID}, wire.ContentRef)
	lookup := map[string]string{"eventId": updated.NotificationEventID}
	full := mcpWork[core.Event](t, worker, "event_get", lookup)
	var payload struct{ Requirements string }
	require.NoError(t, json.Unmarshal(full.Payload, &payload))
	require.Equal(t, updated.Issue.Requirements, payload.Requirements)
	mcpWorkError(t, foreign, "event_get", "not_found", lookup)
	notice.accept()
	waitDelivery(t, p, updated.NotificationEventID, "delivered")
	require.Len(t, issueEvents(t, p, "issue.requirements_updated"), 1)
	view := mcpWork[issueDetail](t, topic, "work_query", core.QueryRequest{Collection: "issues", ID: a.ID})
	require.Equal(t, updated.Issue, view.Issue)
	require.NotNil(t, view.LatestRequirementsNotification)
	require.Equal(t, "delivered", view.LatestRequirementsNotification.Status)
	require.Equal(t, updated.Issue.RequirementsRevision, view.LatestRequirementsNotification.Revision)
	require.Equal(t, updated.Issue, mcpWork[core.WorkReceipt](t, worker, "issue_get_current", struct{}{}).Issue)
	t.Log("two public HTTP writers at the same Issue revision committed once; stdio replay and full requirements match the native truncated act event")

	a = updated.Issue
	reportReq := core.ReportRequest{OperationID: "report", Kind: "progress", Summary: "Applied updated requirements", Revision: a.Revision}
	mcpWorkError(t, worker, "report_submit", "invalid_argument", reportReq)
	reportReq.Revision = a.RequirementsRevision
	report := mcpWork[core.Report](t, worker, "report_submit", reportReq)
	require.Equal(t, a.RequirementsRevision, report.Revision)
	reportInput := peer.next(t, "turn/start")
	decodeBatch(t, reportInput, report.EventID)
	reportInput.answer <- nativeDeliveryAnswer{complete: true}
	waitDelivery(t, p, report.EventID, "delivered")

	complete := core.CompleteIssueRequest{IssueID: b.ID, CompleteRequest: acceptance("complete-b", b.Revision, "verified.txt")}
	done := mcpWork[core.WorkReceipt](t, topic, "issue_complete", complete)
	require.Equal(t, done, mcpWork[core.WorkReceipt](t, topic, "issue_complete", complete))
	facts := issueEvents(t, p, "issue.dependency_changed")
	require.Len(t, facts, 1)
	factInput := peer.next(t, "thread/inject_items")
	require.Equal(t, topicBinding.SessionID, factInput.session)
	fact := decodeBatch(t, factInput, facts[0].ID).Events[0]
	require.Equal(t, "context", fact.Intent)
	var change struct {
		DependencyIssueID, PreviousStatus, Status string
		DependentIssueIDs                         []string
	}
	require.NoError(t, json.Unmarshal(fact.Payload, &change))
	require.Equal(t, b.ID, change.DependencyIssueID)
	require.Equal(t, "pending", change.PreviousStatus)
	require.Equal(t, "completed", change.Status)
	require.Equal(t, []string{a.ID}, change.DependentIssueIDs)
	factInput.accept()
	waitDelivery(t, p, fact.EventID, "delivered")
	require.Equal(t, a, readRecord[core.Issue](t, p, "issues/"+a.ID))
	require.Equal(t, c, readRecord[core.Issue](t, p, "issues/"+c.ID))
	require.Len(t, page(t, p, "executions").Items, 1)
	peer.quiet(t, 300*time.Millisecond)
	t.Log("dependency completion injected a context fact into Topic; no dependent state change, native stop or follow-on start")

	startC := mcpWork[core.WorkReceipt](t, topic, "issue_start", core.StartIssueRequest{
		OperationID: "start-c", IssueID: c.ID, ExpectedRevision: c.Revision, ResolveBlockerIDs: []string{c.Blockers[0].ID},
	})
	require.Equal(t, "blocked", startC.Issue.Status)
	require.Equal(t, c.Blockers[1:], startC.Issue.Blockers)
	require.Equal(t, c.RequirementsRevision, startC.Issue.RequirementsRevision)
	cInput := peer.next(t, "turn/start")
	decodeBatch(t, cInput, startC.Execution.StartEventID)
	cInput.accept()
	waitDelivery(t, p, startC.Execution.StartEventID, "delivered")
	resolve := core.UpdateBlockersRequest{
		OperationID: "all-blockers", IssueID: a.ID, ExpectedRevision: a.Revision,
		ResolveBlockerIDs: []string{a.Blockers[0].ID, a.Blockers[1].ID},
	}
	mcpWorkError(t, topic, "issue_update_blockers", "invalid_argument", resolve)
	require.Equal(t, a, readRecord[core.Issue](t, p, "issues/"+a.ID))
	resolve.UnblockedStatus = "active"
	resolved := mcpWork[core.WorkReceipt](t, topic, "issue_update_blockers", resolve).Issue
	require.Equal(t, "active", resolved.Status)
	require.Empty(t, resolved.Blockers)
	require.Equal(t, a.CurrentExecutionID, resolved.CurrentExecutionID)
	require.Equal(t, a.RequirementsRevision, resolved.RequirementsRevision)
	peer.quiet(t, 300*time.Millisecond)

	rejected := mcpWork[core.WorkReceipt](t, topic, "issue_update_requirements", core.UpdateRequirementsRequest{
		OperationID: "native-rejection", IssueID: a.ID, ExpectedRevision: resolved.Revision,
		Requirements: "Keep this requirement when delivery fails.", NotifyWorker: true,
	})
	rejectedInput := peer.next(t, "turn/start")
	require.Equal(t, workerBinding.SessionID, rejectedInput.session)
	decodeBatch(t, rejectedInput, rejected.NotificationEventID)
	rejectedInput.answer <- nativeDeliveryAnswer{code: -32602, message: "failed to submit turn input: PlanMode"}
	failed := waitDelivery(t, p, rejected.NotificationEventID, "blocked")
	require.NotEmpty(t, failed.LastError)
	observation := readRecord[issueDetail](t, p, "issues/"+a.ID)
	require.Equal(t, rejected.Issue, observation.Issue)
	require.NotNil(t, observation.LatestRequirementsNotification)
	require.Equal(t, rejected.NotificationEventID, observation.LatestRequirementsNotification.ID)
	require.Equal(t, failed.LastError, observation.LatestRequirementsNotification.LastError)
	require.Equal(t, "blocked", observation.LatestRequirementsNotification.Status)
	peer.quiet(t, 300*time.Millisecond)
	t.Log("an explicit native rejection left the committed requirements readable and surfaced the delivery error")
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
			waitDelivery(t, p, work.Execution.StartEventID, "delivered")
			r := core.UpdateRequirementsRequest{
				OperationID: "crash-update", IssueID: work.Issue.ID,
				ExpectedRevision: work.Issue.Revision, Requirements: "Verify the crash-safe requirements", NotifyWorker: true,
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
			events := issueEvents(t, p, "issue.requirements_updated")
			if stage == "requirements_before_notification" {
				require.Equal(t, work.Issue, beforeReplay.Issue)
				require.Nil(t, beforeReplay.LatestRequirementsNotification)
				require.Empty(t, events)
			} else {
				require.Equal(t, r.Requirements, beforeReplay.Requirements)
				require.Equal(t, work.Issue.RequirementsRevision+1, beforeReplay.RequirementsRevision)
				require.Len(t, events, 1)
				require.Equal(t, beforeReplay.LatestRequirementsEventID, events[0].ID)
			}
			mcp := startRoleMCP(t, f, topic)
			saved := mcpWork[core.WorkReceipt](t, mcp, "issue_update_requirements", r)
			require.Equal(t, saved, mcpWork[core.WorkReceipt](t, mcp, "issue_update_requirements", r))
			require.Equal(t, r.Requirements, saved.Issue.Requirements)
			require.Equal(t, work.Issue.RequirementsRevision+1, saved.Issue.RequirementsRevision)
			require.Len(t, issueEvents(t, p, "issue.requirements_updated"), 1)
			require.Equal(t, binding.ID, waitDelivery(t, p, saved.NotificationEventID, "delivered").BindingID)
			t.Logf("%s restart saw %d committed notifications and matching requirements; replay delivered one event", stage, len(events))
		})
	}
}
