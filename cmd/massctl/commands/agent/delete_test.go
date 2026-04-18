package agent

import (
	"bytes"
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	pkgariapi "github.com/zoumo/mass/pkg/ari/api"
)

func TestDeleteSuccess(t *testing.T) {
	var deleted bool
	mock := &mockClient{
		deleteFn: func(_ context.Context, _ pkgariapi.ObjectKey, _ pkgariapi.Object) error {
			deleted = true
			return nil
		},
	}

	cmd := NewCommand(newMockClientFn(mock))
	cmd.SetArgs([]string{"delete", "my-agent"})
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})

	err := cmd.Execute()
	require.NoError(t, err)
	assert.True(t, deleted, "Delete should have been called")
}

func TestDeleteMissingArgs(t *testing.T) {
	mock := &mockClient{}
	cmd := NewCommand(newMockClientFn(mock))
	cmd.SetArgs([]string{"delete"})
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})

	err := cmd.Execute()
	assert.Error(t, err, "delete without args should fail")
}

func TestDeleteMultiple(t *testing.T) {
	var names []string
	mock := &mockClient{
		deleteFn: func(_ context.Context, key pkgariapi.ObjectKey, _ pkgariapi.Object) error {
			names = append(names, key.Name)
			return nil
		},
	}

	cmd := NewCommand(newMockClientFn(mock))
	cmd.SetArgs([]string{"delete", "a1", "a2"})
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})

	err := cmd.Execute()
	require.NoError(t, err)
	assert.Equal(t, []string{"a1", "a2"}, names)
}
