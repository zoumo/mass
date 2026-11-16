package sa

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func delegationServer(t *testing.T) (*fakeMASSServer, *t06Server, *t06MCP) {
	t.Helper()
	mass := newFakeMASSServer(t, "")
	project := newTestProject("mass")
	project.CWD = t.TempDir()
	project.Worker.RuntimeRefs = []string{"alternate-worker-runtime"}
	otherProject := project
	otherProject.ID, otherProject.Name = "other", "OTHER"
	config := newTestConfig(t, mass.endpoint(), filepath.Join(t.TempDir(), "state.json"), project, otherProject)
	config.Management.Listen = freeLoopbackAddress(t)
	config.Management.TokenFile = filepath.Join(t.TempDir(), "token")
	require.NoError(t, os.WriteFile(config.Management.TokenFile, []byte("delegation-test"), 0600))
	server := startT06Server(t, config)
	waitT06(t, 15*time.Second, "Router ready", func() bool { return triggerSnapshot(t, server).Router != nil })
	router := startT06MCP(t, "mass", "router", triggerSnapshot(t, server).Router.CapabilityFile)
	return mass, server, router
}

func delegationGrant() WorkAuthorization {
	return WorkAuthorization{Resources: []string{"repo:demo/src", "repo:demo/tests"}, Tools: append([]string(nil), workerGrantTools...), RuntimeRefs: []string{"deepseek-claude", "alternate-worker-runtime"}, Models: []string{"deepseek-worker", "child-model"}, Efforts: []string{"high", "max"}, MaxDepth: 2, MaxTickets: 8}
}

func childRequest(id string) createChildTicketArgs {
	grant := delegationGrant()
	grant.Resources = []string{"repo:demo/src"}
	grant.Tools = []string{"get_ticket_context", "create_child_ticket", "dispatch_child_ticket"}
	grant.RuntimeRefs = []string{"alternate-worker-runtime"}
	grant.Models = []string{"child-model"}
	grant.Efforts = []string{"max"}
	return createChildTicketArgs{RequestID: id, Title: "Independent implementation", Objective: "Implement within repo:demo/src", Acceptance: "Explicit review of source change evidence", Authorization: grant, Execution: WorkerExecution{RuntimeRef: "alternate-worker-runtime", Model: "child-model", Effort: "max"}}
}

