// Package api contains the Shim RPC protocol types and typed client wrapper.
// This file defines the typed ShimClient wrapper for the Shim RPC protocol.
package api

import (
	"context"

	"github.com/zoumo/mass/pkg/jsonrpc"
)

// ShimClient is a typed client for ShimService.
type ShimClient struct {
	c *jsonrpc.Client
}

// NewShimClient creates a ShimClient wrapping the given jsonrpc.Client.
func NewShimClient(c *jsonrpc.Client) *ShimClient {
	return &ShimClient{c: c}
}

// RawClient returns the underlying jsonrpc.Client for DisconnectNotify and Close.
func (c *ShimClient) RawClient() *jsonrpc.Client {
	return c.c
}

// Close closes the underlying connection.
func (c *ShimClient) Close() error {
	return c.c.Close()
}

// DisconnectNotify returns a channel closed when the shim disconnects.
func (c *ShimClient) DisconnectNotify() <-chan struct{} {
	return c.c.DisconnectNotify()
}

func (c *ShimClient) Prompt(ctx context.Context, req *SessionPromptParams) (*SessionPromptResult, error) {
	var result SessionPromptResult
	if err := c.c.Call(ctx, MethodSessionPrompt, req, &result); err != nil {
		return nil, err
	}
	return &result, nil
}

// SendPrompt sends a session/prompt request without waiting for the response.
// The caller should monitor the notification stream for turn progress and
// turn_end events. Use this for interactive/TUI scenarios where blocking
// until turn completion is not desired.
func (c *ShimClient) SendPrompt(ctx context.Context, req *SessionPromptParams) error {
	return c.c.CallAsync(ctx, MethodSessionPrompt, req)
}

func (c *ShimClient) Cancel(ctx context.Context) error {
	return c.c.Call(ctx, MethodSessionCancel, nil, nil)
}

func (c *ShimClient) Load(ctx context.Context, req *SessionLoadParams) error {
	return c.c.Call(ctx, MethodSessionLoad, req, nil)
}

func (c *ShimClient) Subscribe(ctx context.Context, req *SessionSubscribeParams) (*SessionSubscribeResult, error) {
	var result SessionSubscribeResult
	if err := c.c.Call(ctx, MethodSessionSubscribe, req, &result); err != nil {
		return nil, err
	}
	return &result, nil
}

func (c *ShimClient) Status(ctx context.Context) (*RuntimeStatusResult, error) {
	var result RuntimeStatusResult
	if err := c.c.Call(ctx, MethodRuntimeStatus, nil, &result); err != nil {
		return nil, err
	}
	return &result, nil
}

func (c *ShimClient) History(ctx context.Context, req *RuntimeHistoryParams) (*RuntimeHistoryResult, error) {
	var result RuntimeHistoryResult
	if err := c.c.Call(ctx, MethodRuntimeHistory, req, &result); err != nil {
		return nil, err
	}
	return &result, nil
}

func (c *ShimClient) Stop(ctx context.Context) error {
	return c.c.Call(ctx, MethodRuntimeStop, nil, nil)
}
