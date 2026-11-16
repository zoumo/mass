package sa

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRouterBootstrapInjectsStableMCPCapability(t *testing.T) {
	harness := newWorkerHarness(t)
	project := harness.store.Snapshot().Projects["mass"]
	if project.Router == nil {
		t.Fatal("bootstrap did not persist a router")
	}
	session, exists := harness.mass.session(project.Router.SessionID)
	if !exists {
		t.Fatal("router Session was not created in fake massd")
	}
	if len(session.Spec.MCPServers) != 1 {
		t.Fatalf("router MCP servers = %+v", session.Spec.MCPServers)
	}
	mcp := session.Spec.MCPServers[0]
	if !filepath.IsAbs(mcp.Command) || mcp.Command != "/opt/mass-sa" ||
		!containsSequence(mcp.Args, []string{"mcp", "--project", "mass", "--role", "router", "--capability", project.Router.CapabilityFile}) {
		t.Fatalf("router MCP = %+v", mcp)
	}
	if project.Router.CapabilityFile == "" || project.Router.CapabilityDigest == "" {
		t.Fatalf("router capability reference = %+v", project.Router)
	}
	capability, err := readCapabilityFile(project.Router.CapabilityFile)
	if err != nil {
		t.Fatal(err)
	}
	if capability.Token != harness.routers["mass"].Token || capability.Role != "router" || capability.ProjectID != "mass" {
		t.Fatalf("router capability = %+v", capability)
	}
	info, err := os.Stat(project.Router.CapabilityFile)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("router capability mode = %o", info.Mode().Perm())
	}
	if project.Router.CapabilityDigest != capabilityDigest(capability.Token) {
		t.Fatal("router state does not store the capability digest")
	}
	stateBytes, err := os.ReadFile(harness.config.StateFile)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(stateBytes), capability.Token) {
		t.Fatal("durable state contains the router capability token")
	}
	for _, fragment := range []string{
		"create_ticket",
		"create_worker",
		"finish_ticket",
		"idle Session",
		"confirmed report",
	} {
		if !strings.Contains(session.Spec.SystemPrompt, fragment) {
			t.Fatalf("router prompt omitted %q", fragment)
		}
	}

	createsBefore := len(harness.mass.creates())
	if _, err := BootstrapRouters(t.Context(), harness.config, harness.store, WithRouterTools(harness.service)); err != nil {
		t.Fatal(err)
	}
	if createsAfter := len(harness.mass.creates()); createsAfter != createsBefore {
		t.Fatalf("second bootstrap created %d Sessions, want %d", createsAfter, createsBefore)
	}
	again, err := harness.service.EnsureRouterCapability("mass")
	if err != nil {
		t.Fatal(err)
	}
	if again.Token != capability.Token || again.State.File != project.Router.CapabilityFile {
		t.Fatal("router capability path or token changed across bootstrap")
	}

	harness.mass.mu.Lock()
	tampered := harness.mass.sessions[project.Router.SessionID]
	tampered.Spec.MCPServers = nil
	harness.mass.sessions[project.Router.SessionID] = tampered
	harness.mass.mu.Unlock()
	_, err = BootstrapRouters(t.Context(), harness.config, harness.store, WithRouterTools(harness.service))
	if err == nil {
		t.Fatal("bootstrap adopted a router with a drifted MCP configuration")
	}
	if reason, _ := errorReason(err); reason != "router_mcp_mismatch" {
		t.Fatalf("router MCP drift reason = %q: %v", reason, err)
	}
}

