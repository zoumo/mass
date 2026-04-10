// Command workspace-mcp-server is an MCP server over stdio that exposes
// workspace_send and workspace_status tools. It uses the
// modelcontextprotocol/go-sdk for the MCP protocol layer. Outbound ARI calls
// are made to agentd via a Unix domain socket specified by OAR_AGENTD_SOCKET.
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

type ariWorkspaceSendParams struct {
	Workspace string `json:"workspace"`
	From      string `json:"from"`
	To        string `json:"to"`
	Message   string `json:"message"`
}

type ariWorkspaceSendResult struct {
	Delivered bool `json:"delivered"`
}

type ariWorkspaceStatusParams struct {
	Name string `json:"name"`
}

type ariWorkspaceStatusResult struct {
	Name    string               `json:"name"`
	Phase   string               `json:"phase"`
	Path    string               `json:"path,omitempty"`
	Members []ariWorkspaceMember `json:"members,omitempty"`
}

type ariWorkspaceMember struct {
	Workspace    string `json:"workspace"`
	Name         string `json:"name"`
	RuntimeClass string `json:"runtimeClass"`
	State        string `json:"state"`
}

// ────────────────────────────────────────────────────────────────────────────
// Environment configuration
// ────────────────────────────────────────────────────────────────────────────

type config struct {
	agentdSocket  string // OAR_AGENTD_SOCKET
	workspaceName string // OAR_WORKSPACE_NAME
	agentID       string // OAR_AGENT_ID
	agentName     string // OAR_AGENT_NAME
}

func loadConfig() (config, error) {
	c := config{
		agentdSocket:  os.Getenv("OAR_AGENTD_SOCKET"),
		workspaceName: os.Getenv("OAR_WORKSPACE_NAME"),
		agentID:       os.Getenv("OAR_AGENT_ID"),
		agentName:     os.Getenv("OAR_AGENT_NAME"),
	}
	if c.agentdSocket == "" {
		return c, fmt.Errorf("OAR_AGENTD_SOCKET is required")
	}
	if c.workspaceName == "" {
		return c, fmt.Errorf("OAR_WORKSPACE_NAME is required")
	}
	if c.agentID == "" {
		return c, fmt.Errorf("OAR_AGENT_ID is required")
	}
	// OAR_AGENT_NAME may be empty (agent name within workspace is optional)
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

var workspaceSendSchema = json.RawMessage(`{
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

var workspaceStatusSchema = json.RawMessage(`{
  "type": "object",
  "properties": {},
  "required": []
}`)

// ────────────────────────────────────────────────────────────────────────────
// Tool handlers (SDK ToolHandler signature)
// ────────────────────────────────────────────────────────────────────────────

// workspaceSendHandler returns an mcp.ToolHandler for the workspace_send tool.
func workspaceSendHandler(cfg config) mcp.ToolHandler {
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

		log.Printf("workspace_send: target=%s", input.TargetAgent)

		ariParams := ariWorkspaceSendParams{
			Workspace: cfg.workspaceName,
			From:      cfg.agentName,
			To:        input.TargetAgent,
			Message:   input.Message,
		}

		var result ariWorkspaceSendResult
		if err := callARI(ctx, cfg.agentdSocket, "workspace/send", ariParams, &result); err != nil {
			errMsg := err.Error()
			var text string
			if strings.Contains(errMsg, "is busy") || strings.Contains(errMsg, "cancel its current turn") {
				text = fmt.Sprintf("Target agent %s is busy processing another prompt. Cancel its current turn or try again later.", input.TargetAgent)
			} else {
				text = fmt.Sprintf("workspace/send failed: %v", err)
			}
			return &mcp.CallToolResult{
				Content: []mcp.Content{&mcp.TextContent{Text: text}},
				IsError: true,
			}, nil
		}

		var text string
		if result.Delivered {
			text = fmt.Sprintf("Message delivered to %s. The target agent will process it asynchronously.", input.TargetAgent)
		} else {
			text = fmt.Sprintf("Message delivery failed to %s", input.TargetAgent)
		}

		return &mcp.CallToolResult{
			Content: []mcp.Content{&mcp.TextContent{Text: text}},
			IsError: !result.Delivered,
		}, nil
	}
}

// workspaceStatusHandler returns an mcp.ToolHandler for the workspace_status tool.
func workspaceStatusHandler(cfg config) mcp.ToolHandler {
	return func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		log.Printf("workspace_status: workspace=%s", cfg.workspaceName)

		ariParams := ariWorkspaceStatusParams{Name: cfg.workspaceName}
		var result ariWorkspaceStatusResult
		if err := callARI(ctx, cfg.agentdSocket, "workspace/status", ariParams, &result); err != nil {
			return &mcp.CallToolResult{
				Content: []mcp.Content{&mcp.TextContent{Text: fmt.Sprintf("workspace/status failed: %v", err)}},
				IsError: true,
			}, nil
		}

		var sb strings.Builder
		sb.WriteString(fmt.Sprintf("Workspace: %s (phase: %s)\n", result.Name, result.Phase))
		if result.Path != "" {
			sb.WriteString(fmt.Sprintf("Path: %s\n", result.Path))
		}
		sb.WriteString(fmt.Sprintf("Members (%d):\n", len(result.Members)))
		for _, m := range result.Members {
			sb.WriteString(fmt.Sprintf("  - %s [%s] state: %s\n", m.Name, m.RuntimeClass, m.State))
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
	log.SetPrefix("workspace-mcp-server: ")

	// Write logs to a file in the shim state directory if available,
	// otherwise fall back to stderr. Stdout is reserved for MCP JSON-RPC.
	if stateDir := os.Getenv("OAR_STATE_DIR"); stateDir != "" {
		logPath := fmt.Sprintf("%s/workspace-mcp-server.log", stateDir)
		f, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
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

	log.Printf("starting (workspace=%s, agentName=%s, agentID=%s)", cfg.workspaceName, cfg.agentName, cfg.agentID)

	server := mcp.NewServer(&mcp.Implementation{Name: "workspace-mcp-server", Version: "0.1.0"}, nil)

	server.AddTool(&mcp.Tool{
		Name:        "workspace_send",
		Description: "Send a message to another agent in the current workspace",
		InputSchema: workspaceSendSchema,
	}, workspaceSendHandler(cfg))

	server.AddTool(&mcp.Tool{
		Name:        "workspace_status",
		Description: "Get the current workspace membership and status",
		InputSchema: workspaceStatusSchema,
	}, workspaceStatusHandler(cfg))

	ctx := context.Background()
	if err := server.Run(ctx, &mcp.StdioTransport{}); err != nil {
		log.Printf("server exited: %v", err)
	}
}
