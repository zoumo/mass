package sa

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

type workerHarness struct {
	mass       *fakeMASSServer
	config     Config
	store      *StateStore
	bridge     *TicketBridge
	service    *WorkerService
	httpServer *httptest.Server
	routers    map[string]CapabilityGrant
}

func newWorkerHarness(t *testing.T) *workerHarness {
	t.Helper()
	mass := newFakeMASSServer(t, "")
	massProject := newTestProject("mass")
	secondaryProject := newTestProject("mass-other")
	config := newTestConfig(t, mass.endpoint(), filepath.Join(t.TempDir(), "state.json"), massProject, secondaryProject)

	store, err := OpenStateStore(config.StateFile)
	if err != nil {
		t.Fatal(err)
	}
	bridge, err := NewTicketBridge(config, store)
	if err != nil {
		t.Fatal(err)
	}
	service, err := NewWorkerService(config, store, bridge, WorkerServiceOptions{
		CapabilityDir: t.TempDir(),
		MCPCommand:    "/opt/mass-sa",
	})
	if err != nil {
		t.Fatal(err)
	}
	httpServer := httptest.NewServer(service.HTTPHandler())
	t.Cleanup(httpServer.Close)
	if err := service.SetToolEndpoint(httpServer.URL + "/v1/tools"); err != nil {
		t.Fatal(err)
	}
	harness := &workerHarness{
		mass:       mass,
		config:     config,
		store:      store,
		bridge:     bridge,
		service:    service,
		httpServer: httpServer,
		routers:    map[string]CapabilityGrant{},
	}
	if _, err := BootstrapRouters(t.Context(), config, store, WithRouterTools(service)); err != nil {
		t.Fatal(err)
	}
	for _, project := range config.Projects {
		grant, err := service.EnsureRouterCapability(project.ID)
		if err != nil {
			t.Fatal(err)
		}
		harness.routers[project.ID] = grant
	}
	return harness
}

func (h *workerHarness) rootAndChild(t *testing.T, projectID, suffix string) (TicketState, TicketState) {
	t.Helper()
	root, err := h.bridge.CreateTicket(t.Context(), CreateTicketRequest{Acceptance: "Explicit root acceptance",
		ProjectID: projectID,
		Title:     "Root " + suffix,
		Objective: "Deliver root " + suffix + " in " + projectID,
	})
	if err != nil {
		t.Fatal(err)
	}
	ticket, err := h.bridge.CreateTicket(t.Context(), CreateTicketRequest{
		ProjectID:      projectID,
		ParentTicketID: root.ID,
		Title:          "Ticket " + suffix,
		Objective:      "Implement ticket " + suffix,
		Acceptance:     "The ticket behavior is verified for " + suffix,
	})
	if err != nil {
		t.Fatal(err)
	}
	return root, ticket
}

func (h *workerHarness) createTicket(t *testing.T, projectID, rootTicketID, suffix string) TicketState {
	t.Helper()
	ticket, err := h.bridge.CreateTicket(t.Context(), CreateTicketRequest{
		ProjectID:      projectID,
		ParentTicketID: rootTicketID,
		Title:          "Ticket " + suffix,
		Objective:      "Implement ticket " + suffix,
		Acceptance:     "The ticket behavior is verified for " + suffix,
	})
	if err != nil {
		t.Fatal(err)
	}
	return ticket
}

func (h *workerHarness) createWorker(t *testing.T, projectID, rootTicketID, ticketID, name, model, effort string) WorkerView {
	t.Helper()
	status, body := h.callTool(t, h.routers[projectID].Token, "create_worker", createWorkerArgs{
		TicketID: ticketID,
		Name:     name,
		Model:    model,
		Effort:   effort,
	})
	if status != http.StatusOK {
		t.Fatalf("create_worker status %d: %s", status, body)
	}
	return decodeToolResult[WorkerView](t, body)
}

func (h *workerHarness) callTool(t *testing.T, token, tool string, arguments any) (int, []byte) {
	t.Helper()
	payload, err := json.Marshal(arguments)
	if err != nil {
		t.Fatal(err)
	}
	request, err := http.NewRequestWithContext(t.Context(), http.MethodPost, h.httpServer.URL+"/v1/tools/"+tool, bytes.NewReader(payload))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var body bytes.Buffer
	if _, err := body.ReadFrom(response.Body); err != nil {
		t.Fatal(err)
	}
	return response.StatusCode, body.Bytes()
}