func TestRouterMCPDrivesTicketWorkerReportAcceptanceAndRoot(t *testing.T) {
	harness := newWorkerHarness(t)
	client, _, _ := newMCPTestClient(t, "mass", "router", harness.routers["mass"].State.File)
	initialize := client.request("initialize", map[string]any{
		"protocolVersion": "2024-11-05",
		"clientInfo":      map[string]string{"name": "test", "version": "1"},
		"capabilities":    map[string]any{},
	})
	if string(initialize["error"]) != "" {
		t.Fatalf("initialize failed: %s", initialize["error"])
	}
	client.notify("notifications/initialized")

	ticket := mcpToolResultOf[TicketState](t, client, "create_ticket", map[string]any{
		"title":      "Implement through MCP",
		"objective":  "Implement the MCP-driven ticket.",
		"acceptance": "The router can send, review, finish, and close the root.",
	})
	root := ticket
	if ticket.RootTicketID != ticket.ID || ticket.ParentTicketID != "" {
		t.Fatalf("root Ticket = %+v", ticket)
	}
	worker := mcpToolResultOf[WorkerView](t, client, "create_worker", map[string]any{
		"ticketId": ticket.ID,
		"name":     "mcp-worker",
		"model":    "deepseek-mcp-worker",
		"effort":   "max",
	})
	if worker.AssignmentID == "" || worker.SessionID == "" || worker.Model != "deepseek-mcp-worker" {
		t.Fatalf("created worker = %+v", worker)
	}
	sent := mcpToolResultOf[SendToWorkerResult](t, client, "send_to_worker", map[string]any{
		"rootTicketId": root.ID,
		"name":         worker.Name,
		"message":      "Implement the MCP-driven ticket and report the result.",
	})
	if sent.EventID == "" || sent.State != "queued" || sent.Delivered {
		t.Fatalf("sent = %+v", sent)
	}
	run, err := harness.service.ExecuteWorkerForProject(t.Context(), "mass", root.ID, worker.Name)
	if err != nil {
		t.Fatal(err)
	}
	if run.ReportID == "" || !run.Structured || run.SourceEventID == "" {
		t.Fatalf("run result = %+v", run)
	}
	if run.InputEventIDs[0] != sent.EventID {
		t.Fatalf("run input events = %v, want %s", run.InputEventIDs, sent.EventID)
	}

	finished := mcpToolResultOf[TicketState](t, client, "finish_ticket", map[string]any{
		"ticketId":   ticket.ID,
		"resolution": "completed",
		"summary":    "MCP report satisfies the acceptance criteria.",
	})
	if finished.BusinessState != "completed" {
		t.Fatalf("finished ticket = %+v", finished)
	}

}

func TestWorkerExecutionWritesReportToTicketAndRouterInbox(t *testing.T) {
	harness := newWorkerHarness(t)
	root, ticket := harness.rootAndChild(t, "mass", "execution")
	worker := harness.createWorker(t, "mass", root.ID, ticket.ID, "worker-execution", "", "")
	if _, body := harness.callTool(t, harness.routers["mass"].Token, "bind_route", bindRouteArgs{
		RootTicketID: root.ID, RouteKey: "source-execution", WorkerName: worker.Name, AssignmentID: worker.AssignmentID,
	}); len(body) == 0 {
		t.Fatal("bind_route returned no body")
	}
	sent := routerSend(t, harness, root.ID, worker.Name, "Implement and report the ticket.")

	run, err := harness.service.ExecuteWorkerForProject(t.Context(), "mass", root.ID, worker.Name)
	if err != nil {
		t.Fatal(err)
	}
	if run.AssignmentID != worker.AssignmentID || run.TicketID != ticket.ID ||
		run.ReportID == "" || run.RouterInboxEventID == "" {
		t.Fatalf("run result = %+v", run)
	}
	if !run.Structured || run.ReportFallback || run.ParseError != "" {
		t.Fatalf("structured report result = %+v", run)
	}
	const finalText = `{"status":"completed","summary":"worker completed the ticket"}`
	if run.ReportText != finalText {
		t.Fatalf("report text = %q, want %q", run.ReportText, finalText)
	}

	project := harness.store.Snapshot().Projects["mass"]
	input := project.Inbox[sent.EventID]
	if input.State != "delivered" || input.DeliveredAt.IsZero() || input.ExecutionID != run.ExecutionID {
		t.Fatalf("input event = %+v", input)
	}
	assignment := project.Assignments[worker.AssignmentID]
	if assignment.ExecutionState != "completed" || assignment.PromptAcceptedAt.IsZero() {
		t.Fatalf("assignment execution = %+v", assignment)
	}
	if assignment.Model != worker.Model || assignment.Effort != worker.Effort {
		t.Fatalf("assignment config = %+v", assignment)
	}
	ticketAfter := project.Tickets[ticket.ID]
	if ticketAfter.BusinessState != "" || len(ticketAfter.Reports) != 1 {
		t.Fatalf("ticket changed business state without acceptance: %+v", ticketAfter)
	}
	report := ticketAfter.Reports[0]
	if report.Status != "confirmed" || report.ID != run.ReportID ||
		!report.Structured || report.SourceEventID != run.SourceEventID {
		t.Fatalf("report = %+v", report)
	}
	routerReport := project.Inbox[run.RouterInboxEventID]
	if routerReport.Kind != "worker_report" || routerReport.TargetSession.SessionID != project.Router.SessionID ||
		routerReport.SourceEventID != run.SourceEventID {
		t.Fatalf("router report event = %+v", routerReport)
	}
	var payload struct {
		ReportID         string          `json:"reportId"`
		SourceEventID    string          `json:"sourceEventId"`
		SourceTurnID     string          `json:"sourceTurnId"`
		Structured       bool            `json:"structured"`
		StructuredResult json.RawMessage `json:"structuredResult"`
	}
	if err := json.Unmarshal(routerReport.Payload, &payload); err != nil {
		t.Fatal(err)
	}
	if payload.ReportID != run.ReportID || payload.SourceEventID != run.SourceEventID ||
		payload.SourceTurnID != "turn-1" || !payload.Structured || len(payload.StructuredResult) == 0 {
		t.Fatalf("router payload = %+v", payload)
	}
	if _, ok := project.RouteBindings[routeBindingKey(root.ID, "source-execution")]; !ok {
		t.Fatal("execution implicitly invalidated a direct route before acceptance")
	}
	_, closeCalls, prompts := harness.mass.operationCounts()
	if closeCalls != 0 || prompts != 1 {
		t.Fatalf("execution close=%d prompt=%d", closeCalls, prompts)
	}
	subscribe, history := harness.mass.eventCounts()
	if subscribe != 1 || history < 1 {
		t.Fatalf("subscribe=%d history=%d", subscribe, history)
	}
	order := harness.mass.operationOrder()
	subscribeAt := lastOperationIndex(order, "session.subscribe")
	historyAt := lastOperationIndex(order, "session.history")
	promptAt := lastOperationIndex(order, "session.prompt")
	if subscribeAt < 0 || historyAt < subscribeAt || promptAt < historyAt {
		t.Fatalf("operations = %v", order)
	}
	if report.Body != run.ReportText || report.Session != workerSession(worker) || report.AssignmentID != worker.AssignmentID {
		t.Fatalf("persisted report = %+v", report)
	}
}

