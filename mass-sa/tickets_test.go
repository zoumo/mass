package sa

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
)

type ticketFixture struct {
	t     *testing.T
	state string
}

func newTicketFixture(t *testing.T) *ticketFixture {
	t.Helper()
	return &ticketFixture{t: t, state: filepath.Join(t.TempDir(), "state.json")}
}

func (fixture *ticketFixture) config() Config {
	return newTestConfig(fixture.t, "ws://127.0.0.1:24368/rpc", fixture.state, newTestProject("mass"))
}

func (fixture *ticketFixture) bridge() *TicketBridge {
	fixture.t.Helper()
	store, err := OpenStateStore(fixture.state)
	require.NoError(fixture.t, err)
	if store.Snapshot().Projects["mass"].Router == nil {
		require.NoError(fixture.t, store.SetRouter("mass", RouterState{
			SessionRef: SessionRef{SessionID: "router", Namespace: RouterNamespace, Name: "mass-router"},
		}))
	}
	bridge, err := NewTicketBridge(fixture.config(), store)
	require.NoError(fixture.t, err)
	return bridge
}

func mustCreateRootTicket(t *testing.T, bridge *TicketBridge) TicketState {
	t.Helper()
	root, err := bridge.CreateTicket(t.Context(), CreateTicketRequest{
		ProjectID: "mass", Title: "Root work", Objective: "Deliver independently accepted work", Acceptance: "Explicit review",
	})
	require.NoError(t, err)
	return root
}

func mustCreateTicket(t *testing.T, bridge *TicketBridge, rootID string) TicketState {
	t.Helper()
	child, err := bridge.CreateTicket(t.Context(), CreateTicketRequest{
		ProjectID: "mass", ParentTicketID: rootID, Title: "Child work", Objective: "Produce scoped evidence", Acceptance: "Explicit review",
	})
	require.NoError(t, err)
	return child
}

func blockStateWrites(t *testing.T, path string) func() {
	t.Helper()
	dir := filepath.Dir(path)
	parked := dir + "-parked"
	require.NoError(t, os.Rename(dir, parked))
	require.NoError(t, os.WriteFile(dir, []byte("blocked state directory"), 0600))
	restored := false
	restore := func() {
		if !restored {
			require.NoError(t, os.Remove(dir))
			require.NoError(t, os.Rename(parked, dir))
			restored = true
		}
	}
	t.Cleanup(restore)
	return restore
}

func TestLocalTicketPersistenceAndHierarchy(t *testing.T) {
	fixture := newTicketFixture(t)
	bridge := fixture.bridge()
	root := mustCreateRootTicket(t, bridge)
	child := mustCreateTicket(t, bridge, root.ID)
	grandchild := mustCreateTicket(t, bridge, child.ID)
	require.True(t, strings.HasPrefix(root.ID, "t-"))
	require.Equal(t, child.ID, grandchild.ParentTicketID)
	require.Equal(t, root.ID, grandchild.RootTicketID)
	require.Equal(t, root.Namespace, grandchild.Namespace)
	restarted := fixture.bridge()
	readback, err := restarted.ticket("mass", grandchild.ID)
	require.NoError(t, err)
	require.Equal(t, grandchild, readback)
	require.Equal(t, 2, readback.Depth)
	_, err = bridge.CreateTicket(t.Context(), CreateTicketRequest{ProjectID: "missing", Title: "T", Objective: "O", Acceptance: "A"})
	require.Error(t, err)
	_, err = bridge.CreateTicket(t.Context(), CreateTicketRequest{ProjectID: "mass", ParentTicketID: "missing", Title: "T", Objective: "O", Acceptance: "A"})
	require.Error(t, err)
	_, err = bridge.CreateTicket(t.Context(), CreateTicketRequest{ProjectID: "mass", Title: "T", Objective: " ", Acceptance: "A"})
	require.Error(t, err)
}