func decodeToolResult[T any](t *testing.T, body []byte) T {
	t.Helper()
	var envelope struct {
		Result json.RawMessage `json:"result"`
		Error  *OperationError `json:"error"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		t.Fatalf("invalid tool response %s: %v", body, err)
	}
	if envelope.Error != nil {
		t.Fatalf("unexpected tool error: %+v", envelope.Error)
	}
	var result T
	if err := json.Unmarshal(envelope.Result, &result); err != nil {
		t.Fatalf("invalid result %s: %v", envelope.Result, err)
	}
	return result
}

func assertToolError(t *testing.T, status int, body []byte, wantStatus int, wantReason string) {
	t.Helper()
	if status != wantStatus {
		t.Fatalf("tool status = %d, want %d: %s", status, wantStatus, body)
	}
	var envelope struct {
		Error *OperationError `json:"error"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil || envelope.Error == nil {
		t.Fatalf("tool error response is invalid: %s: %v", body, err)
	}
	if envelope.Error.Reason != wantReason {
		t.Fatalf("tool error reason = %q, want %q: %+v", envelope.Error.Reason, wantReason, envelope.Error)
	}
}

func workerCapability(t *testing.T, harness *workerHarness, assignmentID string) (capabilityFile, string) {
	t.Helper()
	path := filepath.Join(harness.service.capDir, assignmentID+".json")
	content, err := readCapabilityFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return content, path
}

func TestRouterCreatesWorkerWithScopedMCPAndConfig(t *testing.T) {
	harness := newWorkerHarness(t)
	root, ticket := harness.rootAndChild(t, "mass", "worker-config")

	worker := harness.createWorker(t, "mass", root.ID, ticket.ID, "worker-one", "deepseek-worker-override", "max")
	if worker.Namespace != WorkerNamespace("mass", root.ID) || worker.AssignmentID == "" || worker.SessionID == "" {
		t.Fatalf("worker identity is incomplete: %+v", worker)
	}
	if worker.Model != "deepseek-worker-override" || worker.Effort != "max" {
		t.Fatalf("worker override was not persisted: %+v", worker)
	}

	capability, capabilityPath := workerCapability(t, harness, worker.AssignmentID)
	info, err := os.Stat(capabilityPath)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("capability mode = %o, want 600", info.Mode().Perm())
	}
	dirInfo, err := os.Stat(filepath.Dir(capabilityPath))
	if err != nil {
		t.Fatal(err)
	}
	if dirInfo.Mode().Perm() != 0o700 {
		t.Fatalf("capability directory mode = %o, want 700", dirInfo.Mode().Perm())
	}
	if !filepath.IsAbs(capabilityPath) || capability.ProjectID != "mass" ||
		capability.RootTicketID != root.ID || capability.WorkerName != "worker-one" ||
		capability.AssignmentID != worker.AssignmentID || capability.Role != "worker" {
		t.Fatalf("capability scope is wrong: %+v", capability)
	}

	creates := harness.mass.creates()
	var workerCreates []json.RawMessage
	for _, raw := range creates {
		var address struct {
			Input struct {
				Metadata SessionMetadata `json:"metadata"`
			} `json:"input"`
		}
		if err := json.Unmarshal(raw, &address); err != nil {
			t.Fatal(err)
		}
		if address.Input.Metadata.Namespace == worker.Namespace {
			workerCreates = append(workerCreates, raw)
		}
	}
	if len(workerCreates) != 1 {
		t.Fatalf("worker session.create calls = %d, want 1", len(workerCreates))
	}
	var create struct {
		Input CreateSessionInput `json:"input"`
	}
	if err := json.Unmarshal(workerCreates[0], &create); err != nil {
		t.Fatal(err)
	}
	if create.Input.Metadata.Namespace != worker.Namespace || create.Input.Metadata.Name != "worker-one" {
		t.Fatalf("unexpected worker Session address: %+v", create.Input.Metadata)
	}
	if create.Input.Spec.Config == nil || create.Input.Spec.Config.Model != "deepseek-worker-override" ||
		create.Input.Spec.Config.Effort != "max" {
		t.Fatalf("worker Session config = %+v", create.Input.Spec.Config)
	}
	if create.Input.InitialInput == nil || len(create.Input.InitialInput.Content) != 1 ||
		!strings.Contains(create.Input.InitialInput.Content[0].Text, "Initialization only") ||
		!strings.Contains(create.Input.InitialInput.Content[0].Text, "Do not execute ticket work") {
		t.Fatalf("worker create-only initialization input = %+v", create.Input.InitialInput)
	}
	if len(worker.InitialInputReceipt) == 0 {
		t.Fatal("worker initialization receipt was not returned")
	}
	storedWorker := harness.store.Snapshot().Projects["mass"].Workers[workerKey(worker.Namespace, worker.Name)]
	if len(storedWorker.InitialInputReceipt) == 0 {
		t.Fatal("worker initialization receipt was not persisted")
	}
	for _, fragment := range []string{
		"memory for mass",
		ticket.RootTicketID,
		ticket.Objective,
		ticket.Acceptance,
		worker.Namespace,
		"list_workers",
		"get_worker",
		"send_to_worker",
	} {
		if !strings.Contains(create.Input.Spec.SystemPrompt, fragment) {
			t.Fatalf("worker prompt omitted %q: %q", fragment, create.Input.Spec.SystemPrompt)
		}
	}
	if len(create.Input.Spec.MCPServers) != 1 {
		t.Fatalf("MCP servers = %+v", create.Input.Spec.MCPServers)
	}
	mcp := create.Input.Spec.MCPServers[0]
	if mcp.Name != "mass-sa" || mcp.Type != "stdio" || !filepath.IsAbs(mcp.Command) || mcp.Command != "/opt/mass-sa" {
		t.Fatalf("unexpected MCP config: %+v", mcp)
	}
	if !containsSequence(mcp.Args, []string{"mcp", "--project", "mass", "--role", "worker", "--capability", capabilityPath}) {
		t.Fatalf("MCP args are incomplete: %v", mcp.Args)
	}
	var rawCreate struct {
		Input struct {
			Spec struct {
				MCPServers []map[string]any `json:"mcpServers"`
			} `json:"spec"`
		} `json:"input"`
	}
	if err := json.Unmarshal(workerCreates[0], &rawCreate); err != nil {
		t.Fatal(err)
	}
	if _, exists := rawCreate.Input.Spec.MCPServers[0]["cwd"]; exists {
		t.Fatalf("worker MCP config contains cwd: %+v", rawCreate.Input.Spec.MCPServers[0])
	}
	if strings.Contains(string(workerCreates[0]), capability.Token) {
		t.Fatal("Session spec contains the capability token")
	}
	stateBytes, err := os.ReadFile(harness.config.StateFile)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(stateBytes), capability.Token) {
		t.Fatal("durable state contains the capability token")
	}
	if !strings.Contains(string(stateBytes), capabilityDigest(capability.Token)) {
		t.Fatal("durable state does not contain the capability digest")
	}

	ticketAfter := harness.store.Snapshot().Projects["mass"].Tickets[ticket.ID]
	if ticketAfter.AssignmentID != worker.AssignmentID {
		t.Fatalf("ticket assignment = %q, want %q", ticketAfter.AssignmentID, worker.AssignmentID)
	}
	stored := harness.store.Snapshot().Projects["mass"].Workers[workerKey(worker.Namespace, worker.Name)]
	if stored.Session.SessionID != worker.SessionID || stored.TicketID != ticket.ID || stored.AssignmentID != worker.AssignmentID {
		t.Fatalf("worker mapping was not persisted: %+v", stored)
	}
}