func TestWorkerDelegationPublicLoop(t *testing.T) {
	mass, server, router := delegationServer(t)
	grant := delegationGrant()
	root := decodeT06[TicketState](t, router.call(t, "create_ticket", createTicketArgs{Title: "Root", Objective: "Deliver scoped work", Acceptance: "Review all evidence", Authorization: &grant}))
	parent := decodeT06[WorkerView](t, router.call(t, "create_worker", createWorkerArgs{TicketID: root.ID, Name: "parent"}))
	worker := startT06MCP(t, "mass", "worker", sessionWorkerCapability(t, server, parent.AssignmentID))
	ordinary := decodeT06[TicketState](t, router.call(t, "create_ticket", createTicketArgs{Title: "Ordinary", Objective: "Direct work", Acceptance: "Explicit review"}))
	ordinaryWorker := decodeT06[WorkerView](t, router.call(t, "create_worker", createWorkerArgs{TicketID: ordinary.ID, Name: "ordinary"}))
	ordinaryMCP := startT06MCP(t, "mass", "worker", sessionWorkerCapability(t, server, ordinaryWorker.AssignmentID))
	var crossProject TicketState
	require.NoError(t, server.client.Request(t.Context(), "POST", "/tickets", map[string]string{"projectId": "other", "title": "Other project", "objective": "Separate project scope", "acceptance": "Review"}, &crossProject))
	require.Equal(t, "not_found", worker.callError(t, "dispatch_child_ticket", dispatchChildTicketArgs{TicketID: crossProject.ID, Name: "cross-project"}).Code)
	require.Equal(t, "delegation_not_authorized", ordinaryMCP.callError(t, "create_child_ticket", childRequest("denied")).Reason)
	beforeNative := triggerSnapshot(t, server)
	// Agent-native tools are not routed to mass-sa or mapped into work items.
	ordinaryMCP.call(t, "get_ticket_context", ticketContextArgs{})
	require.Len(t, triggerSnapshot(t, server).Tickets, len(beforeNative.Tickets))
	require.Empty(t, triggerSnapshot(t, server).Delegations)
	require.Empty(t, triggerSnapshot(t, server).Outputs)
	// Independent stdio clients race the same stable creation identity.
	concurrentArgs := childRequest("concurrent")
	clients := []*t06MCP{worker, startT06MCP(t, "mass", "worker", sessionWorkerCapability(t, server, parent.AssignmentID))}
	responses := make(chan map[string]json.RawMessage, len(clients))
	var group sync.WaitGroup
	beforeConcurrent := len(triggerSnapshot(t, server).Tickets)
	for _, client := range clients {
		group.Go(func() {
			responses <- client.request(t, "tools/call", map[string]any{"name": "create_child_ticket", "arguments": concurrentArgs})
		})
	}
	group.Wait()
	close(responses)
	for response := range responses {
		require.Empty(t, response["error"])
	}
	concurrentChild := decodeT06[ChildTicketResult](t, worker.call(t, "create_child_ticket", concurrentArgs))
	require.NotEmpty(t, concurrentChild.Ticket.ID)
	require.Len(t, triggerSnapshot(t, server).Tickets, beforeConcurrent+1)
	require.Len(t, triggerSnapshot(t, server).Delegations, 1)
	trigger := acceptTestTrigger(t, server, "root-source", "root-thread")
	router.call(t, "decide_trigger", decideTriggerArgs{TriggerID: trigger.TriggerID, Action: "associate", TicketID: root.ID})
	require.NotEmpty(t, triggerSnapshot(t, server).Tickets[root.ID].OutputTriggerID)
	createBefore := len(triggerSnapshot(t, server).Tickets)
	for field, value := range map[string]any{"parentTicketId": ordinary.ID, "rootTicketId": ordinary.ID, "namespace": ordinary.Namespace, "projectId": "other", "ticketId": root.ID} {
		raw, err := json.Marshal(childRequest("forged-" + field))
		require.NoError(t, err)
		var args map[string]any
		require.NoError(t, json.Unmarshal(raw, &args))
		args[field] = value
		require.Equal(t, "invalid_argument", worker.callError(t, "create_child_ticket", args).Code, field)
	}
	for _, change := range []func(*createChildTicketArgs){
		func(a *createChildTicketArgs) { a.Authorization.Resources = []string{"repo:other"} },
		func(a *createChildTicketArgs) { a.Authorization.Tools = append(a.Authorization.Tools, "finish_ticket") },
		func(a *createChildTicketArgs) {
			a.Authorization.Models = []string{"outside-model"}
			a.Execution.Model = "outside-model"
		},
		func(a *createChildTicketArgs) {
			a.Authorization.RuntimeRefs = []string{"unconfigured"}
			a.Execution.RuntimeRef = "unconfigured"
		},
		func(a *createChildTicketArgs) { a.Execution.Effort = "low" },
		func(a *createChildTicketArgs) { a.Authorization.MaxDepth = 3 },
		func(a *createChildTicketArgs) { a.Authorization.MaxTickets = 9 },
	} {
		args := childRequest("outside")
		change(&args)
		require.NotEmpty(t, worker.callError(t, "create_child_ticket", args).Reason)
	}
	require.Len(t, triggerSnapshot(t, server).Tickets, createBefore)
	args := childRequest("implementation-v1")
	child := decodeT06[ChildTicketResult](t, worker.call(t, "create_child_ticket", args))
	require.Nil(t, child.Worker)
	require.Empty(t, child.Ticket.AssignmentID)
	require.Equal(t, root.ID, child.Ticket.ParentTicketID)
	require.Equal(t, root.ID, child.Ticket.RootTicketID)
	require.Equal(t, root.Namespace, child.Ticket.Namespace)
	require.Equal(t, parent.AssignmentID, child.Ticket.CreatedByAssignment)
	require.Equal(t, parent.SessionID, child.Ticket.CreatedBySession.SessionID)
	require.Empty(t, child.Ticket.OutputTriggerID)
	require.Empty(t, child.Ticket.OutputSubscribers)
	require.Equal(t, &args.Execution, child.Ticket.Execution)
	require.Equal(t, child, decodeT06[ChildTicketResult](t, worker.call(t, "create_child_ticket", args)))
	conflict := args
	conflict.Objective = "Different work"
	require.Equal(t, "delegation_request_conflict", worker.callError(t, "create_child_ticket", conflict).Reason)
	require.Len(t, triggerSnapshot(t, server).Tickets, createBefore+1)
	require.Equal(t, "not_found", worker.callError(t, "dispatch_child_ticket", dispatchChildTicketArgs{TicketID: ordinary.ID, Name: "cross-root"}).Code)
	require.Equal(t, "not_found", worker.callError(t, "dispatch_child_ticket", dispatchChildTicketArgs{TicketID: root.ID, Name: "cycle"}).Code)
	childWorker := decodeT06[WorkerView](t, worker.call(t, "dispatch_child_ticket", dispatchChildTicketArgs{TicketID: child.Ticket.ID, Name: "child"}))
	require.NotEqual(t, parent.SessionID, childWorker.SessionID)
	require.NotEqual(t, parent.AssignmentID, childWorker.AssignmentID)
	require.Equal(t, parent.Namespace, childWorker.Namespace)
	session, ok := mass.session(childWorker.SessionID)
	require.True(t, ok)
	require.Equal(t, args.Execution.RuntimeRef, session.Spec.RuntimeRef)
	require.Equal(t, args.Execution.Model, session.Spec.Config.Model)
	require.Equal(t, args.Execution.Effort, session.Spec.Config.Effort)
	require.Contains(t, session.Spec.SystemPrompt, "repo:demo/src")
	require.Contains(t, session.Spec.SystemPrompt, "Native subagents create no platform Ticket")
	continued := decodeT06[WorkerView](t, worker.call(t, "dispatch_child_ticket", dispatchChildTicketArgs{TicketID: child.Ticket.ID, Name: "child"}))
	require.Equal(t, childWorker.AssignmentID, continued.AssignmentID)
	childMCP := startT06MCP(t, "mass", "worker", sessionWorkerCapability(t, server, childWorker.AssignmentID))
	require.Equal(t, "delegation_not_authorized", childMCP.callError(t, "list_workers", emptyArgs{}).Reason)
	require.Equal(t, "not_found", childMCP.callError(t, "get_ticket_context", ticketContextArgs{TicketID: ordinary.ID}).Code)
	grandArgs := childRequest("grandchild")
	grandArgs.WorkerName = "grandchild"
	grand := decodeT06[ChildTicketResult](t, childMCP.call(t, "create_child_ticket", grandArgs))
	require.Equal(t, child.Ticket.ID, grand.Ticket.ParentTicketID)
	require.NotEqual(t, root.ID, grand.Ticket.ParentTicketID)
	require.Equal(t, childWorker.AssignmentID, grand.Ticket.CreatedByAssignment)
	require.Equal(t, root.ID, grand.Ticket.RootTicketID)
	require.Equal(t, root.Namespace, grand.Worker.Namespace)
	grandMCP := startT06MCP(t, "mass", "worker", sessionWorkerCapability(t, server, grand.Worker.AssignmentID))
	require.Equal(t, "delegation_depth_limit", grandMCP.callError(t, "create_child_ticket", childRequest("too-deep")).Reason)
	require.Equal(t, "execution_not_authorized", router.callError(t, "create_worker", createWorkerArgs{TicketID: child.Ticket.ID, Name: "child-escape", Action: "replace", Model: "outside-model", RuntimeRef: "deepseek-claude", Effort: "high"}).Reason)
	sent := decodeT06[SendToWorkerResult](t, router.call(t, "send_to_worker", routerSendToWorkerArgs{RootTicketID: root.ID, Name: childWorker.Name, Message: "Execute the child's independent objective and report scoped evidence"}))
	waitT06(t, 15*time.Second, "independent child report", func() bool {
		p := triggerSnapshot(t, server)
		childState := p.Tickets[child.Ticket.ID]
		event := p.Inbox[sent.EventID]
		assignment := p.Assignments[childWorker.AssignmentID]
		return event.State == "delivered" && event.TargetAssignmentID == childWorker.AssignmentID && event.ExecutionID == assignment.ExecutionID && len(childState.Reports) >= 1 && childState.Reports[len(childState.Reports)-1].Status == "confirmed" && assignment.ExecutionState == "completed"
	})
	var sentToChild bool
	for _, prompt := range promptsForSession(observedPrompts(t, mass), childWorker.SessionID) {
		if strings.Contains(prompt.Text, "### Event "+sent.EventID+"\n") {
			sentToChild = true
		}
	}
	require.True(t, sentToChild, "explicit child execution must be observed in its actual Session prompt")
	for _, parentReport := range triggerSnapshot(t, server).Tickets[root.ID].Reports {
		require.Equal(t, parent.AssignmentID, parentReport.AssignmentID, "the root's own Trigger report must retain its parent Assignment")
		require.NotEqual(t, triggerSnapshot(t, server).Tickets[child.Ticket.ID].Reports[0].ID, parentReport.ID)
	}
	require.Empty(t, triggerSnapshot(t, server).Tickets[child.Ticket.ID].BusinessState, "a report cannot complete the child")
	rootContext := decodeT06[TicketContext](t, worker.call(t, "get_ticket_context", ticketContextArgs{}))
	require.Len(t, rootContext.Children, 2)
	childContext := decodeT06[TicketContext](t, childMCP.call(t, "get_ticket_context", ticketContextArgs{}))
	require.Equal(t, root.ID, childContext.Parent.ID)
	require.NotEmpty(t, childContext.Ticket.Reports)
	require.Equal(t, childWorker.AssignmentID, childContext.Ticket.Reports[0].AssignmentID)
	require.Empty(t, childContext.Threads)
	require.Len(t, triggerSnapshot(t, server).Triggers, 1, "delegation must not fabricate external Triggers")
	router.call(t, "stop_worker", stopWorkerArgs{RootTicketID: root.ID, Name: parent.Name})
	require.Equal(t, "capability_invalid", worker.callError(t, "create_child_ticket", childRequest("expired")).Reason)
	recovered := decodeT06[WorkerView](t, router.call(t, "create_worker", createWorkerArgs{TicketID: root.ID, Name: parent.Name}))
	require.Equal(t, parent.SessionID, recovered.SessionID)
	newParent := startT06MCP(t, "mass", "worker", sessionWorkerCapability(t, server, recovered.AssignmentID))
	require.Equal(t, child.Ticket.ID, decodeT06[ChildTicketResult](t, newParent.call(t, "create_child_ticket", args)).Ticket.ID)
	server.stop(t)
	server = startT06Server(t, server.config)
	require.Equal(t, child.Ticket.ID, decodeT06[ChildTicketResult](t, newParent.call(t, "create_child_ticket", args)).Ticket.ID)
	require.Equal(t, "capability_invalid", worker.callError(t, "create_child_ticket", args).Reason)
	router.call(t, "finish_ticket", finishTicketArgs{TicketID: grand.Ticket.ID, Resolution: "cancelled", Summary: "Explicitly dispose descendant before closing child"})
	finished := decodeT06[TicketState](t, router.call(t, "finish_ticket", finishTicketArgs{TicketID: child.Ticket.ID, Resolution: "cancelled", Summary: "Explicit child outcome"}))
	require.Equal(t, "cancelled", finished.BusinessState)
	for _, output := range triggerSnapshot(t, server).Outputs {
		if output.TicketID == child.Ticket.ID {
			require.Empty(t, output.Targets)
		}
	}
	require.Empty(t, triggerSnapshot(t, server).Tickets[root.ID].BusinessState)
	server.stop(t)
}

