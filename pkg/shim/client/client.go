// Package client provides a typed ShimClient and Dial helpers for connecting
// to shim Unix sockets backed by pkg/jsonrpc.
package client

import (
	"context"
	"encoding/json"

	apishim "github.com/zoumo/mass/pkg/shim/api"
	"github.com/zoumo/mass/pkg/jsonrpc"
)

// ShimClient is a typed client for the Shim JSON-RPC protocol.
type ShimClient struct {
	c *jsonrpc.Client
}

// NewShimClient creates a ShimClient wrapping the given jsonrpc.Client.
func NewShimClient(c *jsonrpc.Client) *ShimClient {
	return &ShimClient{c: c}
}

// RawClient returns the underlying jsonrpc.Client for advanced usage.
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

func (c *ShimClient) Prompt(ctx context.Context, req *apishim.SessionPromptParams) (*apishim.SessionPromptResult, error) {
	var result apishim.SessionPromptResult
	if err := c.c.Call(ctx, apishim.MethodSessionPrompt, req, &result); err != nil {
		return nil, err
	}
	return &result, nil
}

// SendPrompt sends a session/prompt request without waiting for the response.
// The caller should monitor the notification stream for turn progress and
// turn_end events. Use this for interactive/TUI scenarios where blocking
// until turn completion is not desired.
func (c *ShimClient) SendPrompt(ctx context.Context, req *apishim.SessionPromptParams) error {
	return c.c.CallAsync(ctx, apishim.MethodSessionPrompt, req)
}

func (c *ShimClient) Cancel(ctx context.Context) error {
	return c.c.Call(ctx, apishim.MethodSessionCancel, nil, nil)
}

func (c *ShimClient) Load(ctx context.Context, req *apishim.SessionLoadParams) error {
	return c.c.Call(ctx, apishim.MethodSessionLoad, req, nil)
}

func (c *ShimClient) Subscribe(ctx context.Context, req *apishim.SessionSubscribeParams) (*apishim.SessionSubscribeResult, error) {
	var result apishim.SessionSubscribeResult
	if err := c.c.Call(ctx, apishim.MethodSessionSubscribe, req, &result); err != nil {
		return nil, err
	}
	return &result, nil
}

func (c *ShimClient) Status(ctx context.Context) (*apishim.RuntimeStatusResult, error) {
	var result apishim.RuntimeStatusResult
	if err := c.c.Call(ctx, apishim.MethodRuntimeStatus, nil, &result); err != nil {
		return nil, err
	}
	return &result, nil
}

func (c *ShimClient) History(ctx context.Context, req *apishim.RuntimeHistoryParams) (*apishim.RuntimeHistoryResult, error) {
	var result apishim.RuntimeHistoryResult
	if err := c.c.Call(ctx, apishim.MethodRuntimeHistory, req, &result); err != nil {
		return nil, err
	}
	return &result, nil
}

func (c *ShimClient) Stop(ctx context.Context) error {
	return c.c.Call(ctx, apishim.MethodRuntimeStop, nil, nil)
}

// NotificationHandler handles inbound shim/event notifications.
type NotificationHandler func(ctx context.Context, method string, params json.RawMessage)

// Dial connects to a shim socket and returns a typed ShimClient.
func Dial(ctx context.Context, socketPath string, opts ...jsonrpc.DialOption) (*ShimClient, error) {
	c, err := jsonrpc.Dial(ctx, "unix", socketPath, opts...)
	if err != nil {
		return nil, err
	}
	return NewShimClient(c), nil
}

// DialWithHandler connects to a shim socket and registers an event
// notification handler that is called for every inbound shim/event push.
func DialWithHandler(ctx context.Context, socketPath string, handler NotificationHandler) (*ShimClient, error) {
	return Dial(ctx, socketPath, jsonrpc.WithNotificationHandler(jsonrpc.NotificationHandler(handler)))
}

// ParseShimEvent unmarshals a raw shim/event notification params payload into
// a typed ShimEvent.
func ParseShimEvent(params json.RawMessage) (apishim.ShimEvent, error) {
	var ev apishim.ShimEvent
	if err := json.Unmarshal(params, &ev); err != nil {
		return apishim.ShimEvent{}, err
	}
	return ev, nil
}
