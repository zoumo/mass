// Command room-mcp-server is a minimal MCP server over stdio that exposes
// room_send and room_status tools. It reads JSON-RPC 2.0 from stdin and
// writes responses to stdout. Outbound ARI calls are made to agentd via a
// Unix domain socket specified by OAR_AGENTD_SOCKET.
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"os"
	"strings"

	"github.com/sourcegraph/jsonrpc2"
)

// ────────────────────────────────────────────────────────────────────────────
// MCP JSON-RPC types (minimal subset)
// ────────────────────────────────────────────────────────────────────────────

// mcpRequest is a JSON-RPC 2.0 request/notification coming from the MCP client.
type mcpRequest struct {
	JSONRPC string           `json:"jsonrpc"`
	ID      *json.RawMessage `json:"id,omitempty"` // nil for notifications
	Method  string           `json:"method"`
	Params  json.RawMessage  `json:"params,omitempty"`
}

// mcpResponse is a JSON-RPC 2.0 response sent back to the MCP client.
type mcpResponse struct {
	JSONRPC string      `json:"jsonrpc"`
	ID      interface{} `json:"id"`
	Result  interface{} `json:"result,omitempty"`
	Error   *mcpError   `json:"error,omitempty"`
}

// mcpError is the error object in a JSON-RPC 2.0 error response.
type mcpError struct {
	Code    int         `json:"code"`
	Message string      `json:"message"`
	Data    interface{} `json:"data,omitempty"`
}

// ────────────────────────────────────────────────────────────────────────────
// MCP protocol payloads
// ────────────────────────────────────────────────────────────────────────────

type mcpInitializeResult struct {
	ProtocolVersion string              `json:"protocolVersion"`
	Capabilities    mcpCapabilities     `json:"capabilities"`
	ServerInfo      mcpServerInfo       `json:"serverInfo"`
}

type mcpCapabilities struct {
	Tools *mcpToolsCapability `json:"tools,omitempty"`
}

type mcpToolsCapability struct{}

type mcpServerInfo struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

