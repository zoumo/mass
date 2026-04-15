// Package client provides typed ARI client wrappers.
// This file defines typed client wrappers for all ARI service methods.
package client

import (
	"context"

	pkgariapi "github.com/zoumo/mass/pkg/ari/api"
	"github.com/zoumo/mass/pkg/jsonrpc"
)

// ────────────────────────────────────────────────────────────────────────────
// Typed ARI clients (参考 ttrpc generated client pattern)
// ────────────────────────────────────────────────────────────────────────────

// WorkspaceClient is a typed client for WorkspaceService.
type WorkspaceClient struct {
	c *jsonrpc.Client
}

// NewWorkspaceClient creates a WorkspaceClient wrapping the given jsonrpc.Client.
func NewWorkspaceClient(c *jsonrpc.Client) *WorkspaceClient {
	return &WorkspaceClient{c: c}
}

func (c *WorkspaceClient) Create(ctx context.Context, req *pkgariapi.WorkspaceCreateParams) (*pkgariapi.WorkspaceCreateResult, error) {
	var result pkgariapi.WorkspaceCreateResult
	if err := c.c.Call(ctx, pkgariapi.MethodWorkspaceCreate, req, &result); err != nil {
		return nil, err
	}
	return &result, nil
}

func (c *WorkspaceClient) Status(ctx context.Context, req *pkgariapi.WorkspaceStatusParams) (*pkgariapi.WorkspaceStatusResult, error) {
	var result pkgariapi.WorkspaceStatusResult
	if err := c.c.Call(ctx, pkgariapi.MethodWorkspaceStatus, req, &result); err != nil {
		return nil, err
	}
	return &result, nil
}

func (c *WorkspaceClient) List(ctx context.Context) (*pkgariapi.WorkspaceListResult, error) {
	var result pkgariapi.WorkspaceListResult
	if err := c.c.Call(ctx, pkgariapi.MethodWorkspaceList, pkgariapi.WorkspaceListParams{}, &result); err != nil {
		return nil, err
	}
	return &result, nil
}

func (c *WorkspaceClient) Delete(ctx context.Context, req *pkgariapi.WorkspaceDeleteParams) error {
	return c.c.Call(ctx, pkgariapi.MethodWorkspaceDelete, req, nil)
}

func (c *WorkspaceClient) Send(ctx context.Context, req *pkgariapi.WorkspaceSendParams) (*pkgariapi.WorkspaceSendResult, error) {
	var result pkgariapi.WorkspaceSendResult
	if err := c.c.Call(ctx, pkgariapi.MethodWorkspaceSend, req, &result); err != nil {
		return nil, err
	}
	return &result, nil
}

// AgentRunClient is a typed client for AgentRunService.
type AgentRunClient struct {
	c *jsonrpc.Client
}

// NewAgentRunClient creates an AgentRunClient wrapping the given jsonrpc.Client.
func NewAgentRunClient(c *jsonrpc.Client) *AgentRunClient {
	return &AgentRunClient{c: c}
}

func (c *AgentRunClient) Create(ctx context.Context, req *pkgariapi.AgentRunCreateParams) (*pkgariapi.AgentRunCreateResult, error) {
	var result pkgariapi.AgentRunCreateResult
	if err := c.c.Call(ctx, pkgariapi.MethodAgentRunCreate, req, &result); err != nil {
		return nil, err
	}
	return &result, nil
}

func (c *AgentRunClient) Prompt(ctx context.Context, req *pkgariapi.AgentRunPromptParams) (*pkgariapi.AgentRunPromptResult, error) {
	var result pkgariapi.AgentRunPromptResult
	if err := c.c.Call(ctx, pkgariapi.MethodAgentRunPrompt, req, &result); err != nil {
		return nil, err
	}
	return &result, nil
}

func (c *AgentRunClient) Cancel(ctx context.Context, req *pkgariapi.AgentRunCancelParams) error {
	return c.c.Call(ctx, pkgariapi.MethodAgentRunCancel, req, nil)
}

func (c *AgentRunClient) Stop(ctx context.Context, req *pkgariapi.AgentRunStopParams) error {
	return c.c.Call(ctx, pkgariapi.MethodAgentRunStop, req, nil)
}

func (c *AgentRunClient) Delete(ctx context.Context, req *pkgariapi.AgentRunDeleteParams) error {
	return c.c.Call(ctx, pkgariapi.MethodAgentRunDelete, req, nil)
}

func (c *AgentRunClient) Restart(ctx context.Context, req *pkgariapi.AgentRunRestartParams) (*pkgariapi.AgentRunRestartResult, error) {
	var result pkgariapi.AgentRunRestartResult
	if err := c.c.Call(ctx, pkgariapi.MethodAgentRunRestart, req, &result); err != nil {
		return nil, err
	}
	return &result, nil
}

func (c *AgentRunClient) List(ctx context.Context, req *pkgariapi.AgentRunListParams) (*pkgariapi.AgentRunListResult, error) {
	var result pkgariapi.AgentRunListResult
	if err := c.c.Call(ctx, pkgariapi.MethodAgentRunList, req, &result); err != nil {
		return nil, err
	}
	return &result, nil
}

func (c *AgentRunClient) Status(ctx context.Context, req *pkgariapi.AgentRunStatusParams) (*pkgariapi.AgentRunStatusResult, error) {
	var result pkgariapi.AgentRunStatusResult
	if err := c.c.Call(ctx, pkgariapi.MethodAgentRunStatus, req, &result); err != nil {
		return nil, err
	}
	return &result, nil
}

func (c *AgentRunClient) Attach(ctx context.Context, req *pkgariapi.AgentRunAttachParams) (*pkgariapi.AgentRunAttachResult, error) {
	var result pkgariapi.AgentRunAttachResult
	if err := c.c.Call(ctx, pkgariapi.MethodAgentRunAttach, req, &result); err != nil {
		return nil, err
	}
	return &result, nil
}

// AgentClient is a typed client for AgentService.
type AgentClient struct {
	c *jsonrpc.Client
}

// NewAgentClient creates an AgentClient wrapping the given jsonrpc.Client.
func NewAgentClient(c *jsonrpc.Client) *AgentClient {
	return &AgentClient{c: c}
}

func (c *AgentClient) Set(ctx context.Context, req *pkgariapi.AgentSetParams) (*pkgariapi.AgentSetResult, error) {
	var result pkgariapi.AgentSetResult
	if err := c.c.Call(ctx, pkgariapi.MethodAgentSet, req, &result); err != nil {
		return nil, err
	}
	return &result, nil
}

func (c *AgentClient) Get(ctx context.Context, req *pkgariapi.AgentGetParams) (*pkgariapi.AgentGetResult, error) {
	var result pkgariapi.AgentGetResult
	if err := c.c.Call(ctx, pkgariapi.MethodAgentGet, req, &result); err != nil {
		return nil, err
	}
	return &result, nil
}

func (c *AgentClient) List(ctx context.Context) (*pkgariapi.AgentListResult, error) {
	var result pkgariapi.AgentListResult
	if err := c.c.Call(ctx, pkgariapi.MethodAgentList, pkgariapi.AgentListParams{}, &result); err != nil {
		return nil, err
	}
	return &result, nil
}

func (c *AgentClient) Delete(ctx context.Context, req *pkgariapi.AgentDeleteParams) error {
	return c.c.Call(ctx, pkgariapi.MethodAgentDelete, req, nil)
}
