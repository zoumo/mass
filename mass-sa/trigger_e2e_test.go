package sa

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

func triggerSnapshot(t *testing.T, s *t06Server) ProjectState {
	t.Helper()
	store, err := OpenStateStore(s.config.StateFile)
	require.NoError(t, err)
	return store.Snapshot().Projects["mass"]
}

func acceptTestTrigger(t *testing.T, s *t06Server, event, thread string) TriggerReceipt {
	t.Helper()
	var result TriggerReceipt
	require.NoError(t, s.client.Request(t.Context(), "POST", "/triggers", TriggerRequest{ProjectID: "mass", Source: "test/account", SourceEventID: event, ThreadID: thread, Kind: "message", Summary: "Follow the requested work", Content: json.RawMessage(`{"text":"normalized input"}`)}, &result))
	return result
}

func TestTriggerOwnershipPublicLoop(t *testing.T) {
	mass := newFakeMASSServer(t, "")
	multica := newFakeMultica(t)
	project := newTestProject("mass")
	project.CWD = t.TempDir()
	project.Multica = ProjectMultica{WorkspaceID: multica.workspace, ProjectID: multica.project}
	config := newTestConfig(t, mass.endpoint(), filepath.Join(t.TempDir(), "state.json"), project)
	config.Multica.Executable = fakeMulticaBinary
	config.Management.Listen = freeLoopbackAddress(t)
	config.Management.TokenFile = filepath.Join(t.TempDir(), "management-token")
	require.NoError(t, os.WriteFile(config.Management.TokenFile, []byte("trigger-test"), 0600))
	server := startT06Server(t, config)
	waitT06(t, 15*time.Second, "router ready", func() bool { p := triggerSnapshot(t, server); return p.Router != nil && p.Router.CapabilityFile != "" })
	router := startT06MCP(t, "mass", "router", triggerSnapshot(t, server).Router.CapabilityFile)
	routerID := triggerSnapshot(t, server).Router.SessionID
	mass.setState(routerID, "running")
	start := time.Now()
	first := acceptTestTrigger(t, server, "first", "discussion")
	require.Less(t, time.Since(start), 2*time.Second)
	require.Equal(t, first, acceptTestTrigger(t, server, "first", "discussion"))
	state := triggerSnapshot(t, server)
	require.Len(t, state.Triggers, 1)
	require.Empty(t, state.Tickets)
	require.Equal(t, "pending", state.Triggers[first.TriggerID].Decision)
	require.Empty(t, state.Inbox[first.TriggerID+"/"].TargetSession.SessionID)
	var conflict any
	err := server.client.Request(t.Context(), "POST", "/triggers", TriggerRequest{ProjectID: "mass", Source: "test/account", SourceEventID: "first", ThreadID: "other", Kind: "message", Summary: "changed", Content: json.RawMessage(`{}`)}, &conflict)
	require.Error(t, err)
	ignored := decodeT06[TriggerState](t, router.call(t, "decide_trigger", decideTriggerArgs{TriggerID: first.TriggerID, Action: "ignore"}))
	require.Equal(t, "ignored", ignored.Decision)
	ticket := decodeT06[TicketState](t, router.call(t, "create_ticket", createTicketArgs{Title: "Existing work", Objective: "Keep the scope", Acceptance: "Explicit acceptance"}))
	// Create is one durable decision, replay never creates another Issue/Ticket.
	create := acceptTestTrigger(t, server, "create", "new-work")
	args := decideTriggerArgs{TriggerID: create.TriggerID, Action: "create", Title: "From source", Objective: "Handle new request", Acceptance: "Review result"}
	created := decodeT06[TriggerState](t, router.call(t, "decide_trigger", args))
	require.Equal(t, "created", created.Decision)
	require.Len(t, created.TicketIDs, 1)
	require.Equal(t, created, decodeT06[TriggerState](t, router.call(t, "decide_trigger", args)))
	require.Len(t, triggerSnapshot(t, server).Tickets, 2)
	// Associate pending work with existing Ticket, then subsequent input is direct.
	unowned := acceptTestTrigger(t, server, "associate", "bound")
	router.call(t, "decide_trigger", decideTriggerArgs{TriggerID: unowned.TriggerID, Action: "associate", TicketID: ticket.ID})
	direct := acceptTestTrigger(t, server, "direct", "bound")
	require.Equal(t, "direct", triggerSnapshot(t, server).Inbox[direct.TriggerID+"/"+ticket.ID].RoutingPath)
	worker := decodeT06[WorkerView](t, router.call(t, "create_worker", createWorkerArgs{TicketID: ticket.ID, Name: "owner"}))
	waitSessionReports(t, server, ticket, 1)
	workerMCP := startT06MCP(t, "mass", "worker", sessionWorkerCapability(t, server, worker.AssignmentID))
	context := decodeT06[TicketContext](t, workerMCP.call(t, "get_ticket_context", ticketContextArgs{}))
	require.Equal(t, ticket.Objective, context.Ticket.Objective)
	require.Len(t, context.Threads, 1)
	require.Len(t, context.Sessions, 1)
	require.Len(t, context.Threads[0].TriggerIDs, 2)
	require.Equal(t, "worker_not_found", workerMCP.callError(t, "get_ticket_context", ticketContextArgs{TicketID: created.TicketIDs[0]}).Reason)
	require.NotEmpty(t, workerMCP.callError(t, "associate_thread", linkThreadArgs{Source: "test/account", ThreadID: "forged", TicketID: ticket.ID}).Code)
	// Many-to-many association is context, not a broadcast or authorization grant.
	router.call(t, "associate_thread", linkThreadArgs{Source: "test/account", ThreadID: "bound", TicketID: created.TicketIDs[0]})
	router.call(t, "associate_thread", linkThreadArgs{Source: "test/account", ThreadID: "second", TicketID: ticket.ID})
	ambiguous := acceptTestTrigger(t, server, "ambiguous", "bound")
	require.Equal(t, "pending", triggerSnapshot(t, server).Triggers[ambiguous.TriggerID].Decision)
	context = decodeT06[TicketContext](t, workerMCP.call(t, "get_ticket_context", ticketContextArgs{}))
	require.Len(t, context.Threads, 2)
	for _, thread := range context.Threads {
		require.NotContains(t, thread.TriggerIDs, ambiguous.TriggerID)
	}
	var missing any
	require.Error(t, server.client.Request(t.Context(), "GET", "/triggers/"+first.TriggerID+"?projectId=other", nil, &missing))
	// CLI reads the same durable fact, no Root/Assignment parameters.
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	raw, err := yaml.Marshal(server.config)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(configPath, raw, 0600))
	read := runT06CLI(t, "trigger", "get", first.TriggerID, "--config", configPath, "--project", "mass")
	require.Contains(t, string(read), first.TriggerID)
	cliReceipt := runT06JSON[TriggerReceipt](t, "trigger", "accept", "--config", configPath, "--project", "mass", "--source", "cli/account", "--source-event-id", "cli-1", "--thread", "cli-thread", "--summary", "CLI source input")
	mass.setState(routerID, "idle")
	waitT06(t, 15*time.Second, "unowned and ambiguous inputs delivered to Router", func() bool {
		p := triggerSnapshot(t, server)
		return p.Inbox[cliReceipt.TriggerID+"/"].State == "delivered" && p.Inbox[ambiguous.TriggerID+"/"].State == "delivered"
	})
	require.Equal(t, "pending", triggerSnapshot(t, server).Triggers[cliReceipt.TriggerID].Decision)
	require.Len(t, triggerSnapshot(t, server).Tickets, 2)
	server.stop(t)
	server = startT06Server(t, server.config)
	require.Equal(t, first, acceptTestTrigger(t, server, "first", "discussion"))
	require.Len(t, triggerSnapshot(t, server).Threads[threadKey("test/account", "bound")].TicketIDs, 2)
	server.stop(t)
}

