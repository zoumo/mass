package runtime_test

import (
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/zoumo/agent-sdk-go/mcp"

	"github.com/zoumo/mass/mass-go/pkg/runtime"
)

func TestMCPServerSDKServerMapsAndCopiesConfiguration(t *testing.T) {
	server := runtime.MCPServer{
		Name:              "remote",
		Type:              runtime.MCPTransportHTTP,
		Args:              []string{"--unused"},
		Env:               map[string]string{"ENV_TOKEN": "${ENV_TOKEN}"},
		URL:               "https://example.invalid/mcp",
		Headers:           map[string]string{"Authorization": "Bearer ${TOKEN}"},
		EnvHeaders:        map[string]string{"X-API-Key": "API_KEY"},
		BearerTokenEnvVar: "TOKEN",
	}

	converted := server.SDKServer()
	require.Equal(t, mcp.Server{
		Name:              "remote",
		Transport:         mcp.TransportHTTP,
		Args:              []string{"--unused"},
		Env:               map[string]string{"ENV_TOKEN": "${ENV_TOKEN}"},
		URL:               "https://example.invalid/mcp",
		Headers:           map[string]string{"Authorization": "Bearer ${TOKEN}"},
		EnvHeaders:        map[string]string{"X-API-Key": "API_KEY"},
		BearerTokenEnvVar: "TOKEN",
	}, converted)

	converted.Args[0] = "--changed"
	converted.Env["ENV_TOKEN"] = "changed"
	converted.Headers["Authorization"] = "changed"
	converted.EnvHeaders["X-API-Key"] = "changed"
	require.Equal(t, []string{"--unused"}, server.Args)
	require.Equal(t, "${ENV_TOKEN}", server.Env["ENV_TOKEN"])
	require.Equal(t, "Bearer ${TOKEN}", server.Headers["Authorization"])
	require.Equal(t, "API_KEY", server.EnvHeaders["X-API-Key"])
}

func TestValidateSessionDefaults(t *testing.T) {
	servers := []runtime.MCPServer{{
		Name:    "runtime",
		Type:    "stdio",
		Command: "/mcp-server",
		Args:    []string{"--mode", "strict"},
		Env:     map[string]string{"TOKEN": "${TOKEN}"},
	}}
	defaults := runtime.SessionDefaults{
		Features:   &runtime.SessionFeatures{AgentQuestions: runtime.Bool(false)},
		MCPServers: &servers,
	}
	err := defaults.Validate()
	require.NoError(t, err)
	require.False(t, defaults.Features.AgentQuestionsEnabled())
	require.NotNil(t, defaults.MCPServers)
	require.Equal(t, "runtime", (*defaults.MCPServers)[0].Name)
	require.Equal(t, []string{"--mode", "strict"}, (*defaults.MCPServers)[0].Args)
}

func TestValidateSessionDefaultsRejectsInvalidServers(t *testing.T) {
	for _, servers := range [][]runtime.MCPServer{
		{{Type: "stdio", Command: "/one"}},
		{{Name: "one", Type: "stdio"}},
		{
			{Name: "same", Type: "stdio", Command: "/one"},
			{Name: "same", Type: "stdio", Command: "/two"},
		},
		{{Name: "one", Type: "websocket", URL: "https://example.invalid"}},
	} {
		err := (runtime.SessionDefaults{MCPServers: &servers}).Validate()
		require.Error(t, err)
	}
}

func TestValidateMCPServersRejectsUnsafeRemoteConfiguration(t *testing.T) {
	for _, test := range []struct {
		name   string
		server runtime.MCPServer
		want   string
	}{
		{
			name:   "plaintext remote",
			server: runtime.MCPServer{Name: "remote", Type: "http", URL: "http://example.invalid/mcp"},
			want:   "non-loopback http",
		},
		{
			name:   "url credentials",
			server: runtime.MCPServer{Name: "remote", Type: "http", URL: "https://user:secret@example.invalid/mcp"},
			want:   "url without credentials",
		},
		{
			name:   "literal authorization",
			server: runtime.MCPServer{Name: "remote", Type: "http", URL: "https://example.invalid/mcp", Headers: map[string]string{"Authorization": "Bearer literal-token"}},
			want:   `header "Authorization" must reference a secret`,
		},
		{
			name:   "literal env token",
			server: runtime.MCPServer{Name: "local", Type: "stdio", Command: "/mcp", Env: map[string]string{"GITHUB_TOKEN": "literal-token"}},
			want:   `env "GITHUB_TOKEN" must reference a secret`,
		},
		{
			name:   "invalid env header name",
			server: runtime.MCPServer{Name: "remote", Type: "http", URL: "https://example.invalid/mcp", EnvHeaders: map[string]string{"Authorization": "TOKEN=bad"}},
			want:   "must name an environment variable",
		},
		{
			name:   "sse bearer reference",
			server: runtime.MCPServer{Name: "events", Type: "sse", URL: "https://example.invalid/sse", BearerTokenEnvVar: "TOKEN"},
			want:   "sse does not allow envHeaders or bearerTokenEnvVar",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			servers := []runtime.MCPServer{test.server}
			require.ErrorContains(t, runtime.ValidateMCPServers(servers), test.want)
		})
	}
}

