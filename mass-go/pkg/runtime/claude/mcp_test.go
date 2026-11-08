package claude

import (
	"testing"

	"github.com/stretchr/testify/require"
	sdk "github.com/zoumo/agent-sdk-go/claude"

	"github.com/zoumo/mass/mass-go/pkg/runtime"
)

func TestClaudeMCPConfigurationMapping(t *testing.T) {
	servers := []runtime.MCPServer{
		{
			Name:    "local",
			Type:    "stdio",
			Command: "/mcp-server",
			Args:    []string{"--stdio"},
			Env:     map[string]string{"TOKEN": "${TOKEN}"},
		},
		{
			Name:    "remote",
			Type:    "http",
			URL:     "https://example.invalid/http",
			Headers: map[string]string{"Authorization": "Bearer ${TOKEN}"},
		},
		{Name: "events", Type: "sse", URL: "https://example.invalid/sse"},
	}
	converted, err := claudeMCPServers(&servers)
	require.NoError(t, err)
	require.Len(t, converted, 3)
	require.Equal(t, sdkClaudeMCPTransport("stdio"), converted[0].Transport)
	require.Equal(t, sdkClaudeMCPTransport("http"), converted[1].Transport)
	require.Equal(t, sdkClaudeMCPTransport("sse"), converted[2].Transport)
}

func TestClaudeExplicitMCPListUsesStrictConfig(t *testing.T) {
	empty := []runtime.MCPServer{}
	options, err := sessionOptions(runtime.SessionFeatures{}, &empty, "", "/work", "/agent", nil)
	require.NoError(t, err)
	require.True(t, options.StrictMCPConfig)

	options, err = sessionOptions(runtime.SessionFeatures{}, nil, "", "/work", "/agent", nil)
	require.NoError(t, err)
	require.False(t, options.StrictMCPConfig)
}

func TestClaudeMCPConfigurationRejectsCodexOnlyFields(t *testing.T) {
	for _, server := range []runtime.MCPServer{
		{Name: "local", Type: "stdio", Command: "/mcp-server", Cwd: "/work"},
		{Name: "remote", Type: "http", URL: "https://example.invalid/mcp", EnvHeaders: map[string]string{"X-Token": "TOKEN"}},
		{Name: "remote", Type: "http", URL: "https://example.invalid/mcp", BearerTokenEnvVar: "TOKEN"},
	} {
		servers := []runtime.MCPServer{server}
		_, err := claudeMCPServers(&servers)
		require.Error(t, err)
		var public *runtime.OperationError
		require.ErrorAs(t, err, &public)
		require.Equal(t, "unsupported", public.Code)
	}
}

func TestClaudeMCPConfigurationClassifiesPublicValidation(t *testing.T) {
	servers := []runtime.MCPServer{{
		Type:    "stdio",
		Command: "/mcp-server",
	}}
	_, err := claudeMCPServers(&servers)
	require.ErrorIs(t, err, runtime.ErrInvalidArgument)
}

func TestClaudeSessionDefaultsAreNotRuntimeConfig(t *testing.T) {
	config := runtime.Config{
		"executable": "/agent",
		"features":   map[string]any{"agentQuestions": false},
	}
	require.ErrorContains(t, (Factory{}).Validate(config), `unknown process config "features"`)
}

func sdkClaudeMCPTransport(value string) sdk.MCPTransport {
	return sdk.MCPTransport(value)
}