func TestDelegationConfigurationMismatchPublicLoop(t *testing.T) {
	mass, server, router := delegationServer(t)
	grant := delegationGrant()
	root := decodeT06[TicketState](t, router.call(t, "create_ticket", createTicketArgs{Title: "Root", Objective: "Scoped work", Acceptance: "Review", Authorization: &grant}))
	parent := decodeT06[WorkerView](t, router.call(t, "create_worker", createWorkerArgs{TicketID: root.ID, Name: "parent"}))
	worker := startT06MCP(t, "mass", "worker", sessionWorkerCapability(t, server, parent.AssignmentID))
	child := decodeT06[ChildTicketResult](t, worker.call(t, "create_child_ticket", childRequest("mismatch")))
	mass.mu.Lock()
	mass.createConfigMismatch = true
	mass.mu.Unlock()
	dispatch := dispatchChildTicketArgs{TicketID: child.Ticket.ID, Name: "child"}
	require.Equal(t, "worker_identity_unconfirmed", worker.callError(t, "dispatch_child_ticket", dispatch).Reason)
	_, _, before, _ := mass.counts()
	require.Equal(t, "worker_creation_unconfirmed", worker.callError(t, "dispatch_child_ticket", dispatch).Reason)
	_, _, after, _ := mass.counts()
	require.Equal(t, before, after)
	var readback TicketState
	require.NoError(t, server.client.Request(t.Context(), "GET", "/tickets/"+child.Ticket.ID+"?projectId=mass", nil, &readback))
	require.Empty(t, readback.AssignmentID)
	require.False(t, terminalBusinessState(readback.BusinessState))
	server.stop(t)
}

