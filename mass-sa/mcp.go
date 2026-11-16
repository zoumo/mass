package sa

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	rpc2 "github.com/sourcegraph/jsonrpc2"
)

const (
	mcpDefaultProtocolVersion = "2024-11-05"
	mcpClientTimeout          = 30 * time.Second
)

var mcpSupportedProtocolVersions = map[string]struct{}{
	"2024-11-05": {},
	"2025-03-26": {},
	"2025-06-18": {},
	"2025-11-25": {},
}

type mcpToolDefinition struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"inputSchema"`
}

type mcpToolResult struct {
	Content           []mcpTextContent `json:"content"`
	StructuredContent json.RawMessage  `json:"structuredContent,omitempty"`
	IsError           bool             `json:"isError,omitempty"`
}

type mcpTextContent struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type mcpServer struct {
	role      string
	endpoint  string
	token     string
	http      *http.Client
	allTools  []mcpToolDefinition
	toolIndex map[string]struct{}
}

type mcpToolCallParams struct {
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments,omitempty"`
	Meta      json.RawMessage `json:"_meta,omitempty"`
}

type toolHTTPResponse struct {
	Result json.RawMessage `json:"result"`
	Error  *OperationError `json:"error"`
}

type mcpHandler struct {
	server *mcpServer
}

// MCPToolNames returns the exact stable tool set for a capability role.
func MCPToolNames(role string) []string {
	definitions := mcpTools(role)
	names := make([]string, 0, len(definitions))
	for _, definition := range definitions {
		names = append(names, definition.Name)
	}
	return names
}

// RunMCPStdio serves the narrow MCP tool surface over newline-delimited
// JSON-RPC 2.0. Project and role flags must match the 0600 capability file;
// authorization still happens on every HTTP tool call.
func RunMCPStdio(ctx context.Context, input io.Reader, output io.Writer, projectID, role, capabilityPath string) error {
	capability, err := readCapabilityFile(capabilityPath)
	if err != nil {
		return err
	}
	if capability.ProjectID != projectID || capability.Role != role {
		return operationError("failed_precondition", "capability_scope_mismatch", "capability file does not match the MCP process role and project")
	}
	server := newMCPServer(role, capability.Endpoint, capability.Token)
	stream := rpc2.NewPlainObjectStream(readWriteCloser{Reader: input, Writer: output})
	conn := rpc2.NewConn(ctx, stream, mcpHandler{server: server}, rpc2.SetLogger(rpcLogger{}))
	<-conn.DisconnectNotify()
	return nil
}

func newMCPServer(role, endpoint, token string) *mcpServer {
	definitions := mcpTools(role)
	index := make(map[string]struct{}, len(definitions))
	for _, definition := range definitions {
		index[definition.Name] = struct{}{}
	}
	return &mcpServer{
		role:      role,
		endpoint:  strings.TrimRight(endpoint, "/"),
		token:     token,
		http:      &http.Client{Timeout: mcpClientTimeout},
		allTools:  definitions,
		toolIndex: index,
	}
}

func (h mcpHandler) Handle(ctx context.Context, conn *rpc2.Conn, request *rpc2.Request) {
	result, err := h.server.dispatch(ctx, request)
	if request.Notif {
		return
	}
	if err != nil {
		_ = conn.ReplyWithError(ctx, request.ID, mcpRPCError(err))
		return
	}
	_ = conn.Reply(ctx, request.ID, result)
}

