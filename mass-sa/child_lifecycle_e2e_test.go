package sa

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func lifecycleRoot(t *testing.T, server *t06Server, router *t06MCP) (TicketState, WorkerView, *t06MCP) {
	t.Helper()
	grant := delegationGrant()
	root := decodeT06[TicketState](t, router.call(t, "create_ticket", createTicketArgs{Title: "Lifecycle root", Objective: "Coordinate scoped children", Acceptance: "Explicit overall acceptance", Authorization: &grant}))
	worker := decodeT06[WorkerView](t, router.call(t, "create_worker", createWorkerArgs{TicketID: root.ID, Name: "parent"}))
	return root, worker, startT06MCP(t, "mass", "worker", sessionWorkerCapability(t, server, worker.AssignmentID))
}

func lifecycleInputs(t *testing.T, server *t06Server, router *t06MCP, parentID string) []InboxEventState {
	t.Helper()
	return decodeT06[TicketContext](t, router.call(t, "get_ticket_context", ticketContextArgs{TicketID: parentID})).ChildInputs
}

func waitChildInputs(t *testing.T, server *t06Server, parentID string, count int, state string) {
	t.Helper()
	waitT06(t, 15*time.Second, "child lifecycle inputs "+state, func() bool {
		n := 0
		for _, event := range triggerSnapshot(t, server).Inbox {
			if event.TicketID == parentID && event.ChildLifecycle != nil {
				if event.State != state {
					return false
				}
				n++
			}
		}
		return n == count
	})
}

func waitLifecycleReport(t *testing.T, server *t06Server, ticket TicketState) {
	t.Helper()
	waitT06(t, 15*time.Second, "Ticket report from settled current Assignment", func() bool {
		var readback TicketState
		require.NoError(t, server.client.Request(t.Context(), "GET", "/tickets/"+ticket.ID+"?projectId=mass", nil, &readback))
		p := triggerSnapshot(t, server)
		for _, report := range readback.Reports {
			if report.AssignmentID == readback.AssignmentID && report.Status == "confirmed" && p.Assignments[readback.AssignmentID].ExecutionState == "completed" {
				return true
			}
		}
		return false
	})
}