func TestExecutionFinalSelectionRecoveryAndFallback(t *testing.T) {
	t.Run("final answer beats partial", func(t *testing.T) {
		harness := newWorkerHarness(t)
		root, ticket := harness.rootAndChild(t, "mass", "final-selection")
		worker := harness.createWorker(t, "mass", root.ID, ticket.ID, "worker-final", "", "")
		harness.mass.setPromptTurn(fakePromptTurn{
			FinalText:        `{"status":"completed","summary":"final"}`,
			Phase:            "final_answer",
			EmitFinal:        true,
			EmitHistoryFinal: true,
			EmitPartial:      true,
			PartialText:      "still working",
		})
		routerSend(t, harness, root.ID, worker.Name, "Finish the work.")
		run, err := harness.service.ExecuteWorkerForProject(t.Context(), "mass", root.ID, worker.Name)
		if err != nil {
			t.Fatal(err)
		}
		if run.ReportText != `{"status":"completed","summary":"final"}` || !run.Structured {
			t.Fatalf("run = %+v", run)
		}
	})

	t.Run("history recovery", func(t *testing.T) {
		harness := newWorkerHarness(t)
		root, ticket := harness.rootAndChild(t, "mass", "history-recovery")
		worker := harness.createWorker(t, "mass", root.ID, ticket.ID, "worker-history", "", "")
		harness.mass.setPromptTurn(fakePromptTurn{
			FinalText:        `{"status":"completed","summary":"recovered"}`,
			Phase:            "final_answer",
			EmitFinal:        false,
			EmitHistoryFinal: true,
		})
		routerSend(t, harness, root.ID, worker.Name, "Recover the committed final message.")
		run, err := harness.service.ExecuteWorkerForProject(t.Context(), "mass", root.ID, worker.Name)
		if err != nil {
			t.Fatal(err)
		}
		if !run.RecoveredFromHistory || run.SourceEventID == "" || !run.Structured {
			t.Fatalf("run = %+v", run)
		}
	})

	t.Run("json fence", func(t *testing.T) {
		harness := newWorkerHarness(t)
		root, ticket := harness.rootAndChild(t, "mass", "json-fence")
		worker := harness.createWorker(t, "mass", root.ID, ticket.ID, "worker-fence", "", "")
		harness.mass.setPromptTurn(fakePromptTurn{
			FinalText: "```json\n{\"status\":\"completed\",\"summary\":\"fenced\"}\n```",
			Phase:     "final_answer",
			EmitFinal: true,
		})
		routerSend(t, harness, root.ID, worker.Name, "Return one JSON fence.")
		run, err := harness.service.ExecuteWorkerForProject(t.Context(), "mass", root.ID, worker.Name)
		if err != nil {
			t.Fatal(err)
		}
		if !run.Structured || run.ReportFallback || run.ParseError != "" {
			t.Fatalf("run = %+v", run)
		}
	})

	t.Run("unstructured is preserved as fallback", func(t *testing.T) {
		harness := newWorkerHarness(t)
		root, ticket := harness.rootAndChild(t, "mass", "unstructured")
		worker := harness.createWorker(t, "mass", root.ID, ticket.ID, "worker-text", "", "")
		harness.mass.setPromptTurn(fakePromptTurn{
			FinalText:        "Changed the files.\nAll tests passed.",
			Phase:            "",
			EmitFinal:        true,
			EmitHistoryFinal: true,
		})
		routerSend(t, harness, root.ID, worker.Name, "Report in plain text.")
		run, err := harness.service.ExecuteWorkerForProject(t.Context(), "mass", root.ID, worker.Name)
		if err != nil {
			t.Fatal(err)
		}
		if run.Structured || !run.ReportFallback || run.ParseError == "" {
			t.Fatalf("run = %+v", run)
		}
		report := harness.store.Snapshot().Projects["mass"].Tickets[ticket.ID].Reports[0]
		if report.Structured || report.ParseError == "" {
			t.Fatalf("report = %+v", report)
		}
		project := harness.store.Snapshot().Projects["mass"]
		routerReport := project.Inbox[run.RouterInboxEventID]
		var payload struct {
			Report     string `json:"report"`
			Structured bool   `json:"structured"`
			ParseError string `json:"parseError"`
		}
		if err := json.Unmarshal(routerReport.Payload, &payload); err != nil {
			t.Fatal(err)
		}
		if run.RouterInboxEventID == "" || routerReport.Kind != "worker_report" {
			t.Fatal("fallback report was not delivered to the router inbox")
		}
		if payload.Report != run.ReportText || payload.Structured || payload.ParseError == "" {
			t.Fatalf("router fallback payload = %+v", payload)
		}
		reports := harness.store.Snapshot().Projects["mass"].Tickets[ticket.ID].Reports
		if len(reports) != 1 || reports[0].Body != run.ReportText || reports[0].Structured {
			t.Fatalf("reports = %+v", reports)
		}
	})
}

