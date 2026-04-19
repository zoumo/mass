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

func TestApplyMutuallyExclusive(t *testing.T) {
	path := writeYAML(t, `
metadata:
  name: test-agent
spec:
  command: /usr/bin/test
`)
	mock := &mockClient{}
	cmd := NewCommand(newMockClientFn(mock))
	cmd.SetArgs([]string{"apply", "--file", path, "some-name"})

	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "mutually exclusive")
}

func TestApplyNoArgsNoFile(t *testing.T) {
	mock := &mockClient{}
	cmd := NewCommand(newMockClientFn(mock))
	cmd.SetArgs([]string{"apply"})

	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "provide -f")
}

func TestApplyInlineDisabled(t *testing.T) {
	var updatedAgent *pkgariapi.Agent
	mock := &mockClient{
		getFn: func(_ context.Context, key pkgariapi.ObjectKey, obj pkgariapi.Object) error {
			ag := obj.(*pkgariapi.Agent)
			ag.Metadata.Name = key.Name
			ag.Spec.Command = "echo"
			return nil
		},
		updateFn: func(_ context.Context, obj pkgariapi.Object) error {
			updatedAgent = obj.(*pkgariapi.Agent)
			return nil
		},
	}
	cmd := NewCommand(newMockClientFn(mock))
	cmd.SetArgs([]string{"apply", "my-agent", "--disabled"})
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})

	err := cmd.Execute()
	require.NoError(t, err)
	require.NotNil(t, updatedAgent)
	require.NotNil(t, updatedAgent.Spec.Disabled)
	assert.True(t, *updatedAgent.Spec.Disabled)
}

func TestApplyInlineDisabledFalse(t *testing.T) {
	var updatedAgent *pkgariapi.Agent
	mock := &mockClient{
		getFn: func(_ context.Context, key pkgariapi.ObjectKey, obj pkgariapi.Object) error {
			ag := obj.(*pkgariapi.Agent)
			ag.Metadata.Name = key.Name
			ag.Spec.Command = "echo"
			disabled := true
			ag.Spec.Disabled = &disabled
			return nil
		},
		updateFn: func(_ context.Context, obj pkgariapi.Object) error {
			updatedAgent = obj.(*pkgariapi.Agent)
			return nil
		},
	}
	cmd := NewCommand(newMockClientFn(mock))
	cmd.SetArgs([]string{"apply", "my-agent", "--disabled=false"})
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})

	err := cmd.Execute()
	require.NoError(t, err)
	require.NotNil(t, updatedAgent)
	require.NotNil(t, updatedAgent.Spec.Disabled)
	assert.False(t, *updatedAgent.Spec.Disabled)
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
