package codex

import (
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
	require.Equal(t, "https://example.invalid/mcp", converted[1].URL)
	require.Equal(t, "TOKEN", converted[1].BearerTokenEnvVar)
}

func TestCodexMCPConfigurationRejectsUnsupportedProviderFeatures(t *testing.T) {
	for _, server := range []runtime.MCPServer{
		{Name: "dotted.name", Type: "stdio", Command: "/mcp-server"},
		{Name: "sse", Type: "sse", URL: "https://example.invalid/mcp"},
	} {
		servers := []runtime.MCPServer{server}
		_, err := codexMCPServers(&servers, codexMCPOptions(t))
		require.Error(t, err)
	}
}

func TestCodexMCPConfigurationRequiresIsolatedHome(t *testing.T) {
	servers := []runtime.MCPServer{{Name: "remote", Type: "http", URL: "https://example.invalid/mcp"}}
	_, err := codexMCPServers(&servers, runtime.ProcessConfig{})
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
	return runtime.ProcessConfig{Env: map[string]string{"CODEX_HOME": t.TempDir()}}
}