func TestIdleOrChunkOnlyExecutionDoesNotReportSuccess(t *testing.T) {
	cases := map[string]fakePromptTurn{
		"idle only": {
			FinalText:        `{"status":"completed"}`,
			EmitFinal:        false,
			EmitHistoryFinal: false,
		},
		"chunks only": {
			FinalText:        `{"status":"completed"}`,
			EmitFinal:        false,
			EmitHistoryFinal: false,
			EmitChunks:       true,
		},
		"partial only": {
			FinalText:   `{"status":"completed"}`,
			EmitPartial: true,
			PartialText: "This is not a final answer.",
		},
	}
	for name, turn := range cases {
		t.Run(name, func(t *testing.T) {
			harness := newWorkerHarness(t)
			root, ticket := harness.rootAndChild(t, "mass", "no-final")
			worker := harness.createWorker(t, "mass", root.ID, ticket.ID, "worker-no-final", "", "")
			harness.mass.setPromptTurn(turn)
			sent := routerSend(t, harness, root.ID, worker.Name, "Do not report on idle alone.")
			_, err := harness.service.ExecuteWorkerForProject(t.Context(), "mass", root.ID, worker.Name)
			if err == nil {
				t.Fatal("idle or chunk-only execution reported success")
			}
			if reason, _ := errorReason(err); reason != "worker_final_missing" {
				t.Fatalf("reason = %q: %v", reason, err)
			}
			project := harness.store.Snapshot().Projects["mass"]
			if len(project.Tickets[ticket.ID].Reports) != 0 {
				t.Fatal("idle or chunk-only execution persisted a report")
			}
			if project.Inbox[sent.EventID].State != "delivered" {
				t.Fatalf("input event = %+v", project.Inbox[sent.EventID])
			}
			if got := project.Assignments[worker.AssignmentID].ExecutionState; got != "failed" {
				t.Fatalf("execution state = %q", got)
			}
			for _, event := range project.Inbox {
				if event.Kind == "worker_report" {
					t.Fatal("idle or chunk-only execution enqueued a router report")
				}
			}
		})
	}
}

