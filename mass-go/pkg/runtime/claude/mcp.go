package claude

import (
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/zoumo/agent-sdk-go/mcp"

	"github.com/zoumo/mass/mass-go/pkg/runtime"
)

type claudeMCPConfig struct {
	servers    []mcp.Server
	processEnv map[string]string
}

func claudeMCPServers(configured *[]runtime.MCPServer, options runtime.ProcessConfig) (claudeMCPConfig, error) {
	if configured == nil {
		return claudeMCPConfig{}, nil
	}
	for i, server := range *configured {
		if server.Type == runtime.MCPTransportSSE {
			return claudeMCPConfig{}, errors.Join(runtime.UnsupportedOption("mcp_transport_unsupported", "MCP SSE transport is not supported by the agent SDK"), fmt.Errorf("mcp server %d: sse transport is unsupported", i))
		}
		if server.Cwd != "" && server.Type == runtime.MCPTransportStdio {
			return claudeMCPConfig{}, errors.Join(runtime.UnsupportedOption("mcp_cwd_unsupported", "MCP working directories are not supported by this Claude runtime"), fmt.Errorf("mcp server %d: Claude does not support cwd", i))
		}
		if server.EnvHeaders != nil || server.BearerTokenEnvVar != "" {
			return claudeMCPConfig{}, errors.Join(runtime.UnsupportedOption("mcp_auth_fields_unsupported", "MCP environment headers and bearer token references are not supported by this Claude runtime"), fmt.Errorf("mcp server %d: Claude does not support envHeaders or bearerTokenEnvVar", i))
		}
	}
	servers, err := runtime.ResolveMCPServers(*configured)
	if err != nil {
		return claudeMCPConfig{}, errors.Join(runtime.ErrInvalidArgument, err)
	}
	config := claudeMCPConfig{servers: servers}
	for i, server := range servers {
		original := (*configured)[i]
		if err := config.prepareValues(i, "env", server.Env, original.Env, options); err != nil {
			return claudeMCPConfig{}, err
		}
		if err := config.prepareValues(i, "header", server.Headers, original.Headers, options); err != nil {
			return claudeMCPConfig{}, err
		}
	}
	return config, nil
}

func (c *claudeMCPConfig) prepareValues(index int, field string, resolved, original []mcp.NamedValue, options runtime.ProcessConfig) error {
	direct := runtime.MCPDirectValues(original)
	for i, entry := range resolved {
		value := *entry.Value
		if _, explicit := direct[entry.Name]; !explicit {
			// Claude can expand values again while loading the MCP configuration.
			// Reject potentially reinterpreted file data without exposing it.
			if strings.Contains(value, "${") {
				return errors.Join(runtime.ErrInvalidArgument, fmt.Errorf("mcp server %d: file-backed %s contains Claude interpolation syntax", index, field))
			}
			// Pass the file value through the Session environment to keep it
			// out of the native CLI argument list.
			if value != "" {
				if c.processEnv == nil {
					c.processEnv = make(map[string]string)
				}
				name := "MASS_MCP_VALUE_" + strings.ReplaceAll(uuid.NewString(), "-", "")
				c.processEnv[name] = value
				resolved[i] = mcp.Literal(entry.Name, "${"+name+"}")
			}
			continue
		}
		if field == "env" && !runtime.IsSensitiveMCPName(entry.Name) {
			continue
		}
		value, err := runtime.ResolveMCPEnvironmentReferences(value, options.LookupEnv)
		if err != nil {
			return errors.Join(runtime.ErrInvalidArgument, fmt.Errorf("mcp server %d: %s %q: %w", index, field, entry.Name, err))
		}
		if field == "header" && !runtime.ValidMCPHeaderValue(value) {
			return errors.Join(runtime.ErrInvalidArgument, fmt.Errorf("mcp server %d: header %q contains an invalid value", index, entry.Name))
		}
	}
	return nil
}
