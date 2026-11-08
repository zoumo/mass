package codex

import (
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/pelletier/go-toml/v2"
	sdk "github.com/zoumo/agent-sdk-go/codex"

	"github.com/zoumo/mass/mass-go/pkg/runtime"
)

var codexMCPEnvironmentReference = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)\}`)

func codexSessionMCPConfig(configured *[]runtime.MCPServer, cwd string, options runtime.ProcessConfig) ([]sdk.MCPServer, map[string]json.RawMessage, error) {
	servers, err := codexMCPServers(configured, options)
	if err != nil {
		return nil, nil, err
	}
	if configured == nil {
		return servers, nil, nil
	}
	projectConfig, err := codexMCPProjectConfig(cwd)
	if err != nil {
		return nil, nil, err
	}
	return servers, projectConfig, nil
}

func codexMCPServers(configured *[]runtime.MCPServer, options runtime.ProcessConfig) ([]sdk.MCPServer, error) {
	if configured == nil {
		return nil, nil
	}
	servers := *configured
	if err := runtime.ValidateMCPServers(servers); err != nil {
		return nil, codexMCPInvalid(err)
	}
	if err := validateCodexMCPIsolation(options); err != nil {
		return nil, err
	}
	converted := make([]sdk.MCPServer, 0, len(servers))
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
			converted = append(converted, sdk.MCPServer{
				Name:    server.Name,
				Command: server.Command,
				Args:    append([]string(nil), server.Args...),
				Env:     env,
				Cwd:     server.Cwd,
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

func validateCodexMCPIsolation(options runtime.ProcessConfig) error {
	home, explicit := options.Env["CODEX_HOME"]
	if !explicit || !filepath.IsAbs(home) {
		return codexMCPUnsupported("mcp_isolation_unavailable", errors.New("codex mcpServers require explicit absolute env.CODEX_HOME"))
	}
	for _, ambient := range codexAmbientHomes(options) {
		if sameCodexHome(home, ambient) {
			return codexMCPUnsupported("mcp_isolation_unavailable", errors.New("codex mcpServers require an isolated CODEX_HOME"))
		}
	}
	info, err := os.Stat(home)
	if err != nil || !info.IsDir() {
		return codexMCPUnsupported("mcp_isolation_unavailable", errors.New("codex mcpServers require an existing CODEX_HOME directory"))
	}
	b, err := os.ReadFile(filepath.Join(home, "config.toml"))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return codexMCPUnsupported("mcp_isolation_unavailable", errors.New("codex mcpServers cannot read CODEX_HOME config.toml"))
	}
	var config map[string]any
	if err := toml.Unmarshal(b, &config); err != nil {
		return codexMCPUnsupported("mcp_isolation_unavailable", errors.New("codex mcpServers require valid CODEX_HOME config.toml"))
	}
	if containsMCPConfig(config) {
		return codexMCPUnsupported("mcp_isolation_unavailable", errors.New("codex mcpServers require CODEX_HOME without ambient mcp_servers"))
	}
	return nil
}

func codexHTTPMCPServer(index int, server runtime.MCPServer, options runtime.ProcessConfig) (sdk.MCPServer, error) {
	headers := maps.Clone(server.Headers)
	envHeaders := maps.Clone(server.EnvHeaders)
	if envHeaders == nil {
		envHeaders = map[string]string{}
	}
	if err := validateCodexHeaderNames(headers, envHeaders); err != nil {
		return sdk.MCPServer{}, codexMCPInvalid(fmt.Errorf("mcp server %d: %w", index, err))
	}
	bearerTokenEnvVar := server.BearerTokenEnvVar
	for _, key := range slices.Sorted(maps.Keys(headers)) {
		raw := headers[key]
		if strings.ContainsAny(raw, "\r\n") {
			return sdk.MCPServer{}, codexMCPInvalid(fmt.Errorf("mcp server %d: header %q contains an invalid value", index, key))
		}
		prefix, name, exact := runtime.ParseMCPSecretReference(raw)
		if exact {
			if err := codexMCPHeaderReference(name, options); err != nil {
				return sdk.MCPServer{}, codexMCPInvalid(fmt.Errorf("mcp server %d: header %q: %w", index, key, err))
			}
			if runtime.IsSensitiveMCPName(key) && strings.EqualFold(key, "Authorization") && strings.EqualFold(prefix, "Bearer") {
				if bearerTokenEnvVar != "" && bearerTokenEnvVar != name {
					return sdk.MCPServer{}, codexMCPInvalid(fmt.Errorf("mcp server %d: conflicting Authorization bearer references", index))
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
			return sdk.MCPServer{}, codexMCPInvalid(fmt.Errorf("mcp server %d: header %q: %w", index, key, err))
		}
		if strings.ContainsAny(value, "\r\n") {
			return sdk.MCPServer{}, codexMCPInvalid(fmt.Errorf("mcp server %d: header %q contains an invalid value", index, key))
		}
		headers[key] = value
	}
	if err := validateCodexAuthorizationConflict(headers, envHeaders, bearerTokenEnvVar); err != nil {
		return sdk.MCPServer{}, codexMCPInvalid(fmt.Errorf("mcp server %d: %w", index, err))
	}
	for key, name := range envHeaders {
		if err := codexMCPHeaderReference(name, options); err != nil {
			return sdk.MCPServer{}, codexMCPInvalid(fmt.Errorf("mcp server %d: envHeaders %q: %w", index, key, err))
		}
	}
	if bearerTokenEnvVar != "" {
		if err := codexMCPHeaderReference(bearerTokenEnvVar, options); err != nil {
			return sdk.MCPServer{}, codexMCPInvalid(fmt.Errorf("mcp server %d: bearerTokenEnvVar: %w", index, err))
		}
	}
	return sdk.MCPServer{
		Name:              server.Name,
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

// codexMCPProjectConfig disables project configuration layers for the session
// cwd and its ancestors. Codex would otherwise auto-trust a writable project on
// thread start and load MCP servers from its .codex/config.toml.
func codexMCPProjectConfig(cwd string) (map[string]json.RawMessage, error) {
	projects := make(map[string]map[string]string)
	for _, path := range codexMCPProjectPaths(cwd) {
		projects[path] = map[string]string{"trust_level": "untrusted"}
	}
	raw, err := json.Marshal(projects)
	if err != nil {
		return nil, err
	}
	return map[string]json.RawMessage{"projects": raw}, nil
}

func codexMCPProjectPaths(cwd string) []string {
	seen := map[string]struct{}{}
	addAncestors := func(path string) {
		for {
			path = filepath.Clean(path)
			seen[path] = struct{}{}
			parent := filepath.Dir(path)
			if parent == path {
				return
			}
			path = parent
		}
	}
	addAncestors(cwd)
	if canonical, err := filepath.EvalSymlinks(cwd); err == nil {
		addAncestors(canonical)
	}
	return slices.Sorted(maps.Keys(seen))
}

func codexAmbientHomes(options runtime.ProcessConfig) []string {
	homes := []string{}
	if home := os.Getenv("CODEX_HOME"); home != "" {
		homes = append(homes, home)
	}
	if home := os.Getenv("HOME"); home != "" {
		homes = append(homes, filepath.Join(home, ".codex"))
	}
	childHome := options.Env["HOME"]
	if childHome == "" && options.EnvPolicy != "replace" {
		childHome = os.Getenv("HOME")
	}
	if childHome != "" {
		homes = append(homes, filepath.Join(childHome, ".codex"))
	}
	if userHome, err := os.UserHomeDir(); err == nil {
		homes = append(homes, filepath.Join(userHome, ".codex"))
	}
	return homes
}

func sameCodexHome(a, b string) bool {
	if filepath.Clean(a) == filepath.Clean(b) {
		return true
	}
	infoA, errA := os.Stat(a)
	infoB, errB := os.Stat(b)
	return errA == nil && errB == nil && os.SameFile(infoA, infoB)
}

func containsMCPConfig(value any) bool {
	switch typed := value.(type) {
	case map[string]any:
		for key, child := range typed {
			if key == "mcp_servers" || containsMCPConfig(child) {
				return true
			}
		}
	case []any:
		return slices.ContainsFunc(typed, containsMCPConfig)
	}
	return false
}
