package codex

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/zoumo/mass/mass-go/pkg/runtime"
)

func TestCodexMCPConfigurationMapping(t *testing.T) {
	servers := []runtime.MCPServer{
		{
			Name:    "local",
			Type:    "stdio",
			Command: "/mcp-server",
			Args:    []string{"--stdio"},
			Env:     map[string]string{"TOKEN": "value"},
			Cwd:     "/work",
		},
		{
			Name:              "remote",
			Type:              "http",
			URL:               "https://example.invalid/mcp",
			Headers:           map[string]string{"Authorization": "Bearer ${TOKEN}"},
			EnvHeaders:        map[string]string{"X-Token": "TOKEN"},
			BearerTokenEnvVar: "TOKEN",
		},
	}
	converted, err := codexMCPServers(&servers)
	require.NoError(t, err)
	require.Len(t, converted, 2)
	require.Equal(t, "/mcp-server", converted[0].Command)
	require.Equal(t, "/work", converted[0].Cwd)
	require.Equal(t, "https://example.invalid/mcp", converted[1].URL)
	require.Equal(t, "TOKEN", converted[1].BearerTokenEnvVar)
}

func TestCodexMCPConfigurationRejectsUnsupportedProviderFeatures(t *testing.T) {
	for _, server := range []runtime.MCPServer{
		{Name: "dotted.name", Type: "stdio", Command: "/mcp-server"},
		{Name: "sse", Type: "sse", URL: "https://example.invalid/mcp"},
	} {
		servers := []runtime.MCPServer{server}
		_, err := codexMCPServers(&servers)
		require.Error(t, err)
	}
}

func TestCodexSessionDefaultsAreNotRuntimeConfig(t *testing.T) {
	config := runtime.Config{
		"executable": "/agent",
		"mcpServers": []any{map[string]any{
			"name": "remote",
			"type": "http",
			"url":  "https://example.invalid/mcp",
		}},
	}
	require.ErrorContains(t, (Factory{}).Validate(config), `unknown process config "mcpServers"`)
}
