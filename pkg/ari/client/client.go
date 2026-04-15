// Package client provides a Dial helper that returns typed ARI clients
// backed by pkg/jsonrpc.
package client

import (
	"context"

	"github.com/zoumo/mass/pkg/jsonrpc"
)

// ARIClient bundles all three typed ARI clients: workspace, agentrun, and agent.
// Construct it with Dial; use the exported fields to call individual methods.
type ARIClient struct {
	Workspace *WorkspaceClient
	AgentRun  *AgentRunClient
	Agent     *AgentClient

	raw *jsonrpc.Client
}

// Close closes the underlying connection to the ARI server.
func (c *ARIClient) Close() error {
	return c.raw.Close()
}

// DisconnectNotify returns a channel that is closed when the server
// disconnects or the underlying connection is closed.
func (c *ARIClient) DisconnectNotify() <-chan struct{} {
	return c.raw.DisconnectNotify()
}

// Dial connects to the ARI server at the given Unix socket path and returns
// an ARIClient. The connection stays open until Close is called or the server
// disconnects.
func Dial(ctx context.Context, socketPath string, opts ...jsonrpc.DialOption) (*ARIClient, error) {
	c, err := jsonrpc.Dial(ctx, "unix", socketPath, opts...)
	if err != nil {
		return nil, err
	}
	return newARIClient(c), nil
}

// newARIClient wraps an existing jsonrpc.Client and wires the typed sub-clients.
func newARIClient(c *jsonrpc.Client) *ARIClient {
	return &ARIClient{
		Workspace: NewWorkspaceClient(c),
		AgentRun:  NewAgentRunClient(c),
		Agent:     NewAgentClient(c),
		raw:       c,
	}
}
