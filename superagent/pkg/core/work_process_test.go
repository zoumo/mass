package core_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/zoumo/mass/mass-go/pkg/resource"
	"github.com/zoumo/mass/mass-go/pkg/store"

	"github.com/zoumo/mass/superagent/pkg/core"
)

func mcpWork[T any](t *testing.T, c *stdioClient, name string, request any) T {
	t.Helper()
	response := c.call("tools/call", map[string]any{"name": name, "arguments": request})
	require.Empty(t, response["error"], "%s", response["error"])
	var out struct {
		Result T `json:"structuredContent"`
	}
	require.NoError(t, json.Unmarshal(response["result"], &out))
	return out.Result
}

func mcpWorkError(t *testing.T, c *stdioClient, name, want string, request any) core.Error {
	t.Helper()
	response := c.call("tools/call", map[string]any{"name": name, "arguments": request})
	var out struct {
		Error core.Error `json:"data"`
	}
	require.NoError(t, json.Unmarshal(response["error"], &out), "%s", response)
	require.Equal(t, want, out.Error.Code, "%s", response["error"])
	return out.Error
}

func roleWork[T any](t *testing.T, p *process, token, name string, request any) T {
	t.Helper()
	var out T
	require.NoError(t, json.Unmarshal(roleCall(t, p, token, name, request), &out))
	return out
}

func issueInput(workspace, id string) core.CreateIssueRequest {
	return core.CreateIssueRequest{OperationID: id, Title: "Validate sample output", Requirements: "Run the checks and record exact output.", Workspace: workspace, RuntimeRef: "agent", Model: "worker-model"}
}

func acceptance(op string, revision int, ref string) core.CompleteRequest {
	return core.CompleteRequest{OperationID: op, ExpectedRevision: revision, Reason: "Reviewed the checks", Evidence: []string{ref}}
}

type workDeliveryRecord struct {
	internalEvent
	Status   string `json:"status"`
	SentAt   string `json:"sentAt"`
	Revision int    `json:"revision"`
}

func workDeliveries(t *testing.T, p *process, kind string) []workDeliveryRecord {
	t.Helper()
	records := readRecord[struct{ Items []workDeliveryRecord }](t, p, "internal-deliveries?limit=200")
	var found []workDeliveryRecord
	for _, item := range records.Items {
		if item.Kind == kind {
			found = append(found, item)
		}
	}
	return found
}

func waitWorkDelivery(t *testing.T, p *process, id, status string) workDeliveryRecord {
	t.Helper()
	var found workDeliveryRecord
	require.Eventually(t, func() bool {
		found = readRecord[workDeliveryRecord](t, p, "internal-deliveries/"+id)
		return found.Status == status
	}, 7*time.Second, 20*time.Millisecond, "%s should be %s", id, status)
	return found
}

func nativeWorkTopic(t *testing.T, f *fixture, p *process, peer *nativeDeliveryPeer, router *stdioClient, name string) (core.Binding, core.Receipt) {
	t.Helper()
	msg := input(name)
	msg.Message.ThreadRef.ID, msg.Message.Channel = name, nil
	source := submit(t, p, f, msg)
	start := peer.next(t, "turn/start")
	decodeInternalBatch(t, start, source.EventID)
	start.accept()
	waitRouteStatus(t, p, source.EventID, "sent")
	route := newRoute(name, []string{source.MessageID}, f.dir)
	route.Targets[0].Create.Goal = "Complete " + name
	decision := mcpWork[core.RouteDecision](t, router, "route_decide", route)
	summary := mcpWork[core.Topic](t, router, "topic_query", core.QueryRequest{Collection: "topics", ID: decision.Targets[0].TopicID})
	require.Equal(t, route.Targets[0].Create.Goal, summary.Goal)
	topic := waitBinding(t, p, "topic", decision.Targets[0].TopicID, "ready")
	first := peer.next(t, "turn/start")
	require.Equal(t, topic.SessionID, first.session)
	first.answer <- nativeDeliveryAnswer{complete: true}
	waitDelivered(t, p, source.MessageID, "topic", topic.OwnerID)
	return topic, source
}