func TestWorkerCreationWaitsForCreateOnlyInitialization(t *testing.T) {
	harness := newWorkerHarness(t)
	root, ticket := harness.rootAndChild(t, "mass", "worker-initialization")
	harness.mass.setCreateResponseState("unknown")

	worker := harness.createWorker(t, "mass", root.ID, ticket.ID, "worker-initializing", "", "")
	if worker.State != "idle" {
		t.Fatalf("worker state = %q, want idle", worker.State)
	}
	if len(worker.InitialInputReceipt) == 0 {
		t.Fatal("worker initialization receipt was not retained")
	}
}

func TestWorkerCreationRetainsConfirmedIdentityWhenInitializationReadFails(t *testing.T) {
	harness := newWorkerHarness(t)
	root, ticket := harness.rootAndChild(t, "mass", "worker-initialization-read")
	harness.mass.setCreateResponseState("unknown")
	harness.mass.setGetError(&OperationError{
		Code:    "outcome_unknown",
		Reason:  "transport_lost",
		Message: "Session observation was lost",
	})

	status, body := harness.callTool(t, harness.routers["mass"].Token, "create_worker", createWorkerArgs{
		TicketID: ticket.ID,
		Name:     "worker-read-failed",
	})
	assertToolError(t, status, body, http.StatusInternalServerError, "worker_initialization_unconfirmed")

	namespace := WorkerNamespace("mass", root.ID)
	worker := harness.store.Snapshot().Projects["mass"].Workers[workerKey(namespace, "worker-read-failed")]
	if worker.Session.SessionID == "" {
		t.Fatal("confirmed worker Session ID was not retained")
	}
	if len(worker.InitialInputReceipt) == 0 {
		t.Fatal("confirmed worker initialization receipt was not retained")
	}
	if worker.State != "unknown" {
		t.Fatalf("worker state = %q, want unknown", worker.State)
	}
	assignment := harness.store.Snapshot().Projects["mass"].Assignments[worker.AssignmentID]
	if assignment.Session.SessionID != worker.Session.SessionID ||
		assignment.Status != "unknown" ||
		!assignment.ReconciliationRequired ||
		assignment.ReconciliationReason != "worker_initialization_unconfirmed" {
		t.Fatalf("assignment did not retain a reconcilable Session identity: %+v", assignment)
	}

	var workerCreates int
	for _, raw := range harness.mass.creates() {
		var create struct {
			Input struct {
				Metadata SessionMetadata `json:"metadata"`
			} `json:"input"`
		}
		if err := json.Unmarshal(raw, &create); err != nil {
			t.Fatal(err)
		}
		if create.Input.Metadata.Namespace == namespace && create.Input.Metadata.Name == worker.Name {
			workerCreates++
		}
	}
	if workerCreates != 1 {
		t.Fatalf("worker session.create calls = %d, want 1", workerCreates)
	}

	harness.mass.setGetError(nil)
	status, body = harness.callTool(t, harness.routers["mass"].Token, "get_worker", routerGetWorkerArgs{
		RootTicketID: root.ID,
		Name:         worker.Name,
	})
	if status != http.StatusOK {
		t.Fatalf("retained worker was not readable: %d %s", status, body)
	}
	readWorker := decodeToolResult[WorkerView](t, body)
	if readWorker.SessionID != worker.Session.SessionID || len(readWorker.InitialInputReceipt) == 0 {
		t.Fatalf("get_worker lost the confirmed identity: %+v", readWorker)
	}

	capability, capabilityPath := workerCapability(t, harness, worker.AssignmentID)
	capabilityState := harness.store.Snapshot().Capabilities[capabilityDigest(capability.Token)]
	if !capabilityState.RevokedAt.IsZero() {
		t.Fatal("worker capability was revoked after an unconfirmed initialization read")
	}
	if _, err := os.Stat(capabilityPath); err != nil {
		t.Fatalf("worker capability file was removed: %v", err)
	}
	if worker.Session.SessionID == "" || len(worker.InitialInputReceipt) == 0 || worker.State != "unknown" {
		t.Fatalf("retained worker is not readable from durable state: %+v", worker)
	}
}

