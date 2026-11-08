package runtime_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/zoumo/mass/mass-go/pkg/runtime"
)

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
