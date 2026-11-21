package core_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/zoumo/mass/mass-go/pkg/resource"
	"github.com/zoumo/mass/mass-go/pkg/store"

	"github.com/zoumo/mass/superagent/pkg/core"
)

func mcpWork[T any](t *testing.T, c *stdioClient, name string, request any) T {
	t.Helper()
	name, request = mutationFixture(name, request)
	response := c.call("tools/call", map[string]any{"name": name, "arguments": request})
	require.Empty(t, response["error"], "%s", response["error"])
	var out struct {
		Result  T    `json:"structuredContent"`
		IsError bool `json:"isError"`
	}
	require.NoError(t, json.Unmarshal(response["result"], &out))
	require.False(t, out.IsError, "%s", response["result"])
	return out.Result
}

func mcpWorkError(t *testing.T, c *stdioClient, name, want string, request any) core.Error {
	t.Helper()
	name, request = mutationFixture(name, request)
	response := c.call("tools/call", map[string]any{"name": name, "arguments": request})
	if raw := response["error"]; len(raw) != 0 {
		var out struct {
			Error core.Error `json:"data"`
		}
		require.NoError(t, json.Unmarshal(raw, &out))
		require.Equal(t, want, out.Error.Code, "%s", raw)
		return out.Error
	}
	var result mcpTestToolResult
	require.NoError(t, json.Unmarshal(response["result"], &result))
	e := decodeMCPToolError(t, result)
	require.Equal(t, want, e.Code, "%s", result.StructuredContent)
	return e
}

func roleWork[T any](t *testing.T, p *process, token, name string, request any) T {
	t.Helper()
	var out T
	require.NoError(t, json.Unmarshal(roleCall(t, p, token, name, request), &out))
	return out
}

func checkRoleGoalInput(t *testing.T, p *process, client *stdioClient, token, tool, field, oldField string, args map[string]any) {
	t.Helper()
	var result struct {
		Tools []struct {
			Name        string
			InputSchema struct {
				Properties map[string]json.RawMessage
				Required   []string
			}
		}
	}
	require.NoError(t, json.Unmarshal(client.call("tools/list", struct{}{})["result"], &result))
	found := false
	for _, item := range result.Tools {
		if item.Name == tool {
			found = true
			require.Contains(t, item.InputSchema.Properties, field)
			if tool != "update_issue" {
				require.Contains(t, item.InputSchema.Required, field)
			}
			require.NotContains(t, item.InputSchema.Properties, oldField)
		}
	}
	require.True(t, found, "tool %s should be advertised", tool)

	args[oldField] = args[field]
	for _, present := range []bool{true, false} {
		if !present {
			delete(args, field)
		}
		err := mcpWorkError(t, client, tool, "invalid_argument", args)
		require.Equal(t, "invalid or unknown JSON fields", err.Message)
		status, body := requestMCPTool(t, p.endpoint, tool, token, args)
		errorCode(t, status, body, http.StatusBadRequest, "invalid_argument")
	}
	delete(args, oldField)
	mcpWorkError(t, client, tool, "invalid_argument", args)
	status, body := requestMCPTool(t, p.endpoint, tool, token, args)
	errorCode(t, status, body, http.StatusBadRequest, "invalid_argument")
}

func issueInput(workspace, id string) core.CreateIssueRequest {
	return core.CreateIssueRequest{OperationID: id, Title: "Validate sample output", Goal: "Run the checks and record exact output.", Workspace: workspace, RuntimeRef: "agent", Model: "issue-model"}
}

func acceptance(op string, revision int, ref string) core.CompleteRequest {
	return core.CompleteRequest{OperationID: op, ExpectedRevision: revision, Reason: "Reviewed the checks", Evidence: []string{ref}}
}

type workEventRecord struct {
	internalEvent
	Status   string `json:"status"`
	SentAt   string `json:"sentAt"`
	Revision int    `json:"revision"`
}

func workEvents(t *testing.T, p *process, kind string) []workEventRecord {
	t.Helper()
	records := readRecord[struct{ Items []workEventRecord }](t, p, "internal-events?limit=200")
	var found []workEventRecord
	for _, item := range records.Items {
		if item.Kind == kind {
			found = append(found, item)
		}
	}
	return found
}