func TestChildLifecycleCurrentParentPublicLoop(t *testing.T) {
	mass, fake, server, router := delegationServer(t)
	root, parent, worker := lifecycleRoot(t, server, router)
	mass.setState(parent.SessionID, "running")
	args := childRequest("all-states")
	child := decodeT06[ChildTicketResult](t, worker.call(t, "create_child_ticket", args)).Ticket
	require.Equal(t, child.ID, decodeT06[ChildTicketResult](t, worker.call(t, "create_child_ticket", args)).Ticket.ID)
	waitChildInputs(t, server, root.ID, 1, "queued")
	mass.setState(parent.SessionID, "offline")
	router.call(t, "stop_worker", stopWorkerArgs{RootTicketID: root.ID, Name: parent.Name})
	// The child can start and finish while its parent's Session is closed.
	childWorker := decodeT06[WorkerView](t, router.call(t, "create_worker", createWorkerArgs{TicketID: child.ID, Name: "child"}))
	router.call(t, "send_to_worker", routerSendToWorkerArgs{RootTicketID: root.ID, Name: childWorker.Name, Message: "Execute independent child and report evidence"})
	waitLifecycleReport(t, server, child)
	router.call(t, "stop_worker", stopWorkerArgs{RootTicketID: root.ID, Name: childWorker.Name})
	require.Empty(t, triggerSnapshot(t, server).Tickets[child.ID].BusinessState, "ordinary final and idle are not a lifecycle conclusion")
	require.Len(t, lifecycleInputs(t, server, router, root.ID), 1)
	states := []string{"blocked", "waiting_user", "completed", "reopened", "failed", "reopened", "cancelled"}
	for _, state := range states {
		result := decodeT06[TicketState](t, router.call(t, "finish_ticket", finishTicketArgs{TicketID: child.ID, Resolution: state, Summary: "Confirmed child " + state}))
		require.Equal(t, state, result.BusinessState)
		require.Equal(t, TicketResolution(state).issueStatus(), fake.issue(child.Issue.ID).Status)
	}
	inputs := lifecycleInputs(t, server, router, root.ID)
	require.Len(t, inputs, 8)
	for i, event := range inputs {
		require.Equal(t, "queued", event.State)
		require.Empty(t, event.TargetAssignmentID)
		require.Empty(t, event.TargetSession.SessionID)
		require.Empty(t, event.TriggerID)
		require.Empty(t, event.ThreadID)
		require.Equal(t, child.ID, event.ChildLifecycle.TicketID)
		require.Equal(t, parent.SessionID, event.ChildLifecycle.CreatedBySession.SessionID)
		if i == 0 {
			require.Equal(t, "created", event.ChildLifecycle.State)
		} else {
			require.Equal(t, states[i-1], event.ChildLifecycle.State)
			require.Equal(t, "Confirmed child "+states[i-1], event.ChildLifecycle.Summary)
			require.NotEmpty(t, event.ChildLifecycle.ResultCommentID)
			require.NotNil(t, event.ChildLifecycle.Report)
			require.Equal(t, childWorker.AssignmentID, event.ChildLifecycle.Report.AssignmentID)
		}
	}
	require.Empty(t, triggerSnapshot(t, server).Triggers)
	for _, output := range triggerSnapshot(t, server).Outputs {
		require.Empty(t, output.Targets, "source-free child has no channel destinations")
	}
	server.stop(t)
	server = startT06Server(t, server.config)
	replacement := decodeT06[WorkerView](t, router.call(t, "create_worker", createWorkerArgs{TicketID: root.ID, Name: "replacement", Action: "replace"}))
	waitChildInputs(t, server, root.ID, 8, "delivered")
	waitLifecycleReport(t, server, root)
	for _, event := range lifecycleInputs(t, server, router, root.ID) {
		require.Equal(t, replacement.AssignmentID, event.TargetAssignmentID)
		require.Equal(t, replacement.SessionID, event.TargetSession.SessionID)
	}
	require.Empty(t, promptsForSession(observedPrompts(t, mass), parent.SessionID))
	for _, input := range inputs {
		found := 0
		for _, prompt := range promptsForSession(observedPrompts(t, mass), replacement.SessionID) {
			if strings.Contains(prompt.Text, "### Event "+input.ID+"\n") {
				found++
			}
		}
		require.Equal(t, 1, found, input.ID)
	}
	require.Empty(t, triggerSnapshot(t, server).Tickets[root.ID].BusinessState, "child completion cannot close parent")
	accepted := lifecycleInputs(t, server, router, root.ID)
	third := decodeT06[WorkerView](t, router.call(t, "create_worker", createWorkerArgs{TicketID: root.ID, Name: "third", Action: "replace"}))
	require.NotEqual(t, replacement.SessionID, third.SessionID)
	server.stop(t)
	server = startT06Server(t, server.config)
	require.Equal(t, accepted, lifecycleInputs(t, server, router, root.ID))
	require.Empty(t, promptsForSession(observedPrompts(t, mass), third.SessionID), "accepted notifications must not move to another executor")
	// A fresh lifecycle change is a new round on the current Assignment.
	router.call(t, "finish_ticket", finishTicketArgs{TicketID: child.ID, Resolution: "reopened", Summary: "Operator authorizes child follow-up"})
	waitChildInputs(t, server, root.ID, 9, "delivered")
	last := lifecycleInputs(t, server, router, root.ID)[8]
	require.Equal(t, third.AssignmentID, last.TargetAssignmentID)
	require.Equal(t, "reopened", last.ChildLifecycle.State)
	waitLifecycleReport(t, server, root)
	router.call(t, "finish_ticket", finishTicketArgs{TicketID: child.ID, Resolution: "blocked", Summary: "Follow-up needs a dependency"})
	waitChildInputs(t, server, root.ID, 10, "delivered")
	waitLifecycleReport(t, server, root)
	rounds := promptsForSession(observedPrompts(t, mass), third.SessionID)
	require.Len(t, rounds, 2, "separate child changes continue the same parent Assignment across rounds")
	require.Equal(t, third.AssignmentID, lifecycleInputs(t, server, router, root.ID)[9].TargetAssignmentID)
	server.stop(t)
}

