package agentrun

import (
	"bytes"
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	pkgariapi "github.com/zoumo/mass/pkg/ari/api"
)

func TestGetListSuccess(t *testing.T) {
	var listed bool
	mc := newMockClient()
	mc.listFn = func(_ context.Context, _ pkgariapi.ObjectList, _ ...pkgariapi.ListOption) error {
		listed = true
		return nil
	}

	cmd := NewCommand(clientFn(mc))
	cmd.SetArgs([]string{"get"})
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})

	err := cmd.Execute()
	require.NoError(t, err)
	assert.True(t, listed, "List should have been called")
}

func TestGetListWithWorkspaceFilter(t *testing.T) {
	var listed bool
	mc := newMockClient()
	mc.listFn = func(_ context.Context, _ pkgariapi.ObjectList, _ ...pkgariapi.ListOption) error {
		listed = true
		return nil
	}

	cmd := NewCommand(clientFn(mc))
	cmd.SetArgs([]string{"get", "-w", "ws1"})
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})

	err := cmd.Execute()
	require.NoError(t, err)
	assert.True(t, listed, "List should have been called")
}

func TestGetByName(t *testing.T) {
	var gotKey pkgariapi.ObjectKey
	mc := newMockClient()
	mc.getFn = func(_ context.Context, key pkgariapi.ObjectKey, _ pkgariapi.Object) error {
		gotKey = key
		return nil
	}

	cmd := NewCommand(clientFn(mc))
	cmd.SetArgs([]string{"get", "run1", "-w", "ws1"})
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})

	err := cmd.Execute()
	require.NoError(t, err)
	assert.Equal(t, "ws1", gotKey.Workspace)
	assert.Equal(t, "run1", gotKey.Name)
}

func TestGetMissingWorkspace(t *testing.T) {
	mc := newMockClient()
	cmd := NewCommand(clientFn(mc))
	cmd.SetArgs([]string{"get", "run1"})
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})

	err := cmd.Execute()
	require.Error(t, err, "get without -w should fail")
	assert.Contains(t, err.Error(), "--workspace/-w is required")
}
