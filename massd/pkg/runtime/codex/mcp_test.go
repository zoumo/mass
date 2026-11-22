package codex

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/zoumo/agent-sdk-go/mcp"

	"github.com/zoumo/mass/massd/pkg/runtime"
)

func TestCodexMCPConfigurationMapping(t *testing.T) {
	servers := []runtime.MCPServer{
		{
			Name:    "local",
			Type:    "stdio",
			Command: "/mcp-server",
			Args:    []string{"--stdio"},
			Env:     []mcp.NamedValue{mcp.Literal("TOKEN", "${TOKEN}")},
			Cwd:     "/work",
		},
		{
			Name:              "remote",
			Type:              "http",
			URL:               "https://example.invalid/mcp",
			Headers:           []mcp.NamedValue{mcp.Literal("Authorization", "Bearer ${TOKEN}")},
			EnvHeaders:        map[string]string{"X-Token": "TOKEN"},
			BearerTokenEnvVar: "TOKEN",
		},
	}
	converted, err := codexMCPServers(&servers, codexMCPOptions(t))
	require.NoError(t, err)
	require.Len(t, converted, 2)
	require.Equal(t, mcp.TransportStdio, converted[0].Transport)
	require.Equal(t, "/mcp-server", converted[0].Command)
	require.Equal(t, "/work", converted[0].Cwd)
	require.Equal(t, "fixture-secret", plainMCPValues(t, converted[0].Env)["TOKEN"])
	require.Equal(t, "https://example.invalid/mcp", converted[1].URL)
	require.Equal(t, mcp.TransportHTTP, converted[1].Transport)
	require.Equal(t, "TOKEN", converted[1].BearerTokenEnvVar)
	require.NotContains(t, plainMCPValues(t, converted[1].Headers), "Authorization")
	require.Equal(t, "TOKEN", converted[1].EnvHeaders["X-Token"])
}

func TestCodexMCPConfigurationTranslatesAndResolvesSecretReferences(t *testing.T) {
	options := codexMCPOptions(t)
	options.Env["HEADER_TOKEN"] = "header-secret"
	options.Env["PROFILE"] = "fixture-profile"
	servers := []runtime.MCPServer{
		{
			Name:    "local",
			Type:    "stdio",
			Command: "/mcp-server",
			Env: []mcp.NamedValue{
				mcp.Literal("API_TOKEN", "${TOKEN}"),
				mcp.Literal("AUTHORIZATION", "Bearer ${TOKEN}"),
				mcp.Literal("RESOURCE_URI", "https://service.invalid/${PROFILE}"),
				mcp.Literal("RESOURCE_LABEL", "profile-${PROFILE}"),
			},
		},
		{
			Name: "remote",
			Type: "http",
			URL:  "https://example.invalid/mcp",
			Headers: []mcp.NamedValue{
				mcp.Literal("Authorization", "Bearer ${TOKEN}"),
				mcp.Literal("Content-Type", "application/json"),
				mcp.Literal("Credential", "Basic ${TOKEN}"),
				mcp.Literal("X-Profile", "${PROFILE}"),
				mcp.Literal("X-Combined", "profile-${PROFILE}"),
				mcp.Literal("X-Scheme", "Token ${TOKEN}"),
				mcp.Literal("X-Api-Key", "${HEADER_TOKEN}"),
			},
			EnvHeaders: map[string]string{"X-Explicit": "HEADER_TOKEN"},
		},
	}
	converted, err := codexMCPServers(&servers, options)
	require.NoError(t, err)
	env := plainMCPValues(t, converted[0].Env)
	headers := plainMCPValues(t, converted[1].Headers)
	require.Equal(t, "fixture-secret", env["API_TOKEN"])
	require.Equal(t, "Bearer fixture-secret", env["AUTHORIZATION"])
	require.Equal(t, "https://service.invalid/fixture-profile", env["RESOURCE_URI"])
	require.Equal(t, "profile-fixture-profile", env["RESOURCE_LABEL"])
	require.Equal(t, "Basic fixture-secret", headers["Credential"])
	require.Equal(t, "profile-fixture-profile", headers["X-Combined"])
	require.Equal(t, "Token fixture-secret", headers["X-Scheme"])
	require.Equal(t, "application/json", headers["Content-Type"])
	require.NotContains(t, headers, "Authorization")
	require.NotContains(t, headers, "X-Api-Key")
	require.Equal(t, "HEADER_TOKEN", converted[1].EnvHeaders["X-Api-Key"])
	require.Equal(t, "PROFILE", converted[1].EnvHeaders["X-Profile"])
	require.Equal(t, "HEADER_TOKEN", converted[1].EnvHeaders["X-Explicit"])
	require.Equal(t, "TOKEN", converted[1].BearerTokenEnvVar)
}

