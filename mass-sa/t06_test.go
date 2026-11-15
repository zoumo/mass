package sa

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestT06ManagementAPIAuthenticatesScopesAndReadsBack(t *testing.T) {
	harness := newWorkerHarness(t)
	const token = "management-test-token"
	handler, err := NewManagementHandler(harness.service, token)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	client, err := NewManagementClientForEndpoint(server.URL+managementPrefix, token)
	if err != nil {
		t.Fatal(err)
	}

	unauthorized := httptest.NewRequest(http.MethodGet, server.URL+managementPrefix+"/health", nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, unauthorized)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated management status = %d", response.Code)
	}
	if err := client.Health(t.Context()); err != nil {
		t.Fatal(err)
	}

	var ticket TicketState
	if err := client.Request(t.Context(), "POST", "/tickets", map[string]string{"projectId": "missing-project", "objective": "must not create"}, &ticket); err == nil || !IsErrorCode(err, "not_found") {
		t.Fatalf("wrong project = %v", err)
	}
	if err := client.Request(t.Context(), "POST", "/tickets", map[string]string{"projectId": "mass", "objective": "Independent root work"}, &ticket); err != nil {
		t.Fatal(err)
	}
	accepted, err := client.SubmitEvent(t.Context(), AcceptEventRequest{ProjectID: "mass", RootTicketID: ticket.ID, Source: "test", Kind: "objective", Summary: "Independent work", Payload: json.RawMessage(`{}`)})
	if err != nil || accepted.RootTicketID != ticket.ID || accepted.RoutingPath != "router" {
		t.Fatalf("accepted = %+v %v", accepted, err)
	}
	inspection, err := client.InspectTicket(t.Context(), "mass", ticket.ID)
	if err != nil || inspection.Root.ID != ticket.ID || inspection.Counts.Router != 1 || len(inspection.Events) != 1 {
		t.Fatalf("inspection = %+v %v", inspection, err)
	}
	request, err := http.NewRequestWithContext(t.Context(), http.MethodPost, server.URL+managementPrefix+"/tickets",
		bytes.NewBufferString(`{"projectId":"mass","objective":"x","unknown":true}`))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Content-Type", "application/json")
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("unknown management field status = %d: %s", response.Code, response.Body.String())
	}
}

func TestT06DeliveredPromptedRestartRequiresManualReconciliation(t *testing.T) {
	harness := newWorkerHarness(t)
	root, ticket := harness.rootAndChild(t, "mass", "restart-reconcile")
	worker := harness.createWorker(t, "mass", root.ID, ticket.ID, "worker-reconcile", "", "")
	if _, err := harness.service.BindEventRoute(BindEventRouteRequest{
		ProjectID:    "mass",
		RootTicketID: root.ID,
		RouteKey:     "reconcile-key",
		WorkerName:   worker.Name,
		AssignmentID: worker.AssignmentID,
	}); err != nil {
		t.Fatal(err)
	}
	accepted, err := harness.service.AcceptEvent(AcceptEventRequest{
		ProjectID:    "mass",
		RootTicketID: root.ID,
		RouteKey:     "reconcile-key",
		Source:       "test",
		Kind:         "change",
		Summary:      "prompt accepted immediately before process stop",
		Payload:      json.RawMessage(`{"value":"once"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	now := harness.service.now()
	if err := harness.store.updateProject("mass", func(project *ProjectState) error {
		event := project.Inbox[accepted.EventID]
		event.State = "delivered"
		event.DeliveredAt = now
		event.UpdatedAt = now
		project.Inbox[event.ID] = event
		project.Deliveries["b-crash"] = DeliveryBatchState{
			ID:                 "b-crash",
			ProjectID:          "mass",
			RootTicketID:       root.ID,
			TargetAssignmentID: worker.AssignmentID,
			TargetWorkerName:   worker.Name,
			TargetSession:      SessionRef{SessionID: worker.SessionID},
			RoutingPath:        "direct",
			EventIDs:           []string{event.ID},
			State:              "delivered",
			CreatedAt:          now,
			UpdatedAt:          now,
			DeliveredAt:        now,
		}
		assignment := project.Assignments[worker.AssignmentID]
		assignment.ExecutionState = "prompted"
		assignment.PromptAcceptedAt = now
		assignment.UpdatedAt = now
		project.Assignments[assignment.ID] = assignment
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	before := len(observedPrompts(t, harness.mass))
	if err := harness.service.RecoverDispatches(); err != nil {
		t.Fatal(err)
	}
	inspection, err := harness.service.InspectTicket("mass", root.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(inspection.Reconciliation) != 1 ||
		inspection.Reconciliation[0].AssignmentID != worker.AssignmentID ||
		inspection.Reconciliation[0].Reason != "prompt_accepted_without_final" {
		t.Fatalf("reconciliation readout = %+v", inspection.Reconciliation)
	}
	assignment := harness.store.Snapshot().Projects["mass"].Assignments[worker.AssignmentID]
	if assignment.ExecutionState != "prompted" || !assignment.ReconciliationRequired {
		t.Fatalf("assignment after recovery = %+v", assignment)
	}
	dispatcher, err := NewEventDispatcher(harness.service)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := dispatcher.DispatchOnce(t.Context()); err != nil {
		t.Fatal(err)
	}
	if len(observedPrompts(t, harness.mass)) != before {
		t.Fatal("restart recovery resent an accepted prompt")
	}
}

func TestT06ManagementListenMustBeLoopback(t *testing.T) {
	config := newTestConfig(t, "ws://127.0.0.1:24368/rpc", t.TempDir()+"/state.json", newTestProject("mass"))
	config.Management.Listen = "0.0.0.0:8787"
	if err := config.Validate(); err == nil || !strings.Contains(err.Error(), "loopback") {
		t.Fatalf("public management listen error = %v", err)
	}
	config.Management.Listen = "127.0.0.1:0"
	if err := config.Validate(); err != nil {
		t.Fatal(err)
	}
}
