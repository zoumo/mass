package sa

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

func sessionContinuityServer(t *testing.T) (*fakeMASSServer, *t06Server, *t06MCP, TicketState) {
	t.Helper()
	mass := newFakeMASSServer(t, "")
	project := newTestProject("mass")
	project.CWD = t.TempDir()
	project.Worker.RuntimeRefs = []string{"alternate-worker-runtime"}
	config := newTestConfig(t, mass.endpoint(), filepath.Join(t.TempDir(), "state.json"), project)
	config.Management.Listen = freeLoopbackAddress(t)
	config.Management.TokenFile = filepath.Join(t.TempDir(), "management-token")
	require.NoError(t, os.WriteFile(config.Management.TokenFile, []byte("session-continuity-test"), 0600))
	server := startT06Server(t, config)
	var routerPath string
	waitT06(t, 15*time.Second, "router capability provisioning", func() bool {
		store, err := OpenStateStore(config.StateFile)
		if err != nil {
			return false
		}
		router := store.Snapshot().Projects["mass"].Router
		if router == nil {
			return false
		}
		routerPath = router.CapabilityFile
		return routerPath != ""
	})
	router := startT06MCP(t, "mass", "router", routerPath)
	ticket := decodeT06[TicketState](t, router.call(t, "create_ticket", createTicketArgs{Title: "Continuity", Objective: "Keep execution context", Acceptance: "Explicit acceptance"}))
	return mass, server, router, ticket
}

func inspectSessionTicket(t *testing.T, server *t06Server, ticket TicketState) TicketInspection {
	t.Helper()
	var result TicketInspection
	require.NoError(t, server.client.Request(t.Context(), "GET", "/tickets/"+ticket.ID+"/inspect?projectId=mass", nil, &result))
	return result
}

func waitSessionReports(t *testing.T, server *t06Server, ticket TicketState, count int) {
	t.Helper()
	waitT06(t, 15*time.Second, "Ticket reports and settled execution", func() bool {
		result := inspectSessionTicket(t, server, ticket)
		if len(result.Tickets) != 1 || len(result.Tickets[0].Reports) != count {
			return false
		}
		for _, a := range result.Assignments {
			if a.ID == result.Tickets[0].AssignmentID {
				return a.ExecutionState == "completed"
			}
		}
		return false
	})
}

func sessionWorkerCapability(t *testing.T, server *t06Server, assignmentID string) string {
	t.Helper()
	store, err := OpenStateStore(server.config.StateFile)
	require.NoError(t, err)
	for _, cap := range store.Snapshot().Capabilities {
		if cap.AssignmentID == assignmentID {
			return cap.File
		}
	}
	t.Fatal("worker capability not found")
	return ""
}

