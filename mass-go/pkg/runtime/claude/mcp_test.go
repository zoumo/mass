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
	process := runtime.ProcessConfig{Env: map[string]string{"TOKEN": "secret-value"}}
	converted, err := claudeMCPServers(&servers, process)
	require.NoError(t, err)
	require.Len(t, converted, 3)
	require.Equal(t, sdkClaudeMCPTransport("stdio"), converted[0].Transport)
	require.Equal(t, sdkClaudeMCPTransport("http"), converted[1].Transport)
	require.Equal(t, sdkClaudeMCPTransport("sse"), converted[2].Transport)
	require.Equal(t, "${TOKEN}", converted[0].Env["TOKEN"])
	require.Equal(t, "Bearer ${TOKEN}", converted[1].Headers["Authorization"])
}

func TestClaudeExplicitMCPListUsesStrictConfig(t *testing.T) {
	empty := []runtime.MCPServer{}
	process := runtime.ProcessConfig{Executable: "/agent"}
	options, err := sessionOptions(runtime.SessionFeatures{}, &empty, "", "/work", process)
	require.NoError(t, err)
	require.True(t, options.StrictMCPConfig)

	options, err = sessionOptions(runtime.SessionFeatures{}, nil, "", "/work", process)
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
		_, err := claudeMCPServers(&servers, runtime.ProcessConfig{})
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
	_, err := claudeMCPServers(&servers, runtime.ProcessConfig{})
	require.ErrorIs(t, err, runtime.ErrInvalidArgument)
}

func TestClaudeMCPEnvironmentReferencesFailBeforeOpen(t *testing.T) {
	for _, test := range []struct {
		name  string
		env   map[string]string
		value string
	}{
		{name: "missing", value: "Bearer ${MISSING}"},
		{name: "empty", env: map[string]string{"TOKEN": ""}, value: "Bearer ${TOKEN}"},
		{name: "unresolved", env: map[string]string{"TOKEN": "sensitive-value"}, value: "Bearer ${TOKEN:-fallback}"},
	} {
		t.Run(test.name, func(t *testing.T) {
			servers := []runtime.MCPServer{{
				Name:    "remote",
				Type:    "http",
				URL:     "https://example.invalid/mcp",
				Headers: map[string]string{"Authorization": test.value},
			}}
			_, err := claudeMCPServers(&servers, runtime.ProcessConfig{Env: test.env})
			require.ErrorIs(t, err, runtime.ErrInvalidArgument)
			require.NotContains(t, err.Error(), "sensitive-value")
		})
	}
}

func TestClaudeMCPStdioEnvReferenceFailsBeforeOpen(t *testing.T) {
	servers := []runtime.MCPServer{{
		Name:    "local",
		Type:    "stdio",
		Command: "/mcp-server",
		Env:     map[string]string{"TOKEN": "${MISSING}"},
	}}
	_, err := claudeMCPServers(&servers, runtime.ProcessConfig{})
	require.ErrorIs(t, err, runtime.ErrInvalidArgument)
	require.NotContains(t, err.Error(), "${MISSING}")
}

func TestClaudeMCPResolvedHeaderRejectsNewlines(t *testing.T) {
	servers := []runtime.MCPServer{{
		Name:    "remote",
		Type:    "http",
		URL:     "https://example.invalid/mcp",
		Headers: map[string]string{"Authorization": "Bearer ${TOKEN}"},
	}}
	_, err := claudeMCPServers(&servers, runtime.ProcessConfig{Env: map[string]string{"TOKEN": "a\r\nb"}})
	require.ErrorIs(t, err, runtime.ErrInvalidArgument)
	require.NotContains(t, err.Error(), "a\r\nb")
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
