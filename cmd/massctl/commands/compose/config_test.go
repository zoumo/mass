package compose

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseConfig_LocalWorkspace(t *testing.T) {
	input := `
kind: workspace-compose
metadata:
  name: mass-e2e
spec:
  source:
    type: local
    path: /tmp/myproject
  runs:
    - name: codex
      agent: codex
    - name: claude-code
      agent: claude
      systemPrompt: "You are a helpful assistant."
`
	cfg, err := parseConfig([]byte(input))
	require.NoError(t, err)

	assert.Equal(t, "workspace-compose", cfg.Kind)
	assert.Equal(t, "mass-e2e", cfg.Metadata.Name)
	assert.Equal(t, "local", cfg.Spec.Source.Type)
	assert.Equal(t, "/tmp/myproject", cfg.Spec.Source.Path)

	require.Len(t, cfg.Spec.Runs, 2)
	assert.Equal(t, "codex", cfg.Spec.Runs[0].Name)
	assert.Equal(t, "codex", cfg.Spec.Runs[0].Agent)
	assert.Empty(t, cfg.Spec.Runs[0].SystemPrompt)

	assert.Equal(t, "claude-code", cfg.Spec.Runs[1].Name)
	assert.Equal(t, "claude", cfg.Spec.Runs[1].Agent)
	assert.Equal(t, "You are a helpful assistant.", cfg.Spec.Runs[1].SystemPrompt)
}

func TestParseConfig_GitWorkspace(t *testing.T) {
	input := `
kind: workspace-compose
metadata:
  name: my-ws
spec:
  source:
    type: git
    url: https://github.com/example/repo.git
    ref: main
  runs: []
`
	cfg, err := parseConfig([]byte(input))
	require.NoError(t, err)

	assert.Equal(t, "git", cfg.Spec.Source.Type)
	assert.Equal(t, "https://github.com/example/repo.git", cfg.Spec.Source.URL)
	assert.Equal(t, "main", cfg.Spec.Source.Ref)
}

func TestParseConfig_EmptyDirWorkspace(t *testing.T) {
	input := `
kind: workspace-compose
metadata:
  name: scratch
spec:
  source:
    type: emptyDir
  runs:
    - name: gsd-pi
      agent: gsd-pi
`
	cfg, err := parseConfig([]byte(input))
	require.NoError(t, err)

	assert.Equal(t, "scratch", cfg.Metadata.Name)
	assert.Equal(t, "emptyDir", cfg.Spec.Source.Type)
	require.Len(t, cfg.Spec.Runs, 1)
	assert.Equal(t, "gsd-pi", cfg.Spec.Runs[0].Name)
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
  runs: []
`
	_, err := parseConfig([]byte(input))
	assert.ErrorContains(t, err, "kind")
}

func TestParseConfig_MissingMetadataName(t *testing.T) {
	input := `
kind: workspace-compose
metadata:
  name: ""
spec:
  source:
    type: emptyDir
  runs: []
`
	_, err := parseConfig([]byte(input))
	assert.ErrorContains(t, err, "metadata.name")
}

func TestParseConfig_MissingRunName(t *testing.T) {
	input := `
kind: workspace-compose
metadata:
  name: ws
spec:
  source:
    type: emptyDir
  runs:
    - name: ""
      agent: codex
`
	_, err := parseConfig([]byte(input))
	assert.ErrorContains(t, err, "spec.runs[0].name")
}

func TestParseConfig_MissingRunAgent(t *testing.T) {
	input := `
kind: workspace-compose
metadata:
  name: ws
spec:
  source:
    type: emptyDir
  runs:
    - name: codex
      agent: ""
`
	_, err := parseConfig([]byte(input))
	assert.ErrorContains(t, err, "spec.runs[0].agent")
}