func TestValidateMCPServersAcceptsSafeRemoteConfiguration(t *testing.T) {
	for _, server := range []runtime.MCPServer{
		{
			Name:    "remote",
			Type:    "http",
			URL:     "https://example.invalid/mcp",
			Headers: map[string]string{"Authorization": "Bearer ${TOKEN}", "Content-Type": "application/json"},
			EnvHeaders: map[string]string{
				"X-API-Key": "API_KEY",
			},
			BearerTokenEnvVar: "TOKEN",
		},
		{Name: "local", Type: "http", URL: "http://127.0.0.1:8080/mcp"},
		{Name: "local6", Type: "http", URL: "http://[::1]:8080/mcp"},
		{Name: "local-name", Type: "http", URL: "http://localhost:8080/mcp"},
		{Name: "events", Type: "sse", URL: "https://example.invalid/sse", Headers: map[string]string{"Authorization": "${TOKEN}"}},
	} {
		require.NoError(t, runtime.ValidateMCPServers([]runtime.MCPServer{server}))
	}
}

func TestResolveSessionSpecMergesFeaturesByFieldAndMCPServersByName(t *testing.T) {
	defaultQuestions := runtime.Bool(false)
	defaultServers := []runtime.MCPServer{
		{Name: "runtime", Type: "http", URL: "https://example.invalid/runtime"},
		{Name: "shared", Type: "http", URL: "https://example.invalid/shared"},
	}
	defaults := runtime.SessionDefaults{
		Features:   &runtime.SessionFeatures{AgentQuestions: defaultQuestions},
		MCPServers: &defaultServers,
	}

	inherited := defaults.ResolveSessionSpec(runtime.SessionSpec{Cwd: "/work"})
	require.False(t, inherited.Features.AgentQuestionsEnabled())
	require.Equal(t, defaultServers, *inherited.MCPServers)

	overrides := []runtime.MCPServer{
		{Name: "runtime", Type: "stdio", Command: "/session-runtime"},
		{Name: "session", Type: "http", URL: "https://example.invalid/session"},
	}
	overridden := defaults.ResolveSessionSpec(runtime.SessionSpec{
		Cwd:        "/work",
		Features:   &runtime.SessionFeatures{AgentQuestions: runtime.Bool(true)},
		MCPServers: &overrides,
	})
	require.True(t, overridden.Features.AgentQuestionsEnabled())
	require.Equal(t, []runtime.MCPServer{
		{Name: "runtime", Type: "stdio", Command: "/session-runtime"},
		{Name: "shared", Type: "http", URL: "https://example.invalid/shared"},
		{Name: "session", Type: "http", URL: "https://example.invalid/session"},
	}, *overridden.MCPServers)

	featuresOnly := defaults.ResolveSessionSpec(runtime.SessionSpec{
		Cwd:      "/work",
		Features: &runtime.SessionFeatures{},
	})
	require.False(t, featuresOnly.Features.AgentQuestionsEnabled())
	require.Equal(t, defaultServers, *featuresOnly.MCPServers)

	empty := []runtime.MCPServer{}
	emptyMCPSession := defaults.ResolveSessionSpec(runtime.SessionSpec{Cwd: "/work", MCPServers: &empty})
	require.Equal(t, defaultServers, *emptyMCPSession.MCPServers)

	(*defaults.MCPServers)[0].Name = "mutated"
	(*defaultQuestions) = true
	require.False(t, inherited.Features.AgentQuestionsEnabled())
	require.Equal(t, "runtime", (*inherited.MCPServers)[0].Name)
}

func TestMergeSessionMCPServersTreatsSessionEmptyAsNoOverride(t *testing.T) {
	empty := []runtime.MCPServer{}
	require.Nil(t, runtime.MergeSessionMCPServers(nil, nil))

	explicitEmpty := runtime.MergeSessionMCPServers(nil, &empty)
	require.Nil(t, explicitEmpty)

	defaultEmpty := runtime.MergeSessionMCPServers(&empty, nil)
	require.NotNil(t, defaultEmpty)
	require.Empty(t, *defaultEmpty)

	overriddenEmpty := runtime.MergeSessionMCPServers(&empty, &empty)
	require.NotNil(t, overriddenEmpty)
	require.Empty(t, *overriddenEmpty)
}
