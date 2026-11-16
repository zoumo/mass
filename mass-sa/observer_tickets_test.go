package sa

import (
	"bytes"
	"encoding/json"
	"gopkg.in/yaml.v3"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// Synthetic history exercises read-side identity and missing-resource behavior;
// real lifecycle operations are separately exercised by the public loop below.
func seedObserverTicketFixture(t *testing.T, config Config, mass *fakeMASSServer) {
	t.Helper()
	store, err := OpenStateStore(config.StateFile)
	require.NoError(t, err)
	at := time.Now().UTC().Add(-time.Hour)
	require.NoError(t, store.updateProject("alpha", func(p *ProjectState) error {
		for _, ticket := range []TicketState{
			{ID: "t03-root", Title: "责任核对主工作", BusinessState: "blocked", AssignmentID: "t03-new", Objective: `Review <img src=https://invalid.example/unsafe> token=ticket-fixture-secret`, Acceptance: "保留真实派工与输入身份", Reports: []ReportState{{AssignmentID: "t03-old", Session: SessionRef{SessionID: "t03-session"}, SourceEventID: "report-event", Status: "confirmed", ID: "report-local", Body: "Local report body", ReportedAt: at}}},
			{ID: "t03-queued", Title: "排队无 Session", BusinessState: "open"},
			{ID: "t03-child", Title: "独立子工作", ParentTicketID: "t03-root", RootTicketID: "t03-root", Depth: 1, BusinessState: "completed", CreatedByAssignment: "t03-old", CreatedBySession: SessionRef{SessionID: "t03-session"}},
			{ID: "t03-grandchild", Title: "独立孙工作", ParentTicketID: "t03-child", RootTicketID: "t03-root", Depth: 2, BusinessState: "open", AssignmentID: "t03-missing"},
			{ID: "t03-reused", Title: "相同 Session 无关工作", BusinessState: "open", AssignmentID: "t03-other"},
		} {
			ticket.ProjectID = "alpha"
			ticket.CreatedAt = at
			ticket.UpdatedAt = at
			if ticket.RootTicketID == "" {
				ticket.RootTicketID = ticket.ID
			}
			p.Tickets[ticket.ID] = ticket
		}
		for i, a := range []AssignmentState{
			{ID: "t03-old", TicketID: "t03-root", Session: SessionRef{SessionID: "t03-session"}, Status: "stopped", ExecutionID: "t03-execution-old", ExecutionState: "completed"},
			{ID: "t03-resumed", TicketID: "t03-root", Session: SessionRef{SessionID: "t03-session"}, Status: "stopped", AwaitingResumedInput: true},
			{ID: "t03-new", TicketID: "t03-root", Session: SessionRef{SessionID: "t03-session-new"}, Status: "active"},
			{ID: "t03-missing", TicketID: "t03-grandchild", Session: SessionRef{SessionID: "t03-deleted-session"}, Status: "active"},
			{ID: "t03-other", TicketID: "t03-reused", Session: SessionRef{SessionID: "t03-session"}, Status: "active"},
		} {
			a.ProjectID = "alpha"
			a.CreatedAt = at.Add(time.Duration(i) * time.Second)
			a.UpdatedAt = a.CreatedAt
			a.RuntimeRef = "fixture-runtime"
			p.Assignments[a.ID] = a
		}
		p.Triggers["t03-source"] = TriggerState{TriggerReceipt: TriggerReceipt{TriggerID: "t03-source", ProjectID: "alpha", Source: "t03/account", ThreadID: "root-thread", AcceptedAt: at}, Summary: "仅直接关联主工作", TicketIDs: []string{"t03-root"}, Decision: "associated"}
		p.Inbox["t03-fixed"] = InboxEventState{ID: "t03-fixed", ProjectID: "alpha", TicketID: "t03-root", TriggerID: "t03-source", TargetAssignmentID: "t03-old", TargetSession: SessionRef{SessionID: "t03-session"}, ExecutionID: "t03-execution-old", State: "delivered", AcceptedAt: at}
		p.Inbox["t03-internal"] = InboxEventState{ID: "t03-internal", ProjectID: "alpha", TicketID: "t03-root", ChildLifecycle: &ChildLifecycleFact{TicketID: "t03-child", State: "completed", Revision: 2, Summary: "独立子工作完成"}, State: "queued", AcceptedAt: at}
		p.Deliveries["t03-batch"] = DeliveryBatchState{ID: "t03-batch", TargetAssignmentID: "t03-old", TargetSession: SessionRef{SessionID: "t03-session"}, ExecutionID: "t03-execution-old", State: "delivered", EventIDs: []string{"t03-fixed"}, CreatedAt: at}
		return nil
	}))
	require.NoError(t, store.updateProject("beta", func(p *ProjectState) error {
		// Same Ticket ID and Session identity in another Project must never join.
		p.Tickets["t03-root"] = TicketState{ID: "t03-root", ProjectID: "beta", Title: "另一 Project 同名身份", AssignmentID: "beta-owner", RootTicketID: "t03-root"}
		p.Assignments["beta-owner"] = AssignmentState{ID: "beta-owner", ProjectID: "beta", TicketID: "t03-root", Session: SessionRef{SessionID: "t03-session"}}
		return nil
	}))
	for _, id := range []string{"t03-session", "t03-session-new"} {
		mass.addSession(Session{Metadata: SessionMetadata{Name: id}, Spec: SessionSpec{RuntimeRef: "fixture-runtime", MCPServers: []MCPServer{{Args: []string{"private-fixture-capability"}}}}, Status: SessionStatus{SessionID: id, State: "offline"}})
	}
}

func TestObserverTicketPublicProjectionAndTraversal(t *testing.T) {
	config, mass := observerFixture(t)
	seedObserverTicketFixture(t, config, mass)
	base, configPath := startObserverProcess(t, config)
	before, err := os.ReadFile(config.StateFile)
	require.NoError(t, err)
	all := observerGet[ObserverPage[ObserverTicket]](t, base, "tickets")
	require.Len(t, all.Items, 6)
	cli := runT06JSON[ObserverPage[ObserverTicket]](t, "observe", "tickets", "--config", configPath)
	require.Equal(t, all.Items, cli.Items)
	root := observerGet[ObserverTicketDetail](t, base, "tickets/t03-root?project=alpha")
	require.Equal(t, "blocked", root.BusinessState)
	require.Equal(t, "report-local", root.Reports[0].ID)
	require.Equal(t, "Local report body", root.Reports[0].Body)
	require.Equal(t, "t03-new", root.CurrentAssignmentID)
	require.Len(t, root.Assignments, 3)
	require.Len(t, root.Children, 1)
	require.Len(t, root.Descendants, 2)
	require.Len(t, root.Reports, 1)
	require.Equal(t, "t03-old", root.DirectTriggers[0].Deliveries[0].AssignmentID)
	require.Equal(t, "t03-session", root.DirectTriggers[0].Deliveries[0].SessionID)
	require.Len(t, root.InternalInputs, 1)
	require.Equal(t, "child_lifecycle", root.InternalInputs[0].Kind)
	require.Empty(t, root.InternalInputs[0].SessionID)
	require.Equal(t, "observed", root.Assignments[0].Observation.Status)
	require.Equal(t, "offline", root.Assignments[0].Observation.Session.State)
	cliDetail := runT06JSON[ObserverTicketDetail](t, "observe", "tickets", "t03-root", "--project", "alpha", "--config", configPath)
	require.Equal(t, root.ObserverTicket, cliDetail.ObserverTicket)
	require.Len(t, cliDetail.Assignments, 3)
	missing := observerGet[ObserverTicketDetail](t, base, "tickets/t03-grandchild?project=alpha")
	require.Equal(t, "not_found", missing.Assignments[0].Observation.Status)
	require.Empty(t, missing.DirectTriggers)
	queued := observerGet[ObserverTicketDetail](t, base, "tickets/t03-queued?project=alpha")
	require.Empty(t, queued.Assignments)
	require.Equal(t, "unassigned", queued.CurrentAssignmentStatus)
	beta := observerGet[ObserverTicketDetail](t, base, "tickets/t03-root?project=beta")
	require.Len(t, beta.Assignments, 1)
	require.Empty(t, beta.Children)
	require.Empty(t, beta.DirectTriggers)
	require.Empty(t, beta.InternalInputs)
	for _, query := range []string{"state=blocked", "parent=t03-root", "root=t03-root", "search=保留真实"} {
		page := observerGet[ObserverPage[ObserverTicket]](t, base, "tickets?project=alpha&"+query)
		require.NotEmpty(t, page.Items)
	}
	for _, path := range []string{"tickets?project=missing", "tickets/t03-root", "tickets/t03-grandchild?project=beta", "tickets?limit=0", "tickets?state=x&state=y", "tickets?method=session.create", "tickets?status=todo", "tickets/t03-root?project=alpha&limit=1"} {
		response, err := http.Get(base + "/observer/api/" + path)
		require.NoError(t, err)
		response.Body.Close()
		require.GreaterOrEqual(t, response.StatusCode, 400)
	}
	raw, err := json.Marshal(root)
	require.NoError(t, err)
	for _, removed := range []string{`"multica"`, `"multicaStatus"`, `"issue"`, `"commentId"`} {
		require.NotContains(t, string(raw), removed)
	}
	require.NotContains(t, string(raw), "ticket-fixture-secret")
	require.NotContains(t, string(raw), "private-fixture-capability")
	require.NotContains(t, string(raw), "capabilityFile")
	require.Contains(t, string(raw), "[redacted]")
	after, err := os.ReadFile(config.StateFile)
	require.NoError(t, err)
	require.True(t, bytes.Equal(before, after), "reads preserve state including outputs/consumers")

	mass.mu.Lock()
	require.Zero(t, mass.createCount)
	require.Zero(t, mass.resumeCount)
	require.Zero(t, mass.promptCount)
	require.Zero(t, mass.closeCount)
	mass.mu.Unlock()
	first := observerGet[ObserverPage[ObserverTicket]](t, base, "tickets?limit=2")
	require.NotEmpty(t, first.Next)
	client, err := NewManagementClient(config)
	require.NoError(t, err)
	var created TicketState
	require.NoError(t, client.Request(t.Context(), "POST", "/tickets", map[string]string{"projectId": "alpha", "title": "arrival", "objective": "new work", "acceptance": "review"}, &created))
	ids := []string{}
	page := first
	for {
		for _, row := range page.Items {
			ids = append(ids, row.ProjectID+"/"+row.ID)
		}
		if page.Next == "" {
			break
		}
		page = observerGet[ObserverPage[ObserverTicket]](t, base, "tickets?limit=2&cursor="+url.QueryEscape(page.Next))
	}
	expected := []string{}
	for _, row := range all.Items {
		expected = append(expected, row.ProjectID+"/"+row.ID)
	}
	require.Equal(t, expected, ids)
	fresh := observerGet[ObserverPage[ObserverTicket]](t, base, "tickets")
	require.Len(t, fresh.Items, 7)
	response, err := http.Get(base + "/observer/api/tickets?limit=2&state=blocked&cursor=" + url.QueryEscape(first.Next))
	require.NoError(t, err)
	response.Body.Close()
	require.Equal(t, 400, response.StatusCode)
	mass.setGetError(&OperationError{Code: "unavailable", Reason: "injected", Message: "private error"})
	unavailable := observerGet[ObserverTicketDetail](t, base, "tickets/t03-root?project=alpha")
	require.Equal(t, "unavailable", unavailable.Assignments[0].Observation.Status)
	require.True(t, unavailable.Assignments[0].Observation.Stale)
	require.Equal(t, "offline", unavailable.Assignments[0].Observation.Session.State)
}

func TestObserverTicketPublicContinuityAndDelegation(t *testing.T) {
	mass, server, router := delegationServer(t)
	base := "http://" + server.config.Management.Listen
	root, parent, worker := lifecycleRoot(t, server, router)
	source := acceptTestTrigger(t, server, "observer-root-source", "root-thread")
	router.call(t, "decide_trigger", decideTriggerArgs{TriggerID: source.TriggerID, Action: "associate", TicketID: root.ID})
	waitLifecycleReport(t, server, root)
	initial := observerGet[ObserverTicketDetail](t, base, "tickets/"+root.ID+"?project=mass")
	require.Len(t, initial.Assignments, 1)
	require.Len(t, initial.DirectTriggers, 1)
	fixed := initial.DirectTriggers[0].Deliveries
	router.call(t, "stop_worker", stopWorkerArgs{RootTicketID: root.ID, Name: parent.Name})
	resumed := decodeT06[WorkerView](t, router.call(t, "create_worker", createWorkerArgs{TicketID: root.ID, Name: parent.Name}))
	require.Equal(t, parent.SessionID, resumed.SessionID)
	require.NotEqual(t, parent.AssignmentID, resumed.AssignmentID)
	continued := decodeT06[WorkerView](t, router.call(t, "create_worker", createWorkerArgs{TicketID: root.ID, Name: parent.Name}))
	require.Equal(t, resumed.AssignmentID, continued.AssignmentID)
	renewed := observerGet[ObserverTicketDetail](t, base, "tickets/"+root.ID+"?project=mass")
	require.Len(t, renewed.Assignments, 2)
	require.Equal(t, resumed.AssignmentID, renewed.CurrentAssignmentID)
	require.Equal(t, fixed, renewed.DirectTriggers[0].Deliveries)
	// Use the current Session-bound capability to create a child and grandchild.
	worker = startT06MCP(t, "mass", "worker", sessionWorkerCapability(t, server, resumed.AssignmentID))
	mass.setState(resumed.SessionID, "running")
	child := decodeT06[ChildTicketResult](t, worker.call(t, "create_child_ticket", childRequest("observer-child"))).Ticket
	childQueued := observerGet[ObserverTicketDetail](t, base, "tickets/"+child.ID+"?project=mass")
	require.Empty(t, childQueued.Assignments)
	require.Empty(t, childQueued.DirectTriggers)
	childWorker := decodeT06[WorkerView](t, worker.call(t, "dispatch_child_ticket", dispatchChildTicketArgs{TicketID: child.ID, Name: "observer-child"}))
	childMCP := startT06MCP(t, "mass", "worker", sessionWorkerCapability(t, server, childWorker.AssignmentID))
	args := childRequest("observer-grandchild")
	args.Authorization.MaxDepth = 0
	grandchild := decodeT06[ChildTicketResult](t, childMCP.call(t, "create_child_ticket", args)).Ticket
	mass.setState(resumed.SessionID, "idle")
	waitChildInputs(t, server, root.ID, 1, "delivered")
	waitLifecycleReport(t, server, root)
	replacement := decodeT06[WorkerView](t, router.call(t, "create_worker", createWorkerArgs{TicketID: root.ID, Name: "replacement", Action: "replace"}))
	final := observerGet[ObserverTicketDetail](t, base, "tickets/"+root.ID+"?project=mass")
	require.Len(t, final.Assignments, 3)
	require.Equal(t, replacement.AssignmentID, final.CurrentAssignmentID)
	require.NotEqual(t, parent.SessionID, replacement.SessionID)
	require.Equal(t, fixed, final.DirectTriggers[0].Deliveries)
	require.Len(t, final.Children, 1)
	require.Len(t, final.Descendants, 2)
	require.Len(t, final.InternalInputs, 1)
	require.Equal(t, "fixed_evidence", final.InternalInputs[0].TargetResolution)
	require.Equal(t, resumed.AssignmentID, final.InternalInputs[0].AssignmentID)
	grand := observerGet[ObserverTicketDetail](t, base, "tickets/"+grandchild.ID+"?project=mass")
	require.Equal(t, child.ID, grand.Parent.ID)
	require.Empty(t, grand.DirectTriggers)
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	configRaw, err := yaml.Marshal(server.config)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(configPath, configRaw, 0600))
	cli := runT06JSON[ObserverTicketDetail](t, "observe", "tickets", root.ID, "--project", "mass", "--config", configPath)
	require.Equal(t, final.ObserverTicket, cli.ObserverTicket)
	server.stop(t)
}