func TestSessionContinuityPublicLoop(t *testing.T) {
	mass, server, router, ticket := sessionContinuityServer(t)
	worker := decodeT06[WorkerView](t, router.call(t, "create_worker", createWorkerArgs{TicketID: ticket.ID, Name: "continuous"}))
	path := sessionWorkerCapability(t, server, worker.AssignmentID)
	oldMCP := startT06MCP(t, "mass", "worker", path)
	for round := 1; round <= 2; round++ {
		router.call(t, "send_to_worker", routerSendToWorkerArgs{RootTicketID: ticket.ID, Name: worker.Name, Message: fmt.Sprintf("Round %d with prior context", round)})
		waitSessionReports(t, server, ticket, round)
		continued := decodeT06[WorkerView](t, router.call(t, "create_worker", createWorkerArgs{TicketID: ticket.ID, Name: worker.Name}))
		require.Equal(t, worker.SessionID, continued.SessionID)
		require.Equal(t, worker.AssignmentID, continued.AssignmentID)
	}
	historyBefore := mass.historyEvents(worker.SessionID)
	require.GreaterOrEqual(t, len(historyBefore), 4)
	_, _, creates, _ := mass.counts()
	router.call(t, "stop_worker", stopWorkerArgs{RootTicketID: ticket.ID, Name: worker.Name})
	closed := inspectSessionTicket(t, server, ticket)
	require.Equal(t, worker.AssignmentID, closed.Tickets[0].AssignmentID)
	require.False(t, terminalBusinessState(closed.Tickets[0].BusinessState))
	require.Len(t, closed.Tickets[0].Reports, 2)
	require.Equal(t, "capability_invalid", oldMCP.callError(t, "list_workers", emptyArgs{}).Reason)
	recovered := decodeT06[WorkerView](t, router.call(t, "create_worker", createWorkerArgs{TicketID: ticket.ID, Name: worker.Name}))
	require.Equal(t, worker.SessionID, recovered.SessionID)
	require.NotEqual(t, worker.AssignmentID, recovered.AssignmentID)
	require.Equal(t, historyBefore, mass.historyEvents(worker.SessionID))
	require.Equal(t, path, sessionWorkerCapability(t, server, recovered.AssignmentID))
	newMCP := startT06MCP(t, "mass", "worker", path)
	newMCP.call(t, "list_workers", emptyArgs{})
	require.Equal(t, "capability_invalid", oldMCP.callError(t, "list_workers", emptyArgs{}).Reason)
	router.call(t, "send_to_worker", routerSendToWorkerArgs{RootTicketID: ticket.ID, Name: worker.Name, Message: "Continue after resource close"})
	waitSessionReports(t, server, ticket, 3)
	retried := decodeT06[WorkerView](t, router.call(t, "create_worker", createWorkerArgs{TicketID: ticket.ID, Name: worker.Name, Action: "retry"}))
	require.Equal(t, worker.SessionID, retried.SessionID)
	require.NotEqual(t, recovered.AssignmentID, retried.AssignmentID)
	require.Equal(t, "capability_invalid", newMCP.callError(t, "list_workers", emptyArgs{}).Reason)
	_, _, afterRecoveryCreates, _ := mass.counts()
	require.Equal(t, creates, afterRecoveryCreates)
	prompts := promptsForSession(observedPrompts(t, mass), worker.SessionID)
	require.Len(t, prompts, 3)
	server.stop(t)
	server = startT06Server(t, server.config)
	continued := decodeT06[WorkerView](t, router.call(t, "create_worker", createWorkerArgs{TicketID: ticket.ID, Name: worker.Name}))
	require.Equal(t, retried.AssignmentID, continued.AssignmentID)
	require.Equal(t, retried.SessionID, continued.SessionID)
	require.Equal(t, "explicit_replacement_required", router.callError(t, "create_worker", createWorkerArgs{TicketID: ticket.ID, Name: "replacement"}).Reason)
	require.Equal(t, "runtime_not_allowed", router.callError(t, "create_worker", createWorkerArgs{TicketID: ticket.ID, Name: "replacement", RuntimeRef: "unconfigured", Action: "replace"}).Reason)
	require.Equal(t, "explicit_replacement_required", router.callError(t, "create_worker", createWorkerArgs{TicketID: ticket.ID, Name: worker.Name, RuntimeRef: "alternate-worker-runtime"}).Reason)
	replacement := decodeT06[WorkerView](t, router.call(t, "create_worker", createWorkerArgs{TicketID: ticket.ID, Name: "replacement", RuntimeRef: "alternate-worker-runtime", Model: "explicit-model", Effort: "max", Action: "replace"}))
	require.NotEqual(t, worker.SessionID, replacement.SessionID)
	session, ok := mass.session(replacement.SessionID)
	require.True(t, ok)
	require.Equal(t, "explicit-model", session.Spec.Config.Model)
	require.Equal(t, "max", session.Spec.Config.Effort)
	require.Equal(t, "alternate-worker-runtime", session.Spec.RuntimeRef)
	recontinued := decodeT06[WorkerView](t, router.call(t, "create_worker", createWorkerArgs{TicketID: ticket.ID, Name: replacement.Name}))
	require.Equal(t, replacement.AssignmentID, recontinued.AssignmentID)
	require.Equal(t, replacement.RuntimeRef, recontinued.RuntimeRef)
	previous, ok := mass.session(worker.SessionID)
	require.True(t, ok)
	require.Equal(t, "offline", previous.Status.State)
	inspection := inspectSessionTicket(t, server, ticket)
	require.Len(t, inspection.Assignments, 4)
	require.Len(t, inspection.Tickets[0].Reports, 3)
	require.Equal(t, replacement.AssignmentID, inspection.Tickets[0].AssignmentID)
	for _, assignment := range inspection.Assignments {
		if assignment.ID == replacement.AssignmentID {
			require.Equal(t, replacement.RuntimeRef, assignment.RuntimeRef)
		} else {
			require.Equal(t, "superseded", assignment.Status)
		}
	}
	require.Equal(t, prompts, promptsForSession(observedPrompts(t, mass), worker.SessionID))
	server.stop(t)
}

