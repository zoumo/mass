package api_test

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	api "github.com/zoumo/mass/pkg/runtime-spec/api"
)

// ── Status ──────────────────────────────────────────────────────────────────

func TestStatus_String(t *testing.T) {
	t.Parallel()
	assert.Equal(t, "creating", api.StatusCreating.String())
	assert.Equal(t, "idle", api.StatusIdle.String())
	assert.Equal(t, "running", api.StatusRunning.String())
	assert.Equal(t, "restarting", api.StatusRestarting.String())
	assert.Equal(t, "stopped", api.StatusStopped.String())
	assert.Equal(t, "error", api.StatusError.String())
}

// ── ClientProtocol ──────────────────────────────────────────────────────────

func TestClientProtocol_IsValid(t *testing.T) {
	t.Parallel()
	assert.True(t, api.ClientProtocolACP.IsValid())
	assert.False(t, api.ClientProtocol("unknown").IsValid())
	assert.False(t, api.ClientProtocol("").IsValid())
}

// ── PermissionPolicy ────────────────────────────────────────────────────────

func TestPermissionPolicy_IsValid(t *testing.T) {
	t.Parallel()
	assert.True(t, api.ApproveAll.IsValid())
	assert.True(t, api.ApproveReads.IsValid())
	assert.True(t, api.DenyAll.IsValid())
	assert.False(t, api.PermissionPolicy("").IsValid())
	assert.False(t, api.PermissionPolicy("bogus").IsValid())
}

func TestPermissionPolicy_String(t *testing.T) {
	t.Parallel()
	assert.Equal(t, "approve_all", api.ApproveAll.String())
	assert.Equal(t, "approve_reads", api.ApproveReads.String())
	assert.Equal(t, "deny_all", api.DenyAll.String())
}

// ── Config JSON round-trip ──────────────────────────────────────────────────

func TestConfig_JSON_RoundTrip(t *testing.T) {
	t.Parallel()
	cfg := api.Config{
		MassVersion:    "0.1.0",
		Metadata:       api.Metadata{Name: "test-agent", Annotations: map[string]string{"team": "infra"}},
		AgentRoot:      api.AgentRoot{Path: "workspace"},
		ClientProtocol: api.ClientProtocolACP,
		Process: api.Process{
			Command: "/usr/bin/agent",
			Args:    []string{"--verbose"},
			Env:     []string{"FOO=bar"},
		},
		Session: api.Session{
			SystemPrompt: "you are helpful",
			Permissions:  api.ApproveAll,
			McpServers: []api.McpServer{
				{Type: "stdio", Name: "ws", Command: "ws-server", Args: []string{}, Env: []api.EnvVar{{Name: "K", Value: "V"}}},
			},
		},
	}

	data, err := json.Marshal(cfg)
	require.NoError(t, err)

	var decoded api.Config
	require.NoError(t, json.Unmarshal(data, &decoded))

	assert.Equal(t, "0.1.0", decoded.MassVersion)
	assert.Equal(t, "test-agent", decoded.Metadata.Name)
	assert.Equal(t, "infra", decoded.Metadata.Annotations["team"])
	assert.Equal(t, "workspace", decoded.AgentRoot.Path)
	assert.Equal(t, api.ClientProtocolACP, decoded.ClientProtocol)
	assert.Equal(t, "/usr/bin/agent", decoded.Process.Command)
	assert.Equal(t, []string{"--verbose"}, decoded.Process.Args)
	assert.Equal(t, []string{"FOO=bar"}, decoded.Process.Env)
	assert.Equal(t, "you are helpful", decoded.Session.SystemPrompt)
	assert.Equal(t, api.ApproveAll, decoded.Session.Permissions)
	require.Len(t, decoded.Session.McpServers, 1)
	assert.Equal(t, "ws", decoded.Session.McpServers[0].Name)
}

// ── State JSON round-trip ───────────────────────────────────────────────────

func TestState_JSON_RoundTrip(t *testing.T) {
	t.Parallel()
	exitCode := 0
	st := api.State{
		MassVersion: "0.1.0",
		ID:          "sess-abc",
		Status:      api.StatusRunning,
		PID:         12345,
		Bundle:      "/tmp/test-mass/bundles/test",
		Annotations: map[string]string{"agent": "claude"},
		ExitCode:    &exitCode,
		UpdatedAt:   "2026-04-17T12:00:00Z",
		EventCounts: map[string]int{"agent_message": 5, "tool_call": 2},
	}

	data, err := json.Marshal(st)
	require.NoError(t, err)

	var decoded api.State
	require.NoError(t, json.Unmarshal(data, &decoded))

	assert.Equal(t, "sess-abc", decoded.ID)
	assert.Equal(t, api.StatusRunning, decoded.Status)
	assert.Equal(t, 12345, decoded.PID)
	assert.Equal(t, "/tmp/test-mass/bundles/test", decoded.Bundle)
	assert.Equal(t, "claude", decoded.Annotations["agent"])
	require.NotNil(t, decoded.ExitCode)
	assert.Equal(t, 0, *decoded.ExitCode)
	assert.Equal(t, 5, decoded.EventCounts["agent_message"])
}