func TestCodexMCPConfigurationRejectsUnresolvedReferences(t *testing.T) {
	for name, test := range map[string]struct {
		server runtime.MCPServer
		want   string
	}{
		"stdio env": {
			server: runtime.MCPServer{
				Name:    "local",
				Type:    "stdio",
				Command: "/mcp-server",
				Env:     []mcp.NamedValue{mcp.Literal("TOKEN", "${MISSING_TOKEN}")},
			},
			want: "MISSING_TOKEN",
		},
		"stdio ordinary env": {
			server: runtime.MCPServer{
				Name:    "local",
				Type:    "stdio",
				Command: "/mcp-server",
				Env:     []mcp.NamedValue{mcp.Literal("BASE_URL", "${MISSING_TOKEN}")},
			},
			want: "MISSING_TOKEN",
		},
		"ordinary header reference": {
			server: runtime.MCPServer{
				Name:    "remote",
				Type:    "http",
				URL:     "https://example.invalid/mcp",
				Headers: []mcp.NamedValue{mcp.Literal("X-URL", "${MISSING_TOKEN}")},
			},
			want: "MISSING_TOKEN",
		},
		"inline header reference": {
			server: runtime.MCPServer{
				Name:    "remote",
				Type:    "http",
				URL:     "https://example.invalid/mcp",
				Headers: []mcp.NamedValue{mcp.Literal("X-URL", "prefix-${MISSING_TOKEN}")},
			},
			want: "MISSING_TOKEN",
		},
		"inline stdio environment reference": {
			server: runtime.MCPServer{
				Name:    "local",
				Type:    "stdio",
				Command: "/mcp-server",
				Env:     []mcp.NamedValue{mcp.Literal("BASE_URL", "https://service.invalid/${MISSING_TOKEN}")},
			},
			want: "MISSING_TOKEN",
		},
		"unresolved header reference": {
			server: runtime.MCPServer{
				Name:    "remote",
				Type:    "http",
				URL:     "https://example.invalid/mcp",
				Headers: []mcp.NamedValue{mcp.Literal("X-URL", "${MISSING-TOKEN}")},
			},
			want: "unresolved environment reference",
		},
		"bearer header": {
			server: runtime.MCPServer{
				Name: "remote",
				Type: "http",
				URL:  "https://example.invalid/mcp",
				Headers: []mcp.NamedValue{
					mcp.Literal("Authorization", "Bearer ${MISSING_TOKEN}"),
				},
			},
			want: "MISSING_TOKEN",
		},
		"env header": {
			server: runtime.MCPServer{
				Name:       "remote",
				Type:       "http",
				URL:        "https://example.invalid/mcp",
				EnvHeaders: map[string]string{"X-Token": "MISSING_TOKEN"},
			},
			want: "MISSING_TOKEN",
		},
		"bearer token environment variable": {
			server: runtime.MCPServer{
				Name:              "remote",
				Type:              "http",
				URL:               "https://example.invalid/mcp",
				BearerTokenEnvVar: "MISSING_TOKEN",
			},
			want: "MISSING_TOKEN",
		},
		"empty environment variable": {
			server: runtime.MCPServer{
				Name:    "local",
				Type:    "stdio",
				Command: "/mcp-server",
				Env:     []mcp.NamedValue{mcp.Literal("TOKEN", "${EMPTY_TOKEN}")},
			},
			want: "EMPTY_TOKEN",
		},
	} {
		t.Run(name, func(t *testing.T) {
			servers := []runtime.MCPServer{test.server}
			options := codexMCPOptions(t)
			options.Env["EMPTY_TOKEN"] = ""
			_, err := codexMCPServers(&servers, options)
			require.ErrorIs(t, err, runtime.ErrInvalidArgument)
			require.ErrorContains(t, err, test.want)
		})
	}
}

func TestCodexMCPConfigurationRejectsInvalidResolvedHeaderValues(t *testing.T) {
	options := codexMCPOptions(t)
	options.Env["HEADER_VALUE"] = "first\r\nsecond"
	for name, server := range map[string]runtime.MCPServer{
		"resolved reference": {
			Name:    "remote",
			Type:    "http",
			URL:     "https://example.invalid/mcp",
			Headers: []mcp.NamedValue{mcp.Literal("X-Value", "${HEADER_VALUE}")},
		},
		"literal": {
			Name:    "remote",
			Type:    "http",
			URL:     "https://example.invalid/mcp",
			Headers: []mcp.NamedValue{mcp.Literal("X-Value", "first\r\nsecond")},
		},
		"bearer environment variable": {
			Name:              "remote",
			Type:              "http",
			URL:               "https://example.invalid/mcp",
			BearerTokenEnvVar: "HEADER_VALUE",
		},
	} {
		t.Run(name, func(t *testing.T) {
			servers := []runtime.MCPServer{server}
			_, err := codexMCPServers(&servers, options)
			require.ErrorIs(t, err, runtime.ErrInvalidArgument)
			require.NotContains(t, err.Error(), "first")
		})
	}
}

