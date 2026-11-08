package claude

import (
	"errors"
	"fmt"
	"maps"

	sdk "github.com/zoumo/agent-sdk-go/claude"

	"github.com/zoumo/mass/mass-go/pkg/runtime"
)

func claudeMCPServers(configured *[]runtime.MCPServer) ([]sdk.MCPServer, error) {
	if configured == nil {
		return nil, nil
	}
	servers := *configured
	if err := runtime.ValidateMCPServers(servers); err != nil {
		return nil, errors.Join(runtime.ErrInvalidArgument, err)
	}
	converted := make([]sdk.MCPServer, 0, len(servers))
	for i, server := range servers {
		if server.Cwd != "" {
			return nil, errors.Join(runtime.UnsupportedOption("mcp_cwd_unsupported", "MCP working directories are not supported by this Claude runtime"), fmt.Errorf("mcp server %d: Claude does not support cwd", i))
		}
		if server.EnvHeaders != nil || server.BearerTokenEnvVar != "" {
			return nil, errors.Join(runtime.UnsupportedOption("mcp_auth_fields_unsupported", "MCP environment headers and bearer token references are not supported by this Claude runtime"), fmt.Errorf("mcp server %d: Claude does not support envHeaders or bearerTokenEnvVar", i))
		}
		converted = append(converted, sdk.MCPServer{
			Name:      server.Name,
			Transport: sdk.MCPTransport(server.Type),
			Command:   server.Command,
			Args:      append([]string(nil), server.Args...),
			Env:       maps.Clone(server.Env),
			URL:       server.URL,
			Headers:   maps.Clone(server.Headers),
		})
	}
	return converted, nil
}
