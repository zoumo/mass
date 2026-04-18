package workspace

import (
	"bytes"
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	pkgariapi "github.com/zoumo/mass/pkg/ari/api"
)

func TestSendSuccess(t *testing.T) {
	var sentReq *pkgariapi.WorkspaceSendParams
	mc := newMockClient()
	mc.workspaceOps.sendFn = func(_ context.Context, req *pkgariapi.WorkspaceSendParams) (*pkgariapi.WorkspaceSendResult, error) {
		sentReq = req
		return &pkgariapi.WorkspaceSendResult{Delivered: true}, nil
	}

	cmd := NewCommand(clientFn(mc))
	cmd.SetArgs([]string{"send", "--name", "ws1", "--from", "agentA", "--to", "agentB", "--text", "hello"})
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})

	err := cmd.Execute()
	require.NoError(t, err)
	require.NotNil(t, sentReq, "Send should have been called")
	assert.Equal(t, "ws1", sentReq.Workspace)
	assert.Equal(t, "agentA", sentReq.From)
	assert.Equal(t, "agentB", sentReq.To)
}

func TestSendMissingFlags(t *testing.T) {
	mc := newMockClient()

	tests := []struct {
		name string
		args []string
	}{
		{"missing all", []string{"send"}},
		{"missing --from", []string{"send", "--name", "ws1", "--to", "b", "--text", "hi"}},
		{"missing --to", []string{"send", "--name", "ws1", "--from", "a", "--text", "hi"}},
		{"missing --text", []string{"send", "--name", "ws1", "--from", "a", "--to", "b"}},
		{"missing --name", []string{"send", "--from", "a", "--to", "b", "--text", "hi"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cmd := NewCommand(clientFn(mc))
			cmd.SetArgs(tt.args)
			cmd.SetOut(&bytes.Buffer{})
			cmd.SetErr(&bytes.Buffer{})

			err := cmd.Execute()
			assert.Error(t, err, "send with missing required flags should fail")
		})
	}
}