func TestIssueNativeWorkLifecycle(t *testing.T) {
	peer := newNativeDeliveryPeer(t)
	endpoint, massd := startDeliveryMassd(t, peer)
	f, _ := deliveryFixture(t, endpoint)
	p := f.start()
	routerBinding := waitBinding(t, p, "router", "", "ready")
	router := startRoleMCP(t, f, routerBinding)
	mainBinding, source := nativeWorkTopic(t, f, p, peer, router, "work-main")
	otherBinding, other := nativeWorkTopic(t, f, p, peer, router, "work-other")
	topic := startRoleMCP(t, f, mainBinding)
	foreign := startRoleMCP(t, f, otherBinding)
	token := roleToken(t, f, mainBinding)

	create := issueInput(f.dir, "create")
	create.Goal, create.Requirements = create.Requirements, ""
	create.Materials = []core.Material{{Label: "Original request", MessageID: source.MessageID}, {URI: "notes/check.txt"}}
	pending := mcpWork[core.WorkReceipt](t, topic, "issue_create", create)
	require.Equal(t, pending, mcpWork[core.WorkReceipt](t, topic, "issue_create", create))
	require.Equal(t, "pending", pending.Issue.Status)
	require.Equal(t, mainBinding.OwnerID, pending.Issue.TopicID)
	require.Equal(t, create.Goal, pending.Issue.Requirements)
	require.Equal(t, create.Workspace, pending.Issue.Workspace)
	require.Equal(t, create.Materials, pending.Issue.Materials)
	require.Nil(t, pending.Execution)
	require.Empty(t, page(t, p, "executions").Items)
	require.Empty(t, workDeliveries(t, p, "worker_started"))
	require.Len(t, bindings(t, p), 3)
	peer.quiet(t, 300*time.Millisecond)

	invalid := issueInput(f.dir, "invalid")
	invalid.Materials = []core.Material{{MessageID: other.MessageID}}
	mcpWorkError(t, topic, "issue_create", "not_found", invalid)
	mcpWorkError(t, router, "work_query", "forbidden", core.QueryRequest{Collection: "issues"})
	mcpWorkError(t, foreign, "work_query", "not_found", core.QueryRequest{Collection: "issues", ID: pending.Issue.ID})
	mcpWorkError(t, foreign, "issue_start", "not_found", core.StartIssueRequest{OperationID: "foreign", IssueID: pending.Issue.ID, ExpectedRevision: 1})
	mcpWorkError(t, topic, "topic_complete", "failed_precondition", acceptance("too-early", 1, "notes/check.txt"))
	require.Len(t, page(t, p, "issues").Items, 1)
	t.Log("default create remained pending without a Worker binding or native input and cross-Topic material/start was rejected")

	type response struct {
		request core.StartIssueRequest
		status  int
		body    []byte
	}
	startResults := make(chan response, 3)
	concurrent := make(chan struct{})
	for _, op := range []string{"start-a", "start-a", "start-b"} {
		go func() {
			<-concurrent
			r := core.StartIssueRequest{OperationID: op, IssueID: pending.Issue.ID, ExpectedRevision: pending.Issue.Revision}
			status, body := request(t, http.MethodPost, p.endpoint, "/v1/role/issue_start", token, r)
			startResults <- response{r, status, body}
		}()
	}
	close(concurrent)
	var winning []response
	for range 3 {
		select {
		case result := <-startResults:
			if result.status == http.StatusOK {
				winning = append(winning, result)
			} else {
				errorCode(t, result.status, result.body, http.StatusConflict, "conflict")
			}
		case <-time.After(11 * time.Second):
			t.Fatal("start race did not settle")
		}
	}
	require.NotEmpty(t, winning)
	require.Len(t, winning, 1)
	var created struct{ Result core.WorkReceipt }
	require.NoError(t, json.Unmarshal(winning[0].body, &created))
	started := created.Result
	mcpWorkError(t, topic, "issue_start", "conflict", winning[0].request)
	require.NotNil(t, started.Execution)
	x := *started.Execution
	require.Equal(t, x.ID, started.Issue.CurrentExecutionID)
	require.Len(t, page(t, p, "executions").Items, 1)
	require.Len(t, workDeliveries(t, p, "worker_started"), 1)
	workerBinding := waitBinding(t, p, "worker", x.ID, "ready")
	require.Equal(t, mainBinding.Namespace, workerBinding.Namespace)
	require.Equal(t, x.Generation, workerBinding.Generation)
	require.Equal(t, started.Issue.ID, workerBinding.IssueID)
	require.Equal(t, mainBinding.OwnerID, workerBinding.TopicID)
	start := peer.next(t, "turn/start")
	require.Equal(t, workerBinding.SessionID, start.session)
	decodeInternalBatch(t, start, x.StartEventID)
	start.accept()
	waitWorkDelivery(t, p, x.StartEventID, "sent")
	var session resource.Session
	require.NoError(t, massd.GetSession(t.Context(), resource.SessionRef{SessionID: workerBinding.SessionID}, &session))
	require.Contains(t, session.Spec.SystemPrompt, "verify it")
	require.Contains(t, session.Spec.SystemPrompt, "Deduplicate")
	require.Contains(t, session.Spec.SystemPrompt, "never substitutes for a Report")
	require.NotNil(t, session.Spec.MCPServers)
	require.Contains(t, (*session.Spec.MCPServers)[0].Args, filepath.Join(f.data, "capabilities", workerBinding.ID+".json"))
	t.Log("same and different concurrent start operations produced one Execution, stable Event and actual StartOrSteer receipt")

	worker := startRoleMCP(t, f, workerBinding)
	workerToken := roleToken(t, f, workerBinding)
	listed := string(worker.call("tools/list", map[string]any{})["result"])
	require.Contains(t, listed, "issue_get_current")
	require.Contains(t, listed, "report_submit")
	require.NotContains(t, listed, "issue_complete")
	require.NotContains(t, listed, "source_query")
	require.NotContains(t, listed, "outbox")
	current := mcpWork[core.WorkReceipt](t, worker, "issue_get_current", struct{}{})
	require.NotNil(t, current.Execution.WorkObservation)
	require.Equal(t, workerBinding.SessionID, current.Execution.WorkObservation.SessionID)
	require.Equal(t, "turn-"+workerBinding.SessionID, current.Execution.WorkObservation.WorkID)
	current.Execution.WorkObservation = nil
	require.Equal(t, started, current)
	require.Equal(t, source.MessageID, mcpWork[core.Message](t, worker, "issue_material_get", map[string]string{"messageId": source.MessageID}).ID)
	mcpWorkError(t, worker, "issue_material_get", "not_found", map[string]string{"messageId": other.MessageID})
	mcpWorkError(t, worker, "issue_get_current", "invalid_argument", map[string]string{"executionId": x.ID})
	mcpWorkError(t, worker, "work_query", "forbidden", core.QueryRequest{Collection: "reports"})
	mcpWorkError(t, worker, "report_submit", "invalid_argument", map[string]string{"operationId": "forged", "kind": "ready", "summary": "unsafe", "topicId": otherBinding.OwnerID})
	status, data := request(t, http.MethodPost, p.endpoint, "/v1/role/issue_complete", workerToken, struct{}{})
	errorCode(t, status, data, http.StatusForbidden, "forbidden")
	status, data = request(t, http.MethodPost, p.endpoint, "/v1/role/report_submit", token, struct{}{})
	errorCode(t, status, data, http.StatusForbidden, "forbidden")
	t.Log("Worker stdio and authenticated HTTP reject forged identity, foreign material and Topic management")

	var ready core.Report
	for _, kind := range []string{"progress", "blocked", "failed", "ready"} {
		r := core.ReportRequest{
			OperationID: "report-" + kind, Kind: kind, Summary: "Worker " + kind,
			Details: "Check output recorded in workspace.", Evidence: []string{"notes/check.txt"}, Questions: []string{"Inspect exit code?"}, Revision: started.Issue.RequirementsRevision,
		}
		report := mcpWork[core.Report](t, worker, "report_submit", r)
		require.Equal(t, report, mcpWork[core.Report](t, worker, "report_submit", r))
		require.Equal(t, x.ID, report.ExecutionID)
		require.Equal(t, x.Generation, report.Generation)
		require.Equal(t, r.Evidence, report.Evidence)
		require.Equal(t, r.Details, report.Details)
		require.Equal(t, r.Questions, report.Questions)
		require.False(t, report.Historical)
		r.Summary = "changed replay"
		mcpWorkError(t, worker, "report_submit", "conflict", r)
		notice := peer.next(t, "turn/start")
		require.Equal(t, mainBinding.SessionID, notice.session)
		decodeInternalBatch(t, notice, report.EventID)
		notice.answer <- nativeDeliveryAnswer{complete: true}
		waitWorkDelivery(t, p, report.EventID, "sent")
		ready = report
	}
	require.Len(t, workDeliveries(t, p, "report_submitted"), 4)
	require.Len(t, page(t, p, "reports").Items, 4)
	require.Equal(t, "active", readRecord[core.Issue](t, p, "issues/"+started.Issue.ID).Status)
	require.Equal(t, "active", readRecord[core.Topic](t, p, "topics/"+mainBinding.OwnerID).Status)
	review := mcpWork[core.Report](t, topic, "work_query", core.QueryRequest{Collection: "reports", ID: ready.ID})
	require.Equal(t, ready, review)
	mcpWorkError(t, foreign, "work_query", "not_found", core.QueryRequest{Collection: "reports", ID: ready.ID})
	firstPage := mcpWork[core.Page](t, topic, "work_query", core.QueryRequest{Collection: "reports", IssueID: started.Issue.ID, Limit: 1})
	require.NotEmpty(t, firstPage.NextCursor)
	nextPage := mcpWork[core.Page](t, topic, "work_query", core.QueryRequest{Collection: "reports", IssueID: started.Issue.ID, Cursor: firstPage.NextCursor})
	require.Len(t, nextPage.Items, 3)
	mcpWorkError(t, foreign, "work_query", "invalid_argument", core.QueryRequest{Collection: "reports", IssueID: started.Issue.ID, Cursor: firstPage.NextCursor})
	mcpWorkError(t, topic, "work_query", "invalid_argument", core.QueryRequest{Collection: "reports", Cursor: firstPage.NextCursor})
	mcpWorkError(t, topic, "topic_complete", "failed_precondition", acceptance("too-early", 1, ready.ID))
	bad := core.CompleteIssueRequest{IssueID: started.Issue.ID, CompleteRequest: acceptance("accept", 1, ready.ID)}
	conflict := mcpWorkError(t, topic, "issue_complete", "conflict", bad)
	require.Equal(t, started.Issue.Revision, conflict.CurrentRevision)
	bad.ExpectedRevision, bad.Evidence = started.Issue.Revision, nil
	mcpWorkError(t, topic, "issue_complete", "invalid_argument", bad)

	complete := core.CompleteIssueRequest{IssueID: started.Issue.ID, CompleteRequest: acceptance("accept", started.Issue.Revision, ready.ID)}
	done := mcpWork[core.WorkReceipt](t, topic, "issue_complete", complete)
	mcpWorkError(t, topic, "issue_complete", "conflict", complete)
	require.Equal(t, "completed", done.Issue.Status)
	require.Empty(t, done.Issue.CurrentExecutionID)
	require.Equal(t, complete.Evidence, done.Issue.Completion.Evidence)
	require.Equal(t, "historical", done.Execution.Authority)
	require.Equal(t, "ready", done.Execution.PreparationAtRetirement)
	require.Equal(t, "requested", done.Execution.Stop.Status)
	mcpWorkError(t, worker, "issue_get_current", "stale_binding", struct{}{})
	mcpWorkError(t, worker, "issue_material_get", "stale_binding", map[string]string{"messageId": source.MessageID})
	mcpWorkError(t, worker, "delivery_get", "stale_binding", map[string]string{"delivery_id": x.StartEventID})
	var stopped core.Execution
	require.Eventually(t, func() bool {
		stopped = readRecord[core.Execution](t, p, "executions/"+x.ID)
		return stopped.Stop != nil && stopped.Stop.Status == "observed"
	}, 8*time.Second, 20*time.Millisecond)
	require.Equal(t, "controlAcknowledged", stopped.Stop.Receipt.Result)
	require.Equal(t, "turn-"+workerBinding.SessionID, stopped.Stop.Receipt.Target)
	require.Equal(t, "unknown", stopped.Stop.WorkState)
	require.NotNil(t, stopped.Stop.Observation)
	require.Equal(t, "idle", stopped.Stop.Observation.State)
	require.Equal(t, "native_work_identity_not_in_live_event", stopped.Stop.ObservationGap)
	t.Logf("native interrupt acknowledged; native outcome %s, observation gap %s", stopped.Stop.WorkState, stopped.Stop.ObservationGap)

	historicalWorker := startRoleMCP(t, f, workerBinding)
	late := core.ReportRequest{OperationID: "late", Kind: "progress", Summary: "Late report from retired Worker", GoalRevision: x.Revision}
	report := mcpWork[core.Report](t, historicalWorker, "report_submit", late)
	require.True(t, report.Historical)
	require.Equal(t, x.ID, report.ExecutionID)
	require.Equal(t, report, mcpWork[core.Report](t, worker, "report_submit", late))
	context := peer.next(t, "thread/inject_items")
	require.Equal(t, mainBinding.SessionID, context.session)
	require.Equal(t, "context_supplement", decodeInternalBatch(t, context, report.EventID)[0].Kind)
	context.accept()
	waitWorkDelivery(t, p, report.EventID, "sent")
	require.Equal(t, "active", readRecord[core.Topic](t, p, "topics/"+mainBinding.OwnerID).Status)

	auto := issueInput(f.dir, "create-start")
	auto.Start = true
	second := mcpWork[core.WorkReceipt](t, topic, "issue_create", auto)
	require.Equal(t, second, mcpWork[core.WorkReceipt](t, topic, "issue_create", auto))
	secondBinding := waitBinding(t, p, "worker", second.Execution.ID, "ready")
	final := peer.next(t, "turn/start")
	require.Equal(t, secondBinding.SessionID, final.session)
	decodeInternalBatch(t, final, second.Execution.StartEventID)
	final.answer <- nativeDeliveryAnswer{complete: true, final: "Verified the result. This is ordinary model output."}
	waitWorkDelivery(t, p, second.Execution.StartEventID, "sent")
	ended := waitWorkerObservation(t, p, second.Execution, "ended")
	reminder := checkReminderCall(t, peer, secondBinding, ended)
	reminder.answer <- nativeDeliveryAnswer{workID: "ordinary-output-reminder", complete: true}
	waitWorkDelivery(t, p, ended.ReminderEventID, "sent")
	require.Eventually(t, func() bool {
		var ended resource.Session
		return massd.GetSession(t.Context(), resource.SessionRef{SessionID: secondBinding.SessionID}, &ended) == nil && ended.Status.State == "idle"
	}, 5*time.Second, 25*time.Millisecond)
	var output store.HistoryPage
	require.NoError(t, massd.History(t.Context(), resource.SessionRef{SessionID: secondBinding.SessionID}, store.HistoryQuery{}, &output))
	history, err := json.Marshal(output)
	require.NoError(t, err)
	require.Contains(t, string(history), "This is ordinary model output.")
	require.Empty(t, page(t, p, "reports?issueId="+second.Issue.ID).Items)
	require.Equal(t, "active", readRecord[core.Issue](t, p, "issues/"+second.Issue.ID).Status)
	mcpWorkError(t, topic, "topic_complete", "failed_precondition", acceptance("accept-topic", 1, ready.ID))
	mcpWork[core.WorkReceipt](t, topic, "issue_complete", core.CompleteIssueRequest{IssueID: second.Issue.ID, CompleteRequest: acceptance("accept-second", second.Issue.Revision, "notes/check.txt")})
	require.Equal(t, "active", readRecord[core.Topic](t, p, "topics/"+mainBinding.OwnerID).Status)

	topicComplete := acceptance("accept-topic", 1, ready.ID)
	finished := mcpWork[core.Topic](t, topic, "topic_complete", topicComplete)
	require.Equal(t, "completed", finished.Status)
	require.Equal(t, topicComplete.Reason, finished.Completion.Reason)
	mcpWorkError(t, topic, "topic_complete", "failed_precondition", topicComplete)
	mcpWorkError(t, topic, "issue_create", "failed_precondition", issueInput(f.dir, "after-completion"))
	followUp := input("new-source-after-complete")
	followUp.Message.ThreadRef.ID, followUp.Message.Channel = "work-main", nil
	follow := submit(t, p, f, followUp)
	call := peer.next(t, "turn/start")
	require.Equal(t, mainBinding.SessionID, call.session)
	decodeInternalBatch(t, call, follow.EventID)
	call.accept()
	waitRouteStatus(t, p, follow.EventID, "sent")
	require.Equal(t, finished, readRecord[core.Topic](t, p, "topics/"+mainBinding.OwnerID))
	require.Equal(t, "active", readRecord[core.Topic](t, p, "topics/"+otherBinding.OwnerID).Status)
	require.Len(t, page(t, p, "executions").Items, 2)
	t.Log("Report and turn completion left business state active; explicit acceptance retained its evidence; a real follow-up reached the completed Topic without reopening it")
}

