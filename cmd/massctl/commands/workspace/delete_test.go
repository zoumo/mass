package workspace

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
	mc := newMockClient()
	mc.deleteFn = func(_ context.Context, _ pkgariapi.ObjectKey, _ pkgariapi.Object) error {
		deleted = true
		return nil
	}

	cmd := NewCommand(clientFn(mc))
	cmd.SetArgs([]string{"delete", "ws1"})
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})

	err := cmd.Execute()
	require.NoError(t, err)
	assert.True(t, deleted, "Delete should have been called")
}

func TestDeleteMissingArgs(t *testing.T) {
	mc := newMockClient()
	cmd := NewCommand(clientFn(mc))
	cmd.SetArgs([]string{"delete"})
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})

	err := cmd.Execute()
	assert.Error(t, err, "delete without args should fail")
}

func TestDeleteForce(t *testing.T) {
	var (
		stoppedRuns []string
		deletedRuns []string
		deletedWS   bool
	)

	mc := newMockClient()
	mc.listFn = func(_ context.Context, list pkgariapi.ObjectList, _ ...pkgariapi.ListOption) error {
		arList := list.(*pkgariapi.AgentRunList)
		arList.Items = []pkgariapi.AgentRun{
			{Metadata: pkgariapi.ObjectMeta{Name: "run1"}},
			{Metadata: pkgariapi.ObjectMeta{Name: "run2"}},
		}
		return nil
	}
	mc.agentRunOps = &mockAgentRunOps{
		stopFn: func(_ context.Context, key pkgariapi.ObjectKey) error {
			stoppedRuns = append(stoppedRuns, key.Name)
			return nil
		},
	}
	mc.deleteFn = func(_ context.Context, key pkgariapi.ObjectKey, obj pkgariapi.Object) error {
		switch obj.(type) {
		case *pkgariapi.AgentRun:
			deletedRuns = append(deletedRuns, key.Name)
		case *pkgariapi.Workspace:
			deletedWS = true
		}
		return nil
	}

	var buf bytes.Buffer
	cmd := NewCommand(clientFn(mc))
	cmd.SetArgs([]string{"delete", "ws1", "--force"})
	cmd.SetOut(&buf)
	cmd.SetErr(&bytes.Buffer{})

	err := cmd.Execute()
	require.NoError(t, err)
	assert.Equal(t, []string{"run1", "run2"}, stoppedRuns)
	assert.Equal(t, []string{"run1", "run2"}, deletedRuns)
	assert.True(t, deletedWS)
	assert.Contains(t, buf.String(), `agentrun "run1" deleted`)
	assert.Contains(t, buf.String(), `agentrun "run2" deleted`)
	assert.Contains(t, buf.String(), `workspace "ws1" deleted`)
}
