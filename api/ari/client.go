// Package ari contains the ARI protocol types and typed client wrappers.
// This file defines typed client wrappers for all ARI service methods.
package ari

import (
	"context"

	"github.com/zoumo/oar/api"
	"github.com/zoumo/oar/pkg/jsonrpc"
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

func (c *WorkspaceClient) Create(ctx context.Context, req *WorkspaceCreateParams) (*WorkspaceCreateResult, error) {
	var result WorkspaceCreateResult
	if err := c.c.Call(ctx, api.MethodWorkspaceCreate, req, &result); err != nil {
		return nil, err
	}
	return &result, nil
}

func (c *WorkspaceClient) Status(ctx context.Context, req *WorkspaceStatusParams) (*WorkspaceStatusResult, error) {
	var result WorkspaceStatusResult
	if err := c.c.Call(ctx, api.MethodWorkspaceStatus, req, &result); err != nil {
		return nil, err
	}
	return &result, nil
}

func (c *WorkspaceClient) List(ctx context.Context) (*WorkspaceListResult, error) {
	var result WorkspaceListResult
	if err := c.c.Call(ctx, api.MethodWorkspaceList, WorkspaceListParams{}, &result); err != nil {
		return nil, err
	}
	return &result, nil
}

func (c *WorkspaceClient) Delete(ctx context.Context, req *WorkspaceDeleteParams) error {
	return c.c.Call(ctx, api.MethodWorkspaceDelete, req, nil)
}

func (c *WorkspaceClient) Send(ctx context.Context, req *WorkspaceSendParams) (*WorkspaceSendResult, error) {
	var result WorkspaceSendResult
	if err := c.c.Call(ctx, api.MethodWorkspaceSend, req, &result); err != nil {
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

func (c *AgentRunClient) Create(ctx context.Context, req *AgentRunCreateParams) (*AgentRunCreateResult, error) {
	var result AgentRunCreateResult
	if err := c.c.Call(ctx, api.MethodAgentRunCreate, req, &result); err != nil {
		return nil, err
	}
	return &result, nil
}

func (c *AgentRunClient) Prompt(ctx context.Context, req *AgentRunPromptParams) (*AgentRunPromptResult, error) {
	var result AgentRunPromptResult
	if err := c.c.Call(ctx, api.MethodAgentRunPrompt, req, &result); err != nil {
		return nil, err
	}
	return &result, nil
}

func (c *AgentRunClient) Cancel(ctx context.Context, req *AgentRunCancelParams) error {
	return c.c.Call(ctx, api.MethodAgentRunCancel, req, nil)
}

func (c *AgentRunClient) Stop(ctx context.Context, req *AgentRunStopParams) error {
	return c.c.Call(ctx, api.MethodAgentRunStop, req, nil)
}

func (c *AgentRunClient) Delete(ctx context.Context, req *AgentRunDeleteParams) error {
	return c.c.Call(ctx, api.MethodAgentRunDelete, req, nil)
}

func (c *AgentRunClient) Restart(ctx context.Context, req *AgentRunRestartParams) (*AgentRunRestartResult, error) {
	var result AgentRunRestartResult
	if err := c.c.Call(ctx, api.MethodAgentRunRestart, req, &result); err != nil {
		return nil, err
	}
	return &result, nil
}

func (c *AgentRunClient) List(ctx context.Context, req *AgentRunListParams) (*AgentRunListResult, error) {
	var result AgentRunListResult
	if err := c.c.Call(ctx, api.MethodAgentRunList, req, &result); err != nil {
		return nil, err
	}
	return &result, nil
}

func (c *AgentRunClient) Status(ctx context.Context, req *AgentRunStatusParams) (*AgentRunStatusResult, error) {
	var result AgentRunStatusResult
	if err := c.c.Call(ctx, api.MethodAgentRunStatus, req, &result); err != nil {
		return nil, err
	}
	return &result, nil
}

func (c *AgentRunClient) Attach(ctx context.Context, req *AgentRunAttachParams) (*AgentRunAttachResult, error) {
	var result AgentRunAttachResult
	if err := c.c.Call(ctx, api.MethodAgentRunAttach, req, &result); err != nil {
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

func (c *AgentClient) Set(ctx context.Context, req *AgentSetParams) (*AgentSetResult, error) {
	var result AgentSetResult
	if err := c.c.Call(ctx, api.MethodAgentSet, req, &result); err != nil {
		return nil, err
	}
	return &result, nil
}

func (c *AgentClient) Get(ctx context.Context, req *AgentGetParams) (*AgentGetResult, error) {
	var result AgentGetResult
	if err := c.c.Call(ctx, api.MethodAgentGet, req, &result); err != nil {
		return nil, err
	}
	return &result, nil
}

func (c *AgentClient) List(ctx context.Context) (*AgentListResult, error) {
	var result AgentListResult
	if err := c.c.Call(ctx, api.MethodAgentList, AgentListParams{}, &result); err != nil {
		return nil, err
	}
	return &result, nil
}

func (c *AgentClient) Delete(ctx context.Context, req *AgentDeleteParams) error {
	return c.c.Call(ctx, api.MethodAgentDelete, req, nil)
}
