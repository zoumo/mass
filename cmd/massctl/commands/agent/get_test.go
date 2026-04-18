package agent

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
	mock := &mockClient{
		listFn: func(_ context.Context, _ pkgariapi.ObjectList, _ ...pkgariapi.ListOption) error {
			listed = true
			return nil
		},
	}

	cmd := NewCommand(newMockClientFn(mock))
	cmd.SetArgs([]string{"get"})
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})

	err := cmd.Execute()
	require.NoError(t, err)
	assert.True(t, listed, "List should have been called")
}

func TestGetByName(t *testing.T) {
	var gotKey pkgariapi.ObjectKey
	mock := &mockClient{
		getFn: func(_ context.Context, key pkgariapi.ObjectKey, _ pkgariapi.Object) error {
			gotKey = key
			return nil
		},
	}

	cmd := NewCommand(newMockClientFn(mock))
	cmd.SetArgs([]string{"get", "my-agent"})
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})

	err := cmd.Execute()
	require.NoError(t, err)
	assert.Equal(t, "my-agent", gotKey.Name)
}

func TestGetMultiple(t *testing.T) {
	var keys []string
	mock := &mockClient{
		getFn: func(_ context.Context, key pkgariapi.ObjectKey, _ pkgariapi.Object) error {
			keys = append(keys, key.Name)
			return nil
		},
	}

	cmd := NewCommand(newMockClientFn(mock))
	cmd.SetArgs([]string{"get", "agent-a", "agent-b"})
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})

	err := cmd.Execute()
	require.NoError(t, err)
	assert.Equal(t, []string{"agent-a", "agent-b"}, keys)
}

func TestGetJSON(t *testing.T) {
	mock := &mockClient{
		listFn: func(_ context.Context, _ pkgariapi.ObjectList, _ ...pkgariapi.ListOption) error {
			return nil
		},
	}

	var buf bytes.Buffer
	cmd := NewCommand(newMockClientFn(mock))
	cmd.SetArgs([]string{"get", "-o", "json"})
	cmd.SetOut(&buf)
	cmd.SetErr(&bytes.Buffer{})

	err := cmd.Execute()
	require.NoError(t, err)
	assert.Contains(t, buf.String(), "[")
}
