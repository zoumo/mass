package sa

import (
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"
	"time"
)

type mcpTestClient struct {
	t      *testing.T
	input  *io.PipeWriter
	output *io.PipeReader
	nextID int
}

func newMCPTestClient(t *testing.T, projectID, role, capabilityPath string) (*mcpTestClient, context.CancelFunc, <-chan error) {
	t.Helper()
	serverReader, clientWriter := io.Pipe()
	clientReader, serverWriter := io.Pipe()
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() {
		done <- RunMCPStdio(ctx, serverReader, serverWriter, projectID, role, capabilityPath)
	}()
	client := &mcpTestClient{t: t, input: clientWriter, output: clientReader}
	t.Cleanup(func() {
		cancel()
		_ = client.input.Close()
		_ = client.output.Close()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Error("MCP stdio process did not stop")
		}
	})
	return client, cancel, done
}

func (c *mcpTestClient) request(method string, params any) map[string]json.RawMessage {
	c.t.Helper()
	c.nextID++
	message := map[string]any{
		"jsonrpc": "2.0",
		"id":      c.nextID,
		"method":  method,
	}
	if params != nil {
		message["params"] = params
	}
	if err := json.NewEncoder(c.input).Encode(message); err != nil {
		c.t.Fatal(err)
	}
	return c.response()
}

func (c *mcpTestClient) notify(method string) {
	c.t.Helper()
	if err := json.NewEncoder(c.input).Encode(map[string]any{
		"jsonrpc": "2.0",
		"method":  method,
	}); err != nil {
		c.t.Fatal(err)
	}
}

func (c *mcpTestClient) response() map[string]json.RawMessage {
	c.t.Helper()
	var response map[string]json.RawMessage
	if err := json.NewDecoder(c.output).Decode(&response); err != nil {
		c.t.Fatal(err)
	}
	return response
}

func TestMCPExposesOnlyWorkerToolsAndEnforcesServerScope(t *testing.T) {
	harness := newWorkerHarness(t)
	root, ticket := harness.rootAndChild(t, "mass", "mcp")
	worker := harness.createWorker(t, "mass", root.ID, ticket.ID, "worker-mcp", "", "")
	capability, capabilityPath := workerCapability(t, harness, worker.AssignmentID)

	client, _, _ := newMCPTestClient(t, "mass", "worker", capabilityPath)
	initialize := client.request("initialize", map[string]any{
		"protocolVersion": "2024-11-05",
		"clientInfo":      map[string]string{"name": "test", "version": "1"},
		"capabilities":    map[string]any{},
	})
	if string(initialize["error"]) != "" {
		t.Fatalf("initialize failed: %s", initialize["error"])
	}
	client.notify("notifications/initialized")

	listed := client.request("tools/list", map[string]any{})
	var toolsResult struct {
		Tools []mcpToolDefinition `json:"tools"`
	}
	if err := json.Unmarshal(listed["result"], &toolsResult); err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, tool := range toolsResult.Tools {
		names = append(names, tool.Name)
		if tool.InputSchema["additionalProperties"] != false {
			t.Fatalf("tool %s permits unknown arguments: %+v", tool.Name, tool.InputSchema)
		}
	}
	if strings.Join(names, ",") != "list_workers,get_worker,send_to_worker,get_ticket_context,create_child_ticket,dispatch_child_ticket" {
		t.Fatalf("worker MCP tools = %v", names)
	}
	if required, _ := toolsResult.Tools[0].InputSchema["required"].([]any); len(required) != 0 {
		t.Fatalf("worker list_workers schema required = %+v, want no arguments", required)
	}

	called := client.request("tools/call", map[string]any{
		"name":      "list_workers",
		"arguments": map[string]any{},
	})
	if string(called["error"]) != "" {
		t.Fatalf("tools/call failed: %s", called["error"])
	}
	var toolResult mcpToolResult
	if err := json.Unmarshal(called["result"], &toolResult); err != nil {
		t.Fatal(err)
	}
	var workers ListWorkersResult
	if err := json.Unmarshal(toolResult.StructuredContent, &workers); err != nil {
		t.Fatal(err)
	}
	if len(workers.Workers) != 1 || workers.Workers[0].AssignmentID != worker.AssignmentID {
		t.Fatalf("MCP worker list = %+v", workers)
	}

	forged := client.request("tools/call", map[string]any{
		"name": "get_worker",
		"arguments": map[string]any{
			"name":         "worker-mcp",
			"rootTicketId": root.ID,
		},
	})
	if string(forged["error"]) == "" {
		t.Fatalf("forged worker scope was accepted: %s", forged["result"])
	}
	var forgedError struct {
		Code int64          `json:"code"`
		Data OperationError `json:"data"`
	}
	if err := json.Unmarshal(forged["error"], &forgedError); err != nil {
		t.Fatal(err)
	}
	if forgedError.Code != -32000 || forgedError.Data.Reason != "invalid_tool_arguments" {
		t.Fatalf("forged MCP error = %+v", forgedError)
	}

	routerOnly := client.request("tools/call", map[string]any{
		"name": "create_worker",
		"arguments": map[string]any{
			"rootTicketId": root.ID,
			"ticketId":     ticket.ID,
			"name":         "forged-worker",
		},
	})
	if string(routerOnly["error"]) == "" {
		t.Fatal("worker MCP exposed create_worker")
	}
	var routerError struct {
		Data OperationError `json:"data"`
	}
	if err := json.Unmarshal(routerOnly["error"], &routerError); err != nil {
		t.Fatal(err)
	}
	if routerError.Data.Reason != "tool_not_found" {
		t.Fatalf("router-only tool error = %+v", routerError)
	}

	if capability.Role != "worker" || capability.AssignmentID != worker.AssignmentID {
		t.Fatalf("MCP capability scope = %+v", capability)
	}
}

