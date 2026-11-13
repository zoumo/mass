package claude

import (
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/zoumo/agent-sdk-go/mcp"

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
	}
	process := runtime.ProcessConfig{Env: map[string]string{"TOKEN": "secret-value"}}
	converted, err := claudeMCPServers(&servers, process)
	require.NoError(t, err)
	require.Len(t, converted, 2)
	require.Equal(t, mcp.TransportStdio, converted[0].Transport)
	require.Equal(t, mcp.TransportHTTP, converted[1].Transport)
	require.Equal(t, "${TOKEN}", converted[0].Env["TOKEN"])
	require.Equal(t, "Bearer ${TOKEN}", converted[1].Headers["Authorization"])
}

func TestClaudeMCPEmptyAndAbsentListsAreSDKEquivalent(t *testing.T) {
	empty := []runtime.MCPServer{}
	converted, err := claudeMCPServers(&empty, runtime.ProcessConfig{})
	require.NoError(t, err)
	require.Empty(t, converted)
	converted, err = claudeMCPServers(nil, runtime.ProcessConfig{})
	require.NoError(t, err)
	require.Nil(t, converted)
}

func TestClaudeMCPRejectsSSEBeforeNativeEffects(t *testing.T) {
	servers := []runtime.MCPServer{{Name: "events", Type: "sse", URL: "https://example.invalid/sse"}}
	_, err := claudeMCPServers(&servers, runtime.ProcessConfig{})
	var public *runtime.OperationError
	require.ErrorAs(t, err, &public)
	require.Equal(t, "unsupported", public.Code)
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
	for _, value := range []string{"a\rb", "a\nb", "a\r\nb"} {
		servers := []runtime.MCPServer{{
			Name:    "remote",
			Type:    "http",
			URL:     "https://example.invalid/mcp",
			Headers: map[string]string{"Authorization": "Bearer ${TOKEN}"},
		}}
		_, err := claudeMCPServers(&servers, runtime.ProcessConfig{Env: map[string]string{"TOKEN": value}})
		require.ErrorIs(t, err, runtime.ErrInvalidArgument)
		require.NotContains(t, err.Error(), value)
	}
}

func TestClaudeMCPSecretReferenceRejectedBeforeNativeOpen(t *testing.T) {
	driver, err := (Factory{RecoveryDir: t.TempDir()}).New("agent", runtime.Config{
		"executable": "/nonexistent/claude",
		"envPolicy":  "replace",
	})
	require.NoError(t, err)
	_, err = driver.Start(t.Context())
	require.NoError(t, err)

	servers := []runtime.MCPServer{{
		Name:    "remote",
		Type:    "http",
		URL:     "https://example.invalid/mcp",
		Headers: map[string]string{"Authorization": "Bearer ${MISSING}"},
	}}
	_, err = driver.OpenSession(t.Context(), runtime.Address{Namespace: "one", SessionName: "session"}, runtime.SessionSpec{
		Cwd: "/work", MCPServers: &servers,
	})
	require.ErrorIs(t, err, runtime.ErrInvalidArgument)
}

func TestClaudeSessionDefaultsAreNotRuntimeConfig(t *testing.T) {
	config := runtime.Config{
		"executable": "/agent",
		"features":   map[string]any{"agentQuestions": false},
	}
	require.ErrorContains(t, (Factory{}).Validate(config), `unknown process config "features"`)
}