func TestSessionUnknownInputIsNotReplayedPublicLoop(t *testing.T) {
	mass, server, router, ticket := sessionContinuityServer(t)
	worker := decodeT06[WorkerView](t, router.call(t, "create_worker", createWorkerArgs{TicketID: ticket.ID, Name: "unknown"}))
	mass.setDropPromptReply(true)
	router.call(t, "send_to_worker", routerSendToWorkerArgs{RootTicketID: ticket.ID, Name: worker.Name, Message: "Uncertain write must not repeat"})
	waitT06(t, 15*time.Second, "unknown delivery", func() bool {
		for _, event := range inspectSessionTicket(t, server, ticket).Events {
			if event.State == "unknown" {
				return true
			}
		}
		return false
	})
	_, _, promptCount := mass.operationCounts()
	require.Equal(t, 1, promptCount)
	server.stop(t)
	mass.setDropPromptReply(false)
	server = startT06Server(t, server.config)
	for _, action := range []string{"retry", "replace"} {
		name := worker.Name
		if action == "replace" {
			name = "unsafe-replacement"
		}
		require.Equal(t, "worker_execution_unconfirmed", router.callError(t, "create_worker", createWorkerArgs{TicketID: ticket.ID, Name: name, Action: action}).Reason)
	}
	inspection := inspectSessionTicket(t, server, ticket)
	require.Len(t, inspection.Assignments, 1)
	require.Equal(t, worker.AssignmentID, inspection.Tickets[0].AssignmentID)
	_, _, afterRestart := mass.operationCounts()
	require.Equal(t, promptCount, afterRestart)
	server.stop(t)
}

func TestSessionBusyReplacementIsFencedPublicLoop(t *testing.T) {
	mass, server, router, ticket := sessionContinuityServer(t)
	worker := decodeT06[WorkerView](t, router.call(t, "create_worker", createWorkerArgs{TicketID: ticket.ID, Name: "busy"}))
	gate := mass.blockPromptFinal(worker.SessionID)
	t.Cleanup(func() {
		select {
		case <-gate:
		default:
			close(gate)
		}
	})
	router.call(t, "send_to_worker", routerSendToWorkerArgs{RootTicketID: ticket.ID, Name: worker.Name, Message: "Hold execution until explicit test release"})
	waitT06(t, 15*time.Second, "accepted input", func() bool {
		for _, event := range inspectSessionTicket(t, server, ticket).Events {
			if event.State == "delivered" {
				return true
			}
		}
		return false
	})
	require.Equal(t, "worker_execution_in_progress", router.callError(t, "create_worker", createWorkerArgs{TicketID: ticket.ID, Name: "conflicting", Action: "replace"}).Reason)
	close(gate)
	waitSessionReports(t, server, ticket, 1)
	mass.setState(worker.SessionID, "running")
	require.Equal(t, "worker_not_quiescent", router.callError(t, "create_worker", createWorkerArgs{TicketID: ticket.ID, Name: "conflicting", Action: "replace"}).Reason)
	mass.setState(worker.SessionID, "unknown")
	require.Equal(t, "worker_not_quiescent", router.callError(t, "create_worker", createWorkerArgs{TicketID: ticket.ID, Name: worker.Name, Action: "retry"}).Reason)
	inspection := inspectSessionTicket(t, server, ticket)
	require.Len(t, inspection.Assignments, 1)
	server.stop(t)
}

func TestSessionAcceptedInputSurvivesRestartWithoutReplay(t *testing.T) {
	mass, server, router, ticket := sessionContinuityServer(t)
	worker := decodeT06[WorkerView](t, router.call(t, "create_worker", createWorkerArgs{TicketID: ticket.ID, Name: "accepted"}))
	gate := mass.blockPromptFinal(worker.SessionID)
	t.Cleanup(func() {
		select {
		case <-gate:
		default:
			close(gate)
		}
	})
	router.call(t, "send_to_worker", routerSendToWorkerArgs{RootTicketID: ticket.ID, Name: worker.Name, Message: "Accepted before process restart"})
	waitT06(t, 15*time.Second, "accepted delivery", func() bool {
		for _, event := range inspectSessionTicket(t, server, ticket).Events {
			if event.State == "delivered" {
				return true
			}
		}
		return false
	})
	before := promptsForSession(observedPrompts(t, mass), worker.SessionID)
	require.Len(t, before, 1)
	server.stop(t)
	server = startT06Server(t, server.config)
	require.Equal(t, "worker_execution_unconfirmed", router.callError(t, "create_worker", createWorkerArgs{TicketID: ticket.ID, Name: worker.Name, Action: "retry"}).Reason)
	inspection := inspectSessionTicket(t, server, ticket)
	require.Len(t, inspection.Assignments, 1)
	require.False(t, terminalBusinessState(inspection.Tickets[0].BusinessState))
	var delivered int
	for _, event := range inspection.Events {
		if event.State == "delivered" {
			delivered++
			require.Equal(t, worker.AssignmentID, event.TargetAssignmentID)
			require.Equal(t, worker.SessionID, event.TargetSession.SessionID)
		}
	}
	require.Equal(t, 1, delivered)
	require.Equal(t, before, promptsForSession(observedPrompts(t, mass), worker.SessionID))
	server.stop(t)
	close(gate)
}