func workBoundaryTopic(t *testing.T, f *fixture, p *process) (core.Binding, string) {
	t.Helper()
	router := waitBinding(t, p, "router", "", "ready")
	m := submit(t, p, f, input("work-boundary"))
	decision := decide(t, p, roleToken(t, f, router), newRoute("work-boundary", []string{m.MessageID}, f.dir))
	topic := waitBinding(t, p, "topic", decision.Targets[0].TopicID, "ready")
	waitDelivered(t, p, m.MessageID, "topic", topic.OwnerID)
	return topic, roleToken(t, f, topic)
}

func TestIssueReportCommitCrash(t *testing.T) {
	for _, stage := range []string{"report_before_notification", "report_after_commit"} {
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
			worker := roleToken(t, f, binding)
			waitWorkDelivery(t, p, work.Execution.StartEventID, "sent")
			r := core.ReportRequest{OperationID: "crash-report", Kind: "ready", Summary: "Verified before crash", GoalRevision: work.Execution.Revision}
			payload, err := json.Marshal(r)
			require.NoError(t, err)
			req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, p.endpoint+"/v1/role/report_submit", bytes.NewReader(payload))
			require.NoError(t, err)
			req.Header.Set("Authorization", "Bearer "+worker)
			done := make(chan error, 1)
			go func() {
				response, err := http.DefaultClient.Do(req)
				if err == nil {
					_ = response.Body.Close()
				}
				done <- err
			}()
			waitWorkGate(t, gate, stage)
			p.stop(t, true)
			require.Error(t, <-done)
			p = f.start()
			expected := 0
			if stage == "report_after_commit" {
				expected = 1
			}
			require.Len(t, page(t, p, "reports").Items, expected)
			require.Len(t, readRecord[core.Issue](t, p, "issues/"+work.Issue.ID).Reports, expected)
			require.Len(t, workDeliveries(t, p, "report_submitted"), expected)
			mcp := startRoleMCP(t, f, binding)
			saved := mcpWork[core.Report](t, mcp, "report_submit", r)
			require.Equal(t, saved, mcpWork[core.Report](t, mcp, "report_submit", r))
			require.Len(t, page(t, p, "reports").Items, 1)
			require.Equal(t, []core.Report{saved}, readRecord[core.Issue](t, p, "issues/"+work.Issue.ID).Reports)
			require.Len(t, workDeliveries(t, p, "report_submitted"), 1)
			require.Equal(t, topic.OwnerID, waitWorkDelivery(t, p, saved.EventID, "sent").Recipient.TopicID)
			t.Logf("%s restart had %d Reports and %d notifications before replay; exactly one of each after", stage, expected, expected)
		})
	}
}

