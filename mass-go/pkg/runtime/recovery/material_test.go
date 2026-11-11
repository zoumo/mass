package recovery

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/zoumo/mass/mass-go/pkg/runtime"
)

func TestSessionOnlyOptionsDoNotPinRecoveryTargeting(t *testing.T) {
	v := Vault{Dir: t.TempDir(), Kind: "codex", Owner: "agent", Executable: "/agent"}
	address := runtime.Address{Namespace: "one", SessionName: "demo"}
	empty := []runtime.MCPServer{}
	initial := runtime.SessionSpec{
		Cwd:        "/work",
		Features:   &runtime.SessionFeatures{AgentQuestions: runtime.Bool(false)},
		MCPServers: &empty,
	}
	require.NoError(t, v.Save("native", address, initial, map[string]string{"id": "native"}))

	changed := initial
	changed.Features = &runtime.SessionFeatures{AgentQuestions: runtime.Bool(true)}
	changed.MCPServers = &[]runtime.MCPServer{{Name: "remote", Type: "http", URL: "https://example.invalid/mcp"}}
	_, err := v.Load("native", address, changed)
	require.NoError(t, err)

	changed.Cwd = "/different"
	_, err = v.Load("native", address, changed)
	require.Error(t, err)
}

func TestRecoveryReadsLegacySpecWithSessionOptions(t *testing.T) {
	v := Vault{Dir: t.TempDir(), Kind: "codex", Owner: "agent", Executable: "/agent"}
	address := runtime.Address{Namespace: "one", SessionName: "demo"}
	servers := []runtime.MCPServer{{Name: "legacy", Type: "stdio", Command: "/mcp"}}
	context, err := json.Marshal(map[string]string{"id": "native"})
	require.NoError(t, err)
	material, err := json.Marshal(Material{
		Format:     1,
		Kind:       v.Kind,
		Owner:      v.Owner,
		ID:         "native",
		Executable: v.Executable,
		Address:    address,
		Spec: runtime.SessionSpec{
			Cwd:        "/work",
			Features:   &runtime.SessionFeatures{AgentQuestions: runtime.Bool(false)},
			MCPServers: &servers,
		},
		Context: context,
	})
	require.NoError(t, err)
	path, err := v.path("native")
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
	require.NoError(t, os.WriteFile(path, material, 0o600))

	_, err = v.Load("native", address, runtime.SessionSpec{Cwd: "/work"})
	require.NoError(t, err)
}

func TestSaveNormalizesLegacySpecWithSessionOptions(t *testing.T) {
	v := Vault{Dir: t.TempDir(), Kind: "codex", Owner: "agent", Executable: "/agent"}
	address := runtime.Address{Namespace: "one", SessionName: "demo"}
	servers := []runtime.MCPServer{{Name: "legacy", Type: "stdio", Command: "/mcp"}}
	context, err := json.Marshal(map[string]string{"id": "native"})
	require.NoError(t, err)
	material, err := json.Marshal(Material{
		Format:     1,
		Kind:       v.Kind,
		Owner:      v.Owner,
		ID:         "native",
		Executable: v.Executable,
		Address:    address,
		Spec: runtime.SessionSpec{
			Cwd:        "/work",
			Features:   &runtime.SessionFeatures{AgentQuestions: runtime.Bool(false)},
			MCPServers: &servers,
		},
		Context: context,
	})
	require.NoError(t, err)
	path, err := v.path("native")
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
	require.NoError(t, os.WriteFile(path, material, 0o600))

	native := map[string]string{"id": "native"}
	require.NoError(t, v.Save("native", address, runtime.SessionSpec{Cwd: "/work"}, native))

	saved, err := os.ReadFile(path)
	require.NoError(t, err)
	var normalized Material
	require.NoError(t, json.Unmarshal(saved, &normalized))
	require.Nil(t, normalized.Spec.Features)
	require.Nil(t, normalized.Spec.MCPServers)

	require.ErrorContains(t, v.Save("native", address, runtime.SessionSpec{Cwd: "/different"}, native), "existing recovery material conflicts")
}

func TestInitialSessionConfigurationDoesNotChangeRecoveryIdentity(t *testing.T) {
	for _, kind := range []string{"codex", "claude"} {
		t.Run(kind, func(t *testing.T) {
			v := Vault{Dir: t.TempDir(), Kind: kind, Owner: "account", Executable: "/agent"}
			address := runtime.Address{Namespace: "one", SessionName: "configured"}
			legacy := runtime.SessionSpec{Cwd: "/work"}
			require.NoError(t, v.Save("native", address, legacy, map[string]string{"id": "native", "account": "account"}))
			changed := legacy
			changed.Config = &runtime.InitialSessionConfig{Model: "new", Effort: "MAX"}
			_, err := v.Load("native", address, changed)
			require.NoError(t, err)
			require.NoError(t, v.Save("native", address, changed, map[string]string{"id": "native", "account": "account"}))
			_, err = v.Load("other-id", address, changed)
			require.Error(t, err)
			changed.Cwd = "/wrong"
			_, err = v.Load("native", address, changed)
			require.Error(t, err)
			v.Owner = "other-account"
			_, err = v.Load("native", address, legacy)
			require.Error(t, err)
		})
	}
}