func TestObserverTicketTraversalFreezeAndReferenceIsolation(t *testing.T) {
	config, mass := observerFixture(t)
	seedObserverTicketFixture(t, config, mass)
	store, err := OpenStateStore(config.StateFile)
	require.NoError(t, err)
	observer := newObserver(config, store)
	query := url.Values{"project": {"alpha"}, "state": {"open"}, "limit": {"1"}}
	raw, err := observer.Query(t.Context(), "tickets", query)
	require.NoError(t, err)
	first := raw.(ObserverPage[ObserverTicket])
	require.NotEmpty(t, first.Next)
	require.NoError(t, store.updateProject("alpha", func(p *ProjectState) error {
		for id, ticket := range p.Tickets {
			ticket.BusinessState = "completed"
			ticket.AssignmentID = "t03-other"
			p.Tickets[id] = ticket
		}
		return nil
	}))
	query.Set("cursor", first.Next)
	nextRaw, err := observer.Query(t.Context(), "tickets", query)
	require.NoError(t, err)
	next := nextRaw.(ObserverPage[ObserverTicket])
	require.Equal(t, "open", next.Items[0].BusinessState)
	require.NotEqual(t, "t03-other", next.Items[0].CurrentAssignmentID)
	query.Del("cursor")
	fresh, err := observer.Query(t.Context(), "tickets", query)
	require.NoError(t, err)
	require.Empty(t, fresh.(ObserverPage[ObserverTicket]).Items)
	row := observerTicketSummary(store.Snapshot().Projects["alpha"], store.Snapshot().Projects["alpha"].Tickets["t03-root"])
	require.Equal(t, "reference_unavailable", row.CurrentAssignmentStatus)
	require.Empty(t, row.CurrentSessionID, "sharing a Session does not grant another Ticket's Assignment")
	observer.mu.Lock()
	for id, traversal := range observer.ticketPages {
		traversal.At = time.Now().Add(-16 * time.Minute)
		observer.ticketPages[id] = traversal
	}
	observer.mu.Unlock()
	query.Set("cursor", first.Next)
	_, err = observer.Query(t.Context(), "tickets", query)
	require.Error(t, err)
	require.Contains(t, err.Error(), "invalid_observer_cursor")
}
