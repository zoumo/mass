// Package client provides a typed ARI client implementing api.Client.
package client

import (
	"context"
	"encoding/json"
	"fmt"

	pkgariapi "github.com/zoumo/mass/pkg/ari/api"
	runapi "github.com/zoumo/mass/pkg/agentrun/api"
	"github.com/zoumo/mass/pkg/jsonrpc"
)

// Compile-time checks.
var (
	_ pkgariapi.Client       = (*ariClient)(nil)
	_ pkgariapi.AgentRunOps  = (*agentRunOps)(nil)
	_ pkgariapi.WorkspaceOps = (*workspaceOps)(nil)
)

// ariClient implements api.Client using JSON-RPC.
type ariClient struct {
	raw        *jsonrpc.Client
	agentRuns  agentRunOps
	workspaces workspaceOps
}

// Dial connects to the ARI server at the given Unix socket path and returns
// a typed Client. The connection stays open until Close is called.
func Dial(ctx context.Context, socketPath string, opts ...jsonrpc.DialOption) (pkgariapi.Client, error) {
	c, err := jsonrpc.Dial(ctx, "unix", socketPath, opts...)
	if err != nil {
		return nil, err
	}
	return newClient(c), nil
}

// newClient wraps a jsonrpc.Client into a typed api.Client.
func newClient(c *jsonrpc.Client) *ariClient {
	ac := &ariClient{raw: c}
	ac.agentRuns = agentRunOps{c: c}
	ac.workspaces = workspaceOps{c: c}
	return ac
}

// Close closes the underlying connection.
func (c *ariClient) Close() error { return c.raw.Close() }

// DisconnectNotify returns a channel that is closed when the connection drops.
func (c *ariClient) DisconnectNotify() <-chan struct{} { return c.raw.DisconnectNotify() }

// AgentRuns returns the sub-interface for non-CRUD agent run operations.
func (c *ariClient) AgentRuns() pkgariapi.AgentRunOps { return &c.agentRuns }

// Workspaces returns the sub-interface for non-CRUD workspace operations.
func (c *ariClient) Workspaces() pkgariapi.WorkspaceOps { return &c.workspaces }

// ────────────────────────────────────────────────────────────────────────────
// CRUD — type-switched routing
// ────────────────────────────────────────────────────────────────────────────

// Create persists a new domain object. obj is updated in-place with
// server-assigned fields.
func (c *ariClient) Create(ctx context.Context, obj pkgariapi.Object) error {
	switch o := obj.(type) {
	case *pkgariapi.Workspace:
		return callInto(c.raw, ctx, pkgariapi.MethodWorkspaceCreate, o, o)
	case *pkgariapi.AgentRun:
		return callInto(c.raw, ctx, pkgariapi.MethodAgentRunCreate, o, o)
	case *pkgariapi.Agent:
		return callInto(c.raw, ctx, pkgariapi.MethodAgentCreate, o, o)
	default:
		return fmt.Errorf("ari: Create: unsupported type %T", obj)
	}
}

// Get retrieves a domain object by key. obj is updated in-place.
func (c *ariClient) Get(ctx context.Context, key pkgariapi.ObjectKey, obj pkgariapi.Object) error {
	switch o := obj.(type) {
	case *pkgariapi.Workspace:
		return callInto(c.raw, ctx, pkgariapi.MethodWorkspaceGet, key, o)
	case *pkgariapi.AgentRun:
		return callInto(c.raw, ctx, pkgariapi.MethodAgentRunGet, key, o)
	case *pkgariapi.Agent:
		return callInto(c.raw, ctx, pkgariapi.MethodAgentGet, key, o)
	default:
		return fmt.Errorf("ari: Get: unsupported type %T", obj)
	}
}

// Update modifies an existing domain object. obj is updated in-place.
func (c *ariClient) Update(ctx context.Context, obj pkgariapi.Object) error {
	switch o := obj.(type) {
	case *pkgariapi.Agent:
		return callInto(c.raw, ctx, pkgariapi.MethodAgentUpdate, o, o)
	default:
		return fmt.Errorf("ari: Update: unsupported type %T", obj)
	}
}

