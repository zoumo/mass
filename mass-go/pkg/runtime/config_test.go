package runtime_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/zoumo/mass/mass-go/pkg/runtime"
	"github.com/zoumo/mass/mass-go/pkg/runtime/claude"
	"github.com/zoumo/mass/mass-go/pkg/runtime/codex"
)

func TestRealRuntimeConfigurationDoesNotRequireSyntheticScenario(t *testing.T) {
	for name, factory := range map[string]runtime.Factory{"codex": codex.Factory{}, "claude": claude.Factory{}} {
		t.Run(name, func(t *testing.T) {
			require.NoError(t, factory.Validate(runtime.Config{"executable": "/opt/agents/" + name, "envPolicy": "inherit", "env": map[string]any{"MASS_TEST": "explicit"}}))
		})
	}
}

func TestRuntimeConfigurationRejectsInvalidLaunchInputs(t *testing.T) {
	for name, factory := range map[string]runtime.Factory{"codex": codex.Factory{}, "claude": claude.Factory{}} {
		for _, config := range []runtime.Config{
			{"executable": "relative"},
			{"executable": "/agent", "scenario": "/scenario\x00.json"},
			{"executable": "/agent", "envPolicy": "implicit"},
			{"executable": "/agent", "env": map[string]any{"TOKEN": 123}},
			{"executable": "/agent", "env": map[string]string{"BAD=KEY": "value"}},
			{"executable": "/agent", "args": []any{123}},
			{"executable": "/agent", "args": []string{"bad\x00arg"}},
			{"executable": "/agent", "unknown": true},
		} {
			t.Run(name, func(t *testing.T) { require.Error(t, factory.Validate(config)) })
		}
	}
	require.Error(t, (claude.Factory{}).Validate(runtime.Config{"executable": "/agent", "args": []string{"--ignored"}}))
}

func TestRuntimeEnvironmentPolicyIsExplicit(t *testing.T) {
	t.Setenv("MASS_CONFIG_INHERITED", "parent")
	for _, policy := range []string{"inherit", "replace"} {
		config, err := runtime.ParseProcessConfig(runtime.Config{"executable": "/agent", "envPolicy": policy, "env": map[string]string{"MASS_CONFIG_OVERRIDE": "configured"}})
		require.NoError(t, err)
		env := config.Environment("codex")
		require.Contains(t, env, "MASS_CONFIG_OVERRIDE=configured")
		if policy == "inherit" {
			require.Contains(t, env, "MASS_CONFIG_INHERITED=parent")
			value, ok := config.LookupEnv("MASS_CONFIG_INHERITED")
			require.True(t, ok)
			require.Equal(t, "parent", value)
		} else {
			require.Equal(t, []string{"MASS_CONFIG_OVERRIDE=configured"}, env)
			_, ok := config.LookupEnv("MASS_CONFIG_INHERITED")
			require.False(t, ok)
		}
		value, ok := config.LookupEnv("MASS_CONFIG_OVERRIDE")
		require.True(t, ok)
		require.Equal(t, "configured", value)
	}
	config, err := runtime.ParseProcessConfig(runtime.Config{"executable": "/agent", "envPolicy": "replace"})
	require.NoError(t, err)
	require.NotNil(t, config.Environment("claude"), "nil would silently inherit the environment in the SDK")
}
