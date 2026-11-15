package sa

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"gopkg.in/yaml.v3"
)

type t06Server struct {
	command  *exec.Cmd
	client   *ManagementClient
	config   Config
	logPath  string
	stopOnce sync.Once
	waitErr  error
}

func startT06Server(t *testing.T, config Config) *t06Server {
	t.Helper()
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	raw, err := yaml.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configPath, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	client, err := NewManagementClient(config)
	if err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(t.TempDir(), "serve.log")
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	command := exec.Command(massSABinary, "serve", "--config", configPath)
	command.Stdout = logFile
	command.Stderr = logFile
	if err := command.Start(); err != nil {
		_ = logFile.Close()
		t.Fatal(err)
	}
	server := &t06Server{command: command, client: client, config: config, logPath: logPath}
	t.Cleanup(func() {
		server.stop(t)
		_ = logFile.Close()
	})
	waitT06(t, 15*time.Second, "mass-sa management startup", func() bool {
		if err := client.Health(context.Background()); err == nil {
			return true
		}
		if command.ProcessState != nil && command.ProcessState.Exited() {
			t.Fatalf("mass-sa serve exited during startup: %s", server.logs())
		}
		return false
	})
	return server
}

func (s *t06Server) stop(t *testing.T) {
	t.Helper()
	s.stopOnce.Do(func() {
		if s.command == nil || s.command.Process == nil {
			return
		}
		_ = s.command.Process.Signal(syscall.SIGTERM)
		done := make(chan error, 1)
		go func() {
			done <- s.command.Wait()
		}()
		select {
		case s.waitErr = <-done:
		case <-time.After(20 * time.Second):
			_ = s.command.Process.Kill()
			s.waitErr = <-done
			t.Errorf("mass-sa serve did not stop within 20s")
		}
	})
}

func (s *t06Server) logs() string {
	raw, _ := os.ReadFile(s.logPath)
	if len(raw) > 12<<10 {
		raw = raw[len(raw)-(12<<10):]
	}
	return string(raw)
}

func runT06CLI(t *testing.T, args ...string) []byte {
	t.Helper()
	command := exec.Command(massSABinary, args...)
	var stdout, stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	if err := command.Run(); err != nil {
		t.Fatalf("mass-sa %s failed: %v\nstdout:\n%s\nstderr:\n%s", strings.Join(args, " "), err, stdout.String(), stderr.String())
	}
	return stdout.Bytes()
}

func runT06JSON[T any](t *testing.T, args ...string) T {
	t.Helper()
	raw := runT06CLI(t, args...)
	var result T
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatalf("mass-sa %s returned invalid JSON: %v\n%s", strings.Join(args, " "), err, raw)
	}
	return result
}

func waitT06(t *testing.T, timeout time.Duration, description string, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", description)
}

type t06MCP struct {
	command *exec.Cmd
	input   io.WriteCloser
	output  *bufio.Reader
	nextID  int
	logPath string
}

func startT06MCP(t *testing.T, projectID, role, capabilityPath string) *t06MCP {
	t.Helper()
	logPath := filepath.Join(t.TempDir(), "mcp.log")
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	inputReader, inputWriter, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	outputReader, outputWriter, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	command := exec.Command(massSABinary, "mcp", "--project", projectID, "--role", role, "--capability", capabilityPath)
	command.Stdin = inputReader
	command.Stdout = outputWriter
	command.Stderr = logFile
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	_ = inputReader.Close()
	_ = outputWriter.Close()
	client := &t06MCP{command: command, input: inputWriter, output: bufio.NewReader(outputReader), logPath: logPath}
	t.Cleanup(func() {
		_ = client.input.Close()
		done := make(chan error, 1)
		go func() { done <- command.Wait() }()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			_ = command.Process.Kill()
			<-done
			t.Errorf("stdio MCP process did not stop")
		}
		_ = outputReader.Close()
		_ = logFile.Close()
	})
	initialized := client.request(t, "initialize", map[string]any{
		"protocolVersion": "2024-11-05",
		"clientInfo":      map[string]string{"name": "t06-e2e", "version": "1"},
		"capabilities":    map[string]any{},
	})
	if string(initialized["error"]) != "" {
		t.Fatalf("MCP initialize failed: %s\n%s", initialized["error"], client.logs())
	}
	client.notify(t, "notifications/initialized")
	return client
}