func TestSessionInterruptedLifecycleReconcilesSameIdentity(t *testing.T) {
	for _, operation := range []string{"close", "resume"} {
		t.Run(operation, func(t *testing.T) {
			mass, server, router, ticket := sessionContinuityServer(t)
			worker := decodeT06[WorkerView](t, router.call(t, "create_worker", createWorkerArgs{TicketID: ticket.ID, Name: "interrupted", Model: "retained-model", Effort: "max"}))
			mass.setDropLifecycleReply(operation, true)
			require.Equal(t, "outcome_unknown", router.callError(t, "create_worker", createWorkerArgs{TicketID: ticket.ID, Name: worker.Name, Action: "retry"}).Code)
			inspection := inspectSessionTicket(t, server, ticket)
			require.NotEmpty(t, inspection.Reconciliation)
			_, _, creates, _ := mass.counts()
			server.stop(t)
			mass.setDropLifecycleReply(operation, false)
			server = startT06Server(t, server.config)
			recovered := decodeT06[WorkerView](t, router.call(t, "create_worker", createWorkerArgs{TicketID: ticket.ID, Name: worker.Name, Action: "retry"}))
			require.Equal(t, worker.SessionID, recovered.SessionID)
			require.NotEqual(t, worker.AssignmentID, recovered.AssignmentID)
			require.Equal(t, worker.Model, recovered.Model)
			require.Equal(t, worker.Effort, recovered.Effort)
			_, _, afterCreates, _ := mass.counts()
			require.Equal(t, creates, afterCreates)
			require.Empty(t, promptsForSession(observedPrompts(t, mass), worker.SessionID))
			require.Empty(t, inspectSessionTicket(t, server, ticket).Reconciliation)
			server.stop(t)
		})
	}
}

func TestSessionConfigurationMismatchDoesNotCreateAnotherExecutor(t *testing.T) {
	mass, server, router, ticket := sessionContinuityServer(t)
	mass.mu.Lock()
	mass.createConfigMismatch = true
	mass.mu.Unlock()
	require.Equal(t, "worker_identity_unconfirmed", router.callError(t, "create_worker", createWorkerArgs{TicketID: ticket.ID, Name: "mismatched", Model: "requested-model", Effort: "high"}).Reason)
	_, _, before, _ := mass.counts()
	require.Equal(t, "worker_creation_unconfirmed", router.callError(t, "create_worker", createWorkerArgs{TicketID: ticket.ID, Name: "another", Action: "replace"}).Reason)
	_, _, after, _ := mass.counts()
	require.Equal(t, before, after)
	inspection := inspectSessionTicket(t, server, ticket)
	require.Len(t, inspection.Assignments, 1)
	require.True(t, inspection.Assignments[0].ReconciliationRequired)
	require.Empty(t, inspection.Tickets[0].AssignmentID)
	server.stop(t)
}

