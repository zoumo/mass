package codex

import (
	"encoding/json"
	"os"
	"path/filepath"
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
			Env:     map[string]string{"TOKEN": "${TOKEN}"},
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
	converted, err := codexMCPServers(&servers, codexMCPOptions(t))
	require.NoError(t, err)
	require.Len(t, converted, 2)
	require.Equal(t, "/mcp-server", converted[0].Command)
	require.Equal(t, "/work", converted[0].Cwd)
	require.Equal(t, "fixture-secret", converted[0].Env["TOKEN"])
	require.Equal(t, "https://example.invalid/mcp", converted[1].URL)
	require.Equal(t, "TOKEN", converted[1].BearerTokenEnvVar)
	require.NotContains(t, converted[1].Headers, "Authorization")
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
			Env: map[string]string{
				"API_TOKEN":      "${TOKEN}",
				"AUTHORIZATION":  "Bearer ${TOKEN}",
				"RESOURCE_URI":   "https://service.invalid/${PROFILE}",
				"RESOURCE_LABEL": "profile-${PROFILE}",
			},
		},
		{
			Name: "remote",
			Type: "http",
			URL:  "https://example.invalid/mcp",
			Headers: map[string]string{
				"Authorization": "Bearer ${TOKEN}",
				"Content-Type":  "application/json",
				"Credential":    "Basic ${TOKEN}",
				"X-Profile":     "${PROFILE}",
				"X-Combined":    "profile-${PROFILE}",
				"X-Scheme":      "Token ${TOKEN}",
				"X-Api-Key":     "${HEADER_TOKEN}",
			},
			EnvHeaders: map[string]string{"X-Explicit": "HEADER_TOKEN"},
		},
	}
	converted, err := codexMCPServers(&servers, options)
	require.NoError(t, err)
	require.Equal(t, "fixture-secret", converted[0].Env["API_TOKEN"])
	require.Equal(t, "Bearer fixture-secret", converted[0].Env["AUTHORIZATION"])
	require.Equal(t, "https://service.invalid/fixture-profile", converted[0].Env["RESOURCE_URI"])
	require.Equal(t, "profile-fixture-profile", converted[0].Env["RESOURCE_LABEL"])
	require.Equal(t, "Basic fixture-secret", converted[1].Headers["Credential"])
	require.Equal(t, "profile-fixture-profile", converted[1].Headers["X-Combined"])
	require.Equal(t, "Token fixture-secret", converted[1].Headers["X-Scheme"])
	require.Equal(t, "application/json", converted[1].Headers["Content-Type"])
	require.NotContains(t, converted[1].Headers, "Authorization")
	require.NotContains(t, converted[1].Headers, "X-Api-Key")
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
				Env:     map[string]string{"TOKEN": "${MISSING_TOKEN}"},
			},
			want: "MISSING_TOKEN",
		},
		"stdio ordinary env": {
			server: runtime.MCPServer{
				Name:    "local",
				Type:    "stdio",
				Command: "/mcp-server",
				Env:     map[string]string{"BASE_URL": "${MISSING_TOKEN}"},
			},
			want: "MISSING_TOKEN",
		},
		"ordinary header reference": {
			server: runtime.MCPServer{
				Name:    "remote",
				Type:    "http",
				URL:     "https://example.invalid/mcp",
				Headers: map[string]string{"X-URL": "${MISSING_TOKEN}"},
			},
			want: "MISSING_TOKEN",
		},
		"inline header reference": {
			server: runtime.MCPServer{
				Name:    "remote",
				Type:    "http",
				URL:     "https://example.invalid/mcp",
				Headers: map[string]string{"X-URL": "prefix-${MISSING_TOKEN}"},
			},
			want: "MISSING_TOKEN",
		},
		"inline stdio environment reference": {
			server: runtime.MCPServer{
				Name:    "local",
				Type:    "stdio",
				Command: "/mcp-server",
				Env:     map[string]string{"BASE_URL": "https://service.invalid/${MISSING_TOKEN}"},
			},
			want: "MISSING_TOKEN",
		},
		"unresolved header reference": {
			server: runtime.MCPServer{
				Name:    "remote",
				Type:    "http",
				URL:     "https://example.invalid/mcp",
				Headers: map[string]string{"X-URL": "${MISSING-TOKEN}"},
			},
			want: "unresolved environment reference",
		},
		"bearer header": {
			server: runtime.MCPServer{
				Name: "remote",
				Type: "http",
				URL:  "https://example.invalid/mcp",
				Headers: map[string]string{
					"Authorization": "Bearer ${MISSING_TOKEN}",
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
				Env:     map[string]string{"TOKEN": "${EMPTY_TOKEN}"},
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
			Headers: map[string]string{"X-Value": "${HEADER_VALUE}"},
		},
		"literal": {
			Name:    "remote",
			Type:    "http",
			URL:     "https://example.invalid/mcp",
			Headers: map[string]string{"X-Value": "first\r\nsecond"},
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

func TestCodexMCPProjectConfigDisablesProjectLayers(t *testing.T) {
	root := t.TempDir()
	cwd := filepath.Join(root, "repo", "nested")
	require.NoError(t, os.MkdirAll(cwd, 0o700))
	config, err := codexMCPProjectConfig(cwd)
	require.NoError(t, err)
	var projects map[string]map[string]string
	require.NoError(t, json.Unmarshal(config["projects"], &projects))
	for _, path := range []string{cwd, filepath.Dir(cwd), filepath.Dir(filepath.Dir(cwd))} {
		require.Equal(t, "untrusted", projects[path]["trust_level"])
	}
	canonical, err := filepath.EvalSymlinks(cwd)
	require.NoError(t, err)
	require.Equal(t, "untrusted", projects[canonical]["trust_level"])
	top := cwd
	for {
		parent := filepath.Dir(top)
		if parent == top {
			break
		}
		top = parent
	}
	require.Equal(t, "untrusted", projects[top]["trust_level"])
}

func TestCodexMCPProjectConfigIncludesSymlinkSpelling(t *testing.T) {
	realDir := t.TempDir()
	link := filepath.Join(t.TempDir(), "link")
	require.NoError(t, os.Symlink(realDir, link))
	config, err := codexMCPProjectConfig(link)
	require.NoError(t, err)
	var projects map[string]map[string]string
	require.NoError(t, json.Unmarshal(config["projects"], &projects))
	require.Equal(t, "untrusted", projects[link]["trust_level"])
	canonical, err := filepath.EvalSymlinks(realDir)
	require.NoError(t, err)
	require.Equal(t, "untrusted", projects[canonical]["trust_level"])
}

func TestCodexSessionMCPConfigExcludesProjectLayersOnlyForExplicitLists(t *testing.T) {
	options := codexMCPOptions(t)
	cwd := filepath.Join(t.TempDir(), "repo")
	require.NoError(t, os.MkdirAll(cwd, 0o700))

	servers, config, err := codexSessionMCPConfig(nil, cwd, options)
	require.NoError(t, err)
	require.Nil(t, servers)
	require.Nil(t, config)

	empty := []runtime.MCPServer{}
	servers, config, err = codexSessionMCPConfig(&empty, cwd, options)
	require.NoError(t, err)
	require.NotNil(t, servers)
	require.Empty(t, servers)
	require.NotNil(t, config)
	require.Contains(t, config, "projects")

	explicit := []runtime.MCPServer{{Name: "remote", Type: "http", URL: "https://example.invalid/mcp"}}
	servers, config, err = codexSessionMCPConfig(&explicit, cwd, options)
	require.NoError(t, err)
	require.Len(t, servers, 1)
	require.NotNil(t, config)
}

func TestCodexMCPConfigurationRejectsConflictingAuthentication(t *testing.T) {
	options := codexMCPOptions(t)
	options.Env["OTHER_TOKEN"] = "other-secret"
	for name, server := range map[string]runtime.MCPServer{
		"bearer references": {
			Name: "remote",
			Type: "http",
			URL:  "https://example.invalid/mcp",
			Headers: map[string]string{
				"Authorization": "Bearer ${TOKEN}",
			},
			BearerTokenEnvVar: "OTHER_TOKEN",
		},
		"duplicate header declarations": {
			Name: "remote",
			Type: "http",
			URL:  "https://example.invalid/mcp",
			Headers: map[string]string{
				"X-Api-Key": "${TOKEN}",
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
			Headers:           map[string]string{"Authorization": "${TOKEN}"},
			BearerTokenEnvVar: "OTHER_TOKEN",
		},
		"literal authorization": {
			Name:              "remote",
			Type:              "http",
			URL:               "https://example.invalid/mcp",
			Headers:           map[string]string{"Authorization": "Basic literal"},
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
		Env:     map[string]string{"TOKEN": "literal-secret"},
	}}
	_, err := codexMCPServers(&servers, codexMCPOptions(t))
	require.ErrorIs(t, err, runtime.ErrInvalidArgument)
	require.NotContains(t, err.Error(), "literal-secret")
}

func TestCodexMCPConfigurationRequiresIsolatedHome(t *testing.T) {
	servers := []runtime.MCPServer{{Name: "remote", Type: "http", URL: "https://example.invalid/mcp"}}
	_, err := codexMCPServers(&servers, runtime.ProcessConfig{})
	require.ErrorContains(t, err, "explicit absolute env.CODEX_HOME")
	var public *runtime.OperationError
	require.ErrorAs(t, err, &public)
	require.Equal(t, "unsupported", public.Code)

	empty := []runtime.MCPServer{}
	_, err = codexMCPServers(&empty, runtime.ProcessConfig{})
	require.ErrorContains(t, err, "explicit absolute env.CODEX_HOME")

	ambient := t.TempDir()
	t.Setenv("CODEX_HOME", ambient)
	_, err = codexMCPServers(&servers, runtime.ProcessConfig{Env: map[string]string{"CODEX_HOME": ambient}})
	require.ErrorContains(t, err, "isolated CODEX_HOME")
}

func TestCodexMCPConfigurationRejectsAmbientServers(t *testing.T) {
	servers := []runtime.MCPServer{{Name: "remote", Type: "http", URL: "https://example.invalid/mcp"}}
	for name, config := range map[string]string{
		"top level": "[mcp_servers.ambient]\ncommand = \"/ambient\"\n",
		"profile":   "[profiles.work.mcp_servers.ambient]\ncommand = \"/ambient\"\n",
	} {
		t.Run(name, func(t *testing.T) {
			home := t.TempDir()
			require.NoError(t, os.WriteFile(filepath.Join(home, "config.toml"), []byte(config), 0o600))
			_, err := codexMCPServers(&servers, runtime.ProcessConfig{Env: map[string]string{"CODEX_HOME": home}})
			require.ErrorContains(t, err, "without ambient mcp_servers")
		})
	}
}

func TestCodexMCPConfigurationAllowsIsolatedHomeConfig(t *testing.T) {
	home := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(home, "config.toml"), []byte("[profiles.work]\nmodel = \"test\"\n"), 0o600))
	servers := []runtime.MCPServer{{Name: "remote", Type: "http", URL: "https://example.invalid/mcp"}}
	converted, err := codexMCPServers(&servers, runtime.ProcessConfig{Env: map[string]string{"CODEX_HOME": home}})
	require.NoError(t, err)
	require.Len(t, converted, 1)
}

func TestCodexMCPConfigurationRejectsAliasedAmbientHome(t *testing.T) {
	ambient := t.TempDir()
	alias := filepath.Join(t.TempDir(), "codex-alias")
	require.NoError(t, os.Symlink(ambient, alias))
	t.Setenv("CODEX_HOME", ambient)
	servers := []runtime.MCPServer{{Name: "remote", Type: "http", URL: "https://example.invalid/mcp"}}
	_, err := codexMCPServers(&servers, runtime.ProcessConfig{Env: map[string]string{"CODEX_HOME": alias}})
	require.ErrorContains(t, err, "isolated CODEX_HOME")
}

func TestCodexMCPConfigurationRejectsAmbientHomeUnderChildHOME(t *testing.T) {
	processHome := t.TempDir()
	childHome := t.TempDir()
	t.Setenv("HOME", processHome)
	t.Setenv("CODEX_HOME", "")
	servers := []runtime.MCPServer{{Name: "remote", Type: "http", URL: "https://example.invalid/mcp"}}
	_, err := codexMCPServers(&servers, runtime.ProcessConfig{
		EnvPolicy: "inherit",
		Env: map[string]string{
			"HOME":       childHome,
			"CODEX_HOME": filepath.Join(childHome, ".codex"),
		},
	})
	require.ErrorContains(t, err, "isolated CODEX_HOME")
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
