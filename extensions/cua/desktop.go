package cua

import (
	"context"
	"encoding/json"
	"errors"
	"os/exec"
	"path/filepath"
	"time"

	_ "embed"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// The macOS input schemas come from cua-driver 0.63.1's describe command.
// Keep discovery independent of whether the configured computers are online.
//
//go:embed desktop_tools.json
var desktopToolsJSON []byte

type desktopTool struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	InputSchema struct {
		Type                 string         `json:"type"`
		AdditionalProperties bool           `json:"additionalProperties"`
		Properties           map[string]any `json:"properties"`
		Required             []string       `json:"required,omitempty"`
	} `json:"inputSchema"`
}

func registerDesktopTools(server *mcp.Server, cfg *config) (func() error, error) {
	sessions := make(map[string]*officialMCPSession)
	var ids []string
	for _, id := range cfg.computerIDs() {
		if cfg.computers[id].driver != nil {
			sessions[id] = new(officialMCPSession)
			ids = append(ids, id)
		}
	}
	closeDesktop := func() error { return closeOfficialSessions(sessions) }
	if len(sessions) == 0 {
		return closeDesktop, nil
	}

	var tools []desktopTool
	if err := json.Unmarshal(desktopToolsJSON, &tools); err != nil {
		return nil, errors.New("cannot load Cua Driver tool schemas")
	}
	for _, tool := range tools {
		tool.InputSchema.Properties["computer_id"] = map[string]any{
			"type": "string", "enum": ids,
			"description": "Computer with Cua Driver configured. May be omitted when only one computer is configured.",
		}
		if len(cfg.computers) > 1 {
			tool.InputSchema.Required = append(tool.InputSchema.Required, "computer_id")
		}
		server.AddTool(&mcp.Tool{
			Name:        tool.Name,
			Description: tool.Description,
			InputSchema: tool.InputSchema,
		}, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			target, args, err := desktopArguments(cfg, req.Params.Arguments, tool.InputSchema.Properties)
			if err != nil {
				return toolError(err), nil
			}
			session, err := sessions[target.id].get(ctx, cfg, target, connectDriver)
			if err != nil {
				return toolError(err), nil
			}
			return callOfficialTool(ctx, session, target, tool.Name, args)
		})
	}
	return closeDesktop, nil
}

func desktopArguments(cfg *config, raw json.RawMessage, properties map[string]any) (*computer, map[string]json.RawMessage, error) {
	args := make(map[string]json.RawMessage)
	if len(raw) > 0 && (json.Unmarshal(raw, &args) != nil || args == nil) {
		return nil, nil, errors.New("arguments to Cua Driver must be an object")
	}
	for key := range args {
		if _, known := properties[key]; !known {
			return nil, nil, errors.New("unknown argument for Cua Driver tool")
		}
	}
	var id *string
	if value, exists := args["computer_id"]; exists && (json.Unmarshal(value, &id) != nil || id == nil) {
		return nil, nil, errors.New("computer_id must be a string")
	}
	target, err := cfg.resolveComputer(id)
	if err != nil {
		return nil, nil, err
	}
	if target.driver == nil {
		return nil, nil, errors.New("no Cua Driver is configured for this computer")
	}

	// Only computer_id belongs to the bridge. Keep Driver arguments in their
	// original JSON form so IDs and nested selectors reach it unchanged.
	delete(args, "computer_id")
	return target, args, nil
}

func connectDriver(ctx context.Context, cfg *config, target *computer, dir string) (*mcp.ClientSession, error) {
	setup, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	// The relay command and Driver MCP session must outlive individual calls.
	cmd := exec.CommandContext(context.WithoutCancel(ctx), cfg.cuaCLI,
		"spacesd", "exec", "-i", "--embedded", "--state-dir", filepath.Join(dir, "sandboxes"), target.url.String(),
		"--", target.driver.CLI, "mcp", "--socket", target.driver.Socket)
	cmd.Dir = dir
	cmd.Env = append(cuaCommandEnv(cfg, dir), "CUA_ENV_TOKEN="+target.token)
	return connectOfficialMCP(setup, cmd)
}