func TestDelegationBudgetPublicLoop(t *testing.T) {
	_, server, router := delegationServer(t)
	grant := delegationGrant()
	grant.MaxTickets = 2
	root := decodeT06[TicketState](t, router.call(t, "create_ticket", createTicketArgs{Title: "Root", Objective: "Scoped work", Acceptance: "Review", Authorization: &grant}))
	parent := decodeT06[WorkerView](t, router.call(t, "create_worker", createWorkerArgs{TicketID: root.ID, Name: "parent"}))
	worker := startT06MCP(t, "mass", "worker", sessionWorkerCapability(t, server, parent.AssignmentID))
	args := childRequest("only-child")
	args.Authorization.MaxTickets = 2
	child := decodeT06[ChildTicketResult](t, worker.call(t, "create_child_ticket", args))
	other := args
	other.RequestID = "over-budget"
	require.Equal(t, "delegation_resource_limit", worker.callError(t, "create_child_ticket", other).Reason)
	require.Len(t, triggerSnapshot(t, server).Tickets, 2)
	require.Len(t, triggerSnapshot(t, server).Delegations, 1)
	require.Len(t, lifecycleInputs(t, server, router, root.ID), 1)
	server.stop(t)
	server = startT06Server(t, server.config)
	require.Equal(t, child.Ticket.ID, decodeT06[ChildTicketResult](t, worker.call(t, "create_child_ticket", args)).Ticket.ID)
	require.Equal(t, "delegation_resource_limit", worker.callError(t, "create_child_ticket", other).Reason)
	server.stop(t)
}
