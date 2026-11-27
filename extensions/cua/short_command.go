package cua

import (
	"context"
	"encoding/json"
	"errors"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const spaceBashTool = "space_bash"

func registerShortCommand(server *mcp.Server, cfg *config) func() error {
	sessions := make(map[string]*officialMCPSession, len(cfg.computers))
	for id := range cfg.computers {
		sessions[id] = new(officialMCPSession)
	}
	required := []string{"command"}
	if len(sessions) > 1 {
		required = append(required, "computer_id")
	}
	server.AddTool(&mcp.Tool{
		Name:        spaceBashTool,
		Description: "Run a short shell command on a configured computer. Cua returns stdout, stderr and the exit code.",
		InputSchema: map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"required":             required,
			"properties": map[string]any{
				"computer_id": map[string]any{"type": "string", "enum": cfg.computerIDs(), "description": "Configured computer ID. May be omitted when only one computer is configured."},
				"command":     map[string]any{"type": "string", "description": "Shell command line."},
				"timeout":     map[string]any{"type": "integer", "minimum": 0, "description": "Command timeout in seconds. The Cua default is 60."},
			},
		},
	}, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		target, args, err := shortCommandArguments(cfg, req.Params.Arguments)
		if err != nil {
			return toolError(err), nil
		}
		session, err := sessions[target.id].get(ctx, cfg, target, connectShortCommand)
		if err != nil {
			return toolError(err), nil
		}
		return callOfficialTool(ctx, session, target, spaceBashTool, args)
	})

	return func() error {
		return closeOfficialSessions(sessions)
	}
}

func shortCommandArguments(cfg *config, raw json.RawMessage) (*computer, map[string]any, error) {
	var fields map[string]json.RawMessage
	if len(raw) > 0 && json.Unmarshal(raw, &fields) != nil {
		return nil, nil, errors.New("space_bash arguments must be an object")
	}
	for key := range fields {
		switch key {
		case "computer_id", "command", "timeout":
		default:
			return nil, nil, errors.New("space_bash only accepts computer_id, command and timeout")
		}
	}
	var id *string
	if value, exists := fields["computer_id"]; exists && (json.Unmarshal(value, &id) != nil || id == nil) {
		return nil, nil, errors.New("computer_id must be a string")
	}
	var command *string
	if json.Unmarshal(fields["command"], &command) != nil || command == nil {
		return nil, nil, errors.New("command must be a string")
	}
	var timeout *uint64
	if value, exists := fields["timeout"]; exists && (json.Unmarshal(value, &timeout) != nil || timeout == nil) {
		return nil, nil, errors.New("timeout must be an unsigned integer")
	}
	target, err := cfg.resolveComputer(id)
	if err != nil {
		return nil, nil, err
	}
	args := map[string]any{"space": target.id, "command": *command}
	if timeout != nil {
		args["timeout"] = *timeout
	}
	return target, args, nil
}

func connectShortCommand(ctx context.Context, cfg *config, target *computer, dir string) (*mcp.ClientSession, error) {
	setup, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	env := cuaCommandEnv(cfg, dir)
	state := filepath.Join(dir, "sandboxes")

	// Only the registration process receives the token in its environment. Cua
	// stores it in its own credentials file inside this private directory.
	register := exec.CommandContext(setup, cfg.cuaCLI, "spaces", "add", target.url.String(), "--name", target.id,
		"--embedded", "--state-dir", state)
	register.Dir = dir
	register.Env = append([]string{"CUA_ENV_TOKEN=" + target.token}, env...)
	if err := register.Run(); err != nil {
		if setup.Err() != nil {
			return nil, errors.New("registration with Cua canceled or timed out")
		}
		return nil, errors.New("cannot register the configured computer with Cua")
	}

	// MCP owns this command's lifetime. A single tool's context only bounds the
	// handshake and its own call, so later calls can use the same session.
	cmd := exec.CommandContext(context.WithoutCancel(ctx), cfg.cuaCLI, "mcp", "--embedded", "--sandbox", target.id, "--state-dir", state,
		"--permissions", "spaces:"+spaceBashTool)
	cmd.Dir = dir
	cmd.Env = env
	return connectOfficialMCP(setup, cmd)
}