func TestWorkerCollaborationIsConfinedToProjectRoot(t *testing.T) {
	harness := newWorkerHarness(t)
	root, ticket := harness.rootAndChild(t, "mass", "same-root")
	ticketTwo := harness.createTicket(t, "mass", root.ID, "same-root-two")
	workerOne := harness.createWorker(t, "mass", root.ID, ticket.ID, "worker-one", "", "")
	workerTwo := harness.createWorker(t, "mass", root.ID, ticketTwo.ID, "worker-two", "", "")
	otherRoot, otherTicket := harness.rootAndChild(t, "mass", "other-root")
	otherWorker := harness.createWorker(t, "mass", otherRoot.ID, otherTicket.ID, "worker-other", "", "")
	secondaryRoot, secondaryTicket := harness.rootAndChild(t, "mass-other", "secondary-root")
	secondaryWorker := harness.createWorker(t, "mass-other", secondaryRoot.ID, secondaryTicket.ID, "worker-secondary", "", "")

	workerOneCap, _ := workerCapability(t, harness, workerOne.AssignmentID)
	workerOneToken := workerOneCap.Token
	secondaryCap, _ := workerCapability(t, harness, secondaryWorker.AssignmentID)
	status, body := harness.callTool(t, workerOneToken, "list_workers", emptyArgs{})
	if status != http.StatusOK {
		t.Fatalf("worker list status %d: %s", status, body)
	}
	list := decodeToolResult[ListWorkersResult](t, body)
	var names []string
	for _, worker := range list.Workers {
		names = append(names, worker.Name)
	}
	sort.Strings(names)
	if strings.Join(names, ",") != "worker-one,worker-two" {
		t.Fatalf("worker list names = %v", names)
	}

	status, body = harness.callTool(t, workerOneToken, "get_worker", workerGetArgs{Name: "worker-two"})
	if status != http.StatusOK {
		t.Fatalf("same-root get failed: %d %s", status, body)
	}
	if got := decodeToolResult[WorkerView](t, body); got.AssignmentID != workerTwo.AssignmentID {
		t.Fatalf("get_worker returned %+v", got)
	}

	crossStatus, crossBody := harness.callTool(t, workerOneToken, "get_worker", workerGetArgs{Name: otherWorker.Name})
	missingStatus, missingBody := harness.callTool(t, workerOneToken, "get_worker", workerGetArgs{Name: "missing-worker"})
	assertToolError(t, crossStatus, crossBody, http.StatusNotFound, "worker_not_found")
	assertToolError(t, missingStatus, missingBody, http.StatusNotFound, "worker_not_found")
	if !bytes.Equal(crossBody, missingBody) {
		t.Fatalf("cross-root and missing get differ:\ncross: %s\nmissing: %s", crossBody, missingBody)
	}

	crossStatus, crossBody = harness.callTool(t, workerOneToken, "send_to_worker", workerSendToWorkerArgs{
		Name: otherWorker.Name, Message: "do not cross this boundary",
	})
	missingStatus, missingBody = harness.callTool(t, workerOneToken, "send_to_worker", workerSendToWorkerArgs{
		Name: "missing-worker", Message: "do not cross this boundary",
	})
	assertToolError(t, crossStatus, crossBody, http.StatusNotFound, "worker_not_found")
	assertToolError(t, missingStatus, missingBody, http.StatusNotFound, "worker_not_found")
	if !bytes.Equal(crossBody, missingBody) {
		t.Fatalf("cross-root and missing send differ:\ncross: %s\nmissing: %s", crossBody, missingBody)
	}

	status, body = harness.callTool(t, workerOneToken, "get_worker", workerSendToWorkerArgs{Name: secondaryWorker.Name, Message: "forged"})
	assertToolError(t, status, body, http.StatusBadRequest, "invalid_tool_arguments")
	for _, forgedArgs := range []map[string]any{
		{"namespace": secondaryWorker.Namespace},
		{"projectId": "mass-other"},
		{"token": secondaryCap.Token},
	} {
		status, body = harness.callTool(t, workerOneToken, "list_workers", forgedArgs)
		assertToolError(t, status, body, http.StatusBadRequest, "invalid_tool_arguments")
	}
	status, body = harness.callTool(t, workerOneToken, "create_worker", createWorkerArgs{
		TicketID: ticket.ID, Name: "forged-worker",
	})
	assertToolError(t, status, body, http.StatusNotFound, "tool_not_found")
	status, body = harness.callTool(t, "not-a-capability", "list_workers", emptyArgs{})
	assertToolError(t, status, body, http.StatusUnauthorized, "capability_invalid")

	status, body = harness.callTool(t, harness.routers["mass"].Token, "create_worker", createWorkerArgs{
		TicketID: secondaryTicket.ID, Name: "cross-project-worker",
	})
	assertToolError(t, status, body, http.StatusNotFound, "worker_not_found")

	status, body = harness.callTool(t, secondaryCap.Token, "get_worker", workerGetArgs{Name: workerOne.Name})
	assertToolError(t, status, body, http.StatusNotFound, "worker_not_found")
	status, body = harness.callTool(t, secondaryCap.Token, "send_to_worker", workerSendToWorkerArgs{
		Name: workerOne.Name, Message: "cross-project attempt",
	})
	assertToolError(t, status, body, http.StatusNotFound, "worker_not_found")
}