func TestUnknownPromptOutcomeIsPersistedWithoutResend(t *testing.T) {
	harness := newWorkerHarness(t)
	root, ticket := harness.rootAndChild(t, "mass", "unknown-prompt")
	worker := harness.createWorker(t, "mass", root.ID, ticket.ID, "worker-unknown", "", "")
	harness.mass.setDropPromptReply(true)
	sent := routerSend(t, harness, root.ID, worker.Name, "This prompt reply is lost.")
	_, err := harness.service.ExecuteWorkerForProject(t.Context(), "mass", root.ID, worker.Name)
	if err == nil || !IsErrorCode(err, "outcome_unknown") {
		t.Fatalf("error = %v, want outcome_unknown", err)
	}
	project := harness.store.Snapshot().Projects["mass"]
	if project.Inbox[sent.EventID].State != "unknown" {
		t.Fatalf("input event = %+v", project.Inbox[sent.EventID])
	}
	if got := project.Assignments[worker.AssignmentID].ExecutionState; got != "unknown" {
		t.Fatalf("execution state = %q", got)
	}
	_, _, prompts := harness.mass.operationCounts()
	if prompts != 1 {
		t.Fatalf("prompts = %d, want 1", prompts)
	}
	if _, err := harness.service.ExecuteWorkerForProject(t.Context(), "mass", root.ID, worker.Name); err == nil {
		t.Fatal("unknown prompt was automatically retried")
	} else if reason, _ := errorReason(err); reason != "worker_work_missing" {
		t.Fatalf("second execution reason = %q", reason)
	}
	_, _, prompts = harness.mass.operationCounts()
	if prompts != 1 {
		t.Fatalf("prompts after retry = %d, want 1", prompts)
	}
	if reports := project.Tickets[ticket.ID].Reports; len(reports) != 0 {
		t.Fatalf("unknown prompt wrote reports: %+v", reports)
	}
}

func TestNewAttemptSupersedesWithoutRedirectingQueuedWork(t *testing.T) {
	harness := newWorkerHarness(t)
	root, ticket := harness.rootAndChild(t, "mass", "new-attempt")
	oldWorker := harness.createWorker(t, "mass", root.ID, ticket.ID, "worker-old", "", "")
	sent := routerSend(t, harness, root.ID, oldWorker.Name, "Queued for the old Assignment.")

	status, body := harness.callTool(t, harness.routers["mass"].Token, "create_worker", createWorkerArgs{TicketID: ticket.ID, Name: "worker-new", Model: "deepseek-retry-worker", Effort: "max", Action: "replace"})
	if status != http.StatusOK {
		t.Fatalf("replace worker: %d %s", status, body)
	}
	newWorker := decodeToolResult[WorkerView](t, body)
	project := harness.store.Snapshot().Projects["mass"]
	if project.Tickets[ticket.ID].AssignmentID != newWorker.AssignmentID {
		t.Fatalf("ticket assignment = %q, want %q", project.Tickets[ticket.ID].AssignmentID, newWorker.AssignmentID)
	}
	if got := project.Assignments[oldWorker.AssignmentID].Status; got != "superseded" {
		t.Fatalf("old assignment status = %q", got)
	}
	oldAssignment := project.Assignments[oldWorker.AssignmentID]
	if oldAssignment.Model != oldWorker.Model || oldAssignment.Effort != oldWorker.Effort {
		t.Fatalf("new override changed old Assignment: %+v", oldAssignment)
	}
	newAssignment := project.Assignments[newWorker.AssignmentID]
	if newAssignment.Model != "deepseek-retry-worker" || newAssignment.Effort != "max" {
		t.Fatalf("new Assignment override = %+v", newAssignment)
	}
	if project.Inbox[sent.EventID].TargetAssignmentID != oldWorker.AssignmentID ||
		project.Inbox[sent.EventID].State != "queued" {
		t.Fatalf("queued event was redirected: %+v", project.Inbox[sent.EventID])
	}
	if _, err := harness.service.ExecuteWorkerForProject(t.Context(), "mass", root.ID, oldWorker.Name); err == nil {
		t.Fatal("superseded Assignment executed queued work")
	} else if reason, _ := errorReason(err); reason != "assignment_inactive" {
		t.Fatalf("old execution reason = %q", reason)
	}
	if _, err := harness.service.ExecuteWorkerForProject(t.Context(), "mass", root.ID, newWorker.Name); err == nil {
		t.Fatal("new Assignment consumed the old target's queued event")
	} else if reason, _ := errorReason(err); reason != "worker_work_missing" {
		t.Fatalf("new execution reason = %q", reason)
	}
	status, body = harness.callTool(t, harness.routers["mass"].Token, "send_to_worker", routerSendToWorkerArgs{
		RootTicketID: root.ID, Name: oldWorker.Name, Message: "Do not route to a superseded worker.",
	})
	assertToolError(t, status, body, http.StatusConflict, "worker_unavailable")
	status, body = harness.callTool(t, harness.routers["mass"].Token, "bind_route", bindRouteArgs{
		RootTicketID: root.ID, RouteKey: "stale-route", WorkerName: oldWorker.Name, AssignmentID: oldWorker.AssignmentID,
	})
	assertToolError(t, status, body, http.StatusConflict, "assignment_inactive")
	capability, _ := workerCapability(t, harness, oldWorker.AssignmentID)
	status, body = harness.callTool(t, capability.Token, "list_workers", emptyArgs{})
	assertToolError(t, status, body, http.StatusUnauthorized, "capability_invalid")
}