// List retrieves domain objects matching the given options.
func (c *ariClient) List(ctx context.Context, list pkgariapi.ObjectList, opts ...pkgariapi.ListOption) error {
	o := pkgariapi.ApplyListOptions(opts...)
	switch l := list.(type) {
	case *pkgariapi.WorkspaceList:
		return callInto(c.raw, ctx, pkgariapi.MethodWorkspaceList, o, l)
	case *pkgariapi.AgentRunList:
		return callInto(c.raw, ctx, pkgariapi.MethodAgentRunList, o, l)
	case *pkgariapi.AgentList:
		return callInto(c.raw, ctx, pkgariapi.MethodAgentList, o, l)
	default:
		return fmt.Errorf("ari: List: unsupported type %T", list)
	}
}

// Delete removes a domain object by key. obj is a type marker.
func (c *ariClient) Delete(ctx context.Context, key pkgariapi.ObjectKey, obj pkgariapi.Object) error {
	var method string
	switch obj.(type) {
	case *pkgariapi.Workspace:
		method = pkgariapi.MethodWorkspaceDelete
	case *pkgariapi.AgentRun:
		method = pkgariapi.MethodAgentRunDelete
	case *pkgariapi.Agent:
		method = pkgariapi.MethodAgentDelete
	default:
		return fmt.Errorf("ari: Delete: unsupported type %T", obj)
	}
	var raw json.RawMessage
	return c.raw.Call(ctx, method, key, &raw)
}

// ────────────────────────────────────────────────────────────────────────────
// AgentRunOps
// ────────────────────────────────────────────────────────────────────────────

type agentRunOps struct{ c *jsonrpc.Client }

func (o *agentRunOps) Prompt(ctx context.Context, key pkgariapi.ObjectKey, prompt []runapi.ContentBlock) (*pkgariapi.AgentRunPromptResult, error) {
	req := pkgariapi.AgentRunPromptParams{
		Workspace: key.Workspace,
		Name:      key.Name,
		Prompt:    prompt,
	}
	var result pkgariapi.AgentRunPromptResult
	if err := o.c.Call(ctx, pkgariapi.MethodAgentRunPrompt, req, &result); err != nil {
		return nil, err
	}
	return &result, nil
}

func (o *agentRunOps) Cancel(ctx context.Context, key pkgariapi.ObjectKey) error {
	var raw json.RawMessage
	return o.c.Call(ctx, pkgariapi.MethodAgentRunCancel, key, &raw)
}

func (o *agentRunOps) Stop(ctx context.Context, key pkgariapi.ObjectKey) error {
	var raw json.RawMessage
	return o.c.Call(ctx, pkgariapi.MethodAgentRunStop, key, &raw)
}

func (o *agentRunOps) Restart(ctx context.Context, key pkgariapi.ObjectKey) (*pkgariapi.AgentRun, error) {
	var result pkgariapi.AgentRun
	if err := o.c.Call(ctx, pkgariapi.MethodAgentRunRestart, key, &result); err != nil {
		return nil, err
	}
	return &result, nil
}

// ────────────────────────────────────────────────────────────────────────────
// WorkspaceOps
// ────────────────────────────────────────────────────────────────────────────

type workspaceOps struct{ c *jsonrpc.Client }

func (o *workspaceOps) Send(ctx context.Context, req *pkgariapi.WorkspaceSendParams) (*pkgariapi.WorkspaceSendResult, error) {
	var result pkgariapi.WorkspaceSendResult
	if err := o.c.Call(ctx, pkgariapi.MethodWorkspaceSend, req, &result); err != nil {
		return nil, err
	}
	return &result, nil
}

// ────────────────────────────────────────────────────────────────────────────
// helpers
// ────────────────────────────────────────────────────────────────────────────

// callInto calls the method and unmarshals the result into dst.
func callInto(c *jsonrpc.Client, ctx context.Context, method string, params, dst any) error {
	if err := c.Call(ctx, method, params, dst); err != nil {
		return fmt.Errorf("ari: %s: %w", method, err)
	}
	return nil
}