// ── AvailableCommandInput marshal/unmarshal ─────────────────────────────────

func TestAvailableCommandInput_RoundTrip(t *testing.T) {
	t.Parallel()
	input := api.AvailableCommandInput{
		Unstructured: &api.UnstructuredCommandInput{Hint: "do something"},
	}
	data, err := json.Marshal(input)
	require.NoError(t, err)
	assert.Contains(t, string(data), `"hint"`)

	var decoded api.AvailableCommandInput
	require.NoError(t, json.Unmarshal(data, &decoded))
	require.NotNil(t, decoded.Unstructured)
	assert.Equal(t, "do something", decoded.Unstructured.Hint)
}

func TestAvailableCommandInput_Marshal_Empty(t *testing.T) {
	t.Parallel()
	_, err := json.Marshal(api.AvailableCommandInput{})
	require.Error(t, err)
}

func TestAvailableCommandInput_Unmarshal_UnknownShape(t *testing.T) {
	t.Parallel()
	var input api.AvailableCommandInput
	err := json.Unmarshal([]byte(`{"foo":"bar"}`), &input)
	require.Error(t, err)
}

// ── ConfigOption marshal/unmarshal ──────────────────────────────────────────

func TestConfigOption_RoundTrip_Select(t *testing.T) {
	t.Parallel()
	opt := api.ConfigOption{
		Select: &api.ConfigOptionSelect{
			ID:           "opt-1",
			Name:         "model",
			CurrentValue: "gpt-4",
			Options: api.ConfigSelectOptions{
				Ungrouped: []api.ConfigSelectOption{
					{Name: "GPT-4", Value: "gpt-4"},
				},
			},
		},
	}
	data, err := json.Marshal(opt)
	require.NoError(t, err)
	assert.Contains(t, string(data), `"type":"select"`)

	var decoded api.ConfigOption
	require.NoError(t, json.Unmarshal(data, &decoded))
	require.NotNil(t, decoded.Select)
	assert.Equal(t, "opt-1", decoded.Select.ID)
}

func TestConfigOption_Marshal_Empty(t *testing.T) {
	t.Parallel()
	_, err := json.Marshal(api.ConfigOption{})
	require.Error(t, err)
}

func TestConfigOption_Unmarshal_UnknownType(t *testing.T) {
	t.Parallel()
	var opt api.ConfigOption
	err := json.Unmarshal([]byte(`{"type":"unknown"}`), &opt)
	require.Error(t, err)
}

// ── ConfigSelectOptions marshal/unmarshal ───────────────────────────────────

func TestConfigSelectOptions_RoundTrip_Ungrouped(t *testing.T) {
	t.Parallel()
	opts := api.ConfigSelectOptions{
		Ungrouped: []api.ConfigSelectOption{
			{Name: "A", Value: "a"},
			{Name: "B", Value: "b"},
		},
	}
	data, err := json.Marshal(opts)
	require.NoError(t, err)

	var decoded api.ConfigSelectOptions
	require.NoError(t, json.Unmarshal(data, &decoded))
	require.Len(t, decoded.Ungrouped, 2)
	assert.Equal(t, "a", decoded.Ungrouped[0].Value)
}

func TestConfigSelectOptions_RoundTrip_Grouped(t *testing.T) {
	t.Parallel()
	opts := api.ConfigSelectOptions{
		Grouped: []api.ConfigSelectGroup{
			{Group: "models", Name: "Models", Options: []api.ConfigSelectOption{{Name: "A", Value: "a"}}},
		},
	}
	data, err := json.Marshal(opts)
	require.NoError(t, err)

	var decoded api.ConfigSelectOptions
	require.NoError(t, json.Unmarshal(data, &decoded))
	require.Len(t, decoded.Grouped, 1)
	assert.Equal(t, "models", decoded.Grouped[0].Group)
}

func TestConfigSelectOptions_Marshal_Empty(t *testing.T) {
	t.Parallel()
	_, err := json.Marshal(api.ConfigSelectOptions{})
	require.Error(t, err)
}

// ── EnvVar ──────────────────────────────────────────────────────────────────

func TestEnvVar_JSON_RoundTrip(t *testing.T) {
	t.Parallel()
	ev := api.EnvVar{Name: "PATH", Value: "/usr/bin"}
	data, err := json.Marshal(ev)
	require.NoError(t, err)

	var decoded api.EnvVar
	require.NoError(t, json.Unmarshal(data, &decoded))
	assert.Equal(t, "PATH", decoded.Name)
	assert.Equal(t, "/usr/bin", decoded.Value)
}
