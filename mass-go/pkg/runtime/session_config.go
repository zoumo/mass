package runtime

import (
	"maps"
	"slices"
)

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
	if spec.Config != nil {
		config := *spec.Config
		resolved.Config = &config
	}
	resolved.AdditionalDirs = slices.Clone(spec.AdditionalDirs)
	resolved.Features = MergeSessionFeatures(d.Features, spec.Features)
	resolved.MCPServers = MergeSessionMCPServers(d.MCPServers, spec.MCPServers)
	return resolved
}

func MergeSessionMCPServers(defaults, overrides *[]MCPServer) *[]MCPServer {
	if defaults == nil && (overrides == nil || len(*overrides) == 0) {
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