func TestIndependentTicketPublicLoop(t *testing.T) {
	mass := newFakeMASSServer(t, "")
	project := newTestProject("mass")
	project.CWD = t.TempDir()
	config := newTestConfig(t, mass.endpoint(), filepath.Join(t.TempDir(), "v2", "state.json"), project)
	config.Management.Listen = freeLoopbackAddress(t)
	config.Management.TokenFile = filepath.Join(t.TempDir(), "management-token")
	require.NoError(t, os.WriteFile(config.Management.TokenFile, []byte("test-management"), 0600))
	server := startT06Server(t, config)
	raw, err := yaml.Marshal(config)
	require.NoError(t, err)
	configPath := filepath.Join(t.TempDir(), "cli.yaml")
	require.NoError(t, os.WriteFile(configPath, raw, 0600))
	ticket := runT06JSON[TicketState](t, "ticket", "create", "--config", configPath, "--objective", "Deliver independent work")
	require.Equal(t, ticket.ID, ticket.RootTicketID)
	require.Empty(t, ticket.AssignmentID)
	require.NotEmpty(t, ticket.Namespace)
	store, err := OpenStateStore(config.StateFile)
	require.NoError(t, err)
	require.NotEmpty(t, store.Snapshot().Projects["mass"].Tickets)
	inspection := runT06JSON[TicketInspection](t, "ticket", "inspect", ticket.ID, "--config", configPath, "--project", "mass")
	require.Equal(t, ticket.ID, inspection.Root.ID)
	require.Empty(t, store.Snapshot().Projects["mass"].Workers)
	require.Empty(t, ticket.ParentTicketID)
	router := startT06MCP(t, "mass", "router", store.Snapshot().Projects["mass"].Router.CapabilityFile)
	require.Equal(t, "ticket_report_missing", router.callError(t, "finish_ticket", finishTicketArgs{TicketID: ticket.ID, Resolution: "completed", Summary: "Premature"}).Reason)
	worker := decodeT06[WorkerView](t, router.call(t, "create_worker", createWorkerArgs{TicketID: ticket.ID, Name: "independent"}))
	require.Equal(t, ticket.Namespace, worker.Namespace)
	require.Empty(t, decodeT06[TicketState](t, router.call(t, "create_ticket", createTicketArgs{Title: "Separate", Objective: "Unrelated work", Acceptance: "Explicit review"})).AssignmentID)
	other := runT06JSON[TicketState](t, "ticket", "create", "--config", configPath, "--objective", "Separate scope")
	otherWorker := decodeT06[WorkerView](t, router.call(t, "create_worker", createWorkerArgs{TicketID: other.ID, Name: "other"}))
	require.NotEqual(t, worker.Namespace, otherWorker.Namespace)
	store, err = OpenStateStore(config.StateFile)
	require.NoError(t, err)
	var capabilityPath string
	for _, cap := range store.Snapshot().Capabilities {
		if cap.AssignmentID == worker.AssignmentID {
			capabilityPath = cap.File
		}
	}
	require.NotEmpty(t, capabilityPath)
	workerMCP := startT06MCP(t, "mass", "worker", capabilityPath)
	require.Equal(t, "not_found", workerMCP.callError(t, "get_worker", workerGetArgs{Name: otherWorker.Name}).Code)
	router.call(t, "send_to_worker", routerSendToWorkerArgs{RootTicketID: ticket.ID, Name: worker.Name, Message: "Complete the objective and report evidence"})
	waitT06(t, 15*time.Second, "confirmed report", func() bool {
		var current TicketState
		require.NoError(t, server.client.Request(t.Context(), "GET", "/tickets/"+ticket.ID+"?projectId=mass", nil, &current))
		require.False(t, terminalBusinessState(current.BusinessState))
		return len(current.Reports) > 0 && current.Reports[0].Status == "confirmed"
	})
	finished := decodeT06[TicketState](t, router.call(t, "finish_ticket", finishTicketArgs{TicketID: ticket.ID, Resolution: "completed", Summary: "Evidence accepted"}))
	require.Equal(t, "completed", finished.BusinessState)
	readback := runT06JSON[TicketState](t, "ticket", "get", ticket.ID, "--config", configPath)
	require.Equal(t, "completed", readback.BusinessState)
	require.NotEmpty(t, readback.Reports[0].Body)
	server.stop(t)
}

func TestLegacyStateRejectedWithoutRewrite(t *testing.T) {
	for _, version := range []int{1, 2, 3} {
		t.Run(fmt.Sprint(version), func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "state.json")
			raw := []byte(fmt.Sprintf(`{"version":%d,"projects":{}}`, version))
			require.NoError(t, os.WriteFile(path, raw, 0600))
			_, err := OpenStateStore(path)
			require.Error(t, err)
			actual, err := os.ReadFile(path)
			require.NoError(t, err)
			require.Equal(t, raw, actual)
		})
	}
}

func TestRetiredFeatureContractsHaveNoSideEffectsPublicLoop(t *testing.T) {
	_, server, router, ticket := sessionContinuityServer(t)
	before := triggerSnapshot(t, server)
	var result any
	require.Error(t, server.client.Request(t.Context(), "POST", "/features", map[string]string{"projectId": "mass", "objective": "legacy work"}, &result))
	require.Equal(t, "invalid_tool_arguments", router.callError(t, "create_ticket", map[string]string{"featureId": ticket.ID, "title": "Legacy", "objective": "Legacy", "acceptance": "Legacy"}).Reason)
	require.Equal(t, "tool_not_found", router.callError(t, "finish_feature", map[string]string{"featureId": ticket.ID, "summary": "Legacy closure"}).Reason)
	after := triggerSnapshot(t, server)
	require.Equal(t, before.Tickets, after.Tickets)
	require.Equal(t, before.Assignments, after.Assignments)
}
