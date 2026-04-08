// Command room-mcp-server is an MCP server over stdio that exposes room_send
// and room_status tools. It uses the modelcontextprotocol/go-sdk for the MCP
// protocol layer. Outbound ARI calls are made to agentd via a Unix domain
// socket specified by OAR_AGENTD_SOCKET.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"os"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/sourcegraph/jsonrpc2"
)

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
	CommunicationMode string          `json:"communicationMode"`
	Members           []ariRoomMember `json:"members"`
}

type ariRoomMember struct {
	AgentName    string `json:"agentName"`
	RuntimeClass string `json:"runtimeClass"`
	AgentState   string `json:"agentState"`
}

// ────────────────────────────────────────────────────────────────────────────
// Environment configuration
// ────────────────────────────────────────────────────────────────────────────

type config struct {
	agentdSocket string // OAR_AGENTD_SOCKET
	roomName     string // OAR_ROOM_NAME
	agentID      string // OAR_AGENT_ID (was sessionID/OAR_SESSION_ID)
	agentName    string // OAR_AGENT_NAME (was roomAgent/OAR_ROOM_AGENT)
}

func loadConfig() (config, error) {
	c := config{
		agentdSocket: os.Getenv("OAR_AGENTD_SOCKET"),
		roomName:     os.Getenv("OAR_ROOM_NAME"),
		agentID:      os.Getenv("OAR_AGENT_ID"),
		agentName:    os.Getenv("OAR_AGENT_NAME"),
	}
	if c.agentdSocket == "" {
		return c, fmt.Errorf("OAR_AGENTD_SOCKET is required")
	}
	if c.roomName == "" {
		return c, fmt.Errorf("OAR_ROOM_NAME is required")
	}
	if c.agentID == "" {
		return c, fmt.Errorf("OAR_AGENT_ID is required")
	}
	// OAR_AGENT_NAME may be empty (agent name within room is optional)
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

// ────────────────────────────────────────────────────────────────────────────
// Tool handlers (SDK ToolHandler signature)
// ────────────────────────────────────────────────────────────────────────────

// roomSendHandler returns an mcp.ToolHandler for the room_send tool.
func roomSendHandler(cfg config) mcp.ToolHandler {
	return func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		var input struct {
			TargetAgent string `json:"targetAgent"`
			Message     string `json:"message"`
		}
		if err := json.Unmarshal(req.Params.Arguments, &input); err != nil {
			return &mcp.CallToolResult{
				Content: []mcp.Content{&mcp.TextContent{Text: fmt.Sprintf("invalid arguments: %v", err)}},
				IsError: true,
			}, nil
		}
		if input.TargetAgent == "" || input.Message == "" {
			return &mcp.CallToolResult{
				Content: []mcp.Content{&mcp.TextContent{Text: "targetAgent and message are required"}},
				IsError: true,
			}, nil
		}

		log.Printf("room_send: target=%s", input.TargetAgent)

		ariParams := ariRoomSendParams{
			Room:        cfg.roomName,
			TargetAgent: input.TargetAgent,
			Message:     input.Message,
			SenderAgent: cfg.agentName,
			SenderId:    cfg.agentID,
		}

		var result ariRoomSendResult
		if err := callARI(ctx, cfg.agentdSocket, "room/send", ariParams, &result); err != nil {
			return &mcp.CallToolResult{
				Content: []mcp.Content{&mcp.TextContent{Text: fmt.Sprintf("room/send failed: %v", err)}},
				IsError: true,
			}, nil
		}

		var text string
		if result.Delivered {
			text = fmt.Sprintf("Message delivered to %s (stopReason: %s)", input.TargetAgent, result.StopReason)
		} else {
			text = fmt.Sprintf("Message delivery failed to %s", input.TargetAgent)
		}

		return &mcp.CallToolResult{
			Content: []mcp.Content{&mcp.TextContent{Text: text}},
			IsError: !result.Delivered,
		}, nil
	}
}

// roomStatusHandler returns an mcp.ToolHandler for the room_status tool.
func roomStatusHandler(cfg config) mcp.ToolHandler {
	return func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		log.Printf("room_status: room=%s", cfg.roomName)

		ariParams := ariRoomStatusParams{Name: cfg.roomName}
		var result ariRoomStatusResult
		if err := callARI(ctx, cfg.agentdSocket, "room/status", ariParams, &result); err != nil {
			return &mcp.CallToolResult{
				Content: []mcp.Content{&mcp.TextContent{Text: fmt.Sprintf("room/status failed: %v", err)}},
				IsError: true,
			}, nil
		}

		var sb strings.Builder
		sb.WriteString(fmt.Sprintf("Room: %s (mode: %s)\n", result.Name, result.CommunicationMode))
		sb.WriteString(fmt.Sprintf("Members (%d):\n", len(result.Members)))
		for _, m := range result.Members {
			sb.WriteString(fmt.Sprintf("  - %s [%s] state: %s\n", m.AgentName, m.RuntimeClass, m.AgentState))
		}

		return &mcp.CallToolResult{
			Content: []mcp.Content{&mcp.TextContent{Text: sb.String()}},
		}, nil
	}
}

// ────────────────────────────────────────────────────────────────────────────
// Main
// ────────────────────────────────────────────────────────────────────────────

func main() {
	log.SetPrefix("room-mcp-server: ")

	// Write logs to a file in the shim state directory if available,
	// otherwise fall back to stderr. Stdout is reserved for MCP JSON-RPC.
	if stateDir := os.Getenv("OAR_STATE_DIR"); stateDir != "" {
		logPath := fmt.Sprintf("%s/room-mcp-server.log", stateDir)
		f, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
		if err == nil {
			log.SetOutput(f)
		} else {
			log.SetOutput(os.Stderr)
			log.Printf("failed to open log file %s: %v, falling back to stderr", logPath, err)
		}
	} else {
		log.SetOutput(os.Stderr)
	}

	cfg, err := loadConfig()
	if err != nil {
		log.Fatalf("configuration error: %v", err)
	}

	log.Printf("starting (room=%s, agentName=%s, agentID=%s)", cfg.roomName, cfg.agentName, cfg.agentID)

	server := mcp.NewServer(&mcp.Implementation{Name: "room-mcp-server", Version: "0.1.0"}, nil)

	server.AddTool(&mcp.Tool{
		Name:        "room_send",
		Description: "Send a message to another agent in the current room",
		InputSchema: roomSendSchema,
	}, roomSendHandler(cfg))

	server.AddTool(&mcp.Tool{
		Name:        "room_status",
		Description: "Get the current room membership and status",
		InputSchema: roomStatusSchema,
	}, roomStatusHandler(cfg))

	ctx := context.Background()
	if err := server.Run(ctx, &mcp.StdioTransport{}); err != nil {
		log.Printf("server exited: %v", err)
	}
}