func (c *t06MCP) request(t *testing.T, method string, params any) map[string]json.RawMessage {
	t.Helper()
	c.nextID++
	message := map[string]any{"jsonrpc": "2.0", "id": c.nextID, "method": method}
	if params != nil {
		message["params"] = params
	}
	if err := json.NewEncoder(c.input).Encode(message); err != nil {
		t.Fatalf("write MCP request: %v\n%s", err, c.logs())
	}
	line, err := c.output.ReadBytes('\n')
	if err != nil {
		t.Fatalf("read MCP response: %v\n%s", err, c.logs())
	}
	var response map[string]json.RawMessage
	if err := json.Unmarshal(line, &response); err != nil {
		t.Fatalf("invalid MCP response %q: %v", line, err)
	}
	return response
}

func (c *t06MCP) notify(t *testing.T, method string) {
	t.Helper()
	if err := json.NewEncoder(c.input).Encode(map[string]any{"jsonrpc": "2.0", "method": method}); err != nil {
		t.Fatal(err)
	}
}

func (c *t06MCP) call(t *testing.T, tool string, arguments any) json.RawMessage {
	t.Helper()
	response := c.request(t, "tools/call", map[string]any{"name": tool, "arguments": arguments})
	if string(response["error"]) != "" {
		t.Fatalf("MCP %s failed: %s\n%s", tool, response["error"], c.logs())
	}
	var result mcpToolResult
	if err := json.Unmarshal(response["result"], &result); err != nil {
		t.Fatal(err)
	}
	return result.StructuredContent
}

func (c *t06MCP) callError(t *testing.T, tool string, arguments any) OperationError {
	t.Helper()
	response := c.request(t, "tools/call", map[string]any{"name": tool, "arguments": arguments})
	if string(response["error"]) == "" {
		t.Fatalf("MCP %s unexpectedly succeeded", tool)
	}
	var rpcError struct {
		Data OperationError `json:"data"`
	}
	if err := json.Unmarshal(response["error"], &rpcError); err != nil {
		t.Fatal(err)
	}
	return rpcError.Data
}

func (c *t06MCP) logs() string {
	raw, _ := os.ReadFile(c.logPath)
	if len(raw) > 8<<10 {
		raw = raw[len(raw)-(8<<10):]
	}
	return string(raw)
}

func decodeT06[T any](t *testing.T, raw json.RawMessage) T {
	t.Helper()
	var value T
	if err := json.Unmarshal(raw, &value); err != nil {
		t.Fatalf("decode %T: %v: %s", value, err, raw)
	}
	return value
}

func freeLoopbackAddress(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	_ = listener.Close()
	return address
}

func t06Inspection(t *testing.T, client *ManagementClient, rootTicketID string) TicketInspection {
	t.Helper()
	inspection, err := client.InspectTicket(context.Background(), "mass", rootTicketID)
	if err != nil {
		t.Fatal(err)
	}
	return inspection
}

func t06Event(t *testing.T, inspection TicketInspection, eventID string) InboxEventView {
	t.Helper()
	for _, event := range inspection.Events {
		if event.ID == eventID {
			return event
		}
	}
	t.Fatalf("event %s not found in inspection", eventID)
	return InboxEventView{}
}

func t06Ticket(t *testing.T, inspection TicketInspection, ticketID string) TicketState {
	t.Helper()
	for _, ticket := range inspection.Tickets {
		if ticket.ID == ticketID {
			return ticket
		}
	}
	t.Fatalf("ticket %s not found in inspection", ticketID)
	return TicketState{}
}

func waitT06EventDelivered(t *testing.T, server *t06Server, rootTicketID, eventID string) TicketInspection {
	t.Helper()
	var inspection TicketInspection
	waitT06(t, 15*time.Second, "event delivered", func() bool {
		inspection = t06Inspection(t, server.client, rootTicketID)
		event := t06Event(t, inspection, eventID)
		return event.State == "delivered"
	})
	return inspection
}

func retainedRouterInputs(inspection TicketInspection, minimum int) bool {
	retained := map[string]bool{}
	for _, batch := range inspection.Batches {
		if batch.RoutingPath == "router" && batch.ResultState == "confirmed" && batch.FinalEventID != "" {
			for _, eventID := range batch.EventIDs {
				retained[eventID] = true
			}
		}
	}
	count := 0
	for _, event := range inspection.Events {
		if event.RoutingPath == "router" {
			if event.State != "delivered" || !retained[event.ID] {
				return false
			}
			count++
		}
	}
	return count >= minimum
}