func TestSameAssignmentContinuationAndCurrentReportGatesFinish(t *testing.T) {
	harness := newWorkerHarness(t)
	root, ticket := harness.rootAndChild(t, "mass", "continuation")
	worker := harness.createWorker(t, "mass", root.ID, ticket.ID, "worker-continue", "", "")
	if status, body := harness.callTool(t, harness.routers["mass"].Token, "bind_route", bindRouteArgs{
		RootTicketID: root.ID, RouteKey: "continuation", WorkerName: worker.Name, AssignmentID: worker.AssignmentID,
	}); status != http.StatusOK {
		t.Fatalf("bind_route failed: %d %s", status, body)
	}

	for _, message := range []string{"First pass.", "Second pass on the same Assignment."} {
		routerSend(t, harness, root.ID, worker.Name, message)
		if _, err := harness.service.ExecuteWorkerForProject(t.Context(), "mass", root.ID, worker.Name); err != nil {
			t.Fatal(err)
		}
	}
	project := harness.store.Snapshot().Projects["mass"]
	if project.Tickets[ticket.ID].AssignmentID != worker.AssignmentID ||
		len(project.Tickets[ticket.ID].Reports) != 2 {
		t.Fatalf("ticket continuation = %+v", project.Tickets[ticket.ID])
	}
	if _, ok := project.RouteBindings[routeBindingKey(root.ID, "continuation")]; !ok {
		t.Fatal("report invalidated the direct route before acceptance")
	}

	status, body := harness.callTool(t, harness.routers["mass"].Token, "create_worker", createWorkerArgs{TicketID: ticket.ID, Name: "worker-continue-new", Action: "replace"})
	if status != http.StatusOK {
		t.Fatalf("replace worker: %d %s", status, body)
	}
	newWorker := decodeToolResult[WorkerView](t, body)
	project = harness.store.Snapshot().Projects["mass"]
	if len(project.Tickets[ticket.ID].Reports) != 2 {
		t.Fatal("replacement Assignment removed old reports")
	}
	if project.Assignments[worker.AssignmentID].Status != "superseded" {
		t.Fatalf("old Assignment = %+v", project.Assignments[worker.AssignmentID])
	}
	if len(project.RouteBindings) != 0 {
		t.Fatalf("old route bindings remain: %+v", project.RouteBindings)
	}
	revisionBefore := project.Tickets[ticket.ID].Revision
	status, body = harness.callTool(t, harness.routers["mass"].Token, "finish_ticket", finishTicketArgs{
		TicketID: ticket.ID, Resolution: "completed", Summary: "Old report is not current.",
	})
	assertToolError(t, status, body, http.StatusConflict, "ticket_report_missing")
	if got := harness.store.Snapshot().Projects["mass"].Tickets[ticket.ID].Revision; got != revisionBefore {
		t.Fatal("rejected finish changed the business revision")
	}

	routerSend(t, harness, root.ID, newWorker.Name, "Complete the current Assignment.")
	if _, err := harness.service.ExecuteWorkerForProject(t.Context(), "mass", root.ID, newWorker.Name); err != nil {
		t.Fatal(err)
	}
	project = harness.store.Snapshot().Projects["mass"]
	if len(project.Tickets[ticket.ID].Reports) != 3 {
		t.Fatalf("reports after current Assignment = %d", len(project.Tickets[ticket.ID].Reports))
	}
	status, body = harness.callTool(t, harness.routers["mass"].Token, "finish_ticket", finishTicketArgs{
		TicketID: ticket.ID, Resolution: "completed", Summary: "Current Assignment report accepted.",
	})
	if status != http.StatusOK {
		t.Fatalf("finish_ticket status %d: %s", status, body)
	}
	finished := decodeToolResult[TicketState](t, body)
	if finished.BusinessState != "completed" {
		t.Fatalf("finished ticket = %+v", finished)
	}
	revision := finished.Revision
	status, body = harness.callTool(t, harness.routers["mass"].Token, "finish_ticket", finishTicketArgs{
		TicketID: ticket.ID, Resolution: "completed", Summary: "Repeated finish.",
	})
	assertToolError(t, status, body, http.StatusConflict, "ticket_already_resolved")
	if got := harness.store.Snapshot().Projects["mass"].Tickets[ticket.ID].Revision; got != revision {
		t.Fatal("repeated finish changed the business revision")
	}
	_, closeCalls, _ := harness.mass.operationCounts()
	if closeCalls != 1 {
		t.Fatalf("close calls = %d, want only the explicit replacement close", closeCalls)
	}
	project = harness.store.Snapshot().Projects["mass"]
	if len(project.Tickets[ticket.ID].Reports) != 3 || len(project.Outputs) != 1 {
		t.Fatal("expected three reports and one business fact")
	}
}