func TestLocalReportIdentityContentAndAssignment(t *testing.T) {
	fixture := newTicketFixture(t)
	bridge := fixture.bridge()
	ticket := mustCreateRootTicket(t, bridge)
	req := AddReportRequest{
		ProjectID: "mass", TicketID: ticket.ID, AssignmentID: "a-current",
		Session:       SessionRef{SessionID: "native", Namespace: ticket.Namespace, Name: "worker"},
		SourceEventID: "event-1", Body: "Exact original report\n", Structured: false,
	}
	_, err := bridge.AddReport(t.Context(), req)
	require.Error(t, err)
	_, err = bridge.RecordAssignment(t.Context(), RecordAssignmentRequest{ProjectID: "mass", TicketID: ticket.ID, AssignmentID: req.AssignmentID})
	require.NoError(t, err)
	first, err := bridge.AddReport(t.Context(), req)
	require.NoError(t, err)
	require.Len(t, first.Reports, 1)
	require.NotEmpty(t, first.Reports[0].ID)
	require.Equal(t, req.Body, first.Reports[0].Body)
	require.Empty(t, first.BusinessState)
	replayed, err := fixture.bridge().AddReport(t.Context(), req)
	require.NoError(t, err)
	require.Equal(t, first.Reports, replayed.Reports)
	changed := req
	changed.Body = "Different report"
	_, err = bridge.AddReport(t.Context(), changed)
	require.Error(t, err)
	_, err = bridge.RecordAssignment(t.Context(), RecordAssignmentRequest{ProjectID: "mass", TicketID: ticket.ID, AssignmentID: "a-new"})
	require.NoError(t, err)
	_, err = bridge.AddReport(t.Context(), req)
	require.Error(t, err)
	_, err = bridge.FinishTicket(t.Context(), FinishTicketRequest{ProjectID: "mass", TicketID: ticket.ID, Resolution: ResolutionCompleted, Summary: "Old report is insufficient"})
	require.Error(t, err)
}

func TestReportAndRouterNotificationCommitTogether(t *testing.T) {
	harness := newWorkerHarness(t)
	root, ticket := harness.rootAndChild(t, "mass", "report-commit")
	worker := harness.createWorker(t, "mass", root.ID, ticket.ID, "worker-commit", "", "")
	req := AddReportRequest{
		ProjectID: "mass", TicketID: ticket.ID, AssignmentID: worker.AssignmentID,
		Session: workerSession(worker), SourceEventID: "atomic-report", Body: "Persisted evidence",
	}
	before := harness.store.Snapshot()
	restore := blockStateWrites(t, harness.config.StateFile)
	_, err := harness.bridge.AddReport(t.Context(), req)
	require.Error(t, err)
	require.Equal(t, before, harness.store.Snapshot())
	restore()
	result, err := harness.bridge.AddReport(t.Context(), req)
	require.NoError(t, err)
	report := result.Reports[0]
	loaded, err := OpenStateStore(harness.config.StateFile)
	require.NoError(t, err)
	project := loaded.Snapshot().Projects["mass"]
	require.Equal(t, report, project.Tickets[ticket.ID].Reports[0])
	require.Contains(t, string(project.Inbox["report/"+report.ID].Payload), report.ID)
	require.Contains(t, string(project.Inbox["report/"+report.ID].Payload), req.Body)
	count := len(project.Inbox)
	_, err = harness.bridge.AddReport(t.Context(), req)
	require.NoError(t, err)
	require.Len(t, harness.store.Snapshot().Projects["mass"].Inbox, count)
	require.NoError(t, harness.store.updateProject("mass", func(project *ProjectState) error {
		project.Router = nil
		return nil
	}))
	before = harness.store.Snapshot()
	req.SourceEventID = "report-without-router"
	_, err = harness.bridge.AddReport(t.Context(), req)
	require.Equal(t, "router_session_missing", operationReason(err))
	require.Equal(t, before, harness.store.Snapshot())
}