func TestCodexMCPConfigurationRejectsConflictingAuthentication(t *testing.T) {
	options := codexMCPOptions(t)
	options.Env["OTHER_TOKEN"] = "other-secret"
	for name, server := range map[string]runtime.MCPServer{
		"bearer references": {
			Name: "remote",
			Type: "http",
			URL:  "https://example.invalid/mcp",
			Headers: []mcp.NamedValue{
				mcp.Literal("Authorization", "Bearer ${TOKEN}"),
			},
			BearerTokenEnvVar: "OTHER_TOKEN",
		},
		"duplicate header declarations": {
			Name: "remote",
			Type: "http",
			URL:  "https://example.invalid/mcp",
			Headers: []mcp.NamedValue{
				mcp.Literal("X-Api-Key", "${TOKEN}"),
			},
			EnvHeaders: map[string]string{"x-api-key": "OTHER_TOKEN"},
		},
		"environment authorization header": {
			Name:              "remote",
			Type:              "http",
			URL:               "https://example.invalid/mcp",
			EnvHeaders:        map[string]string{"Authorization": "TOKEN"},
			BearerTokenEnvVar: "OTHER_TOKEN",
		},
		"exact authorization reference": {
			Name:              "remote",
			Type:              "http",
			URL:               "https://example.invalid/mcp",
			Headers:           []mcp.NamedValue{mcp.Literal("Authorization", "${TOKEN}")},
			BearerTokenEnvVar: "OTHER_TOKEN",
		},
		"literal authorization": {
			Name:              "remote",
			Type:              "http",
			URL:               "https://example.invalid/mcp",
			Headers:           []mcp.NamedValue{mcp.Literal("Authorization", "Basic literal")},
			BearerTokenEnvVar: "OTHER_TOKEN",
		},
		"lowercase environment authorization header": {
			Name:              "remote",
			Type:              "http",
			URL:               "https://example.invalid/mcp",
			EnvHeaders:        map[string]string{"authorization": "TOKEN"},
			BearerTokenEnvVar: "OTHER_TOKEN",
		},
	} {
		t.Run(name, func(t *testing.T) {
			servers := []runtime.MCPServer{server}
			_, err := codexMCPServers(&servers, options)
			require.ErrorIs(t, err, runtime.ErrInvalidArgument)
		})
	}
}

func TestCodexMCPConfigurationRejectsUnsupportedProviderFeatures(t *testing.T) {
	for _, server := range []runtime.MCPServer{
		{Name: "dotted.name", Type: "stdio", Command: "/mcp-server"},
		{Name: "sse", Type: "sse", URL: "https://example.invalid/mcp"},
	} {
		servers := []runtime.MCPServer{server}
		_, err := codexMCPServers(&servers, codexMCPOptions(t))
		require.Error(t, err)
		var public *runtime.OperationError
		require.ErrorAs(t, err, &public)
		require.Equal(t, "unsupported", public.Code)
	}
}

func TestCodexMCPConfigurationClassifiesPublicValidation(t *testing.T) {
	servers := []runtime.MCPServer{{
		Name:    "local",
		Type:    "stdio",
		Command: "/mcp-server",
		Env:     []mcp.NamedValue{mcp.Literal("TOKEN", "literal-secret")},
	}}
	_, err := codexMCPServers(&servers, codexMCPOptions(t))
	require.ErrorIs(t, err, runtime.ErrInvalidArgument)
	require.NotContains(t, err.Error(), "literal-secret")
}

func TestCodexMCPConfigurationDefersAmbientConfigurationToSDK(t *testing.T) {
	servers := []runtime.MCPServer{{Name: "remote", Type: "http", URL: "https://example.invalid/mcp"}}
	for _, config := range []string{
		"[mcp_servers.ambient]\ncommand = \"/ambient\"\n",
		"[profiles.work.mcp_servers.ambient]\ncommand = \"/ambient\"\n",
	} {
		home := t.TempDir()
		require.NoError(t, os.WriteFile(filepath.Join(home, "config.toml"), []byte(config), 0o600))
		converted, err := codexMCPServers(&servers, runtime.ProcessConfig{Env: map[string]string{"CODEX_HOME": home}})
		require.NoError(t, err)
		require.Len(t, converted, 1, "MASS maps only the effective Session list; ambient merge belongs to the SDK/native provider")
		require.Equal(t, "remote", converted[0].Name)
	}
	converted, err := codexMCPServers(&servers, runtime.ProcessConfig{})
	require.NoError(t, err, "Session MCP does not require an isolated CODEX_HOME")
	require.Len(t, converted, 1)
	empty := []runtime.MCPServer{}
	converted, err = codexMCPServers(&empty, runtime.ProcessConfig{})
	require.NoError(t, err)
	require.NotNil(t, converted)
	require.Empty(t, converted)
}

func TestCodexMCPConfigurationWithoutDeclarationKeepsAmbientBehavior(t *testing.T) {
	servers, err := codexMCPServers(nil, runtime.ProcessConfig{})
	require.NoError(t, err)
	require.Nil(t, servers)
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

func codexMCPOptions(t *testing.T) runtime.ProcessConfig {
	t.Helper()
	return runtime.ProcessConfig{
		EnvPolicy: "replace",
		Env: map[string]string{
			"CODEX_HOME": t.TempDir(),
			"TOKEN":      "fixture-secret",
		},
	}
}

func plainMCPValues(t *testing.T, values []mcp.NamedValue) map[string]string {
	t.Helper()
	plain, err := mcp.LiteralValues(values)
	require.NoError(t, err)
	return plain
}
