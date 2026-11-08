package runtime

import (
	"fmt"
	"net"
	"net/url"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

// MCPTransport is one provider-neutral MCP server transport.
type MCPTransport string

const (
	MCPTransportStdio MCPTransport = "stdio"
	MCPTransportHTTP  MCPTransport = "http"
	MCPTransportSSE   MCPTransport = "sse"
)

func (t MCPTransport) valid() bool {
	switch t {
	case MCPTransportStdio, MCPTransportHTTP, MCPTransportSSE:
		return true
	default:
		return false
	}
}

// MCPServer is the provider-neutral Session MCP configuration. Runtime records
// may provide defaults, but only Session opening or recovery consumes them.
type MCPServer struct {
	Name              string            `json:"name" minLength:"1"`
	Type              MCPTransport      `json:"type" enum:"stdio,http,sse"`
	Command           string            `json:"command,omitempty"`
	Args              []string          `json:"args,omitempty"`
	Env               map[string]string `json:"env,omitempty"`
	Cwd               string            `json:"cwd,omitempty"`
	URL               string            `json:"url,omitempty"`
	Headers           map[string]string `json:"headers,omitempty"`
	EnvHeaders        map[string]string `json:"envHeaders,omitempty"`
	BearerTokenEnvVar string            `json:"bearerTokenEnvVar,omitempty"`
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
		if !server.Type.valid() {
			return fmt.Errorf("mcp server %d: type must be stdio, http or sse", i)
		}
		if server.Type == MCPTransportStdio {
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
			continue
		}
		if server.URL == "" {
			return fmt.Errorf("mcp server %d: %s requires url", i, server.Type)
		}
		if err := validateMCPURL(i, server); err != nil {
			return err
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
		if server.Type == MCPTransportSSE && (len(server.EnvHeaders) > 0 || server.BearerTokenEnvVar != "") {
			return fmt.Errorf("mcp server %d: sse does not allow envHeaders or bearerTokenEnvVar", i)
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
	for key, value := range server.Env {
		if IsSensitiveMCPName(key) && !mcpSecretReference.MatchString(value) {
			return fmt.Errorf("mcp server %d: env %q must reference a secret", index, key)
		}
	}
	for key, value := range server.Headers {
		if IsSensitiveMCPName(key) && !mcpSecretReference.MatchString(value) {
			return fmt.Errorf("mcp server %d: header %q must reference a secret", index, key)
		}
	}
	for key, value := range server.EnvHeaders {
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