func TestFinishTicketRejectsPendingAndUnknownReportsAndAcceptsAfterStop(t *testing.T) {
	harness := newWorkerHarness(t)
	root, ticket := harness.rootAndChild(t, "mass", "report-gate")
	worker := harness.createWorker(t, "mass", root.ID, ticket.ID, "worker-gate", "", "")
	if _, err := harness.bridge.AddReport(t.Context(), AddReportRequest{
		ProjectID:     "mass",
		TicketID:      ticket.ID,
		AssignmentID:  worker.AssignmentID,
		Session:       workerSession(worker),
		SourceEventID: "event-gate",
		Body:          "A confirmed local report.",
	}); err != nil {
		t.Fatal(err)
	}
	for _, statusValue := range []string{"", "writing", "unknown"} {
		if err := harness.store.updateProject("mass", func(project *ProjectState) error {
			stored := project.Tickets[ticket.ID]
			for index := range stored.Reports {
				stored.Reports[index].Status = statusValue
			}
			project.Tickets[ticket.ID] = stored
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		callStatus, callBody := harness.callTool(t, harness.routers["mass"].Token, "finish_ticket", finishTicketArgs{
			TicketID: ticket.ID, Resolution: "completed", Summary: "Incomplete report must not finish.",
		})
		assertToolError(t, callStatus, callBody, http.StatusConflict, "ticket_report_missing")
	}
	if got := len(harness.store.Snapshot().Projects["mass"].Outputs); got != 0 {
		t.Fatalf("report gate emitted %d business facts", got)
	}

	if status, body := harness.callTool(t, harness.routers["mass"].Token, "bind_route", bindRouteArgs{
		RootTicketID: root.ID, RouteKey: "stop", WorkerName: worker.Name, AssignmentID: worker.AssignmentID,
	}); status != http.StatusOK {
		t.Fatalf("bind_route failed: %d %s", status, body)
	}
	if err := harness.store.updateProject("mass", func(project *ProjectState) error {
		stored := project.Tickets[ticket.ID]
		for index := range stored.Reports {
			stored.Reports[index].Status = "confirmed"
		}
		project.Tickets[ticket.ID] = stored
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if status, body := harness.callTool(t, harness.routers["mass"].Token, "stop_worker", stopWorkerArgs{
		RootTicketID: root.ID, Name: worker.Name,
	}); status != http.StatusOK {
		t.Fatalf("stop_worker failed: %d %s", status, body)
	}
	project := harness.store.Snapshot().Projects["mass"]
	if project.Tickets[ticket.ID].BusinessState != "" || len(project.Tickets[ticket.ID].Reports) != 1 {
		t.Fatal("stopping the worker changed business completion")
	}
	status, body := harness.callTool(t, harness.routers["mass"].Token, "finish_ticket", finishTicketArgs{
		TicketID: ticket.ID, Resolution: "completed", Summary: "Accept the confirmed report after stop.",
	})
	if status != http.StatusOK {
		t.Fatalf("finish_ticket status %d: %s", status, body)
	}
	if got := harness.store.Snapshot().Projects["mass"].Tickets[ticket.ID].BusinessState; got != "completed" {
		t.Fatal("accepted report after stop did not complete Ticket")
	}
}

func TestParentTicketRequiresEveryChildResolution(t *testing.T) {
	h := newWorkerHarness(t)
	root, first := h.rootAndChild(t, "mass", "closure")
	second := h.createTicket(t, "mass", root.ID, "second")
	_, err := h.bridge.FinishTicket(t.Context(), FinishTicketRequest{ProjectID: "mass", TicketID: root.ID, Resolution: ResolutionFailed, Summary: "Premature closure"})
	if reason, _ := errorReason(err); reason != "unfinished_child_tickets" {
		t.Fatalf("parent closure = %v", err)
	}
	for _, child := range []TicketState{first, second} {
		if _, err := h.bridge.FinishTicket(t.Context(), FinishTicketRequest{ProjectID: "mass", TicketID: child.ID, Resolution: ResolutionCancelled, Summary: "Explicit child disposition"}); err != nil {
			t.Fatal(err)
		}
	}
	parent, err := h.bridge.FinishTicket(t.Context(), FinishTicketRequest{ProjectID: "mass", TicketID: root.ID, Resolution: ResolutionFailed, Summary: "Explicit parent conclusion"})
	if err != nil || parent.BusinessState != "failed" {
		t.Fatalf("parent = %+v, %v", parent, err)
	}
}

func TestReportSourceEventIsIdempotentAcrossRestart(t *testing.T) {
	fixture := newTicketFixture(t)
	bridge := fixture.bridge()
	root := mustCreateRootTicket(t, bridge)
	ticket := mustCreateTicket(t, bridge, root.ID)
	request := AddReportRequest{
		ProjectID:     "mass",
		TicketID:      ticket.ID,
		AssignmentID:  "a-idempotent",
		Session:       SessionRef{SessionID: "native-idempotent", Namespace: "mass-f-idempotent", Name: "worker"},
		SourceEventID: "event-idempotent",
		Structured:    true,
		Body:          `{"status":"completed"}`,
	}
	if _, err := bridge.RecordAssignment(t.Context(), RecordAssignmentRequest{ProjectID: "mass", TicketID: ticket.ID, AssignmentID: request.AssignmentID}); err != nil {
		t.Fatal(err)
	}
	first, err := bridge.AddReport(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	second, err := bridge.AddReport(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	if len(second.Reports) != 1 || second.Reports[0].ID != first.Reports[0].ID {
		t.Fatalf("second report = %+v", second.Reports)
	}
	store, err := OpenStateStore(fixture.config().StateFile)
	if err != nil {
		t.Fatal(err)
	}
	restarted, err := NewTicketBridge(fixture.config(), store)
	if err != nil {
		t.Fatal(err)
	}
	third, err := restarted.AddReport(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	if len(third.Reports) != 1 || third.Reports[0].ID != first.Reports[0].ID {
		t.Fatalf("restart report = %+v", third.Reports)
	}
}

func routerSend(t *testing.T, harness *workerHarness, rootTicketID, workerName, message string) SendToWorkerResult {
	t.Helper()
	status, body := harness.callTool(t, harness.routers["mass"].Token, "send_to_worker", routerSendToWorkerArgs{
		RootTicketID: rootTicketID,
		Name:         workerName,
		Message:      message,
	})
	if status != http.StatusOK {
		t.Fatalf("send_to_worker status %d: %s", status, body)
	}
	return decodeToolResult[SendToWorkerResult](t, body)
}

func workerSession(worker WorkerView) SessionRef {
	return SessionRef{SessionID: worker.SessionID, Namespace: worker.Namespace, Name: worker.Name}
}

func mcpToolResultOf[T any](t *testing.T, client *mcpTestClient, tool string, arguments any) T {
	t.Helper()
	response := client.request("tools/call", map[string]any{
		"name":      tool,
		"arguments": arguments,
	})
	if string(response["error"]) != "" {
		t.Fatalf("%s failed: %s", tool, response["error"])
	}
	var result mcpToolResult
	if err := json.Unmarshal(response["result"], &result); err != nil {
		t.Fatal(err)
	}
	var decoded T
	if err := json.Unmarshal(result.StructuredContent, &decoded); err != nil {
		t.Fatalf("%s result %s: %v", tool, result.StructuredContent, err)
	}
	return decoded
}

func lastOperationIndex(operations []string, method string) int {
	index := -1
	for position, operation := range operations {
		if operation == method {
			index = position
		}
	}
	return index
}
