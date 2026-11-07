package codex

import (
	"fmt"
	"maps"
	"strings"

	sdk "github.com/zoumo/agent-sdk-go/codex"

	"github.com/zoumo/mass/mass-go/pkg/runtime"
)

func codexMCPServers(configured *[]runtime.MCPServer) ([]sdk.MCPServer, error) {
	if configured == nil {
		return nil, nil
	}
	servers := *configured
	if err := runtime.ValidateMCPServers(servers); err != nil {
		return nil, err
	}
	converted := make([]sdk.MCPServer, 0, len(servers))
	for i, server := range servers {
		if strings.Contains(server.Name, ".") {
			return nil, fmt.Errorf("mcp server %d: Codex names must not contain dots", i)
		}
		switch server.Type {
		case "stdio":
			converted = append(converted, sdk.MCPServer{
				Name:    server.Name,
				Command: server.Command,
				Args:    append([]string(nil), server.Args...),
				Env:     maps.Clone(server.Env),
				Cwd:     server.Cwd,
			})
		case "http":
			converted = append(converted, sdk.MCPServer{
				Name:              server.Name,
				URL:               server.URL,
				Headers:           maps.Clone(server.Headers),
				EnvHeaders:        maps.Clone(server.EnvHeaders),
				BearerTokenEnvVar: server.BearerTokenEnvVar,
			})
		case "sse":
			return nil, fmt.Errorf("mcp server %d: Codex does not support sse transport", i)
		}
	}
	return converted, nil
}