func TestT06OfflineCLIAndMCPEndToEnd(t *testing.T) {
	mass := newFakeMASSServer(t, "")
	multica := newFakeMultica(t)
	project := newTestProject("mass")
	project.CWD = t.TempDir()
	project.Multica = ProjectMultica{WorkspaceID: multica.workspace, ProjectID: multica.project}
	stateFile := filepath.Join(t.TempDir(), "state.json")
	config := newTestConfig(t, mass.endpoint(), stateFile, project)
	config.Multica.Executable = fakeMulticaBinary
	config.Management.Listen = freeLoopbackAddress(t)
	tokenPath := filepath.Join(t.TempDir(), "management-token")
	if err := os.WriteFile(tokenPath, []byte("offline-management-token\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	config.Management.TokenFile = tokenPath
	config.Projects[0].Inbox.MergeWindowMS = 1500
	server := startT06Server(t, config)

	configForCLI := filepath.Join(t.TempDir(), "cli-config.yaml")
	rawConfig, err := yaml.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configForCLI, rawConfig, 0o600); err != nil {
		t.Fatal(err)
	}
	cli := func(args ...string) []string {
		return append(append([]string(nil), args...), "--config", configForCLI)
	}

	ticket := runT06JSON[TicketState](t, cli("ticket", "create", "--project", "mass", "--title", "Offline work", "--objective", "Process routed inputs", "--acceptance", "Confirmed report and explicit acceptance")...)
	firstRoot := ticket
	initial := runT06JSON[AcceptedEvent](t, cli("event", "--project", "mass", "--root-ticket", ticket.ID, "--kind", "objective", "--summary", "Queued independent work")...)
	firstInspection := waitT06EventDelivered(t, server, ticket.ID, initial.EventID)
	if firstInspection.Counts.Router != 1 {
		t.Fatalf("initial router counts = %+v", firstInspection.Counts)
	}
	projectState := func() ProjectState {
		store, err := OpenStateStore(stateFile)
		if err != nil {
			t.Fatal(err)
		}
		return store.Snapshot().Projects["mass"]
	}()
	if projectState.Router == nil {
		t.Fatal("router was not bootstrapped")
	}
	routerMCP := startT06MCP(t, "mass", "router", projectState.Router.CapabilityFile)
	worker := decodeT06[WorkerView](t, routerMCP.call(t, "create_worker", map[string]any{
		"ticketId": ticket.ID,
		"name":     "worker-offline",
	}))
	if worker.SessionID == "" || worker.AssignmentID == "" || worker.Namespace != WorkerNamespace("mass", firstRoot.ID) {
		t.Fatalf("worker = %+v", worker)
	}

	beforeBinding := runT06JSON[AcceptedEvent](t, cli("event",
		"--project", "mass",
		"--root-ticket", firstRoot.ID,
		"--route-key", "offline-route",
		"--kind", "push",
		"--summary", "first unbound event",
		"--payload", `{"index":0}`)...)
	if beforeBinding.RoutingPath != "router" {
		t.Fatalf("first route = %+v", beforeBinding)
	}
	waitT06EventDelivered(t, server, firstRoot.ID, beforeBinding.EventID)

	binding := runT06JSON[RouteBindingView](t, cli("route", "bind",
		"--project", "mass",
		"--root-ticket", firstRoot.ID,
		"--route-key", "offline-route",
		"--worker", worker.Name,
		"--assignment", worker.AssignmentID)...)
	if binding.AssignmentID != worker.AssignmentID {
		t.Fatalf("route binding = %+v", binding)
	}
	routes := runT06JSON[struct {
		RouteBindings []RouteBindingView `json:"routeBindings"`
	}](t, cli("route", "list", "--project", "mass", "--root-ticket", firstRoot.ID)...)
	if len(routes.RouteBindings) != 1 || routes.RouteBindings[0].RouteKey != "offline-route" {
		t.Fatalf("route readback = %+v", routes.RouteBindings)
	}

	// Hold the public MASS target busy while the CLI admits the complete
	// batch; process startup duration must not decide how many turns we test.
	mass.setState(worker.SessionID, "running")
	var directEvents []AcceptedEvent
	for index := 1; index <= 3; index++ {
		event := runT06JSON[AcceptedEvent](t, cli("event",
			"--project", "mass",
			"--root-ticket", firstRoot.ID,
			"--route-key", "offline-route",
			"--kind", "push",
			"--summary", fmt.Sprintf("batched event %d", index),
			"--payload", fmt.Sprintf(`{"index":%d}`, index))...)
		if event.RoutingPath != "direct" || event.TargetAssignmentID != worker.AssignmentID {
			t.Fatalf("direct event = %+v", event)
		}
		directEvents = append(directEvents, event)
	}
	waitT06(t, 10*time.Second, "all admitted inputs past merge window", func() bool {
		inspection := t06Inspection(t, server.client, firstRoot.ID)
		last := t06Event(t, inspection, directEvents[len(directEvents)-1].EventID)
		return time.Since(last.AcceptedAt) >= time.Duration(config.Projects[0].Inbox.MergeWindowMS)*time.Millisecond
	})
	mass.setState(worker.SessionID, "idle")
	waitT06(t, 20*time.Second, "one batched worker report", func() bool {
		inspection := t06Inspection(t, server.client, firstRoot.ID)
		currentTicket := t06Ticket(t, inspection, ticket.ID)
		if len(currentTicket.Reports) != 1 || currentTicket.Reports[0].Status != "confirmed" {
			return false
		}
		for _, event := range directEvents {
			if t06Event(t, inspection, event.EventID).State != "delivered" {
				return false
			}
		}
		return inspection.Counts.Direct == 3
	})
	waitT06(t, 20*time.Second, "router final outputs retained", func() bool {
		inspection := t06Inspection(t, server.client, firstRoot.ID)
		return retainedRouterInputs(inspection, 3)
	})
	firstInspection = t06Inspection(t, server.client, firstRoot.ID)
	if firstInspection.Counts.Direct != 3 || firstInspection.Counts.Delivered < 6 {
		t.Fatalf("root delivery counts = %+v", firstInspection.Counts)
	}
	directBatches := 0
	for _, batch := range firstInspection.Batches {
		if batch.RoutingPath == "direct" {
			directBatches++
			if len(batch.EventIDs) != 3 || batch.State != "delivered" {
				t.Fatalf("direct batch = %+v", batch)
			}
		}
	}
	if directBatches != 1 {
		t.Fatalf("direct batch count = %d, want 1", directBatches)
	}
	workerCapabilityPath := filepath.Join(filepath.Dir(stateFile), "capabilities", worker.AssignmentID+".json")
	workerMCP := startT06MCP(t, "mass", "worker", workerCapabilityPath)
	workerList := decodeT06[ListWorkersResult](t, workerMCP.call(t, "list_workers", map[string]any{}))
	if len(workerList.Workers) != 1 || workerList.Workers[0].AssignmentID != worker.AssignmentID {
		t.Fatalf("worker MCP list = %+v", workerList)
	}

	secondTicket := runT06JSON[TicketState](t, cli("ticket", "create", "--project", "mass", "--objective", "Scope isolation work")...)
	secondWorker := decodeT06[WorkerView](t, routerMCP.call(t, "create_worker", map[string]any{
		"ticketId": secondTicket.ID,
		"name":     "worker-scope",
	}))
	workerList = decodeT06[ListWorkersResult](t, workerMCP.call(t, "list_workers", map[string]any{}))
	if len(workerList.Workers) != 1 || workerList.Workers[0].AssignmentID != worker.AssignmentID {
		t.Fatalf("cross-root worker leaked into list: %+v", workerList)
	}
	crossGet := workerMCP.callError(t, "get_worker", map[string]any{"name": secondWorker.Name})
	crossSend := workerMCP.callError(t, "send_to_worker", map[string]any{"name": secondWorker.Name, "message": "forged"})
	if crossGet.Reason != "worker_not_found" || crossSend.Reason != "worker_not_found" {
		t.Fatalf("cross-root errors = get:%+v send:%+v", crossGet, crossSend)
	}

	revoked := runT06JSON[RouteBindingView](t, cli("route", "revoke",
		"--project", "mass",
		"--root-ticket", firstRoot.ID,
		"--route-key", "offline-route")...)
	if !revoked.Revoked || revoked.AssignmentID != worker.AssignmentID {
		t.Fatalf("revoked route = %+v", revoked)
	}
	routes = runT06JSON[struct {
		RouteBindings []RouteBindingView `json:"routeBindings"`
	}](t, cli("route", "list", "--project", "mass", "--root-ticket", firstRoot.ID)...)
	if len(routes.RouteBindings) != 0 {
		t.Fatalf("routes after revoke = %+v", routes.RouteBindings)
	}

	finishedTicket := decodeT06[TicketState](t, routerMCP.call(t, "finish_ticket", map[string]any{
		"ticketId":   ticket.ID,
		"resolution": "completed",
		"summary":    "Worker report met the acceptance criteria.",
	}))
	if finishedTicket.BusinessState != "completed" || finishedTicket.Status != "done" {
		t.Fatalf("finished ticket = %+v", finishedTicket)
	}
	if comments := multica.comments(ticket.Issue.ID); len(comments) < 2 {
		t.Fatalf("ticket comments = %+v, want worker report and resolution", comments)
	}

	secondBinding := runT06JSON[RouteBindingView](t, cli("route", "bind",
		"--project", "mass",
		"--root-ticket", secondTicket.ID,
		"--route-key", "scope-route",
		"--worker", secondWorker.Name,
		"--assignment", secondWorker.AssignmentID)...)
	if secondBinding.AssignmentID != secondWorker.AssignmentID {
		t.Fatalf("second binding = %+v", secondBinding)
	}
	secondEvent := runT06JSON[AcceptedEvent](t, cli("event",
		"--project", "mass",
		"--root-ticket", secondTicket.ID,
		"--route-key", "scope-route",
		"--kind", "scope",
		"--summary", "second root work",
		"--payload", `{"scope":"second"}`)...)
	if secondEvent.RoutingPath != "direct" {
		t.Fatalf("second root event = %+v", secondEvent)
	}
	waitT06(t, 20*time.Second, "second ticket report", func() bool {
		current := t06Ticket(t, t06Inspection(t, server.client, secondTicket.ID), secondTicket.ID)
		return len(current.Reports) == 1 && current.Reports[0].Status == "confirmed"
	})
	waitT06(t, 20*time.Second, "second router final outputs", func() bool {
		return retainedRouterInputs(t06Inspection(t, server.client, secondTicket.ID), 1)
	})
	decodeT06[TicketState](t, routerMCP.call(t, "finish_ticket", map[string]any{
		"ticketId":   secondTicket.ID,
		"resolution": "completed",
		"summary":    "Second root worker report accepted.",
	}))
	unknownTicket := runT06JSON[TicketState](t, cli("ticket", "create", "--project", "mass", "--objective", "Unknown and restart evidence")...)
	mass.setDropPromptReply(true)
	unknownEvent := runT06JSON[AcceptedEvent](t, cli("event",
		"--project", "mass",
		"--root-ticket", unknownTicket.ID,
		"--kind", "unknown",
		"--summary", "prompt acceptance is unknown",
		"--payload", `{"unknown":"once"}`)...)
	waitT06(t, 15*time.Second, "unknown router prompt", func() bool {
		return t06Event(t, t06Inspection(t, server.client, unknownTicket.ID), unknownEvent.EventID).State == "unknown"
	})
	mass.setDropPromptReply(false)
	_, _, promptsAfterUnknown := mass.operationCounts()
	time.Sleep(800 * time.Millisecond)
	_, _, promptsLater := mass.operationCounts()
	if promptsLater != promptsAfterUnknown {
		t.Fatalf("unknown prompt was resent: before=%d after=%d", promptsAfterUnknown, promptsLater)
	}

	queuedEvent := runT06JSON[AcceptedEvent](t, cli("event",
		"--project", "mass",
		"--root-ticket", unknownTicket.ID,
		"--kind", "restart",
		"--summary", "accepted immediately before restart",
		"--payload", `{"restart":"queued"}`)...)
	if queuedEvent.State != "queued" {
		t.Fatalf("queued event = %+v", queuedEvent)
	}
	server.stop(t)
	restarted := startT06Server(t, config)
	restartedInspection := waitT06EventDelivered(t, restarted, unknownTicket.ID, queuedEvent.EventID)
	if restartedInspection.Counts.Unknown < 1 {
		t.Fatalf("unknown state was not retained across restart: %+v", restartedInspection.Counts)
	}

}
