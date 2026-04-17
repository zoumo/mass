package store_test

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zoumo/mass/pkg/agentd/store"
)

func TestResourceError_ErrorString(t *testing.T) {
	err := &store.ResourceError{
		Op:       "create",
		Resource: "workspace",
		Key:      "my-ws",
		Err:      store.ErrAlreadyExists,
	}
	assert.Equal(t, "create workspace my-ws: store: already exists", err.Error())
}

func TestResourceError_UnwrapAlreadyExists(t *testing.T) {
	err := &store.ResourceError{
		Op:       "create",
		Resource: "agent",
		Key:      "ws/agent-1",
		Err:      store.ErrAlreadyExists,
	}
	require.ErrorIs(t, err, store.ErrAlreadyExists)
	assert.NotErrorIs(t, err, store.ErrNotFound)
}

func TestResourceError_UnwrapNotFound(t *testing.T) {
	err := &store.ResourceError{
		Op:       "update",
		Resource: "workspace",
		Key:      "missing-ws",
		Err:      store.ErrNotFound,
	}
	require.ErrorIs(t, err, store.ErrNotFound)
	assert.NotErrorIs(t, err, store.ErrAlreadyExists)
}

func TestResourceError_WrappedInFmtErrorf(t *testing.T) {
	inner := &store.ResourceError{
		Op:       "delete",
		Resource: "agent",
		Key:      "ws/agent-2",
		Err:      store.ErrNotFound,
	}
	wrapped := errors.Join(errors.New("outer context"), inner)
	require.ErrorIs(t, wrapped, store.ErrNotFound)

	var re *store.ResourceError
	require.ErrorAs(t, wrapped, &re)
	assert.Equal(t, "delete", re.Op)
	assert.Equal(t, "agent", re.Resource)
	assert.Equal(t, "ws/agent-2", re.Key)
}
