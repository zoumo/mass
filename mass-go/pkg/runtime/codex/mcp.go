package codex

import (
	"errors"
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strings"

	"github.com/zoumo/agent-sdk-go/agent"

	"github.com/zoumo/mass/mass-go/pkg/runtime"
)

var codexMCPEnvironmentReference = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)\}`)

func codexMCPServers(configured *[]runtime.MCPServer, options runtime.ProcessConfig) ([]agent.MCPServer, error) {
	if configured == nil {
		return nil, nil
	}
	servers := *configured
	if err := runtime.ValidateMCPServers(servers); err != nil {
		return nil, codexMCPInvalid(err)
	}
	converted := make([]agent.MCPServer, 0, len(servers))
	for i, server := range servers {
		if strings.Contains(server.Name, ".") {
			return nil, codexMCPUnsupported("mcp_server_name_unsupported", fmt.Errorf("mcp server %d: Codex names must not contain dots", i))
		}
		switch server.Type {
		case runtime.MCPTransportStdio:
			env, err := codexMCPEnvironment(i, server.Env, options)
			if err != nil {
				return nil, err
			}
			converted = append(converted, agent.MCPServer{
				Name:      server.Name,
				Transport: agent.MCPTransportStdio,
				Command:   server.Command,
				Args:      append([]string(nil), server.Args...),
				Env:       env,
				Cwd:       server.Cwd,
			})
		case runtime.MCPTransportHTTP:
			remote, err := codexHTTPMCPServer(i, server, options)
			if err != nil {
				return nil, err
			}
			converted = append(converted, remote)
		case runtime.MCPTransportSSE:
			return nil, codexMCPUnsupported("mcp_transport_unsupported", fmt.Errorf("mcp server %d: Codex does not support sse transport", i))
		}
	}
	return converted, nil
}

func codexHTTPMCPServer(index int, server runtime.MCPServer, options runtime.ProcessConfig) (agent.MCPServer, error) {
	headers := maps.Clone(server.Headers)
	envHeaders := maps.Clone(server.EnvHeaders)
	if envHeaders == nil {
		envHeaders = map[string]string{}
	}
	if err := validateCodexHeaderNames(headers, envHeaders); err != nil {
		return agent.MCPServer{}, codexMCPInvalid(fmt.Errorf("mcp server %d: %w", index, err))
	}
	bearerTokenEnvVar := server.BearerTokenEnvVar
	for _, key := range slices.Sorted(maps.Keys(headers)) {
		raw := headers[key]
		if strings.ContainsAny(raw, "\r\n") {
			return agent.MCPServer{}, codexMCPInvalid(fmt.Errorf("mcp server %d: header %q contains an invalid value", index, key))
		}
		prefix, name, exact := runtime.ParseMCPSecretReference(raw)
		if exact {
			if err := codexMCPHeaderReference(name, options); err != nil {
				return agent.MCPServer{}, codexMCPInvalid(fmt.Errorf("mcp server %d: header %q: %w", index, key, err))
			}
			if runtime.IsSensitiveMCPName(key) && strings.EqualFold(key, "Authorization") && strings.EqualFold(prefix, "Bearer") {
				if bearerTokenEnvVar != "" && bearerTokenEnvVar != name {
					return agent.MCPServer{}, codexMCPInvalid(fmt.Errorf("mcp server %d: conflicting Authorization bearer references", index))
				}
				bearerTokenEnvVar = name
				delete(headers, key)
				continue
			}
			if prefix == "" {
				envHeaders[key] = name
				delete(headers, key)
				continue
			}
		}
		value, err := codexMCPResolveValue(raw, options)
		if err != nil {
			return agent.MCPServer{}, codexMCPInvalid(fmt.Errorf("mcp server %d: header %q: %w", index, key, err))
		}
		if strings.ContainsAny(value, "\r\n") {
			return agent.MCPServer{}, codexMCPInvalid(fmt.Errorf("mcp server %d: header %q contains an invalid value", index, key))
		}
		headers[key] = value
	}
	if err := validateCodexAuthorizationConflict(headers, envHeaders, bearerTokenEnvVar); err != nil {
		return agent.MCPServer{}, codexMCPInvalid(fmt.Errorf("mcp server %d: %w", index, err))
	}
	for key, name := range envHeaders {
		if err := codexMCPHeaderReference(name, options); err != nil {
			return agent.MCPServer{}, codexMCPInvalid(fmt.Errorf("mcp server %d: envHeaders %q: %w", index, key, err))
		}
	}
	if bearerTokenEnvVar != "" {
		if err := codexMCPHeaderReference(bearerTokenEnvVar, options); err != nil {
			return agent.MCPServer{}, codexMCPInvalid(fmt.Errorf("mcp server %d: bearerTokenEnvVar: %w", index, err))
		}
	}
	return agent.MCPServer{
		Name:              server.Name,
		Transport:         agent.MCPTransportHTTP,
		URL:               server.URL,
		Headers:           headers,
		EnvHeaders:        envHeaders,
		BearerTokenEnvVar: bearerTokenEnvVar,
	}, nil
}

func codexMCPEnvironment(index int, env map[string]string, options runtime.ProcessConfig) (map[string]string, error) {
	resolved := maps.Clone(env)
	for _, key := range slices.Sorted(maps.Keys(env)) {
		value, err := codexMCPResolveValue(env[key], options)
		if err != nil {
			return nil, codexMCPInvalid(fmt.Errorf("mcp server %d: env %q: %w", index, key, err))
		}
		resolved[key] = value
	}
	return resolved, nil
}

func codexMCPResolveValue(value string, options runtime.ProcessConfig) (string, error) {
	if !strings.Contains(value, "${") {
		return value, nil
	}
	matches := codexMCPEnvironmentReference.FindAllStringSubmatchIndex(value, -1)
	if len(matches) == 0 {
		return "", errors.New("contains an unresolved environment reference")
	}
	var resolved strings.Builder
	last := 0
	for _, match := range matches {
		if strings.Contains(value[last:match[0]], "${") {
			return "", errors.New("contains an unresolved environment reference")
		}
		resolved.WriteString(value[last:match[0]])
		replacement, err := codexMCPReference(value[match[2]:match[3]], options)
		if err != nil {
			return "", err
		}
		resolved.WriteString(replacement)
		last = match[1]
	}
	if strings.Contains(value[last:], "${") {
		return "", errors.New("contains an unresolved environment reference")
	}
	resolved.WriteString(value[last:])
	return resolved.String(), nil
}

func codexMCPHeaderReference(name string, options runtime.ProcessConfig) error {
	value, err := codexMCPReference(name, options)
	if err != nil {
		return err
	}
	if strings.ContainsAny(value, "\r\n") {
		return fmt.Errorf("environment variable %q has an invalid header value", name)
	}
	return nil
}

func codexMCPReference(name string, options runtime.ProcessConfig) (string, error) {
	return runtime.LookupMCPEnvironmentReference(options.LookupEnv, name)
}

func validateCodexHeaderNames(headers, envHeaders map[string]string) error {
	seen := make(map[string]struct{}, len(headers)+len(envHeaders))
	for _, values := range []map[string]string{headers, envHeaders} {
		for key := range values {
			canonical := strings.ToLower(key)
			if _, ok := seen[canonical]; ok {
				return fmt.Errorf("header %q is declared more than once", key)
			}
			seen[canonical] = struct{}{}
		}
	}
	return nil
}

func validateCodexAuthorizationConflict(headers, envHeaders map[string]string, bearerTokenEnvVar string) error {
	if bearerTokenEnvVar == "" {
		return nil
	}
	for _, values := range []map[string]string{headers, envHeaders} {
		for key := range values {
			if strings.EqualFold(key, "Authorization") {
				return errors.New("authorization header conflicts with bearerTokenEnvVar")
			}
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