func TestRouterTargetsOnlyTheRequestedRootAndProject(t *testing.T) {
	harness := newWorkerHarness(t)
	rootOne, ticketOne := harness.rootAndChild(t, "mass", "router-scope-one")
	harness.createWorker(t, "mass", rootOne.ID, ticketOne.ID, "worker-one", "", "")
	rootTwo, ticketTwo := harness.rootAndChild(t, "mass", "router-scope-two")
	workerTwo := harness.createWorker(t, "mass", rootTwo.ID, ticketTwo.ID, "worker-two", "", "")
	secondaryRoot, secondaryTicket := harness.rootAndChild(t, "mass-other", "router-scope-secondary")
	secondaryWorker := harness.createWorker(t, "mass-other", secondaryRoot.ID, secondaryTicket.ID, "worker-secondary", "", "")

	routerToken := harness.routers["mass"].Token
	status, body := harness.callTool(t, routerToken, "send_to_worker", routerSendToWorkerArgs{
		RootTicketID: rootTwo.ID,
		Name:         workerTwo.Name,
		Message:      "review root two",
	})
	if status != http.StatusOK {
		t.Fatalf("router send status %d: %s", status, body)
	}
	result := decodeToolResult[SendToWorkerResult](t, body)
	events := harness.store.Snapshot().Projects["mass"].Inbox
	event := events[result.EventID]
	if event.RootTicketID != rootTwo.ID || event.TargetAssignmentID != workerTwo.AssignmentID ||
		event.TargetSession.SessionID != workerTwo.SessionID || event.Source != "router" {
		t.Fatalf("router root-scoped event = %+v", event)
	}
	for _, stored := range events {
		if stored.RootTicketID != rootTwo.ID {
			t.Fatalf("event crossed into root %q: %+v", stored.RootTicketID, stored)
		}
	}

	crossStatus, crossBody := harness.callTool(t, routerToken, "send_to_worker", routerSendToWorkerArgs{
		RootTicketID: rootOne.ID,
		Name:         workerTwo.Name,
		Message:      "do not cross this boundary",
	})
	missingStatus, missingBody := harness.callTool(t, routerToken, "send_to_worker", routerSendToWorkerArgs{
		RootTicketID: rootOne.ID,
		Name:         "missing-worker",
		Message:      "do not cross this boundary",
	})
	assertToolError(t, crossStatus, crossBody, http.StatusNotFound, "worker_not_found")
	assertToolError(t, missingStatus, missingBody, http.StatusNotFound, "worker_not_found")
	if !bytes.Equal(crossBody, missingBody) {
		t.Fatalf("router cross-root and missing send differ:\ncross: %s\nmissing: %s", crossBody, missingBody)
	}

	status, body = harness.callTool(t, routerToken, "send_to_worker", routerSendToWorkerArgs{
		RootTicketID: secondaryRoot.ID,
		Name:         secondaryWorker.Name,
		Message:      "cross-project attempt",
	})
	assertToolError(t, status, body, http.StatusNotFound, "worker_not_found")
	status, body = harness.callTool(t, routerToken, "get_worker", routerGetWorkerArgs{
		RootTicketID: secondaryRoot.ID,
		Name:         secondaryWorker.Name,
	})
	assertToolError(t, status, body, http.StatusNotFound, "worker_not_found")
	status, body = harness.callTool(t, routerToken, "list_workers", routerListWorkersArgs{
		RootTicketID: secondaryRoot.ID,
	})
	assertToolError(t, status, body, http.StatusNotFound, "worker_not_found")
}

