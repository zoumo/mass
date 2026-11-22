package codex

import (
	"errors"
	"fmt"
	"maps"
	"strings"

	"github.com/zoumo/agent-sdk-go/mcp"

	"github.com/zoumo/mass/massd/pkg/runtime"
)

func codexMCPServers(configured *[]runtime.MCPServer, options runtime.ProcessConfig) ([]mcp.Server, error) {
	if configured == nil {
		return nil, nil
	}
	for i, server := range *configured {
		if strings.Contains(server.Name, ".") {
			return nil, codexMCPUnsupported("mcp_server_name_unsupported", fmt.Errorf("mcp server %d: Codex names must not contain dots", i))
		}
		if server.Type == runtime.MCPTransportSSE {
			return nil, codexMCPUnsupported("mcp_transport_unsupported", fmt.Errorf("mcp server %d: Codex does not support sse transport", i))
		}
	}
	servers, err := runtime.ResolveMCPServers(*configured)
	if err != nil {
		return nil, codexMCPInvalid(err)
	}
	for i, server := range servers {
		original := (*configured)[i]
		switch server.Transport {
		case runtime.MCPTransportStdio:
			direct := runtime.MCPDirectValues(original.Env)
			for j, entry := range server.Env {
				if _, ok := direct[entry.Name]; !ok {
					continue
				}
				value, err := runtime.ResolveMCPEnvironmentReferences(*entry.Value, options.LookupEnv)
				if err != nil {
					return nil, codexMCPInvalid(fmt.Errorf("mcp server %d: env %q: %w", i, entry.Name, err))
				}
				server.Env[j] = mcp.Literal(entry.Name, value)
			}
		case runtime.MCPTransportHTTP:
			server, err = codexHTTPMCPServer(i, server, original.Headers, options)
			if err != nil {
				return nil, err
			}
		}
		servers[i] = server
	}
	return servers, nil
}

func codexHTTPMCPServer(index int, server mcp.Server, original []mcp.NamedValue, options runtime.ProcessConfig) (mcp.Server, error) {
	envHeaders := maps.Clone(server.EnvHeaders)
	if envHeaders == nil {
		envHeaders = map[string]string{}
	}
	if err := validateCodexHeaderNames(server.Headers, envHeaders); err != nil {
		return mcp.Server{}, codexMCPInvalid(fmt.Errorf("mcp server %d: %w", index, err))
	}
	direct := runtime.MCPDirectValues(original)
	bearerTokenEnvVar := server.BearerTokenEnvVar
	headers := server.Headers[:0]
	for _, entry := range server.Headers {
		key, raw := entry.Name, *entry.Value
		if _, ok := direct[key]; !ok {
			headers = append(headers, entry)
			continue
		}
		prefix, name, exact := runtime.ParseMCPSecretReference(raw)
		if exact {
			if err := codexMCPHeaderReference(name, options); err != nil {
				return mcp.Server{}, codexMCPInvalid(fmt.Errorf("mcp server %d: header %q: %w", index, key, err))
			}
			if strings.EqualFold(key, "Authorization") && strings.EqualFold(prefix, "Bearer") {
				if bearerTokenEnvVar != "" && bearerTokenEnvVar != name {
					return mcp.Server{}, codexMCPInvalid(fmt.Errorf("mcp server %d: conflicting Authorization bearer references", index))
				}
				bearerTokenEnvVar = name
				continue
			}
			if prefix == "" {
				envHeaders[key] = name
				continue
			}
		}
		value, err := runtime.ResolveMCPEnvironmentReferences(raw, options.LookupEnv)
		if err != nil {
			return mcp.Server{}, codexMCPInvalid(fmt.Errorf("mcp server %d: header %q: %w", index, key, err))
		}
		if !runtime.ValidMCPHeaderValue(value) {
			return mcp.Server{}, codexMCPInvalid(fmt.Errorf("mcp server %d: header %q contains an invalid value", index, key))
		}
		headers = append(headers, mcp.Literal(key, value))
	}
	if err := validateCodexAuthorizationConflict(headers, envHeaders, bearerTokenEnvVar); err != nil {
		return mcp.Server{}, codexMCPInvalid(fmt.Errorf("mcp server %d: %w", index, err))
	}
	for key, name := range envHeaders {
		if err := codexMCPHeaderReference(name, options); err != nil {
			return mcp.Server{}, codexMCPInvalid(fmt.Errorf("mcp server %d: envHeaders %q: %w", index, key, err))
		}
	}
	if bearerTokenEnvVar != "" {
		if err := codexMCPHeaderReference(bearerTokenEnvVar, options); err != nil {
			return mcp.Server{}, codexMCPInvalid(fmt.Errorf("mcp server %d: bearerTokenEnvVar: %w", index, err))
		}
	}
	server.Headers = headers
	server.EnvHeaders = envHeaders
	server.BearerTokenEnvVar = bearerTokenEnvVar
	return server, nil
}

func codexMCPHeaderReference(name string, options runtime.ProcessConfig) error {
	value, err := runtime.LookupMCPEnvironmentReference(options.LookupEnv, name)
	if err != nil {
		return err
	}
	if !runtime.ValidMCPHeaderValue(value) {
		return fmt.Errorf("environment variable %q has an invalid header value", name)
	}
	return nil
}

func validateCodexHeaderNames(headers []mcp.NamedValue, envHeaders map[string]string) error {
	seen := make(map[string]struct{}, len(headers)+len(envHeaders))
	for _, entry := range headers {
		seen[strings.ToLower(entry.Name)] = struct{}{}
	}
	for key := range envHeaders {
		canonical := strings.ToLower(key)
		if _, ok := seen[canonical]; ok {
			return errors.New("header is declared more than once")
		}
		seen[canonical] = struct{}{}
	}
	return nil
}

func validateCodexAuthorizationConflict(headers []mcp.NamedValue, envHeaders map[string]string, bearerTokenEnvVar string) error {
	if bearerTokenEnvVar == "" {
		return nil
	}
	for _, entry := range headers {
		if strings.EqualFold(entry.Name, "Authorization") {
			return errors.New("authorization header conflicts with bearerTokenEnvVar")
		}
	}
	for key := range envHeaders {
		if strings.EqualFold(key, "Authorization") {
			return errors.New("authorization header conflicts with bearerTokenEnvVar")
		}
	}
	return nil
}

func codexMCPInvalid(err error) error {
	return errors.Join(runtime.ErrInvalidArgument, err)
}

func codexMCPUnsupported(reason string, err error) error {
	return errors.Join(runtime.UnsupportedOption(reason, "MCP configuration is not supported by this Codex runtime"), err)
}
