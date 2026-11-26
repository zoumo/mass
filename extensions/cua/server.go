package cua

import (
	"context"
	"errors"
	"log/slog"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Run serves a single stdio connection and owns its downstream clients.
func Run(ctx context.Context, configPath string) error {
	cfg, err := loadConfig(configPath)
	if err != nil {
		return err
	}
	server := mcp.NewServer(&mcp.Implementation{Name: "cua-mcp"}, &mcp.ServerOptions{Logger: mcpLogger})
	closeShell := registerShortCommand(server, cfg)
	err = server.Run(ctx, &mcp.StdioTransport{})
	switch {
	case err == nil, errors.Is(err, context.Canceled):
		err = nil
	default:
		err = errors.New("stdio session failed")
	}
	return errors.Join(err, closeShell())
}

// Protocol logs can contain raw peer messages.
var mcpLogger = slog.New(slog.DiscardHandler)
