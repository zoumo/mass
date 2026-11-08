package runtime

import (
	"fmt"
	"maps"
	"path/filepath"
	"slices"
	"strings"
	"unicode/utf8"
)

// MCPServer is the provider-neutral Session MCP configuration. Runtime records
// may provide defaults, but only Session opening or recovery consumes them.
type MCPServer struct {
	Name              string            `json:"name"`
	Type              string            `json:"type" enum:"stdio,http,sse"`
	Command           string            `json:"command,omitempty"`
	Args              []string          `json:"args,omitempty"`
	Env               map[string]string `json:"env,omitempty"`
	Cwd               string            `json:"cwd,omitempty"`
	URL               string            `json:"url,omitempty"`
	Headers           map[string]string `json:"headers,omitempty"`
	EnvHeaders        map[string]string `json:"envHeaders,omitempty"`
	BearerTokenEnvVar string            `json:"bearerTokenEnvVar,omitempty"`
}

// SessionDefaults are Runtime-provided defaults for Session-only options.
type SessionDefaults struct {
	Features   *SessionFeatures `json:"features,omitempty"`
	MCPServers *[]MCPServer     `json:"mcpServers,omitempty"`
}

// Validate checks the provider-neutral Session defaults stored on a Runtime.
func (d SessionDefaults) Validate() error {
	if d.MCPServers == nil {
		return nil
	}
	return ValidateMCPServers(*d.MCPServers)
}

// ResolveSessionSpec applies Runtime defaults without changing Session-level
// explicit values. Feature fields merge by field, while MCP servers merge and
// replace by name.
func (d SessionDefaults) ResolveSessionSpec(spec SessionSpec) SessionSpec {
	resolved := spec
	resolved.AdditionalDirs = slices.Clone(spec.AdditionalDirs)
	resolved.Features = MergeSessionFeatures(d.Features, spec.Features)
	resolved.MCPServers = MergeSessionMCPServers(d.MCPServers, spec.MCPServers)
	return resolved
}

func CloneSessionFeatures(features *SessionFeatures) *SessionFeatures {
	if features == nil {
		return nil
	}
	cloned := *features
	if features.AgentQuestions != nil {
		cloned.AgentQuestions = Bool(*features.AgentQuestions)
	}
	return &cloned
}

func MergeSessionFeatures(defaults, overrides *SessionFeatures) *SessionFeatures {
	if defaults == nil {
		return CloneSessionFeatures(overrides)
	}
	merged := *defaults
	if overrides != nil && overrides.AgentQuestions != nil {
		merged.AgentQuestions = Bool(*overrides.AgentQuestions)
	}
	if merged.AgentQuestions != nil {
		merged.AgentQuestions = Bool(*merged.AgentQuestions)
	}
	return &merged
}

func MergeSessionMCPServers(defaults, overrides *[]MCPServer) *[]MCPServer {
	if defaults == nil && overrides == nil {
		return nil
	}
	merged := []MCPServer{}
	if defaults != nil {
		merged = CloneMCPServers(*defaults)
	}
	index := make(map[string]int, len(merged))
	for i, server := range merged {
		index[server.Name] = i
	}
	if overrides != nil {
		for _, server := range *overrides {
			cloned := CloneMCPServers([]MCPServer{server})[0]
			if i, ok := index[server.Name]; ok {
				merged[i] = cloned
				continue
			}
			index[server.Name] = len(merged)
			merged = append(merged, cloned)
		}
	}
	return &merged
}

func CloneMCPServers(servers []MCPServer) []MCPServer {
	if servers == nil {
		return nil
	}
	cloned := make([]MCPServer, len(servers))
	for i, server := range servers {
		cloned[i] = server
		cloned[i].Args = slices.Clone(server.Args)
		cloned[i].Env = maps.Clone(server.Env)
		cloned[i].Headers = maps.Clone(server.Headers)
		cloned[i].EnvHeaders = maps.Clone(server.EnvHeaders)
	}
	return cloned
}