func waitWorkGate(t *testing.T, dir, stage string) {
	t.Helper()
	require.Eventually(t, func() bool { _, err := os.Stat(filepath.Join(dir, stage)); return err == nil }, 10*time.Second, 20*time.Millisecond)
}

func TestIssueCompleteFencesUnclaimedStart(t *testing.T) {
	boundary := newRouteBoundary(t)
	f := fixtureAt(t)
	f.execution(boundary)
	gate := t.TempDir()
	p := f.start("SUPERAGENT_TEST_GATE_STAGE=worker_before_claim", "SUPERAGENT_TEST_GATE_DIR="+gate)
	t.Cleanup(func() { write(t, filepath.Join(gate, "release"), "release") })
	_, token := workBoundaryTopic(t, f, p)
	create := issueInput(f.dir, "create-active")
	create.Start = true
	work := roleWork[core.WorkReceipt](t, p, token, "issue_create", create)
	waitWorkGate(t, gate, "worker_before_claim")
	updated := roleWork[core.WorkReceipt](t, p, token, "issue_update_requirements", core.UpdateRequirementsRequest{
		OperationID: "update-before-claim", IssueID: work.Issue.ID, ExpectedRevision: work.Issue.Revision,
		Requirements: "An update before the first Worker claim", NotifyWorker: true,
	})
	complete := core.CompleteIssueRequest{IssueID: work.Issue.ID, CompleteRequest: acceptance("complete", updated.Issue.Revision, "already-verified.txt")}
	done := roleWork[core.WorkReceipt](t, p, token, "issue_complete", complete)
	require.Nil(t, done.Execution.Stop)
	require.Equal(t, "historical", done.Execution.Authority)
	write(t, filepath.Join(gate, "release"), "release")
	require.Empty(t, waitWorkDelivery(t, p, work.Execution.StartEventID, "superseded").SentAt)
	require.Empty(t, waitWorkDelivery(t, p, updated.NotificationEventID, "superseded").SentAt)
	p.stop(t, false)
	p = f.start()
	require.Empty(t, waitWorkDelivery(t, p, work.Execution.StartEventID, "superseded").SentAt)
	require.Empty(t, waitWorkDelivery(t, p, updated.NotificationEventID, "superseded").SentAt)
	require.Equal(t, done.Execution.BindingID, waitBinding(t, p, "worker", work.Execution.ID, "revoked").ID)
	t.Log("prepared but unclaimed startup and requirements update remained superseded on restart, with no native input attempt or stop request")
}