func TestBusinessDecisionAndFactsCommitTogether(t *testing.T) {
	fixture := newTicketFixture(t)
	bridge := fixture.bridge()
	root := mustCreateRootTicket(t, bridge)
	ticket := mustCreateTicket(t, bridge, root.ID)
	req := FinishTicketRequest{ProjectID: "mass", TicketID: ticket.ID, Resolution: ResolutionBlocked, Summary: "Waiting for dependency"}
	before := bridge.store.Snapshot()
	restore := blockStateWrites(t, fixture.state)
	_, err := bridge.FinishTicket(t.Context(), req)
	require.Error(t, err)
	require.Equal(t, before, bridge.store.Snapshot())
	restore()
	result, err := bridge.FinishTicket(t.Context(), req)
	require.NoError(t, err)
	require.Equal(t, "blocked", result.BusinessState)
	require.Equal(t, uint64(1), result.Revision)
	restarted := fixture.bridge()
	project := restarted.store.Snapshot().Projects["mass"]
	require.Equal(t, result, project.Tickets[ticket.ID])
	require.Len(t, project.Outputs, 1)
	require.Equal(t, req.Summary, project.Outputs["fact/"+ticket.ID+"/1"].Summary)
	require.Len(t, project.Inbox, 1)
	require.Equal(t, "blocked", project.Inbox["child/"+ticket.ID+"/1/blocked"].ChildLifecycle.State)
	_, err = bridge.FinishTicket(t.Context(), req)
	require.Error(t, err)
	require.Len(t, bridge.store.Snapshot().Projects["mass"].Outputs, 1)
}

func TestDelegationAndNotificationCommitTogether(t *testing.T) {
	harness := newWorkerHarness(t)
	grant := WorkAuthorization{
		Resources: []string{"repo:demo"}, Tools: workerGrantTools,
		RuntimeRefs: []string{"deepseek-claude"}, Models: []string{"deepseek-worker"},
		Efforts: []string{"high"}, MaxDepth: 2, MaxTickets: 2,
	}
	root, err := harness.service.createTicket(t.Context(), "mass", createTicketArgs{
		Title: "Atomic delegation", Objective: "Deliver scoped work", Acceptance: "Review", Authorization: &grant,
	})
	require.NoError(t, err)
	parent := harness.createWorker(t, "mass", root.ID, root.ID, "parent", "", "")
	capability, _ := workerCapability(t, harness, parent.AssignmentID)
	args := createChildTicketArgs{
		RequestID: "stable-child", Title: "Child", Objective: "Scoped work", Acceptance: "Review",
		Authorization: grant, Execution: WorkerExecution{RuntimeRef: "deepseek-claude", Model: "deepseek-worker", Effort: "high"},
	}
	before := harness.store.Snapshot()
	restore := blockStateWrites(t, harness.config.StateFile)
	status, body := harness.callTool(t, capability.Token, "create_child_ticket", args)
	assertToolError(t, status, body, http.StatusServiceUnavailable, "state_unavailable")
	require.Equal(t, before, harness.store.Snapshot())
	restore()
	status, body = harness.callTool(t, capability.Token, "create_child_ticket", args)
	require.Equal(t, http.StatusOK, status, string(body))
	child := decodeToolResult[ChildTicketResult](t, body).Ticket
	loaded, err := OpenStateStore(harness.config.StateFile)
	require.NoError(t, err)
	project := loaded.Snapshot().Projects["mass"]
	require.Len(t, project.Tickets, 2)
	require.Equal(t, child.ID, project.Delegations[root.ID+":"+args.RequestID].TicketID)
	require.Equal(t, child.ID, project.Inbox["child/"+child.ID+"/0/created"].ChildLifecycle.TicketID)
	status, body = harness.callTool(t, capability.Token, "create_child_ticket", args)
	require.Equal(t, http.StatusOK, status, string(body))
	require.Equal(t, child, decodeToolResult[ChildTicketResult](t, body).Ticket)
	require.Len(t, harness.store.Snapshot().Projects["mass"].Inbox, 1)
}

func TestConcurrentLocalTicketCreation(t *testing.T) {
	bridge := newTicketFixture(t).bridge()
	const count = 16
	var group sync.WaitGroup
	results := make(chan error, count)
	for range count {
		group.Go(func() {
			_, err := bridge.CreateTicket(context.Background(), CreateTicketRequest{
				ProjectID: "mass", Title: "Concurrent work", Objective: "Distinct task", Acceptance: "Review",
			})
			results <- err
		})
	}
	group.Wait()
	close(results)
	for err := range results {
		require.NoError(t, err)
	}
	reloaded, err := OpenStateStore(bridge.store.path)
	require.NoError(t, err)
	require.Len(t, reloaded.Snapshot().Projects["mass"].Tickets, count)
}
