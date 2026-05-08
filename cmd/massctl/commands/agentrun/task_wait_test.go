package agentrun

import (
	"bytes"
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/zoumo/mass/cmd/massctl/commands/cliutil"
	pkgariapi "github.com/zoumo/mass/pkg/ari/api"
	apiruntime "github.com/zoumo/mass/pkg/runtime-spec/api"
)

func TestTaskWaitReturnsExitErrorAndClosesClient(t *testing.T) {
	mc := newMockClient()
	mc.getFn = func(_ context.Context, _ pkgariapi.ObjectKey, obj pkgariapi.Object) error {
		ar := obj.(*pkgariapi.AgentRun)
		ar.Status.Phase = apiruntime.PhaseError
		ar.Status.ErrorMessage = "boom"
		return nil
	}
	mc.agentRunOps.taskGetFn = func(_ context.Context, _ *pkgariapi.AgentRunTaskGetParams) (*pkgariapi.AgentTask, error) {
		return &pkgariapi.AgentTask{ID: "task-0001"}, nil
	}

	cmd := newTaskWaitCmd(clientFn(mc))
	cmd.SetArgs([]string{"task-0001", "-w", "ws1", "--run", "agent-a"})
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})

	err := cmd.Execute()
	require.Error(t, err)

	var exitErr *cliutil.ExitError
	require.ErrorAs(t, err, &exitErr, "task wait should return an ExitError")
	assert.Equal(t, 2, exitErr.Code)
	assert.EqualValues(t, 1, mc.closeCount.Load(), "client should be closed before returning")
}
