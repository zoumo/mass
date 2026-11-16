package sa

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// This story uses the same serve process, public CLI/API and stdio MCP as an
// adapter and Agents. Fault-specific cases live beside their public harnesses.
func TestTicketStoryPublicLoop(t *testing.T) {
	mass, server, router := delegationServer(t)
	config := outputTestConfig(t, server)
	routerID := triggerSnapshot(t, server).Router.SessionID
	mass.setState(routerID, "running")
	ingress := []string{"trigger", "accept", "--config", config, "--project", "mass", "--source", "test/account", "--source-event-id", "story", "--thread", "origin", "--summary", "Deliver and review delegated evidence"}
	start := time.Now()
	receipt := runT06JSON[TriggerReceipt](t, ingress...)
	require.Less(t, time.Since(start), 2*time.Second)
	require.Equal(t, receipt, runT06JSON[TriggerReceipt](t, ingress...))
	require.Empty(t, triggerSnapshot(t, server).Tickets)
	grant := delegationGrant()
	root := decodeT06[TicketState](t, router.call(t, "create_ticket", createTicketArgs{Title: "Public story", Objective: "Review independently delegated evidence", Acceptance: "All descendants resolved and overall evidence accepted", Authorization: &grant}))
	require.Empty(t, root.AssignmentID)
	router.call(t, "decide_trigger", decideTriggerArgs{TriggerID: receipt.TriggerID, Action: "associate", TicketID: root.ID})
	ack := decodeT06[OutputRecord](t, router.call(t, "reply_trigger", replyTriggerArgs{TriggerID: receipt.TriggerID, RequestID: "accepted", Kind: "accepted", TicketID: root.ID}))
	require.Len(t, readTestOutputs(t, server, "origin", nil).Outputs, 1)
	parent := decodeT06[WorkerView](t, router.call(t, "create_worker", createWorkerArgs{TicketID: root.ID, Name: "parent"}))
	waitLifecycleReport(t, server, root)
	oldPath := sessionWorkerCapability(t, server, parent.AssignmentID)
	oldMCP := startT06MCP(t, "mass", "worker", oldPath)
	router.call(t, "stop_worker", stopWorkerArgs{RootTicketID: root.ID, Name: parent.Name})
	require.Equal(t, "capability_invalid", oldMCP.callError(t, "get_ticket_context", ticketContextArgs{}).Reason)
	resumed := decodeT06[WorkerView](t, router.call(t, "create_worker", createWorkerArgs{TicketID: root.ID, Name: parent.Name}))
	require.Equal(t, parent.SessionID, resumed.SessionID)
	require.NotEqual(t, parent.AssignmentID, resumed.AssignmentID)
	require.Equal(t, oldPath, sessionWorkerCapability(t, server, resumed.AssignmentID))
	worker := startT06MCP(t, "mass", "worker", oldPath)
	childArgs := childRequest("story-child")
	childArgs.WorkerName = "child"
	child := decodeT06[ChildTicketResult](t, worker.call(t, "create_child_ticket", childArgs))
	// Close the parent before child conclusions; child work stays independent.
	mass.setState(resumed.SessionID, "offline")
	router.call(t, "stop_worker", stopWorkerArgs{RootTicketID: root.ID, Name: resumed.Name})
	childMCP := startT06MCP(t, "mass", "worker", sessionWorkerCapability(t, server, child.Worker.AssignmentID))
	grandArgs := childRequest("story-grandchild")
	grandArgs.WorkerName = "grandchild"
	grand := decodeT06[ChildTicketResult](t, childMCP.call(t, "create_child_ticket", grandArgs))
	require.Equal(t, root.ID, child.Ticket.ParentTicketID)
	require.Equal(t, child.Ticket.ID, grand.Ticket.ParentTicketID)
	router.call(t, "finish_ticket", finishTicketArgs{TicketID: child.Ticket.ID, Resolution: "blocked", Summary: "Waiting for independent evidence"})
	require.Equal(t, "unfinished_child_tickets", router.callError(t, "finish_ticket", finishTicketArgs{TicketID: root.ID, Resolution: "cancelled", Summary: "Cannot discard live descendants"}).Reason)
	router.call(t, "send_to_worker", routerSendToWorkerArgs{RootTicketID: root.ID, Name: grand.Worker.Name, Message: "Produce the independently reviewable evidence"})
	waitLifecycleReport(t, server, grand.Ticket)
	router.call(t, "finish_ticket", finishTicketArgs{TicketID: grand.Ticket.ID, Resolution: "completed", Summary: "Grandchild evidence reviewed"})
	waitLifecycleReport(t, server, child.Ticket)
	router.call(t, "finish_ticket", finishTicketArgs{TicketID: child.Ticket.ID, Resolution: "completed", Summary: "Child evidence and grandchild reviewed"})
	require.Empty(t, readTestOutputs(t, server, "unrelated", nil).Outputs)
	creates := len(triggerSnapshot(t, server).Tickets)
	server.stop(t)
	server = startT06Server(t, server.config)
	resumedAgain := decodeT06[WorkerView](t, router.call(t, "create_worker", createWorkerArgs{TicketID: root.ID, Name: parent.Name}))
	require.Equal(t, parent.SessionID, resumedAgain.SessionID)
	waitChildInputs(t, server, root.ID, 3, "delivered")
	waitLifecycleReport(t, server, root)
	context := decodeT06[TicketContext](t, router.call(t, "get_ticket_context", ticketContextArgs{TicketID: root.ID}))
	require.False(t, terminalBusinessState(context.Ticket.BusinessState))
	for _, input := range context.ChildInputs {
		require.Equal(t, resumedAgain.AssignmentID, input.TargetAssignmentID)
		require.Equal(t, parent.SessionID, input.TargetSession.SessionID)
	}
	completed := decodeT06[TicketState](t, router.call(t, "finish_ticket", finishTicketArgs{TicketID: root.ID, Resolution: "completed", Summary: "Overall evidence explicitly accepted"}))
	require.Equal(t, "completed", completed.BusinessState)
	require.Equal(t, completed, triggerSnapshot(t, server).Tickets[root.ID])
	page := readTestOutputs(t, server, "origin", nil)
	require.Len(t, page.Outputs, 2, "ordinary finals and source-free children stay internal")
	fact := page.Outputs[1]
	require.Equal(t, "ticket.completed", fact.Kind)
	router.call(t, "reply_trigger", replyTriggerArgs{TriggerID: receipt.TriggerID, RequestID: "summary", Kind: "summary", TicketID: root.ID, FactID: fact.ID, Text: "All delegated evidence and the overall result were reviewed."})
	all := readTestOutputs(t, server, "origin", nil)
	require.Len(t, all.Outputs, 3)
	for _, output := range all.Outputs {
		runT06CLI(t, "output", "ack", output.ID, "--config", config, "--project", "mass", "--consumer", "adapter", "--source", "test/account", "--thread", "origin", "--outcome", "failed", "--reference", "simulated connector outage")
	}
	_, _, prompts := mass.operationCounts()
	server.stop(t)
	server = startT06Server(t, server.config)
	require.Empty(t, readTestOutputs(t, server, "origin", nil).Outputs)
	replay := runT06JSON[OutputPage](t, "output", "read", "--config", config, "--project", "mass", "--consumer", "recovering-adapter", "--source", "test/account", "--thread", "origin")
	require.Len(t, replay.Outputs, 3)
	require.Equal(t, receipt, runT06JSON[TriggerReceipt](t, ingress...))
	echo := runT06JSON[TriggerReceipt](t, "trigger", "accept", "--config", config, "--project", "mass", "--source", "test/account", "--source-event-id", "echo", "--thread", "origin", "--summary", "Channel echo", "--echo-of-output", ack.ID)
	require.Equal(t, "ignored", runT06JSON[TriggerState](t, "trigger", "get", echo.TriggerID, "--config", config, "--project", "mass").Decision)
	readback := runT06JSON[TicketState](t, "ticket", "get", root.ID, "--config", config, "--project", "mass")
	require.Equal(t, "completed", readback.BusinessState)
	require.Len(t, triggerSnapshot(t, server).Tickets, creates)
	_, _, after := mass.operationCounts()
	require.Equal(t, prompts, after)
	// A separate cancellation explicitly disposes descendants before the root.
	cancelRoot, _, cancelMCP := lifecycleRoot(t, server, router)
	cancelChild := decodeT06[ChildTicketResult](t, cancelMCP.call(t, "create_child_ticket", childRequest("cancel"))).Ticket
	router.call(t, "finish_ticket", finishTicketArgs{TicketID: cancelChild.ID, Resolution: "cancelled", Summary: "Operator explicitly disposes child"})
	router.call(t, "finish_ticket", finishTicketArgs{TicketID: cancelRoot.ID, Resolution: "cancelled", Summary: "Operator explicitly cancels overall work"})
	require.Equal(t, "cancelled", runT06JSON[TicketState](t, "ticket", "get", cancelRoot.ID, "--config", config, "--project", "mass").BusinessState)
	server.stop(t)
}