func TestMCPRouterExposesExactlyRouterTools(t *testing.T) {
	harness := newWorkerHarness(t)
	root, ticket := harness.rootAndChild(t, "mass", "mcp-router")
	worker := harness.createWorker(t, "mass", root.ID, ticket.ID, "worker-mcp-router", "", "")

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

	listed := client.request("tools/list", map[string]any{})
	var toolsResult struct {
		Tools []mcpToolDefinition `json:"tools"`
	}
	if err := json.Unmarshal(listed["result"], &toolsResult); err != nil {
		t.Fatal(err)
	}
	names := make([]string, 0, len(toolsResult.Tools))
	for _, tool := range toolsResult.Tools {
		names = append(names, tool.Name)
	}
	want := "create_ticket,create_worker,list_workers,get_worker,send_to_worker,bind_route,finish_ticket,stop_worker,get_ticket_context,reply_trigger,subscribe_output,decide_trigger,associate_thread"
	if got := strings.Join(names, ","); got != want {
		t.Fatalf("router MCP tools = %q, want %q", got, want)
	}
	var listSchema map[string]any
	for _, tool := range toolsResult.Tools {
		if tool.Name == "list_workers" {
			listSchema = tool.InputSchema
			break
		}
	}
	properties, _ := listSchema["properties"].(map[string]any)
	required, _ := listSchema["required"].([]any)
	if len(properties) != 1 || len(required) != 1 || required[0] != "rootTicketId" {
		t.Fatalf("router list_workers schema = %+v, want required rootTicketId", listSchema)
	}

	listedWorkers := client.request("tools/call", map[string]any{
		"name":      "list_workers",
		"arguments": map[string]any{"rootTicketId": root.ID},
		"_meta": map[string]any{
			"claudecode/toolUseId": "call-router-list",
			"progressToken":        2,
		},
	})
	if string(listedWorkers["error"]) != "" {
		t.Fatalf("router list_workers failed: %s", listedWorkers["error"])
	}
	var listResult mcpToolResult
	if err := json.Unmarshal(listedWorkers["result"], &listResult); err != nil {
		t.Fatal(err)
	}
	var workers ListWorkersResult
	if err := json.Unmarshal(listResult.StructuredContent, &workers); err != nil {
		t.Fatal(err)
	}
	if len(workers.Workers) != 1 || workers.Workers[0].Name != worker.Name {
		t.Fatalf("router list_workers result = %+v", workers)
	}

	called := client.request("tools/call", map[string]any{
		"name": "get_worker",
		"arguments": map[string]any{
			"rootTicketId": root.ID,
			"name":         worker.Name,
		},
	})
	if string(called["error"]) != "" {
		t.Fatalf("router get_worker failed: %s", called["error"])
	}
	var toolResult mcpToolResult
	if err := json.Unmarshal(called["result"], &toolResult); err != nil {
		t.Fatal(err)
	}
	var got WorkerView
	if err := json.Unmarshal(toolResult.StructuredContent, &got); err != nil {
		t.Fatal(err)
	}
	if got.AssignmentID != worker.AssignmentID {
		t.Fatalf("router MCP get_worker = %+v", got)
	}
}
