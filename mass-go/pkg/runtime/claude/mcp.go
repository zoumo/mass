package claude

import (
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/zoumo/agent-sdk-go/agent"

	"github.com/zoumo/mass/mass-go/pkg/runtime"
)

func claudeMCPServers(configured *[]runtime.MCPServer, options runtime.ProcessConfig) ([]agent.MCPServer, error) {
	if configured == nil {
		return nil, nil
	}
	servers := *configured
	if err := runtime.ValidateMCPServers(servers); err != nil {
		return nil, errors.Join(runtime.ErrInvalidArgument, err)
	}
	converted := make([]agent.MCPServer, 0, len(servers))
	for i, server := range servers {
		if server.Type == runtime.MCPTransportSSE {
			return nil, errors.Join(runtime.UnsupportedOption("mcp_transport_unsupported", "MCP SSE transport is not supported by the agent SDK"), fmt.Errorf("mcp server %d: sse transport is unsupported", i))
		}
		if server.Cwd != "" {
			return nil, errors.Join(runtime.UnsupportedOption("mcp_cwd_unsupported", "MCP working directories are not supported by this Claude runtime"), fmt.Errorf("mcp server %d: Claude does not support cwd", i))
		}
		if server.EnvHeaders != nil || server.BearerTokenEnvVar != "" {
			return nil, errors.Join(runtime.UnsupportedOption("mcp_auth_fields_unsupported", "MCP environment headers and bearer token references are not supported by this Claude runtime"), fmt.Errorf("mcp server %d: Claude does not support envHeaders or bearerTokenEnvVar", i))
		}
		if err := validateClaudeMCPSecrets(i, server, options); err != nil {
			return nil, err
		}
		converted = append(converted, agent.MCPServer{
			Name:      server.Name,
			Transport: agent.MCPTransport(server.Type),
			Command:   server.Command,
			Args:      append([]string(nil), server.Args...),
			Env:       maps.Clone(server.Env),
			URL:       server.URL,
			Headers:   maps.Clone(server.Headers),
		})
	}
	return converted, nil
}

// validateClaudeMCPSecrets keeps ${VAR} references for the CLI to expand, but
// fails before opening when a secret reference or its value is unusable.
func validateClaudeMCPSecrets(index int, server runtime.MCPServer, options runtime.ProcessConfig) error {
	for _, field := range []struct {
		name       string
		values     map[string]string
		noNewlines bool
	}{
		{name: "env", values: server.Env},
		{name: "header", values: server.Headers, noNewlines: true},
	} {
		for _, key := range slices.Sorted(maps.Keys(field.values)) {
			if !runtime.IsSensitiveMCPName(key) {
				continue
			}
			_, name, _ := runtime.ParseMCPSecretReference(field.values[key])
			resolved, err := runtime.LookupMCPEnvironmentReference(options.LookupEnv, name)
			if err != nil {
				return errors.Join(runtime.ErrInvalidArgument, fmt.Errorf("mcp server %d: %s %q: %w", index, field.name, key, err))
			}
			if field.noNewlines && strings.ContainsAny(resolved, "\r\n") {
				return errors.Join(runtime.ErrInvalidArgument, fmt.Errorf("mcp server %d: header %q contains an invalid value", index, key))
			}
		}
	}
	return nil
}