func TestChildLifecycleAncestorClosureFencePublicLoop(t *testing.T) {
	mass, fake, server, router := delegationServer(t)
	root, parent, worker := lifecycleRoot(t, server, router)
	mass.setState(parent.SessionID, "running")
	childArgs := childRequest("child-fence")
	childArgs.WorkerName = "child"
	child := decodeT06[ChildTicketResult](t, worker.call(t, "create_child_ticket", childArgs))
	childMCP := startT06MCP(t, "mass", "worker", sessionWorkerCapability(t, server, child.Worker.AssignmentID))
	grand := decodeT06[ChildTicketResult](t, childMCP.call(t, "create_child_ticket", childRequest("grand-fence"))).Ticket
	router.call(t, "finish_ticket", finishTicketArgs{TicketID: grand.ID, Resolution: "cancelled", Summary: "Dispose grandchild explicitly"})
	router.call(t, "finish_ticket", finishTicketArgs{TicketID: child.Ticket.ID, Resolution: "cancelled", Summary: "Dispose child explicitly"})
	server.stop(t)
	t.Setenv("FAKE_MULTICA_FAIL", "status")
	server = startT06Server(t, server.config)
	require.NotEmpty(t, router.callError(t, "finish_ticket", finishTicketArgs{TicketID: root.ID, Resolution: "cancelled", Summary: "Explicitly close parent"}).Reason)
	require.NotNil(t, triggerSnapshot(t, server).Tickets[root.ID].Transition)
	before := len(fake.invocationsOf("issue", "status"))
	for _, ticketID := range []string{child.Ticket.ID, grand.ID} {
		require.Equal(t, "parent_transition_pending", router.callError(t, "finish_ticket", finishTicketArgs{TicketID: ticketID, Resolution: "reopened", Summary: "Cannot race ancestor closure"}).Reason)
	}
	require.Len(t, fake.invocationsOf("issue", "status"), before)
	server.stop(t)
}

func TestChildLifecycleOfflineAndUnknownPublicLoop(t *testing.T) {
	for _, mode := range []string{"offline", "unknown"} {
		t.Run(mode, func(t *testing.T) {
			mass, _, server, router := delegationServer(t)
			root, parent, worker := lifecycleRoot(t, server, router)
			mass.setState(parent.SessionID, "offline")
			child := decodeT06[ChildTicketResult](t, worker.call(t, "create_child_ticket", childRequest(mode))).Ticket
			waitChildInputs(t, server, root.ID, 1, "queued")
			server.stop(t)
			server = startT06Server(t, server.config)
			require.Empty(t, promptsForSession(observedPrompts(t, mass), parent.SessionID))
			if mode == "unknown" {
				mass.setDropPromptReply(true)
			}
			recovered := decodeT06[WorkerView](t, router.call(t, "create_worker", createWorkerArgs{TicketID: root.ID, Name: parent.Name}))
			require.Equal(t, parent.SessionID, recovered.SessionID)
			state := map[string]string{"offline": "delivered", "unknown": "unknown"}[mode]
			waitChildInputs(t, server, root.ID, 1, state)
			before := lifecycleInputs(t, server, router, root.ID)
			require.Equal(t, recovered.AssignmentID, before[0].TargetAssignmentID)
			require.Equal(t, child.ID, before[0].ChildLifecycle.TicketID)
			server.stop(t)
			mass.setDropPromptReply(false)
			_, _, promptsBefore := mass.operationCounts()
			server = startT06Server(t, server.config)
			require.Equal(t, before, lifecycleInputs(t, server, router, root.ID))
			if mode == "unknown" {
				failure := router.callError(t, "create_worker", createWorkerArgs{TicketID: root.ID, Name: "replacement", Action: "replace"})
				require.Equal(t, "worker_execution_unconfirmed", failure.Reason)
			}
			_, _, promptsAfter := mass.operationCounts()
			require.Equal(t, promptsBefore, promptsAfter, "restart cannot replay attempted lifecycle inputs")
			server.stop(t)
		})
	}
}