func TestWorkerSendPersistsQueuedEventWithoutClaimingDelivery(t *testing.T) {
	harness := newWorkerHarness(t)
	root, ticket := harness.rootAndChild(t, "mass", "queue")
	targetTicket := harness.createTicket(t, "mass", root.ID, "queue-target")
	source := harness.createWorker(t, "mass", root.ID, ticket.ID, "worker-source", "", "")
	target := harness.createWorker(t, "mass", root.ID, targetTicket.ID, "worker-target", "", "")
	sourceCap, _ := workerCapability(t, harness, source.AssignmentID)

	status, body := harness.callTool(t, sourceCap.Token, "send_to_worker", workerSendToWorkerArgs{
		Name: target.Name, Message: "Please review the implementation",
	})
	if status != http.StatusOK {
		t.Fatalf("send status %d: %s", status, body)
	}
	result := decodeToolResult[SendToWorkerResult](t, body)
	if result.State != "queued" || result.Delivered || result.EventID == "" {
		t.Fatalf("send result = %+v", result)
	}
	event := harness.store.Snapshot().Projects["mass"].Inbox[result.EventID]
	if event.State != "queued" || event.TargetAssignmentID != target.AssignmentID ||
		event.TargetSession.SessionID != target.SessionID || event.Source != "worker:worker-source" {
		t.Fatalf("queued event = %+v", event)
	}
	var payload map[string]string
	if err := json.Unmarshal(event.Payload, &payload); err != nil || payload["message"] != "Please review the implementation" {
		t.Fatalf("event payload = %s: %v", event.Payload, err)
	}
	_, closeCalls, prompts := harness.mass.operationCounts()
	if closeCalls != 0 || prompts != 0 {
		t.Fatalf("send performed close=%d prompt=%d calls", closeCalls, prompts)
	}
}

