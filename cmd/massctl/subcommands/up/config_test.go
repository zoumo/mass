package up

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseConfig_LocalWorkspace(t *testing.T) {
	input := `
kind: workspace-up
metadata:
  name: mass-e2e
spec:
  source:
    type: local
    path: /tmp/myproject
  agents:
    - metadata:
        name: codex
      spec:
        agent: codex
    - metadata:
        name: claude-code
      spec:
        agent: claude
        restartPolicy: try_reload
        systemPrompt: "You are a helpful assistant."
`
	cfg, err := parseConfig([]byte(input))
	require.NoError(t, err)

	assert.Equal(t, "workspace-up", cfg.Kind)
	assert.Equal(t, "mass-e2e", cfg.Metadata.Name)
	assert.Equal(t, "local", cfg.Spec.Source.Type)
	assert.Equal(t, "/tmp/myproject", cfg.Spec.Source.Path)

	require.Len(t, cfg.Spec.Agents, 2)
	assert.Equal(t, "codex", cfg.Spec.Agents[0].Metadata.Name)
	assert.Equal(t, "codex", cfg.Spec.Agents[0].Spec.Agent)
	assert.Empty(t, cfg.Spec.Agents[0].Spec.RestartPolicy)
	assert.Empty(t, cfg.Spec.Agents[0].Spec.SystemPrompt)

	assert.Equal(t, "claude-code", cfg.Spec.Agents[1].Metadata.Name)
	assert.Equal(t, "claude", cfg.Spec.Agents[1].Spec.Agent)
	assert.Equal(t, "try_reload", cfg.Spec.Agents[1].Spec.RestartPolicy)
	assert.Equal(t, "You are a helpful assistant.", cfg.Spec.Agents[1].Spec.SystemPrompt)
}

func TestParseConfig_GitWorkspace(t *testing.T) {
	input := `
kind: workspace-up
metadata:
  name: my-ws
spec:
  source:
    type: git
    url: https://github.com/example/repo.git
    ref: main
  agents: []
`
	cfg, err := parseConfig([]byte(input))
	require.NoError(t, err)

	assert.Equal(t, "git", cfg.Spec.Source.Type)
	assert.Equal(t, "https://github.com/example/repo.git", cfg.Spec.Source.URL)
	assert.Equal(t, "main", cfg.Spec.Source.Ref)
}

func TestParseConfig_EmptyDirWorkspace(t *testing.T) {
	input := `
kind: workspace-up
metadata:
  name: scratch
spec:
  source:
    type: emptyDir
  agents:
    - metadata:
        name: gsd-pi
      spec:
        agent: gsd-pi
`
	cfg, err := parseConfig([]byte(input))
	require.NoError(t, err)

	assert.Equal(t, "scratch", cfg.Metadata.Name)
	assert.Equal(t, "emptyDir", cfg.Spec.Source.Type)
	require.Len(t, cfg.Spec.Agents, 1)
	assert.Equal(t, "gsd-pi", cfg.Spec.Agents[0].Metadata.Name)
}

func TestParseConfig_InvalidYAML(t *testing.T) {
	_, err := parseConfig([]byte("not: valid: yaml: {{{"))
	assert.Error(t, err)
}

func TestParseConfig_WrongKind(t *testing.T) {
	input := `
kind: agent
metadata:
  name: foo
spec:
  source:
    type: emptyDir
  agents: []
`
	_, err := parseConfig([]byte(input))
	assert.ErrorContains(t, err, "kind")
}

func TestParseConfig_MissingMetadataName(t *testing.T) {
	input := `
kind: workspace-up
metadata:
  name: ""
spec:
  source:
    type: emptyDir
  agents: []
`
	_, err := parseConfig([]byte(input))
	assert.ErrorContains(t, err, "metadata.name")
}

func TestParseConfig_MissingAgentMetadataName(t *testing.T) {
	input := `
kind: workspace-up
metadata:
  name: ws
spec:
  source:
    type: emptyDir
  agents:
    - metadata:
        name: ""
      spec:
        agent: codex
`
	_, err := parseConfig([]byte(input))
	assert.ErrorContains(t, err, "spec.agents[0].metadata.name")
}

func TestParseConfig_MissingAgentSpecAgent(t *testing.T) {
	input := `
kind: workspace-up
metadata:
  name: ws
spec:
  source:
    type: emptyDir
  agents:
    - metadata:
        name: codex
      spec:
        agent: ""
`
	_, err := parseConfig([]byte(input))
	assert.ErrorContains(t, err, "spec.agents[0].spec.agent")
}