func TestChildLifecycleClosureAndLateAuditPublicLoop(t *testing.T) {
	mass, fake, server, router := delegationServer(t)
	root, parent, worker := lifecycleRoot(t, server, router)
	childArgs := childRequest("child")
	childArgs.WorkerName = "child"
	child := decodeT06[ChildTicketResult](t, worker.call(t, "create_child_ticket", childArgs))
	waitLifecycleReport(t, server, root)
	childMCP := startT06MCP(t, "mass", "worker", sessionWorkerCapability(t, server, child.Worker.AssignmentID))
	grand := decodeT06[ChildTicketResult](t, childMCP.call(t, "create_child_ticket", childRequest("grandchild"))).Ticket
	waitLifecycleReport(t, server, child.Ticket)
	before := len(fake.invocationsOf("issue", "status"))
	for _, resolution := range []string{"completed", "cancelled", "failed"} {
		require.Equal(t, "unfinished_child_tickets", router.callError(t, "finish_ticket", finishTicketArgs{TicketID: root.ID, Resolution: resolution, Summary: "Attempt closing with unfinished child"}).Reason)
		require.Equal(t, "unfinished_child_tickets", router.callError(t, "finish_ticket", finishTicketArgs{TicketID: child.Ticket.ID, Resolution: resolution, Summary: "Attempt closing with unfinished grandchild"}).Reason)
	}
	require.Len(t, fake.invocationsOf("issue", "status"), before, "closure rejection must precede external status mutations")
	// Parent resource lifecycle is independent of still-running child work.
	router.call(t, "stop_worker", stopWorkerArgs{RootTicketID: root.ID, Name: parent.Name})
	replacement := decodeT06[WorkerView](t, router.call(t, "create_worker", createWorkerArgs{TicketID: root.ID, Name: "replacement", Action: "replace"}))
	require.Equal(t, "active", triggerSnapshot(t, server).Assignments[child.Worker.AssignmentID].Status)
	require.Empty(t, triggerSnapshot(t, server).Tickets[child.Ticket.ID].BusinessState)
	router.call(t, "finish_ticket", finishTicketArgs{TicketID: grand.ID, Resolution: "cancelled", Summary: "Operator cancels descendant work explicitly"})
	router.call(t, "finish_ticket", finishTicketArgs{TicketID: child.Ticket.ID, Resolution: "cancelled", Summary: "Operator cancels child after disposing descendants"})
	// A late reopened grandchild under a terminated child is still a root
	// responsibility; checking only the direct child's terminal flag is unsafe.
	router.call(t, "finish_ticket", finishTicketArgs{TicketID: grand.ID, Resolution: "reopened", Summary: "Operator reopens descendant work"})
	require.Equal(t, "unfinished_child_tickets", router.callError(t, "finish_ticket", finishTicketArgs{TicketID: root.ID, Resolution: "cancelled", Summary: "Cannot strand a reopened descendant"}).Reason)
	router.call(t, "finish_ticket", finishTicketArgs{TicketID: grand.ID, Resolution: "cancelled", Summary: "Explicitly dispose reopened descendant"})
	waitT06(t, 15*time.Second, "replacement reviewed child cancellation", func() bool {
		p := triggerSnapshot(t, server)
		for _, r := range p.Tickets[root.ID].Reports {
			if r.AssignmentID == replacement.AssignmentID && r.Status == "confirmed" && p.Assignments[replacement.AssignmentID].ExecutionState == "completed" {
				return true
			}
		}
		return false
	})
	router.call(t, "finish_ticket", finishTicketArgs{TicketID: root.ID, Resolution: "completed", Summary: "Accept overall work with explicit child disposition"})
	require.Equal(t, "done", fake.issue(root.Issue.ID).Status)
	beforeLate := len(promptsForSession(observedPrompts(t, mass), replacement.SessionID))
	for _, state := range []string{"reopened", "blocked", "cancelled"} {
		router.call(t, "finish_ticket", finishTicketArgs{TicketID: child.Ticket.ID, Resolution: state, Summary: "Late child " + state})
	}
	late := lifecycleInputs(t, server, router, root.ID)
	require.Len(t, late, 5)
	for _, event := range late[2:] {
		require.Equal(t, "queued", event.State)
		require.Empty(t, event.TargetAssignmentID)
	}
	server.stop(t)
	server = startT06Server(t, server.config)
	require.Equal(t, late, lifecycleInputs(t, server, router, root.ID))
	readback := runT06JSON[TicketState](t, "ticket", "get", root.ID, "--config", outputTestConfig(t, server), "--project", "mass")
	require.Equal(t, "completed", readback.BusinessState)
	afterLate := len(promptsForSession(observedPrompts(t, mass), replacement.SessionID))
	require.Equal(t, beforeLate, afterLate, "late facts cannot reopen or execute the completed parent")
	// The same audit rule preserves an operator's explicit cancellation.
	router.call(t, "finish_ticket", finishTicketArgs{TicketID: root.ID, Resolution: "reopened", Summary: "Operator changes overall conclusion"})
	router.call(t, "finish_ticket", finishTicketArgs{TicketID: root.ID, Resolution: "cancelled", Summary: "Operator stops overall work"})
	router.call(t, "finish_ticket", finishTicketArgs{TicketID: child.Ticket.ID, Resolution: "reopened", Summary: "Late child follow-up after parent cancellation"})
	router.call(t, "finish_ticket", finishTicketArgs{TicketID: child.Ticket.ID, Resolution: "failed", Summary: "Explicit late child failure"})
	server.stop(t)
	server = startT06Server(t, server.config)
	audit := decodeT06[TicketContext](t, router.call(t, "get_ticket_context", ticketContextArgs{TicketID: root.ID}))
	require.Equal(t, "cancelled", audit.Ticket.BusinessState)
	require.Len(t, audit.ChildInputs, 7)
	require.Equal(t, "failed", audit.ChildInputs[6].ChildLifecycle.State)
	afterCancelled := len(promptsForSession(observedPrompts(t, mass), replacement.SessionID))
	require.Equal(t, beforeLate, afterCancelled)
	server.stop(t)
}