func (s *mcpServer) dispatch(ctx context.Context, request *rpc2.Request) (any, error) {
	switch request.Method {
	case "initialize":
		var params struct {
			ProtocolVersion string          `json:"protocolVersion"`
			Capabilities    json.RawMessage `json:"capabilities,omitempty"`
			ClientInfo      json.RawMessage `json:"clientInfo,omitempty"`
		}
		if request.Params != nil {
			if err := json.Unmarshal(*request.Params, &params); err != nil || params.ProtocolVersion == "" {
				return nil, rpc2InvalidParams()
			}
		} else {
			return nil, rpc2InvalidParams()
		}
		protocolVersion := params.ProtocolVersion
		if _, supported := mcpSupportedProtocolVersions[protocolVersion]; !supported {
			protocolVersion = mcpDefaultProtocolVersion
		}
		return map[string]any{
			"protocolVersion": protocolVersion,
			"capabilities": map[string]any{
				"tools": map[string]any{"listChanged": false},
			},
			"serverInfo": map[string]string{"name": "mass-sa", "version": "0.1.0-demo"},
		}, nil
	case "notifications/initialized":
		return nil, nil
	case "ping":
		return map[string]any{}, nil
	case "tools/list":
		var params struct {
			Cursor string `json:"cursor,omitempty"`
		}
		if request.Params != nil {
			decoder := json.NewDecoder(bytes.NewReader(*request.Params))
			decoder.DisallowUnknownFields()
			if err := decoder.Decode(&params); err != nil {
				return nil, rpc2InvalidParams()
			}
		}
		if params.Cursor != "" {
			return nil, rpc2InvalidParams()
		}
		return map[string]any{"tools": s.allTools}, nil
	case "tools/call":
		if request.Params == nil {
			return nil, rpc2InvalidParams()
		}
		var params mcpToolCallParams
		decoder := json.NewDecoder(bytes.NewReader(*request.Params))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&params); err != nil || params.Name == "" {
			return nil, rpc2InvalidParams()
		}
		if _, exists := s.toolIndex[params.Name]; !exists {
			return nil, toolNotFound()
		}
		if len(bytes.TrimSpace(params.Arguments)) == 0 {
			params.Arguments = json.RawMessage(`{}`)
		}
		raw, err := s.call(ctx, params.Name, params.Arguments)
		if err != nil {
			return nil, err
		}
		return mcpToolResult{
			Content: []mcpTextContent{{
				Type: "text",
				Text: string(raw),
			}},
			StructuredContent: raw,
		}, nil
	default:
		return nil, &rpc2.Error{Code: rpc2.CodeMethodNotFound, Message: "Method not found"}
	}
}

