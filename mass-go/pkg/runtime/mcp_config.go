package runtime

import (
	"fmt"
	"net"
	"net/url"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/zoumo/agent-sdk-go/mcp"
)

// MCPTransport is the provider-neutral MCP transport defined by the agent SDK.
// MASS keeps its own JSON field name ("type"), but not a second enum.
type MCPTransport = mcp.Transport

const (
	MCPTransportStdio = mcp.TransportStdio
	MCPTransportHTTP  = mcp.TransportHTTP
	MCPTransportSSE   = mcp.TransportSSE
)

// MCPServer is the provider-neutral Session MCP configuration. Runtime records
// may provide defaults, but only Session opening or recovery consumes them.
type MCPServer struct {
	Name              string             `json:"name" minLength:"1"`
	Type              MCPTransport       `json:"type" enum:"stdio,http,sse"`
	Command           string             `json:"command,omitempty"`
	Args              []string           `json:"args,omitempty"`
	Env               []mcp.NamedValue   `json:"env,omitempty"`
	EnvFrom           []mcp.ImportSource `json:"envFrom,omitempty"`
	Cwd               string             `json:"cwd,omitempty"`
	URL               string             `json:"url,omitempty"`
	Headers           []mcp.NamedValue   `json:"headers,omitempty"`
	HeadersFrom       []mcp.ImportSource `json:"headersFrom,omitempty"`
	EnvHeaders        map[string]string  `json:"envHeaders,omitempty"`
	BearerTokenEnvVar string             `json:"bearerTokenEnvVar,omitempty"`
}

// SDKServer maps the persisted Session shape to an independent SDK value.
// MASS keeps the wire field name "type".
func (s MCPServer) SDKServer() mcp.Server {
	return mcp.CloneServers([]mcp.Server{s.sdkServer()})[0]
}

func (s MCPServer) sdkServer() mcp.Server {
	return mcp.Server{
		Name:              s.Name,
		Transport:         s.Type,
		Command:           s.Command,
		Args:              s.Args,
		Env:               s.Env,
		EnvFrom:           s.EnvFrom,
		Cwd:               s.Cwd,
		URL:               s.URL,
		Headers:           s.Headers,
		HeadersFrom:       s.HeadersFrom,
		EnvHeaders:        s.EnvHeaders,
		BearerTokenEnvVar: s.BearerTokenEnvVar,
	}
}

func sdkMCPServers(servers []MCPServer) []mcp.Server {
	if servers == nil {
		return nil
	}
	converted := make([]mcp.Server, len(servers))
	for i, server := range servers {
		converted[i] = server.sdkServer()
	}
	return converted
}

// ValidateMCPServers checks the persisted configuration without accessing files.
func ValidateMCPServers(servers []MCPServer) error {
	if err := mcp.ValidateServers(sdkMCPServers(servers)); err != nil {
		return err
	}
	return validateMCPPolicy(servers)
}

// ResolveMCPServers reads files for one native open, resume, or load. It keeps
// the original configuration intact for persistence and source attribution.
func ResolveMCPServers(servers []MCPServer) ([]mcp.Server, error) {
	if err := validateMCPPolicy(servers); err != nil {
		return nil, err
	}
	return mcp.ResolveServers(sdkMCPServers(servers), nil)
}

// MCPDirectValues indexes explicit value entries. A resolved entry without a
// matching name came from a file, and its contents are opaque to interpolation.
func MCPDirectValues(entries []mcp.NamedValue) map[string]struct{} {
	names := make(map[string]struct{}, len(entries))
	for _, entry := range entries {
		if entry.Value != nil {
			names[entry.Name] = struct{}{}
		}
	}
	return names
}

func validateMCPPolicy(servers []MCPServer) error {
	for i, server := range servers {
		if err := validateMCPServerText(i, server); err != nil {
			return err
		}
		switch server.Type {
		case MCPTransportStdio:
			if server.Cwd != "" && !filepath.IsAbs(server.Cwd) {
				return fmt.Errorf("mcp server %d: cwd must be absolute", i)
			}
		case MCPTransportHTTP, MCPTransportSSE:
			if err := validateMCPURL(i, server); err != nil {
				return err
			}
			if server.Type == MCPTransportSSE && (len(server.EnvHeaders) > 0 || server.BearerTokenEnvVar != "") {
				return fmt.Errorf("mcp server %d: sse does not allow envHeaders or bearerTokenEnvVar", i)
			}
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
	} {
		if strings.ContainsRune(field.value, 0) {
			return fmt.Errorf("mcp server %d: %s must not contain NUL", index, field.name)
		}
	}
	for argIndex, arg := range server.Args {
		if strings.ContainsRune(arg, 0) {
			return fmt.Errorf("mcp server %d: args[%d] must not contain NUL", index, argIndex)
		}
	}
	for _, entry := range server.Env {
		if entry.Value != nil && IsSensitiveMCPName(entry.Name) && !mcpSecretReference.MatchString(*entry.Value) {
			return fmt.Errorf("mcp server %d: env %q must reference a secret", index, entry.Name)
		}
	}
	for key, value := range server.EnvHeaders {
		if key == "" || !utf8.ValidString(key) || strings.ContainsAny(key, "=\x00") {
			return fmt.Errorf("mcp server %d: envHeaders contains an invalid entry", index)
		}
		if !mcpEnvironmentName.MatchString(value) {
			return fmt.Errorf("mcp server %d: envHeaders %q must name an environment variable", index, key)
		}
	}
	if server.BearerTokenEnvVar != "" && !mcpEnvironmentName.MatchString(server.BearerTokenEnvVar) {
		return fmt.Errorf("mcp server %d: bearerTokenEnvVar must name an environment variable", index)
	}
	return nil
}

func validateMCPURL(index int, server MCPServer) error {
	u, err := url.Parse(server.URL)
	if err != nil || u.Host == "" || u.User != nil {
		return fmt.Errorf("mcp server %d: %s requires a url without credentials", index, server.Type)
	}
	switch strings.ToLower(u.Scheme) {
	case "https":
		return nil
	case "http":
		if loopbackHost(u.Hostname()) {
			return nil
		}
		return fmt.Errorf("mcp server %d: non-loopback http is not allowed", index)
	default:
		return fmt.Errorf("mcp server %d: url must use https, or http on loopback", index)
	}
}

func loopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
