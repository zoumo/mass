// Package workspacemcp implements the "mass workspace-mcp" subcommand.
package workspacemcp

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/spf13/cobra"

	"github.com/charmbracelet/x/term"

	pkgariapi "github.com/zoumo/mass/pkg/ari/api"
	ariclient "github.com/zoumo/mass/pkg/ari/client"

	"github.com/zoumo/mass/internal/logging"
)

// ────────────────────────────────────────────────────────────────────────────
// Environment configuration
// ────────────────────────────────────────────────────────────────────────────

type config struct {
	massSocket    string // MASS_SOCKET
	workspaceName string // MASS_WORKSPACE_NAME
	agentName     string // MASS_AGENT_NAME
}

func loadConfig() (config, error) {
	c := config{
		massSocket:    os.Getenv("MASS_SOCKET"),
		workspaceName: os.Getenv("MASS_WORKSPACE_NAME"),
		agentName:     os.Getenv("MASS_AGENT_NAME"),
	}
	if c.massSocket == "" {
		return c, fmt.Errorf("MASS_SOCKET is required")
	}
	if c.workspaceName == "" {
		return c, fmt.Errorf("MASS_WORKSPACE_NAME is required")
	}
	return c, nil
}

// ────────────────────────────────────────────────────────────────────────────
// Tool schemas
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
    },
    "needsReply": {
      "type": "boolean",
      "description": "Set to true when you expect the target agent to reply back via workspace message"
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
// Tool handlers
// ────────────────────────────────────────────────────────────────────────────

func workspaceSendHandler(cfg config, client pkgariapi.Client, logger *slog.Logger) mcp.ToolHandler {
	return func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		var input struct {
			TargetAgent string `json:"targetAgent"`
			Message     string `json:"message"`
			NeedsReply  bool   `json:"needsReply"`
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

		logger.Info("workspace_send", "target", input.TargetAgent, "needsReply", input.NeedsReply)

		result, err := client.Workspaces().Send(ctx, &pkgariapi.WorkspaceSendParams{
			Workspace:  cfg.workspaceName,
			From:       cfg.agentName,
			To:         input.TargetAgent,
			Message:    []pkgariapi.ContentBlock{pkgariapi.TextBlock(input.Message)},
			NeedsReply: input.NeedsReply,
		})
		if err != nil {
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

func workspaceStatusHandler(cfg config, client pkgariapi.Client, logger *slog.Logger) mcp.ToolHandler {
	return func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		logger.Info("workspace_status", "workspace", cfg.workspaceName)

		// Fetch workspace details.
		var ws pkgariapi.Workspace
		if err := client.Get(ctx, pkgariapi.ObjectKey{Name: cfg.workspaceName}, &ws); err != nil {
			return &mcp.CallToolResult{
				Content: []mcp.Content{&mcp.TextContent{Text: fmt.Sprintf("workspace/get failed: %v", err)}},
				IsError: true,
			}, nil
		}

		// Fetch workspace members via agentrun/list with workspace filter.
		var members pkgariapi.AgentRunList
		if err := client.List(ctx, &members, pkgariapi.InWorkspace(cfg.workspaceName)); err != nil {
			return &mcp.CallToolResult{
				Content: []mcp.Content{&mcp.TextContent{Text: fmt.Sprintf("agentrun/list failed: %v", err)}},
				IsError: true,
			}, nil
		}

		var sb strings.Builder
		sb.WriteString(fmt.Sprintf("Workspace: %s (phase: %s)\n", ws.Metadata.Name, ws.Status.Phase))
		if ws.Status.Path != "" {
			sb.WriteString(fmt.Sprintf("Path: %s\n", ws.Status.Path))
		}
		sb.WriteString(fmt.Sprintf("Members (%d):\n", len(members.Items)))
		for _, m := range members.Items {
			sb.WriteString(fmt.Sprintf("  - %s [%s] state: %s\n", m.Metadata.Name, m.Spec.Agent, m.Status.State))
		}

		return &mcp.CallToolResult{
			Content: []mcp.Content{&mcp.TextContent{Text: sb.String()}},
		}, nil
	}
}

// ────────────────────────────────────────────────────────────────────────────
// Subcommand
// ────────────────────────────────────────────────────────────────────────────

// NewCommand returns the "workspace-mcp" cobra command.
func NewCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "workspace-mcp",
		Short: "Run the workspace MCP server (stdio transport)",
		Long: `workspace-mcp exposes workspace_send and workspace_status MCP tools over stdio.
Reads MASS_SOCKET, MASS_WORKSPACE_NAME, and MASS_AGENT_NAME from the environment.`,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			return run()
		},
	}
}

func run() error {
	// Determine log output target.
	var w io.Writer = os.Stderr
	if stateDir := os.Getenv("MASS_STATE_DIR"); stateDir != "" {
		logPath := filepath.Join(stateDir, "workspace-mcp-server.log")
		f, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
		if err != nil {
			// Cannot open log file; warn on stderr and continue with stderr.
			fmt.Fprintf(os.Stderr, "workspace-mcp-server: failed to open log file %s: %v, falling back to stderr\n", logPath, err)
		} else {
			w = f
			defer f.Close()
		}
	}

	// Initialize slog from env (inherited from mass daemon via generateConfig).
	logLevel := os.Getenv("MASS_LOG_LEVEL")
	logFormat := os.Getenv("MASS_LOG_FORMAT")
	level, err := logging.ParseLevel(logLevel)
	if err != nil {
		level = slog.LevelInfo
	}
	if logFormat == "" || logFormat == "auto" {
		logFormat = "text" // default to text: workspace-mcp typically writes to file
		if f, ok := w.(*os.File); ok && term.IsTerminal(f.Fd()) {
			logFormat = "pretty"
		}
	}
	handler := logging.NewHandler(logFormat, level, w)
	logger := slog.New(handler)
	slog.SetDefault(logger)

	cfg, err := loadConfig()
	if err != nil {
		return fmt.Errorf("configuration error: %w", err)
	}

	logger.Info("starting", "workspace", cfg.workspaceName, "agent", cfg.agentName)

	// Connect to ARI server (persistent connection).
	ctx := context.Background()
	client, err := ariclient.Dial(ctx, cfg.massSocket)
	if err != nil {
		return fmt.Errorf("dial ARI: %w", err)
	}
	defer client.Close()

	server := mcp.NewServer(&mcp.Implementation{Name: "workspace-mcp-server", Version: "0.1.0"}, nil)

	server.AddTool(&mcp.Tool{
		Name:        "workspace_send",
		Description: "Send a message to another agent in the current workspace",
		InputSchema: workspaceSendSchema,
	}, workspaceSendHandler(cfg, client, logger))

	server.AddTool(&mcp.Tool{
		Name:        "workspace_status",
		Description: "Get the current workspace membership and status",
		InputSchema: workspaceStatusSchema,
	}, workspaceStatusHandler(cfg, client, logger))

	if err := server.Run(ctx, &mcp.StdioTransport{}); err != nil {
		logger.Error("server exited", "error", err)
	}
	return nil
}