func (s *mcpServer) call(ctx context.Context, tool string, arguments json.RawMessage) (json.RawMessage, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, s.endpoint+"/"+url.PathEscape(tool), bytes.NewReader(arguments))
	if err != nil {
		return nil, operationError("invalid_argument", "invalid_tool_endpoint", "tool endpoint cannot be used")
	}
	request.Header.Set("Authorization", "Bearer "+s.token)
	request.Header.Set("Content-Type", "application/json")
	response, err := s.http.Do(request)
	if err != nil {
		return nil, operationError("unavailable", "tool_unavailable", "tool HTTP endpoint is unavailable")
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(response.Body, maxToolHTTPBody+1))
	if err != nil || len(raw) > maxToolHTTPBody {
		return nil, operationError("unavailable", "tool_response_invalid", "tool HTTP response is unavailable or too large")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var envelope toolHTTPResponse
	if err := decoder.Decode(&envelope); err != nil {
		return nil, operationError("unavailable", "tool_response_invalid", "tool HTTP response is invalid")
	}
	if envelope.Error != nil {
		return nil, envelope.Error
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 || len(envelope.Result) == 0 {
		return nil, operationError("unavailable", "tool_response_invalid", "tool HTTP request did not return a result")
	}
	return envelope.Result, nil
}

func mcpRPCError(err error) *rpc2.Error {
	var rpcErr *rpc2.Error
	if errors.As(err, &rpcErr) {
		return rpcErr
	}
	operation := OperationError{
		Code:    "unavailable",
		Reason:  "tool_failure",
		Message: "tool operation failed",
	}
	var pointer *OperationError
	if errors.As(err, &pointer) {
		operation = *pointer
	}
	errorValue := &rpc2.Error{
		Code:    -32000,
		Message: operation.Message,
	}
	errorValue.SetError(operation)
	return errorValue
}

func rpc2InvalidParams() *rpc2.Error {
	return &rpc2.Error{Code: rpc2.CodeInvalidParams, Message: "Invalid params"}
}

func mcpTools(role string) []mcpToolDefinition {
	contextTool := mcpToolDefinition{Name: "get_ticket_context", Description: "Read an authorized Ticket objective, progress, parent/child results, Thread references, and Assignment/Session history references. No transcripts are broadcast. Worker scope is its root Ticket; omit ticketId for the caller's own Ticket.", InputSchema: closedObjectSchema(map[string]any{"ticketId": stringSchema("Ticket ID")}, nil)}
	workerTools := []mcpToolDefinition{
		{
			Name:        "list_workers",
			Description: "List workers in the caller's current root Ticket scope.",
			InputSchema: closedObjectSchema(nil, nil),
		},
		{
			Name:        "get_worker",
			Description: "Get one worker in the caller's current root Ticket scope.",
			InputSchema: closedObjectSchema(
				map[string]any{"name": stringSchema("Worker Session name")},
				[]string{"name"},
			),
		},
		{
			Name:        "send_to_worker",
			Description: "Persist a message for one worker in the caller's current root Ticket scope. Queueing is not delivery.",
			InputSchema: closedObjectSchema(
				map[string]any{
					"name":    stringSchema("Target worker Session name"),
					"message": stringSchema("Message to queue"),
				},
				[]string{"name", "message"},
			),
		},
	}
	if role != "router" {
		return append(workerTools, contextTool,
			mcpToolDefinition{Name: "create_child_ticket", Description: "Optionally delegate independent work within the current Ticket authorization. Parent, root, namespace and creator come from the current authenticated Assignment. requestId is stable; conflicts and unconfirmed creation cannot be retried. Exact resource/tool/execution subsets and absolute depth/root resource limits apply. Omit workerName to queue. No external Trigger or inherited output destination is created. Native Agent subagents stay native and create no platform work.", InputSchema: closedObjectSchema(map[string]any{"requestId": stringSchema("Stable creation identity within this parent Ticket"), "title": stringSchema("Child title"), "objective": stringSchema("Independent objective within authorized resources"), "acceptance": stringSchema("Independent acceptance criteria"), "authorization": authorizationSchema(), "execution": closedObjectSchema(map[string]any{"runtimeRef": stringSchema("Exact configured Runtime"), "model": stringSchema("Exact model; empty only for an omitted default"), "effort": stringSchema("Exact effort")}, []string{"runtimeRef", "effort"}), "workerName": stringSchema("Optional worker name for immediate first dispatch")}, []string{"requestId", "title", "objective", "acceptance", "authorization", "execution"})},
			mcpToolDefinition{Name: "dispatch_child_ticket", Description: "First-dispatch or continue a direct child of the authenticated current Ticket, using its stored authorized execution configuration. Cannot select another parent, root or Project. Unknown prior execution is never replayed.", InputSchema: closedObjectSchema(map[string]any{"ticketId": stringSchema("Direct child Ticket ID"), "name": stringSchema("Worker name")}, []string{"ticketId", "name"})},
		)
	}
	routerTools := []mcpToolDefinition{
		{
			Name:        "create_ticket",
			Description: "Persist a local root Ticket without dispatching a Session.",
			InputSchema: closedObjectSchema(
				map[string]any{
					"title":         stringSchema("Ticket title"),
					"objective":     stringSchema("Ticket objective"),
					"acceptance":    stringSchema("Ticket acceptance criteria"),
					"authorization": authorizationSchema(),
				},
				[]string{"title", "objective", "acceptance"},
			),
		},
		{
			Name:        "create_worker",
			Description: "Dispatch or continue a Ticket worker. Offline recovery and action=retry create a new Assignment on the same Session. action=replace requires a new name and explicitly replaces the Session; unresolved prior executions block recovery.",
			InputSchema: closedObjectSchema(
				map[string]any{
					"ticketId":   stringSchema("Ticket ID"),
					"name":       stringSchema("Worker Session name"),
					"model":      stringSchema("Optional worker model override"),
					"effort":     stringSchema("Optional worker effort override"),
					"action":     stringSchema("Omit to continue; retry to renew the Assignment on the same Session; replace to create a replacement Session"),
					"runtimeRef": stringSchema("Optional project Runtime or one listed in worker.runtimeRefs; changing Runtime requires action=replace"),
				},
				[]string{"ticketId", "name"},
			),
		},
		{
			Name:        "list_workers",
			Description: "List workers in one root Ticket of this project.",
			InputSchema: closedObjectSchema(
				map[string]any{"rootTicketId": stringSchema("Root Ticket ID")},
				[]string{"rootTicketId"},
			),
		},
		{
			Name:        "get_worker",
			Description: "Get one worker in a root of this project.",
			InputSchema: closedObjectSchema(
				map[string]any{
					"rootTicketId": stringSchema("Root Ticket ID"),
					"name":         stringSchema("Worker Session name"),
				},
				[]string{"rootTicketId", "name"},
			),
		},
		{
			Name:        "send_to_worker",
			Description: "Persist a message for one worker in a root of this project. Queueing is not delivery.",
			InputSchema: closedObjectSchema(
				map[string]any{
					"rootTicketId": stringSchema("Root Ticket ID"),
					"name":         stringSchema("Target worker Session name"),
					"message":      stringSchema("Message to queue"),
				},
				[]string{"rootTicketId", "name", "message"},
			),
		},
		{
			Name:        "bind_route",
			Description: "Bind an event route key to a worker's current Assignment.",
			InputSchema: closedObjectSchema(
				map[string]any{
					"rootTicketId": stringSchema("Root Ticket ID"),
					"routeKey":     stringSchema("Stable event route key"),
					"workerName":   stringSchema("Worker Session name"),
					"assignmentId": stringSchema("Current Assignment ID"),
				},
				[]string{"rootTicketId", "routeKey", "workerName", "assignmentId"},
			),
		},
		{
			Name:        "finish_ticket",
			Description: "Atomically persist a business state, its fact and parent lifecycle input. completed requires a confirmed current Assignment report. Before any terminal outcome, explicitly resolve or cancel unfinished children, descendants first. blocked/waiting_user are nonterminal; reopened does not resume execution.",
			InputSchema: closedObjectSchema(
				map[string]any{
					"ticketId":   stringSchema("Ticket ID"),
					"resolution": stringSchema("completed, failed, cancelled, blocked, waiting_user, or reopened"),
					"summary":    stringSchema("Acceptance or rejection summary"),
				},
				[]string{"ticketId", "resolution", "summary"},
			),
		},
		{
			Name:        "stop_worker",
			Description: "Close idle worker runtime resources and revoke its capability, preserving Ticket, Session, history and route references. Dispatch again to recover the same Session with a new Assignment.",
			InputSchema: closedObjectSchema(
				map[string]any{
					"rootTicketId": stringSchema("Root Ticket ID"),
					"name":         stringSchema("Worker Session name"),
				},
				[]string{"rootTicketId", "name"},
			),
		},
	}
	return append(routerTools, contextTool,
		mcpToolDefinition{Name: "reply_trigger", Description: "Publish an explicit external reply to the Trigger source Thread. requestId is stable for retries; changed content is rejected. accepted requires a confirmed Ticket and generates acknowledgement text without claiming execution. clarification and no_execution require text. summary requires ticketId, factId and text. Ordinary final and transcripts are never published.", InputSchema: closedObjectSchema(map[string]any{"triggerId": stringSchema("Trigger ID"), "requestId": stringSchema("Stable reply request identity"), "kind": stringSchema("accepted, clarification, no_execution, or summary"), "ticketId": stringSchema("Ticket associated with this Trigger"), "factId": stringSchema("Confirmed business fact output ID"), "text": stringSchema("Explicit external text; omit for accepted")}, []string{"triggerId", "requestId", "kind"})},
		mcpToolDefinition{Name: "subscribe_output", Description: "Explicitly subscribe a Ticket-associated Trigger source Thread to future business facts. Thread associations alone do not subscribe. No inherited child subscriptions.", InputSchema: closedObjectSchema(map[string]any{"triggerId": stringSchema("Associated Trigger authorizing this destination"), "ticketId": stringSchema("Ticket ID")}, []string{"triggerId", "ticketId"})},
		mcpToolDefinition{Name: "decide_trigger", Description: "Ignore a Trigger, associate it with existing work, or create a new root Ticket. Creation is durable and fenced against replay; unknown creation requires reconciliation. Short replies are a separate capability.", InputSchema: closedObjectSchema(map[string]any{"triggerId": stringSchema("Trigger ID"), "action": stringSchema("ignore, associate, or create"), "ticketId": stringSchema("Existing Ticket for associate"), "title": stringSchema("New Ticket title"), "objective": stringSchema("New Ticket objective"), "acceptance": stringSchema("New Ticket acceptance")}, []string{"triggerId", "action"})},
		mcpToolDefinition{Name: "associate_thread", Description: "Associate a source Thread with a Ticket. Many-to-many links persist across execution changes. Ambiguous Threads go to Router without fan-out.", InputSchema: closedObjectSchema(map[string]any{"source": stringSchema("Source connector/account identity"), "threadId": stringSchema("Source-local Thread identity"), "ticketId": stringSchema("Ticket ID")}, []string{"source", "threadId", "ticketId"})},
	)
}

func closedObjectSchema(properties map[string]any, required []string) map[string]any {
	if properties == nil {
		properties = map[string]any{}
	}
	schema := map[string]any{
		"type":                 "object",
		"properties":           properties,
		"additionalProperties": false,
	}
	if len(required) > 0 {
		schema["required"] = required
	}
	return schema
}

func stringSchema(description string) map[string]any {
	return map[string]any{"type": "string", "description": description}
}

func authorizationSchema() map[string]any {
	properties := map[string]any{}
	for _, field := range []string{"resources", "tools", "runtimeRefs", "models", "efforts"} {
		properties[field] = map[string]any{"type": "array", "items": map[string]any{"type": "string"}}
	}
	properties["maxDepth"] = map[string]any{"type": "integer", "minimum": 0, "maximum": 8, "description": "Absolute depth from root, which has depth zero"}
	properties["maxTickets"] = map[string]any{"type": "integer", "minimum": 1, "maximum": 64, "description": "Root-wide budget including root and uncertain creation reservations"}
	return closedObjectSchema(properties, []string{"resources", "tools", "runtimeRefs", "models", "efforts", "maxDepth", "maxTickets"})
}

type readWriteCloser struct {
	io.Reader
	io.Writer
}

func (readWriteCloser) Close() error {
	return nil
}