func waitWorkEvent(t *testing.T, p *process, id, status string) workEventRecord {
	t.Helper()
	var found workEventRecord
	require.Eventually(t, func() bool {
		found = readRecord[workEventRecord](t, p, "internal-events/"+id)
		return found.Status == status
	}, 7*time.Second, 20*time.Millisecond, "%s should be %s", id, status)
	return found
}

func nativeWorkTopic(t *testing.T, f *fixture, p *process, peer *nativeSubmitPeer, router *stdioClient, name string) (core.Binding, core.Receipt) {
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
	decision := mcpWork[core.RouteDecision](t, router, "route_messages", route)
	summary := mcpListTopicsFixture(t, router, decision.Targets[0].TopicID)
	require.Equal(t, route.Targets[0].Create.Goal, summary.Goal)
	topic := waitBinding(t, p, "topic", decision.Targets[0].TopicID, "ready")
	first := peer.next(t, "turn/start")
	require.Equal(t, topic.SessionID, first.session)
	first.answer <- nativeSubmitAnswer{complete: true}
	waitDelivered(t, p, source.MessageID, "topic", topic.OwnerID)
	return topic, source
}

func TestIssueNativeWorkLifecycle(t *testing.T) {
	peer := newNativeSubmitPeer(t)
	endpoint, massd := startEventTestMassd(t, peer)
	f, _ := eventFixture(t, endpoint)
	receiver, _ := coreOTLPReceiver(t, "")
	p := f.start(traceTestEnv(receiver.URL)...)
	routerBinding := waitBinding(t, p, "router", "", "ready")
	router := startRoleMCP(t, f, routerBinding)
	mainBinding, source := nativeWorkTopic(t, f, p, peer, router, "work-main")
	otherBinding, other := nativeWorkTopic(t, f, p, peer, router, "work-other")
	require.Equal(t, "mass-t1", mainBinding.OwnerID)
	require.Equal(t, "mass-t2", otherBinding.OwnerID)
	topic := startRoleMCP(t, f, mainBinding)
	foreign := startRoleMCP(t, f, otherBinding)
	token := roleToken(t, f, mainBinding)
	long := input("long-issue-material")
	long.Message.ThreadRef.ID, long.Message.Channel = "work-main", nil
	long.Message.Text = strings.Repeat("界", 700)
	longReceipt := submit(t, p, f, long)
	longEvent := peer.next(t, "turn/start")
	require.Equal(t, mainBinding.SessionID, longEvent.session)
	decodeInternalBatch(t, longEvent, longReceipt.EventID)
	longEvent.answer <- nativeSubmitAnswer{complete: true}

	checkRoleGoalInput(t, p, topic, token, "create_issue", "goal", "requirements", map[string]any{
		"operationId": "check-create-input", "title": "Input check", "runtimeRef": "agent", "model": "issue-model", "goal": "Verify the input contract",
	})
	create := issueInput("", "create")
	create.Materials = []core.Material{{Label: "Original request", MessageID: source.MessageID}, {MessageID: longReceipt.MessageID}, {URI: "notes/check.txt"}}
	pending := mcpWork[core.WorkReceipt](t, topic, "create_issue", create)
	require.Equal(t, pending, mcpWork[core.WorkReceipt](t, topic, "create_issue", create))
	require.Equal(t, "mass-t1-i1", pending.Issue.ID)
	changedCreate := create
	changedCreate.Title = "Changed title"
	mcpWorkError(t, topic, "create_issue", "conflict", changedCreate)
	require.Equal(t, "pending", pending.Issue.Status)
	require.Equal(t, mainBinding.OwnerID, pending.Issue.TopicID)
	require.Equal(t, create.Goal, pending.Issue.Requirements)
	require.Equal(t, mainBinding.Workspace, pending.Issue.Workspace)
	require.Equal(t, create.Materials, pending.Issue.Materials)
	require.Nil(t, pending.Execution)
	checkRoleGoalInput(t, p, topic, token, "update_issue", "goal", "requirements", map[string]any{
		"operationId": "check-update-input", "issueId": pending.Issue.ID, "expectedRevision": pending.Issue.Revision, "goal": "Verify the update contract",
	})
	require.Empty(t, page(t, p, "executions").Items)
	require.Empty(t, workEvents(t, p, "issue_session_started"))
	require.Len(t, bindings(t, p), 3)
	peer.quiet(t, 300*time.Millisecond)

	invalid := issueInput(f.dir, "invalid")
	invalid.Materials = []core.Material{{MessageID: other.MessageID}}
	mcpWorkError(t, topic, "create_issue", "not_found", invalid)
	mcpWorkError(t, router, "list_issues", "forbidden", struct{}{})
	mcpWorkError(t, foreign, "get_issue", "not_found", map[string]string{"issueId": pending.Issue.ID})
	mcpWorkError(t, foreign, "start_issue", "not_found", core.StartIssueRequest{OperationID: "foreign", IssueID: pending.Issue.ID, ExpectedRevision: 1})
	mcpWorkError(t, topic, "topic_complete", "failed_precondition", acceptance("too-early", 1, "notes/check.txt"))
	require.Len(t, page(t, p, "issues").Items, 1)
	mcpWorkError(t, foreign, "create_issue", "invalid_argument", issueInput("relative", "invalid-workspace"))
	explicitWorkspace := t.TempDir()
	foreignIssue := mcpWork[core.WorkReceipt](t, foreign, "create_issue", issueInput(explicitWorkspace, "create"))
	require.Equal(t, "mass-t2-i1", foreignIssue.Issue.ID)
	require.Equal(t, explicitWorkspace, foreignIssue.Issue.Workspace)
	t.Log("default create remained pending without a Issue Session binding or native input and cross-Topic material/start was rejected")

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
			status, body := requestMCPTool(t, p.endpoint, "start_issue", token, r)
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
	winner := winning[0]
	if winner.request.OperationID == "start-a" {
		require.Len(t, winning, 2)
	} else {
		require.Equal(t, "start-b", winner.request.OperationID)
		require.Len(t, winning, 1)
	}
	var created struct{ Result core.WorkReceipt }
	require.NoError(t, json.Unmarshal(winner.body, &created))
	started := created.Result
	for _, result := range winning {
		require.Equal(t, winner.request, result.request)
		var replay struct{ Result core.WorkReceipt }
		require.NoError(t, json.Unmarshal(result.body, &replay))
		require.Equal(t, started, replay.Result)
	}
	require.Equal(t, started, mcpWork[core.WorkReceipt](t, topic, "start_issue", winner.request))
	require.NotNil(t, started.Execution)
	x := *started.Execution
	require.Equal(t, "mass-t1-i1-e1", x.ID)
	require.Equal(t, x.ID, started.Issue.CurrentExecutionID)
	startTrace := observedTraces(t, p, "executions", x.ID)
	require.Len(t, startTrace, 1)
	require.Equal(t, "creation", startTrace[0].Phase)
	require.Equal(t, []observerTrace{{TraceID: startTrace[0].TraceID, Phase: "enqueue"}},
		observedTraces(t, p, "internal-events", x.StartEventID))
	require.Len(t, page(t, p, "executions").Items, 1)
	require.Len(t, workEvents(t, p, "issue_session_started"), 1)
	issueBinding := waitBinding(t, p, "issue", x.ID, "ready")
	require.Equal(t, mainBinding.Namespace, issueBinding.Namespace)
	require.Equal(t, x.Generation, issueBinding.Generation)
	require.Equal(t, started.Issue.ID, issueBinding.IssueID)
	require.Equal(t, mainBinding.OwnerID, issueBinding.TopicID)
	start := peer.next(t, "turn/start")
	require.Equal(t, issueBinding.SessionID, start.session)
	materials, err := json.Marshal(create.Materials)
	require.NoError(t, err)
	require.JSONEq(t, fmt.Sprintf(`{"events":[{"event_id":%q,"kind":"issue_session_started","issue":{"id":%q,"goal":%q,"goalRevision":%d,"revision":%d,"status":"active","materials":%s},"messages":[{"id":%q,"text":"Original input work-main","actor":"sender-1"}]}]}`, x.StartEventID, started.Issue.ID, create.Goal, started.Issue.RequirementsRevision, started.Issue.Revision, materials, source.MessageID), start.text)
	var receivedIssue core.Issue
	require.NoError(t, json.Unmarshal(decodeInternalBatch(t, start, x.StartEventID)[0].Issue, &receivedIssue))
	start.accept()
	waitWorkEvent(t, p, x.StartEventID, "sent")
	var session resource.Session
	require.NoError(t, massd.GetSession(t.Context(), resource.SessionRef{SessionID: issueBinding.SessionID}, &session))
	require.Contains(t, session.Spec.SystemPrompt, "verify your result")
	require.Contains(t, session.Spec.SystemPrompt, "deduplicate by event_id")
	require.Contains(t, session.Spec.SystemPrompt, "A final response, ACK or idle does not submit a Report")
	require.NotNil(t, session.Spec.MCPServers)
	require.Contains(t, (*session.Spec.MCPServers)[0].Args, filepath.Join(f.data, "capabilities", issueBinding.ID+".json"))
	t.Log("same and different concurrent start operations produced one Execution, stable Event and actual StartOrSteer receipt")

	issue := startRoleMCP(t, f, issueBinding)
	issueToken := roleToken(t, f, issueBinding)
	listed := string(issue.call("tools/list", map[string]any{})["result"])
	require.Contains(t, listed, "get_issue")
	require.Contains(t, listed, "submit_report")
	require.NotContains(t, listed, "issue_complete")
	require.NotContains(t, listed, "source_query")
	require.NotContains(t, listed, "outbox")
	require.NotContains(t, listed, "binding_get")
	mcpWorkError(t, issue, "binding_get", "forbidden", struct{}{})
	current := mcpWork[core.WorkReceipt](t, issue, "get_issue", struct{}{})
	require.NotNil(t, current.Execution.WorkObservation)
	require.Equal(t, issueBinding.SessionID, current.Execution.WorkObservation.SessionID)
	require.Equal(t, "turn-"+issueBinding.SessionID, current.Execution.WorkObservation.WorkID)
	current.Execution.WorkObservation = nil
	require.Equal(t, started, current)
	material := mcpWork[json.RawMessage](t, issue, "get_message", map[string]string{"messageId": source.MessageID})
	require.JSONEq(t, fmt.Sprintf(`{"id":%q,"text":"Original input work-main","actor":"sender-1"}`, source.MessageID), string(material))
	longMaterial := mcpWork[json.RawMessage](t, issue, "get_message", map[string]string{"messageId": longReceipt.MessageID})
	require.JSONEq(t, fmt.Sprintf(`{"id":%q,"text":%q,"actor":"sender-1"}`, longReceipt.MessageID, long.Message.Text), string(longMaterial))
	mcpWorkError(t, issue, "get_message", "not_found", map[string]string{"messageId": other.MessageID})
	mcpWorkError(t, issue, "get_issue", "invalid_argument", map[string]string{"executionId": x.ID})
	mcpWorkError(t, issue, "get_report", "forbidden", struct{}{})
	mcpWorkError(t, issue, "submit_report", "invalid_argument", map[string]string{"operationId": "forged", "kind": "ready", "summary": "unsafe", "topicId": otherBinding.OwnerID})
	status, data := requestMCPTool(t, p.endpoint, "update_issue", issueToken, map[string]any{
		"operationId": "self-complete", "expectedRevision": started.Issue.Revision,
		"transition": map[string]any{"action": "complete", "reason": "Self-acceptance", "evidence": []string{"notes/check.txt"}},
	})
	errorCode(t, status, data, http.StatusBadRequest, "invalid_argument")
	status, data = requestMCPTool(t, p.endpoint, "submit_report", token, struct{}{})
	errorCode(t, status, data, http.StatusForbidden, "forbidden")
	t.Log("Issue Session stdio and authenticated HTTP reject forged identity, foreign material and Topic management")

	checkRoleGoalInput(t, p, issue, issueToken, "submit_report", "goalRevision", "requirementsRevision", map[string]any{
		"operationId": "check-report-input", "kind": "progress", "summary": "Verify the Report input contract", "goalRevision": receivedIssue.RequirementsRevision,
	})

	var ready core.Report
	var reviewedIssue core.Issue
	for i, kind := range []string{"progress", "blocked", "failed", "ready"} {
		r := core.ReportRequest{
			OperationID: "report-" + kind, Kind: kind, Summary: "Issue Session " + kind,
			Details: "Check output recorded in workspace.", Evidence: []string{"notes/check.txt"}, Questions: []string{"Inspect exit code?"}, GoalRevision: receivedIssue.RequirementsRevision,
		}
		report := mcpWork[core.Report](t, issue, "submit_report", r)
		require.Equal(t, "mass-t1-i1-r"+strconv.Itoa(i+1), report.ID)
		require.Equal(t, report, mcpWork[core.Report](t, issue, "submit_report", r))
		require.Equal(t, x.ID, report.ExecutionID)
		require.Equal(t, x.Generation, report.Generation)
		require.Equal(t, r.Evidence, report.Evidence)
		require.Equal(t, r.Details, report.Details)
		require.Equal(t, r.Questions, report.Questions)
		require.False(t, report.Historical)
		r.Summary = "changed replay"
		mcpWorkError(t, issue, "submit_report", "conflict", r)
		notice := peer.next(t, "turn/start")
		require.Equal(t, mainBinding.SessionID, notice.session)
		view := decodeInternalBatch(t, notice, report.EventID)[0]
		require.JSONEq(t, fmt.Sprintf(`{"id":%q,"issueId":%q,"executionId":%q,"goalRevision":%d,"kind":%q,"summary":%q,"details":%q,"evidence":["notes/check.txt"],"questions":["Inspect exit code?"],"historical":false}`, report.ID, report.IssueID, report.ExecutionID, report.Revision, report.Kind, report.Summary, report.Details), string(view.Report))
		require.JSONEq(t, fmt.Sprintf(`{"id":%q,"title":%q,"goal":%q,"goalRevision":%d,"currentExecutionId":%q,"status":"active","revision":%d}`, started.Issue.ID, started.Issue.Title, create.Goal, started.Issue.RequirementsRevision, x.ID, started.Issue.Revision), string(view.Issue))
		require.NoError(t, json.Unmarshal(view.Issue, &reviewedIssue))
		notice.answer <- nativeSubmitAnswer{complete: true}
		waitWorkEvent(t, p, report.EventID, "sent")
		ready = report
	}
	require.Len(t, workEvents(t, p, "report_submitted"), 4)
	require.Len(t, page(t, p, "reports").Items, 4)
	require.Equal(t, "active", readRecord[core.Issue](t, p, "issues/"+started.Issue.ID).Status)
	require.Equal(t, "active", readRecord[core.Topic](t, p, "topics/"+mainBinding.OwnerID).Status)
	review := mcpWork[core.Report](t, topic, "get_report", map[string]string{"reportId": ready.ID})
	require.Equal(t, ready, review)
	require.Equal(t, ready, readRecord[core.Report](t, p, "reports/"+ready.ID))
	mcpWorkError(t, foreign, "get_report", "not_found", map[string]string{"reportId": ready.ID})
	withReports := mcpWork[core.Issue](t, topic, "get_issue", map[string]any{
		"issueId": started.Issue.ID, "include": []string{"reports"},
	})
	require.Len(t, withReports.Reports, 4)
	mcpWorkError(t, foreign, "get_issue", "not_found", map[string]any{
		"issueId": started.Issue.ID, "include": []string{"reports"},
	})
	mcpWorkError(t, topic, "topic_complete", "failed_precondition", acceptance("too-early", 1, ready.ID))
	bad := core.CompleteIssueRequest{IssueID: started.Issue.ID, CompleteRequest: acceptance("accept", 1, ready.ID)}
	conflict := mcpWorkError(t, topic, "issue_complete", "conflict", bad)
	require.Equal(t, started.Issue.Revision, conflict.CurrentRevision)
	bad.ExpectedRevision, bad.Evidence = started.Issue.Revision, nil
	mcpWorkError(t, topic, "issue_complete", "invalid_argument", bad)

	complete := core.CompleteIssueRequest{IssueID: reviewedIssue.ID, CompleteRequest: acceptance("accept", reviewedIssue.Revision, ready.ID)}
	done := mcpWork[core.WorkReceipt](t, topic, "issue_complete", complete)
	require.Equal(t, done, mcpWork[core.WorkReceipt](t, topic, "issue_complete", complete))
	require.Equal(t, "completed", done.Issue.Status)
	require.Empty(t, done.Issue.CurrentExecutionID)
	require.Equal(t, complete.Evidence, done.Issue.Completion.Evidence)
	require.Equal(t, "historical", done.Execution.Authority)
	require.Equal(t, "ready", done.Execution.PreparationAtRetirement)
	require.Equal(t, "requested", done.Execution.Stop.Status)
	mcpWorkError(t, issue, "get_issue", "stale_binding", struct{}{})
	mcpWorkError(t, issue, "get_message", "stale_binding", map[string]string{"messageId": source.MessageID})
	mcpWorkError(t, issue, "get_internal_event", "stale_binding", map[string]string{"event_id": x.StartEventID})
	var stopped core.Execution
	require.Eventually(t, func() bool {
		stopped = readRecord[core.Execution](t, p, "executions/"+x.ID)
		return stopped.Stop != nil && stopped.Stop.Status == "observed"
	}, 8*time.Second, 20*time.Millisecond)
	require.Equal(t, "controlAcknowledged", stopped.Stop.Receipt.Result)
	require.Equal(t, "turn-"+issueBinding.SessionID, stopped.Stop.Receipt.Target)
	require.Equal(t, "unknown", stopped.Stop.WorkState)
	require.NotNil(t, stopped.Stop.Observation)
	require.Equal(t, "idle", stopped.Stop.Observation.State)
	require.Equal(t, "native_work_identity_not_in_live_event", stopped.Stop.ObservationGap)
	t.Logf("native interrupt acknowledged; native outcome %s, observation gap %s", stopped.Stop.WorkState, stopped.Stop.ObservationGap)

	historicalIssue := startRoleMCP(t, f, issueBinding)
	historicalList := historicalIssue.call("tools/list", nil)
	require.Empty(t, historicalList["error"])
	var historicalTools struct {
		Tools []struct {
			Name string `json:"name"`
		} `json:"tools"`
	}
	require.NoError(t, json.Unmarshal(historicalList["result"], &historicalTools))
	require.Len(t, historicalTools.Tools, 1)
	require.Equal(t, "submit_report", historicalTools.Tools[0].Name)
	identity := initializeMCP(t, p.endpoint, issueToken)
	require.JSONEq(t, fmt.Sprintf(`{"id":%q,"role":"issue","ownerId":%q,"generation":%d,"topicId":%q,"issueId":%q}`,
		issueBinding.ID, issueBinding.OwnerID, issueBinding.Generation, issueBinding.TopicID, issueBinding.IssueID), string(identity))
	late := core.ReportRequest{OperationID: "late", Kind: "progress", Summary: "Late report from retired Issue Session", GoalRevision: x.Revision}
	report := mcpWork[core.Report](t, historicalIssue, "submit_report", late)
	require.Equal(t, "mass-t1-i1-r5", report.ID)
	require.True(t, report.Historical)
	require.Equal(t, x.ID, report.ExecutionID)
	require.Equal(t, report, mcpWork[core.Report](t, issue, "submit_report", late))
	context := peer.next(t, "thread/inject_items")
	require.Equal(t, mainBinding.SessionID, context.session)
	historical := decodeInternalBatch(t, context, report.EventID)[0]
	require.Equal(t, "context_supplement", historical.Kind)
	require.Empty(t, historical.Report)
	require.JSONEq(t, fmt.Sprintf(`{"kind":"report","id":%q}`, report.ID), string(historical.Refs[0]))
	context.accept()
	waitWorkEvent(t, p, report.EventID, "sent")
	require.Equal(t, "active", readRecord[core.Topic](t, p, "topics/"+mainBinding.OwnerID).Status)

	auto := issueInput("", "create-start")
	auto.Start = true
	second := mcpWork[core.WorkReceipt](t, topic, "create_issue", auto)
	require.Equal(t, second, mcpWork[core.WorkReceipt](t, topic, "create_issue", auto))
	require.Equal(t, "mass-t1-i2", second.Issue.ID)
	require.Equal(t, "mass-t1-i2-e1", second.Execution.ID)
	secondBinding := waitBinding(t, p, "issue", second.Execution.ID, "ready")
	require.Equal(t, mainBinding.Workspace, secondBinding.Workspace)
	final := peer.next(t, "turn/start")
	require.Equal(t, secondBinding.SessionID, final.session)
	decodeInternalBatch(t, final, second.Execution.StartEventID)
	final.answer <- nativeSubmitAnswer{complete: true, final: "Verified the result. This is ordinary model output."}
	waitWorkEvent(t, p, second.Execution.StartEventID, "sent")
	ended := waitIssueObservation(t, p, second.Execution, "ended")
	reminder := checkReminderCall(t, peer, secondBinding, ended)
	reminder.answer <- nativeSubmitAnswer{workID: "ordinary-output-reminder", complete: true}
	waitWorkEvent(t, p, ended.ReminderEventID, "sent")
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
	require.Equal(t, finished, mcpWork[core.Topic](t, topic, "topic_complete", topicComplete))
	mcpWorkError(t, topic, "create_issue", "failed_precondition", issueInput(f.dir, "after-completion"))
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
			work := roleWork[core.WorkReceipt](t, p, token, "create_issue", create)
			binding := waitBinding(t, p, "issue", work.Execution.ID, "ready")
			issue := roleToken(t, f, binding)
			waitWorkEvent(t, p, work.Execution.StartEventID, "sent")
			r := core.ReportRequest{OperationID: "crash-report", Kind: "ready", Summary: "Verified before crash", GoalRevision: work.Execution.Revision}
			req, err := newMCPToolRequest(t.Context(), p.endpoint, issue, "submit_report", r)
			require.NoError(t, err)
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
			require.Len(t, workEvents(t, p, "report_submitted"), expected)
			mcp := startRoleMCP(t, f, binding)
			saved := mcpWork[core.Report](t, mcp, "submit_report", r)
			require.Equal(t, "mass-t1-i1-r1", saved.ID)
			require.Equal(t, saved, mcpWork[core.Report](t, mcp, "submit_report", r))
			require.Len(t, page(t, p, "reports").Items, 1)
			require.Equal(t, []core.Report{saved}, readRecord[core.Issue](t, p, "issues/"+work.Issue.ID).Reports)
			require.Len(t, workEvents(t, p, "report_submitted"), 1)
			require.Equal(t, topic.OwnerID, waitWorkEvent(t, p, saved.EventID, "sent").Recipient.TopicID)
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
	p := f.start("SUPERAGENT_TEST_GATE_STAGE=issue_before_claim", "SUPERAGENT_TEST_GATE_DIR="+gate)
	t.Cleanup(func() { write(t, filepath.Join(gate, "release"), "release") })
	_, token := workBoundaryTopic(t, f, p)
	create := issueInput(f.dir, "create-active")
	create.Start = true
	work := roleWork[core.WorkReceipt](t, p, token, "create_issue", create)
	waitWorkGate(t, gate, "issue_before_claim")
	updated := roleWork[core.WorkReceipt](t, p, token, "issue_update_requirements", core.UpdateRequirementsRequest{
		OperationID: "update-before-claim", IssueID: work.Issue.ID, ExpectedRevision: work.Issue.Revision,
		Goal: "An update before the first Issue Session claim", NotifyIssueSession: true,
	})
	complete := core.CompleteIssueRequest{IssueID: work.Issue.ID, CompleteRequest: acceptance("complete", updated.Issue.Revision, "already-verified.txt")}
	done := roleWork[core.WorkReceipt](t, p, token, "issue_complete", complete)
	require.Nil(t, done.Execution.Stop)
	require.Equal(t, "historical", done.Execution.Authority)
	write(t, filepath.Join(gate, "release"), "release")
	require.Empty(t, waitWorkEvent(t, p, work.Execution.StartEventID, "superseded").SentAt)
	require.Empty(t, waitWorkEvent(t, p, updated.NotificationEventID, "superseded").SentAt)
	p.stop(t, false)
	p = f.start()
	require.Empty(t, waitWorkEvent(t, p, work.Execution.StartEventID, "superseded").SentAt)
	require.Empty(t, waitWorkEvent(t, p, updated.NotificationEventID, "superseded").SentAt)
	require.Equal(t, done.Execution.BindingID, waitBinding(t, p, "issue", work.Execution.ID, "revoked").ID)
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
	work := roleWork[core.WorkReceipt](t, p, token, "create_issue", create)
	waitWorkEvent(t, p, work.Execution.StartEventID, "sent")
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
	require.Equal(t, receipt, roleWork[core.WorkReceipt](t, p, token, "issue_complete", complete))
	require.Equal(t, receipt.Issue, readRecord[core.Issue](t, p, "issues/"+receipt.Issue.ID))
	require.Equal(t, stop, readRecord[core.Execution](t, p, "executions/"+work.Execution.ID).Stop)
	t.Log("restart retained the Issue acceptance receipt and the original unresolved stop without resubmitting")
}