func TestTriggerQueuedReplacementAndUnknownPublicLoop(t *testing.T) {
	mass, server, router, ticket := sessionContinuityServer(t)
	router.call(t, "associate_thread", linkThreadArgs{Source: "test/account", ThreadID: "durable", TicketID: ticket.ID})
	worker := decodeT06[WorkerView](t, router.call(t, "create_worker", createWorkerArgs{TicketID: ticket.ID, Name: "old"}))
	router.call(t, "stop_worker", stopWorkerArgs{RootTicketID: ticket.ID, Name: worker.Name})
	queued := acceptTestTrigger(t, server, "queued", "durable")
	eventID := queued.TriggerID + "/" + ticket.ID
	require.Empty(t, triggerSnapshot(t, server).Inbox[eventID].TargetAssignmentID)
	replacement := decodeT06[WorkerView](t, router.call(t, "create_worker", createWorkerArgs{TicketID: ticket.ID, Name: "new", Action: "replace"}))
	waitSessionReports(t, server, ticket, 1)
	event := triggerSnapshot(t, server).Inbox[eventID]
	require.Equal(t, replacement.AssignmentID, event.TargetAssignmentID)
	require.Equal(t, replacement.SessionID, event.TargetSession.SessionID)
	require.Empty(t, promptsForSession(observedPrompts(t, mass), worker.SessionID))
	// Accepted evidence remains with its executor after another explicit replacement.
	third := decodeT06[WorkerView](t, router.call(t, "create_worker", createWorkerArgs{TicketID: ticket.ID, Name: "third", Action: "replace"}))
	require.Equal(t, event, triggerSnapshot(t, server).Inbox[eventID])
	mass.setDropPromptReply(true)
	unknown := acceptTestTrigger(t, server, "unknown", "durable")
	unknownID := unknown.TriggerID + "/" + ticket.ID
	waitT06(t, 15*time.Second, "unknown Trigger delivery", func() bool { return triggerSnapshot(t, server).Inbox[unknownID].State == "unknown" })
	before := triggerSnapshot(t, server).Inbox[unknownID]
	require.Equal(t, third.AssignmentID, before.TargetAssignmentID)
	_, _, count := mass.operationCounts()
	server.stop(t)
	mass.setDropPromptReply(false)
	server = startT06Server(t, server.config)
	require.Equal(t, unknown, acceptTestTrigger(t, server, "unknown", "durable"))
	require.Equal(t, before, triggerSnapshot(t, server).Inbox[unknownID])
	require.Equal(t, "worker_execution_unconfirmed", router.callError(t, "create_worker", createWorkerArgs{TicketID: ticket.ID, Name: "fourth", Action: "replace"}).Reason)
	_, _, after := mass.operationCounts()
	require.Equal(t, count, after, fmt.Sprint("unknown input must not be replayed"))
	server.stop(t)
}