// ValidateMCPServers checks the provider-neutral shape before native side
// effects. Provider-specific field and transport support is checked by adapters.
func ValidateMCPServers(servers []MCPServer) error {
	seen := make(map[string]struct{}, len(servers))
	for i, server := range servers {
		if server.Name == "" || !utf8.ValidString(server.Name) {
			return fmt.Errorf("mcp server %d: name must be nonempty valid UTF-8", i)
		}
		if _, ok := seen[server.Name]; ok {
			return fmt.Errorf("mcp server %d: name must be unique", i)
		}
		seen[server.Name] = struct{}{}
		if err := validateMCPServerText(i, server); err != nil {
			return err
		}
		switch server.Type {
		case "stdio":
			if server.Command == "" {
				return fmt.Errorf("mcp server %d: stdio requires command", i)
			}
			switch {
			case server.URL != "":
				return fmt.Errorf("mcp server %d: stdio does not allow url", i)
			case len(server.Headers) > 0:
				return fmt.Errorf("mcp server %d: stdio does not allow headers", i)
			case len(server.EnvHeaders) > 0:
				return fmt.Errorf("mcp server %d: stdio does not allow envHeaders", i)
			case server.BearerTokenEnvVar != "":
				return fmt.Errorf("mcp server %d: stdio does not allow bearerTokenEnvVar", i)
			}
			if server.Cwd != "" && !filepath.IsAbs(server.Cwd) {
				return fmt.Errorf("mcp server %d: cwd must be absolute", i)
			}
		case "http", "sse":
			if server.URL == "" {
				return fmt.Errorf("mcp server %d: %s requires url", i, server.Type)
			}
			switch {
			case server.Command != "":
				return fmt.Errorf("mcp server %d: %s does not allow command", i, server.Type)
			case len(server.Args) > 0:
				return fmt.Errorf("mcp server %d: %s does not allow args", i, server.Type)
			case len(server.Env) > 0:
				return fmt.Errorf("mcp server %d: %s does not allow env", i, server.Type)
			case server.Cwd != "":
				return fmt.Errorf("mcp server %d: %s does not allow cwd", i, server.Type)
			}
		default:
			return fmt.Errorf("mcp server %d: type must be stdio, http or sse", i)
		}
	}
	return nil
}

func validateMCPServerText(index int, server MCPServer) error {
	for _, field := range []struct {
		name  string
		value string
	}{
		{"command", server.Command},
		{"cwd", server.Cwd},
		{"url", server.URL},
		{"bearerTokenEnvVar", server.BearerTokenEnvVar},
	} {
		if !utf8.ValidString(field.value) || strings.ContainsRune(field.value, 0) {
			return fmt.Errorf("mcp server %d: %s must be valid UTF-8 without NUL", index, field.name)
		}
	}
	for argIndex, arg := range server.Args {
		if !utf8.ValidString(arg) || strings.ContainsRune(arg, 0) {
			return fmt.Errorf("mcp server %d: args[%d] must be valid UTF-8 without NUL", index, argIndex)
		}
	}
	for _, field := range []struct {
		name   string
		values map[string]string
	}{
		{"env", server.Env},
		{"headers", server.Headers},
		{"envHeaders", server.EnvHeaders},
	} {
		for key, value := range field.values {
			if key == "" || !utf8.ValidString(key) || !utf8.ValidString(value) || strings.ContainsAny(key, "=\x00") || strings.ContainsRune(value, 0) {
				return fmt.Errorf("mcp server %d: %s contains an invalid entry", index, field.name)
			}
		}
	}
	if server.BearerTokenEnvVar != "" && strings.ContainsAny(server.BearerTokenEnvVar, "=\x00") {
		return fmt.Errorf("mcp server %d: bearerTokenEnvVar must name an environment variable", index)
	}
	return nil
}