func TestStopWorkerClosesSessionAndRevokesAssignmentAuthority(t *testing.T) {
	harness := newWorkerHarness(t)
	root, ticket := harness.rootAndChild(t, "mass", "stop")
	worker := harness.createWorker(t, "mass", root.ID, ticket.ID, "worker-stop", "", "")
	workerCap, capabilityPath := workerCapability(t, harness, worker.AssignmentID)

	status, body := harness.callTool(t, harness.routers["mass"].Token, "bind_route", bindRouteArgs{
		RootTicketID: root.ID, RouteKey: "source-42", WorkerName: worker.Name, AssignmentID: worker.AssignmentID,
	})
	if status != http.StatusOK {
		t.Fatalf("bind_route status %d: %s", status, body)
	}
	if len(harness.store.Snapshot().Projects["mass"].RouteBindings) != 1 {
		t.Fatal("route binding was not persisted")
	}

	status, body = harness.callTool(t, harness.routers["mass"].Token, "stop_worker", stopWorkerArgs{
		RootTicketID: root.ID, Name: worker.Name,
	})
	if status != http.StatusOK {
		t.Fatalf("stop_worker status %d: %s", status, body)
	}
	stopped := decodeToolResult[WorkerView](t, body)
	if stopped.State != "offline" {
		t.Fatalf("stopped worker = %+v", stopped)
	}
	_, closeCalls, _ := harness.mass.operationCounts()
	if closeCalls != 1 {
		t.Fatalf("session.close calls = %d, want 1", closeCalls)
	}
	session, exists := harness.mass.session(worker.SessionID)
	if !exists || session.Status.State != "offline" || session.Metadata.Name != worker.Name {
		t.Fatalf("closed Session identity/history was not preserved: %+v exists=%v", session, exists)
	}

	project := harness.store.Snapshot().Projects["mass"]
	assignment := project.Assignments[worker.AssignmentID]
	if assignment.Status != "closed" || !assignment.StoppedAt.IsZero() {
		t.Fatalf("Session close changed business stop state: %+v", assignment)
	}
	if len(project.RouteBindings) != 1 {
		t.Fatalf("Session close lost route references: %+v", project.RouteBindings)
	}
	capabilityState := harness.store.Snapshot().Capabilities[capabilityDigest(workerCap.Token)]
	if capabilityState.RevokedAt.IsZero() {
		t.Fatal("worker capability was not revoked in durable state")
	}
	if _, err := os.Stat(capabilityPath); err != nil {
		t.Fatalf("stable resume capability path was lost: %v", err)
	}
	status, body = harness.callTool(t, workerCap.Token, "list_workers", emptyArgs{})
	assertToolError(t, status, body, http.StatusUnauthorized, "capability_invalid")

	status, body = harness.callTool(t, harness.routers["mass"].Token, "get_worker", routerGetWorkerArgs{
		RootTicketID: root.ID, Name: worker.Name,
	})
	if status != http.StatusOK {
		t.Fatalf("get stopped worker failed: %d %s", status, body)
	}
	if got := decodeToolResult[WorkerView](t, body); got.State != "offline" {
		t.Fatalf("get_worker after stop = %+v", got)
	}
}

func TestCapabilityFilePermissionsAreRequired(t *testing.T) {
	harness := newWorkerHarness(t)
	root, ticket := harness.rootAndChild(t, "mass", "permissions")
	worker := harness.createWorker(t, "mass", root.ID, ticket.ID, "worker-permissions", "", "")
	_, path := workerCapability(t, harness, worker.AssignmentID)
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := readCapabilityFile(path); err == nil {
		t.Fatal("capability file with group/world permissions was accepted")
	}
}
