package agent

import (
	"bytes"
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	pkgariapi "github.com/zoumo/mass/pkg/ari/api"
)

func TestApplyMissingFile(t *testing.T) {
	mock := &mockClient{}
	cmd := NewCommand(newMockClientFn(mock))
	cmd.SetArgs([]string{"apply", "--file", "/nonexistent/path/agent.yaml"})

	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "reading agent file")
}

func TestApplyInvalidYAML(t *testing.T) {
	path := writeYAML(t, "not: valid: yaml: {{{")
	mock := &mockClient{}
	cmd := NewCommand(newMockClientFn(mock))
	cmd.SetArgs([]string{"apply", "--file", path})

	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "parsing agent YAML")
}

func TestApplyMissingName(t *testing.T) {
	path := writeYAML(t, `
metadata:
  name: ""
spec:
  command: /usr/bin/test
`)
	mock := &mockClient{}
	cmd := NewCommand(newMockClientFn(mock))
	cmd.SetArgs([]string{"apply", "--file", path})

	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "metadata.name")
}

func TestApplyMissingCommand(t *testing.T) {
	path := writeYAML(t, `
metadata:
  name: test-agent
spec:
  command: ""
`)
	mock := &mockClient{}
	cmd := NewCommand(newMockClientFn(mock))
	cmd.SetArgs([]string{"apply", "--file", path})

	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "spec.command")
}

func TestApplySuccess(t *testing.T) {
	path := writeYAML(t, `
metadata:
  name: test-agent
spec:
  command: /usr/bin/test
`)
	mock := &mockClient{
		createFn: func(_ context.Context, _ pkgariapi.Object) error {
			return nil
		},
	}

	cmd := NewCommand(newMockClientFn(mock))
	cmd.SetArgs([]string{"apply", "--file", path})
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})

	err := cmd.Execute()
	require.NoError(t, err)
}
