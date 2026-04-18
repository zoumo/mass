package workspace

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

func TestGetByName(t *testing.T) {
	var gotKey pkgariapi.ObjectKey
	mc := newMockClient()
	mc.getFn = func(_ context.Context, key pkgariapi.ObjectKey, _ pkgariapi.Object) error {
		gotKey = key
		return nil
	}

	cmd := NewCommand(clientFn(mc))
	cmd.SetArgs([]string{"get", "ws1"})
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})

	err := cmd.Execute()
	require.NoError(t, err)
	assert.Equal(t, "ws1", gotKey.Name)
}

func TestGetMultiple(t *testing.T) {
	var keys []string
	mc := newMockClient()
	mc.getFn = func(_ context.Context, key pkgariapi.ObjectKey, _ pkgariapi.Object) error {
		keys = append(keys, key.Name)
		return nil
	}

	cmd := NewCommand(clientFn(mc))
	cmd.SetArgs([]string{"get", "ws1", "ws2"})
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})

	err := cmd.Execute()
	require.NoError(t, err)
	assert.Equal(t, []string{"ws1", "ws2"}, keys)
}