type mcpTool struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"inputSchema"`
}

type mcpToolsListResult struct {
	Tools []mcpTool `json:"tools"`
}

type mcpToolCallParams struct {
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}

type mcpTextContent struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type mcpToolResult struct {
	Content []mcpTextContent `json:"content"`
	IsError bool             `json:"isError,omitempty"`
}

// ────────────────────────────────────────────────────────────────────────────
// ARI types (matching pkg/ari/types.go)
// ────────────────────────────────────────────────────────────────────────────

type ariRoomSendParams struct {
	Room        string `json:"room"`
	TargetAgent string `json:"targetAgent"`
	Message     string `json:"message"`
	SenderAgent string `json:"senderAgent,omitempty"`
	SenderId    string `json:"senderId,omitempty"`
}

type ariRoomSendResult struct {
	Delivered  bool   `json:"delivered"`
	StopReason string `json:"stopReason,omitempty"`
}

type ariRoomStatusParams struct {
	Name string `json:"name"`
}

type ariRoomStatusResult struct {
	Name              string          `json:"name"`
	CommunicationMode string         `json:"communicationMode"`
	Members           []ariRoomMember `json:"members"`
}

type ariRoomMember struct {
	AgentName string `json:"agentName"`
	SessionId string `json:"sessionId"`
	State     string `json:"state"`
}

// ────────────────────────────────────────────────────────────────────────────
// Environment configuration
// ────────────────────────────────────────────────────────────────────────────

type config struct {
	agentdSocket string
	roomName     string
	sessionID    string
	roomAgent    string
}

func loadConfig() (config, error) {
	c := config{
		agentdSocket: os.Getenv("OAR_AGENTD_SOCKET"),
		roomName:     os.Getenv("OAR_ROOM_NAME"),
		sessionID:    os.Getenv("OAR_SESSION_ID"),
		roomAgent:    os.Getenv("OAR_ROOM_AGENT"),
	}
	if c.agentdSocket == "" {
		return c, fmt.Errorf("OAR_AGENTD_SOCKET is required")
	}
	if c.roomName == "" {
		return c, fmt.Errorf("OAR_ROOM_NAME is required")
	}
	if c.sessionID == "" {
		return c, fmt.Errorf("OAR_SESSION_ID is required")
	}
	// OAR_ROOM_AGENT may be empty (per T01 decision)
	return c, nil
}

// ────────────────────────────────────────────────────────────────────────────
// ARI client helper
// ────────────────────────────────────────────────────────────────────────────

// nullHandler is a no-op JSON-RPC handler for the outbound ARI connection.
type nullHandler struct{}

func (nullHandler) Handle(context.Context, *jsonrpc2.Conn, *jsonrpc2.Request) {}

// callARI dials the agentd Unix socket, makes a single JSON-RPC call, and
// closes the connection. Short-lived connections are fine for tool invocations.
func callARI(ctx context.Context, socketPath, method string, params, result interface{}) error {
	nc, err := net.Dial("unix", socketPath)
	if err != nil {
		return fmt.Errorf("dial agentd: %w", err)
	}
	defer nc.Close()

	stream := jsonrpc2.NewPlainObjectStream(nc)
	conn := jsonrpc2.NewConn(ctx, stream, jsonrpc2.AsyncHandler(nullHandler{}))
	defer conn.Close()

	return conn.Call(ctx, method, params, result)
}

// ────────────────────────────────────────────────────────────────────────────
// Tool definitions
// ────────────────────────────────────────────────────────────────────────────

var roomSendSchema = json.RawMessage(`{
  "type": "object",
  "properties": {
    "targetAgent": {
      "type": "string",
      "description": "Name of the agent to send the message to"
    },
    "message": {
      "type": "string",
      "description": "The message text to send"
    }
  },
  "required": ["targetAgent", "message"]
}`)

var roomStatusSchema = json.RawMessage(`{
  "type": "object",
  "properties": {},
  "required": []
}`)

var toolsList = []mcpTool{
	{
		Name:        "room_send",
		Description: "Send a message to another agent in the current room",
		InputSchema: roomSendSchema,
	},
	{
		Name:        "room_status",
		Description: "Get the current room membership and status",
		InputSchema: roomStatusSchema,
	},
}

// ────────────────────────────────────────────────────────────────────────────
// MCP request handler
// ────────────────────────────────────────────────────────────────────────────

// handleRequest processes a single MCP JSON-RPC request and returns a
// response (or nil for notifications that need no response).
func handleRequest(ctx context.Context, cfg config, req *mcpRequest) *mcpResponse {
	switch req.Method {

	// ── MCP lifecycle ──────────────────────────────────────────────────
	case "initialize":
		return &mcpResponse{
			JSONRPC: "2.0",
			ID:      rawID(req.ID),
			Result: mcpInitializeResult{
				ProtocolVersion: "2024-11-05",
				Capabilities:    mcpCapabilities{Tools: &mcpToolsCapability{}},
				ServerInfo:      mcpServerInfo{Name: "room-mcp-server", Version: "0.1.0"},
			},
		}

	case "notifications/initialized":
		// No-op notification; no response required.
		return nil

	// ── Tool discovery ─────────────────────────────────────────────────
	case "tools/list":
		return &mcpResponse{
			JSONRPC: "2.0",
			ID:      rawID(req.ID),
			Result:  mcpToolsListResult{Tools: toolsList},
		}

	// ── Tool execution ─────────────────────────────────────────────────
	case "tools/call":
		return handleToolCall(ctx, cfg, req)

	default:
		// Unknown method — return method not found error.
		return &mcpResponse{
			JSONRPC: "2.0",
			ID:      rawID(req.ID),
			Error: &mcpError{
				Code:    -32601,
				Message: fmt.Sprintf("unknown method %q", req.Method),
			},
		}
	}
}

// handleToolCall dispatches tools/call to the appropriate tool handler.
func handleToolCall(ctx context.Context, cfg config, req *mcpRequest) *mcpResponse {
	var params mcpToolCallParams
	if err := json.Unmarshal(req.Params, &params); err != nil {
		return &mcpResponse{
			JSONRPC: "2.0",
			ID:      rawID(req.ID),
			Error: &mcpError{
				Code:    -32602,
				Message: fmt.Sprintf("invalid params: %v", err),
			},
		}
	}

	switch params.Name {
	case "room_send":
		return handleRoomSend(ctx, cfg, req.ID, params.Arguments)
	case "room_status":
		return handleRoomStatus(ctx, cfg, req.ID)
	default:
		return &mcpResponse{
			JSONRPC: "2.0",
			ID:      rawID(req.ID),
			Result: mcpToolResult{
				Content: []mcpTextContent{{Type: "text", Text: fmt.Sprintf("unknown tool %q", params.Name)}},
				IsError: true,
			},
		}
	}
}

// handleRoomSend calls the agentd room/send ARI method and returns the result.
func handleRoomSend(ctx context.Context, cfg config, id *json.RawMessage, args json.RawMessage) *mcpResponse {
	var input struct {
		TargetAgent string `json:"targetAgent"`
		Message     string `json:"message"`
	}
	if err := json.Unmarshal(args, &input); err != nil {
		return &mcpResponse{
			JSONRPC: "2.0",
			ID:      rawID(id),
			Result: mcpToolResult{
				Content: []mcpTextContent{{Type: "text", Text: fmt.Sprintf("invalid arguments: %v", err)}},
				IsError: true,
			},
		}
	}
	if input.TargetAgent == "" || input.Message == "" {
		return &mcpResponse{
			JSONRPC: "2.0",
			ID:      rawID(id),
			Result: mcpToolResult{
				Content: []mcpTextContent{{Type: "text", Text: "targetAgent and message are required"}},
				IsError: true,
			},
		}
	}

	ariParams := ariRoomSendParams{
		Room:        cfg.roomName,
		TargetAgent: input.TargetAgent,
		Message:     input.Message,
		SenderAgent: cfg.roomAgent,
		SenderId:    cfg.sessionID,
	}

	var result ariRoomSendResult
	if err := callARI(ctx, cfg.agentdSocket, "room/send", ariParams, &result); err != nil {
		return &mcpResponse{
			JSONRPC: "2.0",
			ID:      rawID(id),
			Result: mcpToolResult{
				Content: []mcpTextContent{{Type: "text", Text: fmt.Sprintf("room/send failed: %v", err)}},
				IsError: true,
			},
		}
	}

	text := fmt.Sprintf("Message delivered to %s (stopReason: %s)", input.TargetAgent, result.StopReason)
	if !result.Delivered {
		text = fmt.Sprintf("Message delivery failed to %s", input.TargetAgent)
	}

	return &mcpResponse{
		JSONRPC: "2.0",
		ID:      rawID(id),
		Result: mcpToolResult{
			Content: []mcpTextContent{{Type: "text", Text: text}},
			IsError: !result.Delivered,
		},
	}
}

// handleRoomStatus calls the agentd room/status ARI method and formats the result.
func handleRoomStatus(ctx context.Context, cfg config, id *json.RawMessage) *mcpResponse {
	ariParams := ariRoomStatusParams{Name: cfg.roomName}
	var result ariRoomStatusResult
	if err := callARI(ctx, cfg.agentdSocket, "room/status", ariParams, &result); err != nil {
		return &mcpResponse{
			JSONRPC: "2.0",
			ID:      rawID(id),
			Result: mcpToolResult{
				Content: []mcpTextContent{{Type: "text", Text: fmt.Sprintf("room/status failed: %v", err)}},
				IsError: true,
			},
		}
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("Room: %s (mode: %s)\n", result.Name, result.CommunicationMode))
	sb.WriteString(fmt.Sprintf("Members (%d):\n", len(result.Members)))
	for _, m := range result.Members {
		sb.WriteString(fmt.Sprintf("  - %s (session: %s, state: %s)\n", m.AgentName, m.SessionId, m.State))
	}

	return &mcpResponse{
		JSONRPC: "2.0",
		ID:      rawID(id),
		Result: mcpToolResult{
			Content: []mcpTextContent{{Type: "text", Text: sb.String()}},
		},
	}
}

// ────────────────────────────────────────────────────────────────────────────
// Helpers
// ────────────────────────────────────────────────────────────────────────────

// rawID extracts the ID value from a raw JSON message for use in responses.
// Returns nil for notifications (no ID).
func rawID(raw *json.RawMessage) interface{} {
	if raw == nil {
		return nil
	}
	// Try to unmarshal as int first, then string, fallback to raw.
	var n int64
	if err := json.Unmarshal(*raw, &n); err == nil {
		return n
	}
	var s string
	if err := json.Unmarshal(*raw, &s); err == nil {
		return s
	}
	// Fallback: return raw bytes as json.RawMessage for re-encoding.
	return raw
}

// ────────────────────────────────────────────────────────────────────────────
// Main loop
// ────────────────────────────────────────────────────────────────────────────

func main() {
	// Redirect log output to stderr so it doesn't corrupt the MCP stdout stream.
	log.SetOutput(os.Stderr)
	log.SetPrefix("room-mcp-server: ")

	cfg, err := loadConfig()
	if err != nil {
		log.Fatalf("configuration error: %v", err)
	}

	log.Printf("starting (room=%s, agent=%s, session=%s)", cfg.roomName, cfg.roomAgent, cfg.sessionID)

	ctx := context.Background()
	scanner := bufio.NewScanner(os.Stdin)
	// Increase scanner buffer for large JSON-RPC messages.
	scanner.Buffer(make([]byte, 0, 1024*1024), 1024*1024)
	encoder := json.NewEncoder(os.Stdout)

	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}

		var req mcpRequest
		if err := json.Unmarshal(line, &req); err != nil {
			log.Printf("invalid JSON-RPC message: %v", err)
			// Write a parse error response.
			resp := mcpResponse{
				JSONRPC: "2.0",
				ID:      nil,
				Error:   &mcpError{Code: -32700, Message: "parse error"},
			}
			_ = encoder.Encode(resp)
			continue
		}

		resp := handleRequest(ctx, cfg, &req)
		if resp == nil {
			// Notification — no response needed.
			continue
		}

		if err := encoder.Encode(resp); err != nil {
			log.Printf("failed to write response: %v", err)
		}
	}

	if err := scanner.Err(); err != nil {
		log.Fatalf("stdin read error: %v", err)
	}
}