func TestIssueStopDispatchCrashStaysUnknown(t *testing.T) {
	boundary := newRouteBoundary(t)
	f := fixtureAt(t)
	f.execution(boundary)
	gate := t.TempDir()
	p := f.start("SUPERAGENT_TEST_GATE_STAGE=stop_before_rpc", "SUPERAGENT_TEST_GATE_DIR="+gate)
	t.Cleanup(func() { write(t, filepath.Join(gate, "release"), "release") })
	_, token := workBoundaryTopic(t, f, p)
	create := issueInput(f.dir, "create-stop")
	create.Start = true
	work := roleWork[core.WorkReceipt](t, p, token, "issue_create", create)
	waitWorkDelivery(t, p, work.Execution.StartEventID, "sent")
	complete := core.CompleteIssueRequest{IssueID: work.Issue.ID, CompleteRequest: acceptance("complete-stop", work.Issue.Revision, "verified.txt")}
	receipt := roleWork[core.WorkReceipt](t, p, token, "issue_complete", complete)
	require.Equal(t, "requested", receipt.Execution.Stop.Status)
	waitWorkGate(t, gate, "stop_before_rpc")
	require.Equal(t, "dispatching", readRecord[core.Execution](t, p, "executions/"+work.Execution.ID).Stop.Status)
	p.stop(t, true)
	p = f.start()
	stop := readRecord[core.Execution](t, p, "executions/"+work.Execution.ID).Stop
	require.Equal(t, "unknown", stop.Status)
	require.Equal(t, "unknown", stop.WorkState)
	require.Equal(t, "core_stopped_before_stop_settlement", stop.ObservationGap)
	require.Nil(t, stop.Receipt)
	status, raw := request(t, http.MethodPost, p.endpoint, "/v1/role/issue_complete", token, complete)
	errorCode(t, status, raw, http.StatusConflict, "conflict")
	require.Equal(t, receipt.Issue, readRecord[core.Issue](t, p, "issues/"+receipt.Issue.ID))
	require.Equal(t, stop, readRecord[core.Execution](t, p, "executions/"+work.Execution.ID).Stop)
	t.Log("restart retained the Issue acceptance receipt and the original unresolved stop without resubmitting")
}