func TestChildLifecycleTransitionRecoveryPublicLoop(t *testing.T) {
	for _, afterWrite := range []bool{false, true} {
		t.Run(fmt.Sprint(afterWrite), func(t *testing.T) {
			mass, fake, server, router := delegationServer(t)
			root, parent, worker := lifecycleRoot(t, server, router)
			mass.setState(parent.SessionID, "running")
			child := decodeT06[ChildTicketResult](t, worker.call(t, "create_child_ticket", childRequest("recover"))).Ticket
			server.stop(t)
			t.Setenv("FAKE_MULTICA_FAIL", "status")
			if afterWrite {
				t.Setenv("FAKE_MULTICA_FAIL_AFTER_WRITE", "1")
			}
			server = startT06Server(t, server.config)
			require.NotEmpty(t, router.callError(t, "finish_ticket", finishTicketArgs{TicketID: child.ID, Resolution: "cancelled", Summary: "Explicit child cancellation"}).Reason)
			require.Len(t, lifecycleInputs(t, server, router, root.ID), 1, "unconfirmed writes cannot produce lifecycle facts")
			require.Equal(t, "unfinished_child_tickets", router.callError(t, "finish_ticket", finishTicketArgs{TicketID: root.ID, Resolution: "cancelled", Summary: "Cannot silently discard uncertain child"}).Reason)
			calls := len(fake.invocationsOf("issue", "status"))
			server.stop(t)
			t.Setenv("FAKE_MULTICA_FAIL", "")
			t.Setenv("FAKE_MULTICA_FAIL_AFTER_WRITE", "")
			server = startT06Server(t, server.config)
			waitT06(t, 15*time.Second, "startup transition readback", func() bool {
				return strings.Contains(server.logs(), "business transition still needs reconciliation") || strings.Contains(server.logs(), "router ready")
			})
			if afterWrite {
				runT06CLI(t, "ticket", "reconcile", child.ID, "--config", outputTestConfig(t, server), "--project", "mass")
				inputs := lifecycleInputs(t, server, router, root.ID)
				require.Len(t, inputs, 2)
				require.Equal(t, "cancelled", inputs[1].ChildLifecycle.State)
				require.NotEmpty(t, inputs[1].ChildLifecycle.ResultCommentID)
			} else {
				require.Len(t, lifecycleInputs(t, server, router, root.ID), 1)
			}
			require.Len(t, fake.invocationsOf("issue", "status"), calls)
			server.stop(t)
		})
	}
}
